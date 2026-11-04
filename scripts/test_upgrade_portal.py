"""Portal rollback ordering, with an isolated filesystem and mocked services."""
import io
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import ExitStack, redirect_stdout
from unittest.mock import patch

import upgrade_portal as portal


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
