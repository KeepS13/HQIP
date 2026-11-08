"""Protect the reset boundary using real data/receipt files and refused transitions."""
from contextlib import ExitStack
from pathlib import Path
import json
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import fresh_deploy as fresh
import render_domains


class FreshDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.data = self.root / 'data'
        self.data.mkdir()
        (self.data / 'master.key').write_bytes(b'old-master')
        (self.data / 'hqip.db').write_bytes(b'final-snapshot')
        self.backups = self.root / 'backups'
        self.backups.mkdir()
        self.archive = self.backups / 'final-encrypted.archive'
        self.archive.write_bytes(b'verified-backup')
        self.receipt = self.backups / 'receipt.json'
        self.value = {'version': 1, 'verified': True, 'sourceDataDir': str(self.data),
                      'sourceInventorySHA256': fresh.inventory(self.data), 'archive': str(self.archive),
                      'archiveSHA256': fresh.digest(self.archive)}
        self.receipt.write_text(json.dumps(self.value))
        self.receipt.chmod(0o600)
        self.mocks = ExitStack()
        self.addCleanup(self.mocks.close)
        self.mocks.enter_context(patch.object(fresh, 'DATA', self.data))
        self.mocks.enter_context(patch.object(fresh, 'BACKUPS', self.backups))

    def test_final_receipt_accepts_only_exact_stopped_data_and_backup(self):
        fresh.validate_retirement_receipt(self.receipt)
        (self.data / 'hqip.db').write_bytes(b'late-payment')
        with self.assertRaisesRegex(ValueError, 'no longer matches'):
            fresh.validate_retirement_receipt(self.receipt)
        self.assertEqual((self.data / 'hqip.db').read_bytes(), b'late-payment')

    def test_receipt_cannot_point_outside_backups_or_skip_verification(self):
        self.value['verified'] = False
        self.receipt.write_text(json.dumps(self.value))
        with self.assertRaisesRegex(ValueError, 'Verified final'):
            fresh.validate_retirement_receipt(self.receipt)
        self.value['verified'] = True
        self.value['archive'] = str(self.data / 'hqip.db')
        self.receipt.write_text(json.dumps(self.value))
        with self.assertRaisesRegex(ValueError, 'Verified final'):
            fresh.validate_retirement_receipt(self.receipt)

    def test_nonempty_database_refuses_initialization(self):
        with patch.object(fresh, 'sql', return_value='42'):
            with self.assertRaisesRegex(ValueError, 'not empty'):
                fresh.assert_database_empty()
        with patch.object(fresh, 'sql', return_value='0'):
            fresh.assert_database_empty()

    def test_low_space_checks_real_filesystems_including_staging_parent(self):
        with patch.object(fresh.shutil, 'disk_usage', return_value=SimpleNamespace(free=3 * 1024 ** 3 - 1)):
            with self.assertRaisesRegex(ValueError, '3 GiB'):
                fresh.require_free_space(self.root / 'not-created-yet')
        with patch.object(fresh.shutil, 'disk_usage', side_effect=[
                SimpleNamespace(free=3 * 1024 ** 3), SimpleNamespace(free=2 * 1024 ** 3)]):
            with self.assertRaisesRegex(ValueError, '3 GiB'):
                fresh.require_free_space(self.root, self.backups)
        with patch.object(fresh.shutil, 'disk_usage', return_value=SimpleNamespace(free=3 * 1024 ** 3)):
            fresh.require_free_space(self.root)

    def test_runuser_retains_only_explicit_database_configuration(self):
        migration = {'PATH': '/usr/bin:/bin', 'HQIP_DATABASE_URL': 'postgres://test-only-config',
                     'HQIP_REDIS_URL': 'redis://test-only-config'}
        with patch.object(fresh, 'run') as run:
            fresh.initialize_admin(self.root, migration)
        self.assertEqual(run.call_args.kwargs['env'], migration)
        self.assertEqual(run.call_args.args[0], '/usr/sbin/runuser')
        self.assertIn('--preserve-environment', run.call_args.args)
        self.assertIn('--initialize-postgres', run.call_args.args)
        self.assertNotIn(migration['HQIP_DATABASE_URL'], run.call_args.args)

    def test_existing_business_and_enabled_payment_are_not_empty_station(self):
        value = {name: 0 for name in fresh.EMPTY_TABLES}
        value.update(admins=1, paymentEnabled=0, portalEnabled='false', registrationEnabled='false', controllerID='new-id')
        fresh.assert_empty(value)
        for key, bad in [('paymentEnabled', 1), ('portal_subscriptions', 1), ('registrationEnabled', 'true')]:
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'not empty'):
                fresh.assert_empty({**value, key: bad})

    def test_ordinary_ci_cannot_activate_a_nonprepared_state(self):
        with patch.object(fresh, 'load', return_value={'phase': 'finalized'}), patch.object(fresh, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'prepared station'):
                fresh.activate(self.backups, self.receipt)
            run.assert_not_called()

    def test_active_old_controller_blocks_data_move(self):
        stage = self.root / 'staging'
        stage.mkdir()
        (stage / 'master.key').write_bytes(b'new-master')
        record = {'phase': 'prepared', 'release': str(self.root), 'masterSHA256': fresh.digest(stage / 'master.key')}
        with patch.object(fresh, 'load', return_value=record), patch.object(fresh, 'STAGE', stage), \
             patch.object(fresh, 'verify_full_release'), patch.object(fresh, 'empty_inventory'), \
             patch.object(fresh, 'assert_empty'), patch.object(fresh, 'ready'), patch.object(fresh, 'require_free_space'), \
             patch.object(fresh.subprocess, 'run', return_value=SimpleNamespace(stdout='active\n')):
            with self.assertRaisesRegex(ValueError, 'Stop the retired controller'):
                fresh.activate(self.backups, self.receipt)
        self.assertTrue((self.data / 'hqip.db').exists())
        self.assertTrue(stage.exists())

    def test_low_space_aborts_activation_before_services_or_data_change(self):
        record = {'phase': 'prepared', 'release': str(self.root)}
        with patch.object(fresh, 'load', return_value=record), patch.object(fresh, 'verify_full_release'), \
             patch.object(fresh.shutil, 'disk_usage', return_value=SimpleNamespace(free=1024)), \
             patch.object(fresh, 'run') as run:
            with self.assertRaisesRegex(ValueError, '3 GiB'):
                fresh.activate(self.backups, self.receipt)
            run.assert_not_called()
        self.assertTrue((self.data / 'hqip.db').exists())

    def test_low_space_aborts_prepare_before_initializing_or_starting_services(self):
        base = self.root / 'program'
        release = base / 'releases' / ('b' * 40)
        release.mkdir(parents=True)
        state = self.backups / 'new-state'
        stage = self.root / 'new-data'
        with patch.object(fresh, 'BASE', base), patch.object(fresh, 'STAGE', stage), \
             patch.object(fresh, 'PREP_UNIT', self.root / 'prepare.service'), \
             patch.object(fresh, 'verify_full_release', return_value={'sourceSHA': 'b' * 40}), \
             patch.object(fresh.shutil, 'disk_usage', return_value=SimpleNamespace(free=1024)), \
             patch.object(fresh, 'run') as run:
            with self.assertRaisesRegex(ValueError, '3 GiB'):
                fresh.prepare(release, state)
            run.assert_not_called()
        self.assertFalse(state.exists())
        self.assertFalse(stage.exists())

    def test_log_policies_install_only_owned_targets_and_restart_only_namespace(self):
        release = Path(__file__).resolve().parents[1]
        journal = self.root / 'systemd/journald@hqip-control.conf.d/limits.conf'
        rotate = self.root / 'logrotate.d/hqip-control'
        logs = self.root / 'nginx/hqip-control'
        journal.parent.parent.mkdir()
        rotate.parent.mkdir()
        logs.parent.mkdir()
        unrelated = logs.parent / 'another-project.log'
        unrelated.write_text('keep shared logs')
        with patch.object(fresh, 'JOURNAL_LIMITS', journal), patch.object(fresh, 'LOGROTATE_CONFIG', rotate), \
             patch.object(fresh, 'NGINX_LOG_DIR', logs), patch.object(fresh, 'run') as run:
            fresh.install_log_limits(release)
            self.assertEqual(journal.read_bytes(), (release / 'deploy/hqip-journal-limits.conf').read_bytes())
            self.assertEqual(rotate.read_bytes(), (release / 'deploy/hqip-nginx-logrotate.conf').read_bytes())
            self.assertEqual(unrelated.read_text(), 'keep shared logs')
            calls = [call.args for call in run.call_args_list]
            self.assertEqual(calls, [
                ('chown', 'root:adm', logs),
                ('/usr/sbin/logrotate', '--debug', '--state', '/dev/null', release / 'deploy/hqip-nginx-logrotate.conf'),
                ('systemctl', 'try-restart', 'systemd-journald@hqip-control.service')])
            run.reset_mock()
            fresh.install_log_limits(release)
            self.assertFalse(any(call.args[0] == 'systemctl' for call in run.call_args_list))

    def test_invalid_log_destination_is_rejected_before_installation(self):
        release = Path(__file__).resolve().parents[1]
        journal = self.root / 'systemd/limits.conf'
        journal.mkdir(parents=True)  # Config destination must not be a directory.
        with patch.object(fresh, 'JOURNAL_LIMITS', journal), patch.object(fresh, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'destination'):
                fresh.install_log_limits(release)
            run.assert_not_called()

    def test_rotation_owns_exact_new_nginx_log_without_shared_wildcard(self):
        rule = (Path(__file__).resolve().parents[1] / 'deploy/hqip-nginx-logrotate.conf').read_text()
        owned = '/var/log/nginx/hqip-control/access.log'
        self.assertIn(owned + ' {', rule)
        self.assertEqual(render_domains.render().count('access_log ' + owned + ' '), 6)
        directives = '\n'.join(line for line in rule.splitlines() if not line.lstrip().startswith('#'))
        self.assertNotIn('/var/log/nginx/*.log', directives)
        self.assertNotIn('copytruncate', directives)
        self.assertIn('su root adm', directives)
        self.assertIn('create 0640 www-data adm', directives)
        self.assertIn('--kill-who=main --signal=USR1 nginx.service', directives)
        self.assertNotIn('restart nginx', directives)


if __name__ == '__main__':
    unittest.main()
