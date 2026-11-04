"""Portal rollback ordering, with an isolated filesystem and mocked services."""
import io
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import ExitStack, redirect_stdout
from unittest.mock import patch

import upgrade_portal as portal


class PortalReleaseInvocationTests(unittest.TestCase):
    def test_real_release_invocation_does_not_create_unmanifested_bytecode(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            release = root / 'control/releases/verified'
            scripts = release / 'scripts'
            scripts.mkdir(parents=True)
            for name in ('upgrade_portal.py', 'upgrade_control.py'):
                shutil.copyfile(Path(__file__).parent / name, scripts / name)
            (release / 'hqip').write_bytes(b'fixture binary')
            (release / 'artifacts').mkdir()
            (release / 'artifacts/manifest.json').write_text('{}')
            manifest = {
                'sourceSHA': 'a' * 40,
                'sha256': {
                    path.relative_to(release).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
                    for path in release.rglob('*') if path.is_file()
                },
            }
            (release / 'release.json').write_text(json.dumps(manifest))
            entry = scripts / 'upgrade_portal.py'
            # -E ignores an inherited PYTHONDONTWRITEBYTECODE/PYTHONPYCACHEPREFIX;
            # no -B flag is used. This exercises the shipped script's own guard.
            invocation = subprocess.run([sys.executable, '-E', str(entry), '--help'], cwd=root,
                                        capture_output=True, text=True, timeout=15)
            self.assertEqual(invocation.returncode, 0, invocation.stderr)
            self.assertFalse(list(release.rglob('__pycache__')))
            self.assertFalse(list(release.rglob('*.pyc')))
            # Import and use the actual manifest verifier in another fresh process.
            # Only its fixed installation base changes to the isolated test tree.
            verify = (
                "import pathlib,runpy,sys; "
                "entry=pathlib.Path(sys.argv[1]); sys.path.insert(0,str(entry.parent)); "
                "module=runpy.run_path(str(entry)); control=module['control']; "
                "release=entry.parent.parent; control.BASE=release.parent.parent; "
                "control.verify_release(release)"
            )
            verified = subprocess.run([sys.executable, '-E', '-c', verify, str(entry)], cwd=root,
                                      capture_output=True, text=True, timeout=15)
            self.assertEqual(verified.returncode, 0, verified.stderr)
            self.assertEqual(
                {path.relative_to(release).as_posix() for path in release.rglob('*') if path.is_file()},
                set(manifest['sha256']) | {'release.json'},
            )


class PortalUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.site = self.root / 'sites/hqip.conf'
        self.site.parent.mkdir()
        self.old = b'server_name hqip.5202345.xyz; # before'
        self.new = b'server_name hqip.5202345.xyz; # account portal'
        self.site.write_bytes(self.old)
        self.other = self.site.parent / 'another.conf'
        self.other.write_bytes(b'untouched')
        self.release = self.root / 'release'
        (self.release / 'deploy').mkdir(parents=True)
        (self.release / 'deploy/nginx-hqip.conf').write_bytes(self.new)
        self.state = self.root / 'backup'
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.object(portal, 'SITE', self.site))
        self.stack.enter_context(patch.object(portal.control, 'verify_release', return_value=(
            self.release, {'sha256': {'deploy/nginx-hqip.conf': 'verified-by-control'}})))
        self.stack.enter_context(patch.object(portal.control, 'upgrade', side_effect=lambda *_: self.state.mkdir()))
        self.rollback = self.stack.enter_context(patch.object(portal.control, 'rollback'))
        self.stack.enter_context(patch.object(portal.control, 'healthy'))
        self.run = self.stack.enter_context(patch.object(portal, 'run'))
        self.stack.enter_context(redirect_stdout(io.StringIO()))

    def test_success_keeps_private_snapshot_and_other_site(self):
        portal.upgrade(self.release, self.state)
        self.assertEqual(self.site.read_bytes(), self.new)
        self.assertEqual((self.state / 'nginx.before.conf').read_bytes(), self.old)
        self.assertFalse(json.loads((self.state / 'portal-stage.json').read_text())['activated'])
        self.assertEqual(self.other.read_bytes(), b'untouched')
        self.rollback.assert_not_called()

    def test_snapshot_write_failure_rolls_back_controller(self):
        original = Path.write_bytes
        def write(path, content):
            if path == self.state / 'nginx.before.conf':
                raise OSError('snapshot disk full')
            return original(path, content)
        with patch.object(Path, 'write_bytes', write):
            with self.assertRaisesRegex(OSError, 'snapshot disk full'):
                portal.upgrade(self.release, self.state)
        self.rollback.assert_called_once_with(self.state)
        self.assertEqual(self.site.read_bytes(), self.old)

    def test_activation_guard_preserves_new_routes_on_failed_upgrade(self):
        self.run.side_effect = RuntimeError('nginx failed')
        self.rollback.side_effect = RuntimeError('Unsafe rollback refused')
        with self.assertRaisesRegex(RuntimeError, 'Unsafe rollback refused'):
            portal.upgrade(self.release, self.state)
        self.assertEqual(self.site.read_bytes(), self.new)
        self.assertEqual(self.other.read_bytes(), b'untouched')

    def test_manual_rollback_guard_runs_before_restoring_routes(self):
        portal.upgrade(self.release, self.state)
        self.run.reset_mock()
        self.rollback.side_effect = RuntimeError('Unsafe rollback refused')
        with self.assertRaisesRegex(RuntimeError, 'Unsafe rollback refused'):
            portal.rollback(self.state)
        self.assertEqual(self.site.read_bytes(), self.new)
        self.run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
