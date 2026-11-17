import json
from pathlib import Path
import tempfile
import unittest
import zipfile

import ci_artifacts as transport


class CIArtifactTransportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sha = 'a' * 40
        self.env = {'GITHUB_REPOSITORY': 'KeepS13/HQIP', 'GITHUB_RUN_ID': '123', 'GITHUB_SHA': self.sha,
                    'GITHUB_REF': 'refs/heads/main', 'GITHUB_EVENT_NAME': 'push'}

    def fixture(self, kind):
        source = self.root / 'source'
        dist = source / 'dist'
        dist.mkdir(parents=True, exist_ok=True)
        if kind == 'linux-release':
            checksums = {}
            for name in transport.LINUX_FILES[:-1]:
                path = dist / name
                path.write_bytes(name.encode())
                checksums[name] = transport.digest(path)
            for site in ('admin-web', 'user-web'):
                folder = source / site / 'dist'
                folder.mkdir(parents=True)
                (folder / 'index.html').write_text('test')
            manifest = {'sha256': checksums, 'sourceSHA': self.sha, 'dirty': False}
            (dist / 'manifest.json').write_text(json.dumps(manifest))
        else:
            binary = dist / transport.WINDOWS_FILES[0]
            binary.write_bytes(b'example desktop package')
            manifest = {'filename': binary.name, 'sha256': transport.digest(binary), 'size': binary.stat().st_size,
                        'sourceSHA': self.sha, 'dirty': False}
            (dist / 'desktop-manifest.json').write_text(json.dumps(manifest))
        archive = self.root / (kind + '.zip')
        transport.package(kind, source, archive, self.sha)
        receipt = {'run': '123', 'sourceSHA': self.sha, 'kind': kind,
                   'size': archive.stat().st_size, 'sha256': transport.digest(archive)}
        return source, archive, receipt

    def test_both_layouts_round_trip_and_verify_build_identity(self):
        for kind in ('linux-release', 'windows-release'):
            _, archive, receipt = self.fixture(kind)
            destination = self.root / kind
            transport.unpack(kind, archive, receipt, destination, '123', self.sha)
            transport.verify_files(kind, destination, self.sha)

    def test_archive_tampering_is_rejected(self):
        _, archive, receipt = self.fixture('windows-release')
        with archive.open('ab') as stream:
            stream.write(b'changed')
        with self.assertRaisesRegex(ValueError, 'receipt'):
            transport.unpack('windows-release', archive, receipt, self.root / 'out', '123', self.sha)

    def test_wrong_run_or_commit_is_rejected(self):
        _, archive, receipt = self.fixture('windows-release')
        for run, sha in (('124', self.sha), ('123', 'b' * 40)):
            with self.assertRaisesRegex(ValueError, 'receipt'):
                transport.unpack('windows-release', archive, receipt, self.root / 'out', run, sha)

    def test_traversal_is_rejected_even_with_matching_transport_hash(self):
        _, archive, receipt = self.fixture('windows-release')
        with zipfile.ZipFile(archive, 'a') as output:
            output.writestr('../escaped', b'bad')
        receipt.update(size=archive.stat().st_size, sha256=transport.digest(archive))
        with self.assertRaisesRegex(ValueError, 'Unsafe'):
            transport.unpack('windows-release', archive, receipt, self.root / 'out', '123', self.sha)
        self.assertFalse((self.root / 'escaped').exists())

    def test_dirty_build_and_missing_web_assets_fail_upload(self):
        source, archive, _ = self.fixture('linux-release')
        (source / 'user-web/dist/index.html').unlink()
        with self.assertRaisesRegex(ValueError, 'Missing web'):
            transport.package('linux-release', source, archive, self.sha)
        (source / 'user-web/dist/index.html').write_text('test')
        path = source / 'dist/manifest.json'
        manifest = json.loads(path.read_text())
        manifest['dirty'] = True
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'identity'):
            transport.package('linux-release', source, archive, self.sha)

    def test_only_trusted_main_events_can_use_release_token(self):
        self.assertEqual(transport.identity(self.env), ('KeepS13/HQIP', '123', self.sha))
        for key, value in (('GITHUB_EVENT_NAME', 'pull_request'), ('GITHUB_REF', 'refs/heads/other'),
                           ('GITHUB_RUN_ID', '../123'), ('GITHUB_SHA', 'main')):
            with self.assertRaises(ValueError):
                transport.identity(dict(self.env, **{key: value}))

    def test_published_or_other_commit_releases_are_never_modified(self):
        info = {'draft': True, 'tag_name': 'owned', 'target_commitish': self.sha}
        transport.owned_release(info, 'owned', self.sha)
        for wrong in (None, dict(info, draft=False), dict(info, target_commitish='b' * 40), dict(info, tag_name='other')):
            with self.assertRaises(ValueError):
                transport.owned_release(wrong, 'owned', self.sha)


if __name__ == '__main__':
    unittest.main()
