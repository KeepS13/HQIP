"""Exercise the embedded entrypoint, including a host with no backup parent."""
from contextlib import closing
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import test_upgrade_node_agent as fixture_module

spec = importlib.util.spec_from_file_location(
    'managed_agent_upgrade', Path(__file__).resolve().parents[1] / 'internal/provision/managed_agent_upgrade.py')
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class ManagedAgentUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.fixture = fixture_module.AgentTelemetryUpgradeTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.folder = self.fixture.root
        self.parent = self.folder / 'fresh-host' / 'backups'
        self.request = {'nodeId': self.fixture.node, 'operationId': 'a' * 18, 'sourceSHA': 'a' * 40}
        self.fixture.mocks.enter_context(patch.object(runner.signal, 'signal'))
        self.fixture.mocks.enter_context(patch.object(runner.signal, 'SIGHUP', 1, create=True))
        original_path = sys.path[:]
        self.addCleanup(lambda: sys.path.__setitem__(slice(None), original_path))
        if os.name == 'nt':
            # Only POSIX flock is unavailable locally; Linux CI uses the real lock.
            self.fixture.mocks.enter_context(patch.dict(sys.modules, {
                'fcntl': SimpleNamespace(flock=Mock(), LOCK_EX=2, LOCK_NB=4)}))

    def execute(self):
        code = runner.run(self.folder, self.request, self.parent, self.folder / 'host.lock')
        receipt = json.loads((self.folder / 'result.json').read_text())
        return code, receipt

    def test_first_upgrade_creates_parent_and_preserves_core_and_ledger(self):
        self.assertFalse(self.parent.exists())
        code, receipt = self.execute()
        self.assertEqual(code, 0)
        self.assertEqual(receipt['state'], 'succeeded')
        self.assertTrue(receipt['coreInvocationUnchanged'])
        self.assertTrue(Path(receipt['backup']).is_dir())
        self.assertFalse((self.folder / 'artifacts').exists())
        with closing(sqlite3.connect(self.fixture.ledger)) as db:
            self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone(), (100,))
        for call in self.fixture.calls:
            if call[:2] in [('systemctl', 'stop'), ('systemctl', 'start')]:
                self.assertEqual(call[2], self.fixture.units['agent'])
        if os.name != 'nt':
            self.assertEqual((Path(receipt['backup']).parent.stat().st_mode & 0o777), 0o700)

    def test_backup_parent_file_is_reported_before_service_changes(self):
        self.parent.parent.mkdir()
        self.parent.write_text('not a directory')
        code, receipt = self.execute()
        self.assertEqual(code, 1)
        self.assertEqual(receipt['errorCode'], 'backup_directory')
        self.assertFalse(any('stop' in call for call in self.fixture.calls))

    def test_checksum_failure_is_reported_and_keeps_artifacts(self):
        (self.folder / 'artifacts/agent-linux-amd64').write_bytes(b'bad binary')
        code, receipt = self.execute()
        self.assertEqual(code, 1)
        self.assertEqual(receipt['errorCode'], 'artifact_checksum')
        self.assertTrue((self.folder / 'artifacts').is_dir())
        self.assertFalse(any('stop' in call for call in self.fixture.calls))

    def test_interruption_after_stop_recovers_agent_before_failed_receipt(self):
        stopped = False

        def command(*args, **kwargs):
            nonlocal stopped
            result = self.fixture.command(*args, **kwargs)
            if args == ('systemctl', 'stop', self.fixture.units['agent']) and not stopped:
                stopped = True
                raise RuntimeError('Upgrade interrupted')
            return result

        with patch.object(fixture_module.upgrade.node_ops, 'command', side_effect=command):
            code, receipt = self.execute()
        self.assertEqual(code, 1)
        self.assertEqual(receipt['errorCode'], 'interrupted')
        self.assertIn(('systemctl', 'start', self.fixture.units['agent']), self.fixture.calls)
        states = list(self.parent.glob('*/*/state.json'))
        self.assertEqual(len(states), 1)
        self.assertEqual(json.loads(states[0].read_text())['phase'], 'rolled-back-ledger-preserved')

    def test_failure_receipt_does_not_expose_exception_output(self):
        with patch.object(fixture_module.upgrade, 'upgrade', side_effect=RuntimeError('secret-from-command')):
            code, receipt = self.execute()
        self.assertEqual(code, 1)
        self.assertEqual(receipt['errorCode'], 'upgrade_failed')
        self.assertNotIn('secret-from-command', (self.folder / 'result.json').read_text())

    @unittest.skipIf(os.name == 'nt', 'POSIX host lock')
    def test_busy_host_does_not_run_upgrade(self):
        import fcntl
        with open(self.folder / 'host.lock', 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            code, receipt = self.execute()
        self.assertEqual(code, 1)
        self.assertEqual(receipt['errorCode'], 'host_busy')
        self.assertEqual(self.fixture.calls, [])


if __name__ == '__main__':
    unittest.main()
