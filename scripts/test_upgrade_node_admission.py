"""Upgrade/rollback fault injection in a disposable tree with fake systemd/HTTP."""
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sqlite3
import tempfile
import types
import unittest
from contextlib import ExitStack, closing, redirect_stdout
from unittest.mock import patch

import upgrade_node_admission as upgrade


class NodeUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        for name, relative in [('CONFIG_ROOT', 'etc/hqip-nodes'), ('STATE_ROOT', 'var/lib/hqip-nodes'),
                               ('RELEASE_ROOT', 'opt/hqip-node/releases'), ('UNIT_ROOT', 'etc/systemd/system'),
                               ('BACKUP_ROOT', 'root/backups')]:
            directory = self.root / relative
            directory.mkdir(parents=True, exist_ok=True)
            self.stack.enter_context(patch.object(upgrade, name, directory))
        self.node = 'node-a'
        self.units = upgrade.node_units(self.node)
        self.config = upgrade.CONFIG_ROOT / self.node
        self.state = upgrade.STATE_ROOT / self.node
        self.config.mkdir()
        self.state.mkdir()
        (self.config / 'agent.json').write_text(json.dumps({
            'nodeId': self.node, 'nodeDir': str(self.config), 'managed': True, 'controllerId': 'fixture',
            'listen': '127.0.0.1:19001', 'statsUrl': 'http://127.0.0.1:19002', 'statsSecret': 'fixture-private',
            'statePath': str(self.state / 'state.db'), 'controlUrl': 'https://control.invalid', 'token': 'fixture-token',
        }))
        self.old_yaml = b'listen: 192.0.2.1:443\nauth:\n  type: http\n'
        (self.config / 'hysteria.yaml').write_bytes(self.old_yaml)
        (self.state / 'cache.json').write_bytes(b'old-state')
        with closing(sqlite3.connect(self.state / 'state.db')) as db, db:
            db.execute('CREATE TABLE meta (key TEXT PRIMARY KEY,value TEXT NOT NULL)')
            db.execute("INSERT INTO meta VALUES('credentials','{}')")
        self.other = upgrade.UNIT_ROOT / 'hqip-hysteria-node-b.service'
        self.other.write_bytes(b'another-node-must-remain-unchanged')
        self.running = {unit: True for unit in self.units.values()}
        for kind, unit in self.units.items():
            description = f'HQIP node agent {self.node}' if kind == 'agent' else f'HQIP Hysteria node {self.node}'
            config_file = self.config / ('agent.json' if kind == 'agent' else 'hysteria.yaml')
            (upgrade.UNIT_ROOT / unit).write_text(f'[Unit]\nDescription={description}\n[Service]\nExecStart=/old/{kind} --config {config_file}\n')
        self.artifacts = self.root / 'artifacts'
        self.artifacts.mkdir()
        hashes = {}
        for kind in self.units:
            path = self.artifacts / f'{kind}-linux-amd64'
            path.write_bytes(b'new-' + kind.encode())
            hashes[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
        (self.artifacts / 'manifest.json').write_text(json.dumps({'hqipAdmissionVersion': 1, 'sha256': hashes}))
        self.commands = []
        self.http_calls = 0
        self.capability = {'admission': True}
        self.desired = {'portalEnabled': False, 'devices': [], 'revision': 2}
        self.fail_first_stop = False
        self.fail_first_start = False
        self.stack.enter_context(patch.object(upgrade, 'command', side_effect=self.command))
        self.stack.enter_context(patch.object(upgrade.platform, 'machine', return_value='x86_64'))
        self.stack.enter_context(patch.object(upgrade.time, 'sleep'))
        self.stack.enter_context(patch.object(upgrade.urllib.request, 'build_opener',
                                              return_value=types.SimpleNamespace(open=self.http)))
        self.stack.enter_context(redirect_stdout(io.StringIO()))

    def command(self, *args, check=True):
        self.commands.append(args)
        self.assertEqual(args[0], 'systemctl', 'Unexpected host command')
        action = args[1]
        if action == 'daemon-reload':
            return types.SimpleNamespace(stdout='')
        targets = [args[-1]] if action in ('show', 'is-active') else list(args[2:])
        self.assertTrue(set(targets).issubset(self.running), 'Touched another service')
        if action == 'show':
            unit = targets[0]
            override = upgrade.UNIT_ROOT / (unit + '.d') / '70-hqip-admission.conf'
            text = (override if override.exists() else upgrade.UNIT_ROOT / unit).read_text()
            return types.SimpleNamespace(stdout=text)
        if action == 'is-active':
            return types.SimpleNamespace(stdout='active' if self.running[targets[0]] else 'inactive')
        if action == 'stop':
            self.running[targets[0]] = False
            if self.fail_first_stop:
                self.fail_first_stop = False
                raise subprocess.CalledProcessError(1, args)
            for unit in targets:
                self.running[unit] = False
        elif action == 'start':
            if self.fail_first_start:
                self.fail_first_start = False
                raise subprocess.CalledProcessError(1, args)
            for unit in targets:
                self.running[unit] = True
        else:
            self.fail('Unexpected systemd action: ' + action)
        return types.SimpleNamespace(stdout='')

    def http(self, request, timeout):
        if request.full_url == 'https://control.invalid/api/v1/agents/config':
            self.assertEqual(timeout, 4)
            self.assertFalse(any(self.running.values()), 'Controller checked before node stopped')
            return io.StringIO(json.dumps(self.desired))
        self.http_calls += 1
        self.assertEqual(request.full_url, 'http://127.0.0.1:19002/hqip/capabilities')
        self.assertEqual(timeout, 2)
        return io.StringIO(json.dumps(self.capability))

    def backup(self):
        backups = list(upgrade.BACKUP_ROOT.iterdir())
        self.assertEqual(len(backups), 1)
        return backups[0]

    def recovered(self):
        self.assertTrue(all(self.running.values()))
        self.assertEqual((self.config / 'hysteria.yaml').read_bytes(), self.old_yaml)
        self.assertEqual(self.other.read_bytes(), b'another-node-must-remain-unchanged')
        for unit in self.units.values():
            self.assertFalse((upgrade.UNIT_ROOT / (unit + '.d') / '70-hqip-admission.conf').exists())
        self.assertEqual(json.loads((self.backup() / 'state.json').read_text())['phase'], 'rolled-back')

    def test_success_and_manual_rollback_preserve_current_accounting(self):
        upgrade.upgrade(self.node, self.artifacts)
        self.assertEqual(self.http_calls, 1)
        self.assertIn('hqipAdmission:', (self.config / 'hysteria.yaml').read_text())
        self.assertEqual(json.loads((self.backup() / 'state.json').read_text())['phase'], 'active')
        (self.state / 'cache.json').write_bytes(b'new-traffic-do-not-rewind')
        upgrade.rollback(self.backup())
        self.recovered()
        self.assertEqual((self.state / 'cache.json').read_bytes(), b'new-traffic-do-not-rewind')

    def test_file_checksums_without_python311_file_digest(self):
        samples = [b'', bytes(range(256)) * 8193 + b'partial-final-block']
        with patch.dict(hashlib.__dict__):
            hashlib.__dict__.pop('file_digest', None)
            self.assertFalse(hasattr(hashlib, 'file_digest'))
            for index, sample in enumerate(samples):
                with self.subTest(size=len(sample)):
                    path = self.root / f'checksum-sample-{index}'
                    path.write_bytes(sample)
                    self.assertEqual(upgrade.digest(path), hashlib.sha256(sample).hexdigest())

    def test_upgrade_without_python311_file_digest(self):
        with patch.dict(hashlib.__dict__):
            hashlib.__dict__.pop('file_digest', None)
            upgrade.upgrade(self.node, self.artifacts)
        self.assertTrue(all(self.running.values()))
        self.assertEqual(json.loads((self.backup() / 'state.json').read_text())['phase'], 'active')
        self.assertEqual(self.other.read_bytes(), b'another-node-must-remain-unchanged')

    def test_false_capability_never_reports_upgrade_success(self):
        self.capability = {'admission': False}
        with self.assertRaisesRegex(RuntimeError, 'did not prove admission'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertEqual(self.http_calls, 30)
        self.recovered()

    def test_non_boolean_capability_is_not_success(self):
        self.capability = {'admission': 'true'}
        with self.assertRaisesRegex(RuntimeError, 'did not prove admission'):
            upgrade.upgrade(self.node, self.artifacts)
        self.recovered()

    def test_partial_stop_failure_restores_both_services(self):
        self.fail_first_stop = True
        with self.assertRaises(subprocess.CalledProcessError):
            upgrade.upgrade(self.node, self.artifacts)
        self.recovered()

    def test_start_failure_restores_old_units(self):
        self.fail_first_start = True
        with self.assertRaises(subprocess.CalledProcessError):
            upgrade.upgrade(self.node, self.artifacts)
        self.recovered()

    def test_snapshot_failure_restores_running_state(self):
        with patch.object(upgrade.tarfile, 'open', side_effect=OSError('disk full')):
            with self.assertRaisesRegex(OSError, 'disk full'):
                upgrade.upgrade(self.node, self.artifacts)
        self.recovered()

    def test_inactive_original_service_is_never_started(self):
        self.running[self.units['hysteria']] = False
        with self.assertRaisesRegex(ValueError, 'must be active'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertFalse(self.running[self.units['hysteria']])
        self.assertFalse(any(c[1] in ('stop', 'start') for c in self.commands))

    def test_original_override_is_restored(self):
        path = upgrade.UNIT_ROOT / (self.units['agent'] + '.d') / '70-hqip-admission.conf'
        path.parent.mkdir()
        previous = f'[Service]\nExecStart=\nExecStart=/previous/agent --config {self.config / "agent.json"}\n'
        path.write_text(previous)
        upgrade.upgrade(self.node, self.artifacts)
        upgrade.rollback(self.backup())
        self.assertEqual(path.read_text(), previous)
        self.assertTrue(all(self.running.values()))

    def test_broken_new_override_can_be_rolled_back(self):
        upgrade.upgrade(self.node, self.artifacts)
        path = upgrade.UNIT_ROOT / (self.units['agent'] + '.d') / '70-hqip-admission.conf'
        path.write_text('[Service]\nExecStart=\n')
        upgrade.rollback(self.backup())
        self.recovered()

    def test_invalid_artifact_never_stops_node(self):
        (self.artifacts / 'agent-linux-amd64').write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertFalse(any(c[1] in ('stop', 'start') for c in self.commands))
        self.assertTrue(all(self.running.values()))

    def test_unit_for_another_config_is_rejected_before_stop(self):
        path = upgrade.UNIT_ROOT / self.units['agent']
        path.write_text(path.read_text().replace(str(self.config), str(self.root / 'other-node')))
        with self.assertRaisesRegex(ValueError, 'does not belong'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertFalse(any(c[1] in ('stop', 'start') for c in self.commands))

    def test_forged_rollback_units_never_touch_any_service(self):
        upgrade.upgrade(self.node, self.artifacts)
        path = self.backup() / 'state.json'
        info = json.loads(path.read_text())
        info['units']['hysteria'] = 'nginx.service'
        path.write_text(json.dumps(info))
        self.commands.clear()
        with self.assertRaisesRegex(ValueError, 'ownership'):
            upgrade.rollback(self.backup())
        self.assertEqual(self.commands, [])

    def test_forged_rollback_path_is_rejected_before_stop(self):
        upgrade.upgrade(self.node, self.artifacts)
        path = self.backup() / 'state.json'
        info = json.loads(path.read_text())
        info['overrides']['hysteria']['path'] = str(self.other)
        path.write_text(json.dumps(info))
        self.commands.clear()
        with self.assertRaisesRegex(ValueError, 'rollback override'):
            upgrade.rollback(self.backup())
        self.assertFalse(any(c[1] in ('stop', 'start') for c in self.commands))
        self.assertEqual(self.other.read_bytes(), b'another-node-must-remain-unchanged')

    def test_activated_ledger_blocks_old_core_and_preserves_current_config(self):
        upgrade.upgrade(self.node, self.artifacts)
        current = (self.config / 'hysteria.yaml').read_bytes()
        with closing(sqlite3.connect(self.state / 'state.db')) as db, db:
            db.execute("UPDATE meta SET value=? WHERE key='credentials'", ('{"portalEnabled":true}',))
        with self.assertRaisesRegex(RuntimeError, 'Unsafe node rollback refused'):
            upgrade.rollback(self.backup())
        self.assertFalse(any(self.running.values()))
        self.assertEqual((self.config / 'hysteria.yaml').read_bytes(), current)

    def test_controller_activation_blocks_stale_false_cache(self):
        upgrade.upgrade(self.node, self.artifacts)
        self.desired['portalEnabled'] = True
        with self.assertRaisesRegex(RuntimeError, 'Unsafe node rollback refused'):
            upgrade.rollback(self.backup())
        self.assertFalse(any(self.running.values()))
        upgrade.time.sleep.assert_called_with(31)

    def test_unknown_controller_state_fails_closed(self):
        upgrade.upgrade(self.node, self.artifacts)
        self.desired = {'error': 'unavailable'}
        with self.assertRaisesRegex(RuntimeError, 'Unsafe node rollback refused'):
            upgrade.rollback(self.backup())
        self.assertFalse(any(self.running.values()))

    def test_corrupt_ledger_fails_closed(self):
        upgrade.upgrade(self.node, self.artifacts)
        (self.state / 'state.db').write_bytes(b'corrupt')
        with self.assertRaisesRegex(RuntimeError, 'Unsafe node rollback refused'):
            upgrade.rollback(self.backup())
        self.assertFalse(any(self.running.values()))


if __name__ == '__main__':
    unittest.main()
