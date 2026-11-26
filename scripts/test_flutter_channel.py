import io
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tarfile
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import zipfile

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
import flutter_channel as channel
import install_flutter_preview as install
from render_domains import preview_routes, render


def fixture(root, sequence=3, key=None, extra=b''):
    key = key or Ed25519PrivateKey.generate()
    public = key.public_key().public_bytes_raw().hex()
    root.mkdir()
    entries = []
    for (platform, kind), filename in channel.FILENAMES.items():
        body = b'fixture-' + filename.encode() + extra
        manifest_hash = None
        if filename.endswith('.zip'):
            stream = io.BytesIO()
            files = {name: b'fixture-runtime' + extra for name in ['hqip-ui.exe', 'hqip-runtime.exe', 'hqip-helper.exe', 'sing-box.exe', 'libcronet.dll', 'vcruntime140.dll']}
            manifest = channel.encode({'files': [{'path': name, 'size': len(data), 'sha256': channel.hashlib.sha256(data).hexdigest()} for name, data in files.items()]})
            manifest_hash = channel.hashlib.sha256(manifest).hexdigest()
            with zipfile.ZipFile(stream, 'w') as z:
                for name, data in files.items():
                    z.writestr(name, data)
                z.writestr('payload.json', manifest)
            body = stream.getvalue()
        sha = channel.hashlib.sha256(body).hexdigest()
        v = dict(schema=2, product='hqip-flutter', channel='preview', version=f'0.1.0-preview.{sequence}', sequence=sequence,
                 platform=platform, kind=kind, filename=filename, sha256=sha, size=len(body))
        if manifest_hash:
            v['manifestSha256'] = manifest_hash
        p = root / channel.object_path(v)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(body)
        entries.append(channel.signed(v, key))
    catalog = {'releases': entries}
    releases = channel.catalog_releases(catalog, public)
    (root / 'releases.json').write_bytes(channel.encode(catalog))
    (root / 'installers.json').write_bytes(channel.encode(channel.installers(releases)))
    receipt = dict(schema=1, product='hqip-flutter', channel='preview', sourceSHA='a' * 40, dirty=False,
                   version=f'0.1.0-preview.{sequence}', sequence=sequence,
                   files={p.relative_to(root).as_posix(): {'size': p.stat().st_size, 'sha256': channel.digest(p)} for p in root.rglob('*') if p.is_file()})
    (root / 'receipt.json').write_bytes(channel.encode(channel.signed(receipt, key)))
    return key, public


class BundleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bundle = self.root / 'bundle'
        self.key, self.public = fixture(self.bundle)

    def test_verified_bundle_has_six_entries_and_four_payloads(self):
        receipt, releases = channel.verify_bundle(self.bundle, self.public, 'a' * 40)
        self.assertEqual(receipt['sequence'], 3)
        self.assertEqual(len(releases), 6)
        self.assertEqual(len({v['sha256'] for v in releases}), 4)

    def test_modified_file_wrong_key_and_source_are_rejected(self):
        with self.assertRaises(Exception):
            channel.verify_bundle(self.bundle, Ed25519PrivateKey.generate().public_key().public_bytes_raw().hex())
        with self.assertRaisesRegex(ValueError, 'provenance'):
            channel.verify_bundle(self.bundle, self.public, 'b' * 40)
        path = next((self.bundle / 'objects').rglob('*.apk'))
        path.write_bytes(b'broken')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            channel.verify_bundle(self.bundle, self.public)

    def test_catalog_duplicates_missing_platform_and_cross_channel_rejected(self):
        catalog = json.loads((self.bundle / 'releases.json').read_bytes())
        for mutate in ['duplicate', 'missing', 'channel']:
            value = json.loads(json.dumps(catalog))
            if mutate == 'duplicate':
                value['releases'][0] = value['releases'][1]
            elif mutate == 'missing':
                value['releases'].pop()
            else:
                release = channel.verified(value['releases'][0], self.public)
                release['channel'] = 'stable'
                value['releases'][0] = channel.signed(release, self.key)
            with self.assertRaises(ValueError):
                channel.catalog_releases(value, self.public)

    def test_unlisted_files_and_installer_url_tampering_rejected(self):
        (self.bundle / 'private.seed').write_bytes(b'secret')
        with self.assertRaisesRegex(ValueError, 'Unexpected'):
            channel.verify_bundle(self.bundle, self.public)
        (self.bundle / 'private.seed').unlink()
        (self.bundle / 'installers.json').write_text('{}')
        with self.assertRaises(ValueError):
            channel.verify_bundle(self.bundle, self.public)

    def test_archive_paths_links_and_duplicates_rejected(self):
        for index, mode in enumerate(['parent', 'link', 'duplicate']):
            archive = self.root / f'{mode}.tgz'
            with tarfile.open(archive, 'w:gz') as tar:
                info = tarfile.TarInfo('../escaped' if mode == 'parent' else 'file')
                if mode == 'link':
                    info.type = tarfile.SYMTYPE
                    info.linkname = '/etc/passwd'
                tar.addfile(info)
                if mode == 'duplicate':
                    tar.addfile(info)
            with self.assertRaises(ValueError):
                channel.extract_bundle(archive, self.root / f'extract-{index}')
        self.assertFalse((self.root / 'escaped').exists())

    def test_same_sequence_conflict_and_downgrade_rejected(self):
        base = self.root / 'published'
        base.mkdir()
        install.stage(self.bundle, base, self.public, 'a' * 40)
        install.stage(self.bundle, base, self.public, 'a' * 40)
        other = self.root / 'other'
        fixture(other, key=self.key, extra=b'changed')
        with self.assertRaisesRegex(ValueError, 'different content'):
            install.stage(other, base, self.public, 'a' * 40)
        old = self.root / 'old'
        fixture(old, sequence=2, key=self.key)
        with self.assertRaisesRegex(ValueError, 'older'):
            install.stage(old, base, self.public, 'a' * 40)

    @unittest.skipIf(os.name == 'nt', 'POSIX activation and flock run in Linux CI')
    def test_activation_rollback_and_reload_failure_preserve_official_site(self):
        base = self.root / 'published'
        base.mkdir()
        site = self.root / 'hqip.conf'
        before = 'server {\n' + install.ANCHOR + '{ proxy_pass http://127.0.0.1:18091; }\n}\n'
        site.write_text(before)
        reloads = []
        result = install.activate(self.bundle, base, site, self.public, 'a' * 40, '1-1', reload=lambda: reloads.append(True))
        self.assertEqual(result['phase'], 'active')
        self.assertEqual(install.current_target(base), 'versions/3')
        install.rollback(base, site, '1-1', reload=lambda: reloads.append(True))
        self.assertEqual(site.read_text(), before)
        self.assertIsNone(install.current_target(base))
        self.assertEqual(len(list((base / 'objects').rglob('*.apk'))), 2)
        calls = []
        def fail_first():
            calls.append(True)
            if len(calls) == 1:
                raise RuntimeError('nginx rejected config')
        with self.assertRaisesRegex(RuntimeError, 'rejected'):
            install.activate(self.bundle, base, site, self.public, 'a' * 40, '2-1', reload=fail_first)
        self.assertEqual(site.read_text(), before)
        self.assertIsNone(install.current_target(base))

    @unittest.skipIf(os.name == 'nt', 'POSIX locks run in Linux CI')
    def test_lock_blocks_concurrent_publish_and_rollback_wont_clobber_new_config(self):
        base = self.root / 'published'
        with install.locked(base):
            with self.assertRaises(BlockingIOError):
                with install.locked(base):
                    pass
        site = self.root / 'site'
        site.write_text('server {\n' + install.ANCHOR + '{}\n}\n')
        install.activate(self.bundle, base, site, self.public, 'a' * 40, '1-1', reload=lambda: None)
        site.write_text(site.read_text() + '# another deployment\n')
        with self.assertRaisesRegex(ValueError, 'Another deployment'):
            install.rollback(base, site, '1-1', reload=lambda: None)

    def test_generated_config_and_idempotent_patch_match(self):
        text = render()
        self.assertEqual(install.patch_site(text), text)
        self.assertEqual(text.count(install.BEGIN), 1)
        self.assertEqual(text.count('if ($hqip_cloudflare_peer = 0) { return 403; }'), 4)
        self.assertEqual((Path(__file__).resolve().parents[1] / 'deploy/nginx-hqip.conf').read_text(), text)


@unittest.skipUnless(os.name != 'nt' and shutil.which('nginx'), 'Real Nginx routing runs in Linux CI')
class NginxTests(unittest.TestCase):
    def test_exact_routing_caching_ranges_and_hidden_paths(self):
        folder = self.enterContext(tempfile.TemporaryDirectory())
        root = Path(folder)
        root.chmod(0o755)
        channel_root = root / 'channel'
        (channel_root / 'versions/3').mkdir(parents=True)
        (channel_root / 'current').symlink_to('versions/3')
        for name in ['releases', 'installers']:
            (channel_root / f'current/{name}.json').write_text('{"preview":true}')
        obj = channel_root / 'objects' / ('a' * 64) / channel.FILENAMES[('windows-amd64', 'installer')]
        obj.parent.mkdir(parents=True)
        obj.write_bytes(b'0123456789abcdef')
        class Official(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"official":true}')
            def log_message(self, *_):
                pass
        backend = ThreadingHTTPServer(('127.0.0.1', 0), Official)
        worker = threading.Thread(target=backend.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(backend.server_close)
        self.addCleanup(backend.shutdown)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        routes = preview_routes().replace('/opt/hqip-flutter-preview', channel_root.as_posix()).replace('127.0.0.1:18091', f'127.0.0.1:{backend.server_port}')
        config = root / 'nginx.conf'
        config.write_text(f'pid {root}/nginx.pid; error_log {root}/error.log; events {{}} http {{ access_log off; server {{ listen 127.0.0.1:{port}; {routes} location / {{ return 404; }} }} }}')
        cmd = ['nginx', '-p', folder, '-c', str(config)]
        subprocess.run([*cmd, '-t'], check=True, capture_output=True)
        subprocess.run(cmd, check=True, capture_output=True)
        self.addCleanup(lambda: subprocess.run([*cmd, '-s', 'quit'], capture_output=True))
        base = f'http://127.0.0.1:{port}'
        def get(path, headers=None):
            return urllib.request.urlopen(urllib.request.Request(base + path, headers=headers or {}), timeout=5)
        for endpoint in ['/api/v3/client/releases', '/api/v1/user/installers']:
            with get(endpoint) as r:
                self.assertEqual(json.load(r), {'official': True})
            with get(endpoint + '?client=flutter&channel=preview') as r:
                self.assertEqual(json.load(r), {'preview': True})
                self.assertEqual(r.headers['Cache-Control'], 'no-store')
            for query in ['?client=flutter', '?client=flutter&channel=invalid']:
                with self.assertRaises(urllib.error.HTTPError) as caught:
                    get(endpoint + query)
                self.assertEqual(caught.exception.code, 404)
        path = '/downloads/flutter/preview/' + 'a' * 64 + '/' + obj.name
        with get(path, {'Range': 'bytes=2-5'}) as r:
            self.assertEqual(r.status, 206)
            self.assertEqual(r.read(), b'2345')
            self.assertEqual(r.headers['Content-Range'], 'bytes 2-5/16')
            self.assertIn('immutable', r.headers['Cache-Control'])
        for path in ['/__hqip_flutter_preview/releases.json', '/downloads/flutter/preview/../receipt.json', '/downloads/flutter/preview/' + 'a'*64 + '/receipt.json']:
            with self.assertRaises(urllib.error.HTTPError) as caught:
                get(path)
            self.assertEqual(caught.exception.code, 404)


if __name__ == '__main__':
    unittest.main()
