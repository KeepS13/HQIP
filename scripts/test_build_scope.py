"""Exercise build selection without running compilers or downloading binaries."""
from contextlib import ExitStack, redirect_stderr
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import build


class BuildScopeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.out = self.root / 'dist'
        self.out.mkdir()
        self.commands, self.downloads = [], []
        self.missing = None
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode='w:gz') as package:
            member = tarfile.TarInfo('caddy')
            member.size = len(b'pinned caddy')
            package.addfile(member, io.BytesIO(b'pinned caddy'))
        self.payloads = {name: archive.getvalue() if name.endswith('.tar.gz') else name.encode()
                         for name in build.PINNED}
        self.pinned = {name: (url, hashlib.sha256(self.payloads[name]).hexdigest())
                       for name, (url, _) in build.PINNED.items()}

    def compiler(self, args, **kwargs):
        self.commands.append((args, kwargs.get('env')))
        if args[0] == 'go':
            target = Path(args[args.index('-o') + 1])
            if target.name != self.missing:
                target.write_bytes(('fresh compiled ' + target.name).encode())
        else:
            self.assertEqual(Path(args[1]).name, 'build_hy2.py')
            hashes = {}
            for arch in ('amd64', 'arm64'):
                target = self.out / ('hqip-hysteria-linux-' + arch)
                target.write_bytes(('fresh admission ' + arch).encode())
                hashes[target.name] = hashlib.sha256(target.read_bytes()).hexdigest()
            (self.out / 'hqip-hysteria-manifest.json').write_text(json.dumps({'hqipAdmissionVersion': 1, 'sha256': hashes}))

    def downloader(self, name, url, expected):
        self.downloads.append(name)
        self.assertEqual(hashlib.sha256(self.payloads[name]).hexdigest(), expected)
        (self.out / name).write_bytes(self.payloads[name])

    def execute(self, *options):
        with ExitStack() as stack:
            for name, value in [('ROOT', self.root), ('OUT', self.out), ('PINNED', self.pinned)]:
                stack.enter_context(patch.object(build, name, value))
            stack.enter_context(patch.object(build, 'run', side_effect=self.compiler))
            stack.enter_context(patch.object(build, 'download', side_effect=self.downloader))
            stack.enter_context(patch.object(build.subprocess, 'check_output', side_effect=['a' * 40, '']))
            stack.enter_context(patch.object(sys, 'argv', ['build.py', '--skip-web', *options]))
            build.main()
        return json.loads((self.out / 'manifest.json').read_text())

    def test_server_only_builds_linux_and_ignores_stale_windows_files_and_manifest(self):
        stale = {f'{name}-windows-{arch}.exe' for arch in ('amd64', 'arm64') for name in ('hqip', 'hysteria')}
        for name in stale:
            (self.out / name).write_bytes(b'old release')
        (self.out / 'manifest.json').write_text(json.dumps({'sourceSHA': 'b' * 40, 'sha256': {'stale': '0' * 64}}))
        value = self.execute('--server-only')
        expected = {f'{name}-linux-{arch}' for arch in ('amd64', 'arm64')
                    for name in ('hqip', 'agent', 'hqip-migrate-postgres', 'hysteria')} | {'caddy-linux-amd64'}
        self.assertEqual(set(value['sha256']), expected)
        self.assertEqual(value['sourceSHA'], 'a' * 40)
        self.assertEqual(self.downloads, ['caddy-linux-amd64.tar.gz'])
        self.assertEqual([env['GOOS'] for args, env in self.commands if args[0] == 'go'], ['linux'] * 6)
        self.assertTrue(all((self.out / name).read_bytes() == b'old release' for name in stale))
        for name, digest in value['sha256'].items():
            self.assertEqual(digest, hashlib.sha256((self.out / name).read_bytes()).hexdigest())

    def test_default_build_keeps_legacy_windows_cli_and_hysteria(self):
        value = self.execute()
        windows = {f'{name}-windows-{arch}.exe' for arch in ('amd64', 'arm64') for name in ('hqip', 'hysteria')}
        self.assertTrue(windows <= value['sha256'].keys())
        self.assertEqual(set(self.downloads), {'hysteria-windows-amd64.exe', 'hysteria-windows-arm64.exe', 'caddy-linux-amd64.tar.gz'})
        self.assertEqual([env['GOARCH'] for args, env in self.commands if args[0] == 'go' and env['GOOS'] == 'windows'], ['amd64', 'arm64'])

    def test_released_agents_include_the_vision_tls_adapter(self):
        self.execute('--server-only')
        agents = [args for args, env in self.commands
                  if args[0] == 'go' and args[-1] == './cmd/hqip-agent']
        self.assertEqual(len(agents), 2)
        for args in agents:
            self.assertIn('-tags=with_utls', args)

    def test_production_scope_requires_fresh_go_builds(self):
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
            self.execute('--server-only', '--skip-go')
        self.assertEqual(error.exception.code, 2)
        self.assertEqual(self.commands, [])
        self.assertFalse((self.out / 'manifest.json').exists())

    def test_missing_selected_output_cannot_silently_publish_partial_manifest(self):
        self.missing = 'agent-linux-arm64'
        with self.assertRaises(FileNotFoundError):
            self.execute('--server-only')
        self.assertFalse((self.out / 'manifest.json').exists())

    def test_production_cached_caddy_must_match_pin_and_is_reextracted(self):
        cached = self.out / 'caddy-linux-amd64.tar.gz'
        cached.write_bytes(self.payloads[cached.name])
        (self.out / 'caddy-linux-amd64').write_bytes(b'stale extracted caddy')
        self.execute('--server-only', '--skip-download')
        self.assertEqual(self.downloads, [])
        self.assertEqual((self.out / 'caddy-linux-amd64').read_bytes(), b'pinned caddy')
        cached.write_bytes(b'corrupt cache')
        with self.assertRaisesRegex(ValueError, 'pinned Caddy cache'):
            self.execute('--server-only', '--skip-download')


if __name__ == '__main__':
    unittest.main()
