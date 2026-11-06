from contextlib import ExitStack, closing
import hashlib
import json
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import upgrade_node_agent as upgrade


class AgentTelemetryUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.mocks = ExitStack()
        self.addCleanup(self.mocks.close)
        self.node = 'node-a'
        for name in ['CONFIG_ROOT', 'STATE_ROOT', 'RELEASE_ROOT', 'UNIT_ROOT', 'BACKUP_ROOT']:
            folder = self.root / name.lower()
            folder.mkdir()
            self.mocks.enter_context(patch.object(upgrade.node_ops, name, folder))
        self.config = upgrade.node_ops.CONFIG_ROOT / self.node
        self.state = upgrade.node_ops.STATE_ROOT / self.node
        self.config.mkdir()
        self.state.mkdir()
        self.ledger = self.state / 'state.db'
        self.cfg = {'nodeId': self.node, 'nodeDir': str(self.config), 'managed': True,
                    'controllerId': 'controller', 'statePath': str(self.ledger),
                    'listen': '127.0.0.1:1234', 'statsUrl': 'http://127.0.0.1:1235'}
        (self.config / 'agent.json').write_text(json.dumps(self.cfg))
        (self.config / 'hysteria.yaml').write_text('original-core')
        with closing(sqlite3.connect(self.ledger)) as db, db:
            db.executescript('CREATE TABLE hourly_state(id); CREATE TABLE usage(bytes); INSERT INTO usage VALUES(100);')
        self.units = upgrade.node_ops.node_units(self.node)
        for kind, unit in self.units.items():
            description = 'HQIP node agent' if kind == 'agent' else 'HQIP Hysteria node'
            cfgfile = self.config / ('agent.json' if kind == 'agent' else 'hysteria.yaml')
            (upgrade.node_ops.UNIT_ROOT / unit).write_text(f'Description={description} {self.node}\nExecStart=/old/{kind} --config {cfgfile}\n')
        self.artifacts = self.root / 'artifacts'
        self.artifacts.mkdir()
        (self.artifacts / 'agent-linux-amd64').write_bytes(b'verified-new-agent')
        digest = hashlib.sha256(b'verified-new-agent').hexdigest()
        self.target = upgrade.node_ops.RELEASE_ROOT / digest / 'agent'
        (self.artifacts / 'manifest.json').write_text(json.dumps({'agentTelemetryVersion': 1, 'agentSourceSHA': 'a'*40,
                       'sha256': {'agent-linux-amd64': digest}}))
        self.calls = []
        self.mocks.enter_context(patch.object(upgrade.platform, 'machine', return_value='x86_64'))
        self.mocks.enter_context(patch.object(upgrade.node_ops, 'command', side_effect=self.command))

    def command(self, *args, **kwargs):
        self.calls.append(args)
        text = 'active'
        if 'InvocationID' in args:
            text = 'original-core-invocation'
        if 'ExecStart' in args:
            kind = 'agent' if args[-1] == self.units['agent'] else 'hysteria'
            text = str(self.target if kind == 'agent' else '/old/hysteria') + ' --config ' + str(self.config / ('agent.json' if kind == 'agent' else 'hysteria.yaml'))
        return SimpleNamespace(stdout=text, returncode=0)

    def test_upgrade_and_rollback_never_restore_usage_or_restart_core(self):
        result = upgrade.upgrade(self.node, self.artifacts)
        backup = Path(result['backup'])
        with closing(sqlite3.connect(backup / 'state.db')) as db:
            self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone(), (100,))
        with closing(sqlite3.connect(self.ledger)) as db, db:
            db.execute('UPDATE usage SET bytes=999')
        upgrade.rollback(backup)
        with closing(sqlite3.connect(self.ledger)) as db:
            self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone(), (999,))
        self.assertEqual((self.config / 'hysteria.yaml').read_text(), 'original-core')
        for call in self.calls:
            if call[:2] in [('systemctl', 'start'), ('systemctl', 'stop'), ('systemctl', 'restart')]:
                self.assertEqual(call[2:], (self.units['agent'],))

    def test_bad_binary_is_rejected_before_service_changes(self):
        (self.artifacts / 'agent-linux-amd64').write_bytes(b'corrupted')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertFalse(any('stop' in call for call in self.calls))

    def test_old_backup_cannot_replace_new_override(self):
        result = upgrade.upgrade(self.node, self.artifacts)
        upgrade.override_path(self.units).write_text('a newer managed release')
        self.calls.clear()
        with self.assertRaisesRegex(ValueError, 'newer Agent'):
            upgrade.rollback(Path(result['backup']))
        self.assertFalse(any('stop' in call for call in self.calls))

    def test_interrupted_temporary_file_does_not_block_upgrade_or_rollback(self):
        override = upgrade.override_path(self.units)
        override.parent.mkdir()
        override.write_text('previous telemetry override')
        stale = override.with_name(override.name + '.new')
        stale.write_text('interrupted write')
        with patch.object(upgrade, 'verify_running', side_effect=RuntimeError('simulate startup failure')):
            with self.assertRaisesRegex(RuntimeError, 'startup failure'):
                upgrade.upgrade(self.node, self.artifacts)
        self.assertEqual(override.read_text(), 'previous telemetry override')
        self.assertEqual(stale.read_text(), 'interrupted write')
        self.assertEqual(self.calls[-1], ('systemctl', 'is-active', self.units['agent']))
        self.assertIn(('systemctl', 'start', self.units['agent']), self.calls)


if __name__ == '__main__':
    unittest.main()
