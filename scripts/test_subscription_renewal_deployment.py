import json
from contextlib import closing
from pathlib import Path
import sqlite3
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import subscription_renewal_deployment as renewal
import upgrade_domains as deploy


class SubscriptionRenewalDeploymentTests(unittest.TestCase):
    def test_migration_role_and_database_are_verified(self):
        application = {'HQIP_DATABASE_URL': 'postgres://hqip_app:test@127.0.0.1/hqip'}
        migration = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator:test@127.0.0.1/hqip'}
        context = SimpleNamespace(postgres_enabled=lambda: True, postgres_info=lambda: {}, MIGRATION_ENV=Path('/migration'),
                                  storage_environment=lambda path=None: migration if path else application)
        self.assertEqual(renewal.prepare(context, {'subscriptionRenewalsVersion': 1}), migration)
        for address in ['postgres://hqip_app:test@127.0.0.1/hqip', 'postgres://hqip_migrator:test@127.0.0.1/other']:
            migration['HQIP_DATABASE_URL'] = address
            with self.assertRaises(ValueError):
                renewal.prepare(context, {'subscriptionRenewalsVersion': 1})

    def test_timeout_retains_attempt_gate_and_does_not_report_completion(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            info = {}
            execute = Mock(side_effect=subprocess.TimeoutExpired('migration', 330))
            with self.assertRaisesRegex(RuntimeError, 'uncertain'):
                renewal.migrate(Path('/release'), {'HQIP_DATABASE_URL': 'test-only'}, state, info, execute)
            saved = json.loads((state / 'domains-state.json').read_text())
            self.assertTrue(saved['renewalMigrationAttempted'])
            self.assertNotIn('renewalMigrationCompleted', saved)
            self.assertEqual(execute.call_args.args[0], [str(Path('/release/hqip')), 'migrate-subscription-renewals'])

    def test_success_and_noop(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            execute = Mock(return_value=SimpleNamespace(returncode=0))
            renewal.migrate(Path('/release'), None, state, {}, execute)
            execute.assert_not_called()
            info = {}
            renewal.migrate(Path('/release'), {'database': 'test'}, state, info, execute)
            self.assertTrue(info['renewalMigrationCompleted'])

    def test_sqlite_and_postgres_refuse_incompatible_rollback(self):
        with tempfile.TemporaryDirectory() as directory:
            data = Path(directory)
            with closing(sqlite3.connect(data / 'hqip.db')) as db, db:
                db.execute('CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT)')
                db.execute("INSERT INTO metadata VALUES('subscription_renewals_version','1')")
            with patch.object(deploy.control, 'DATA', data), patch.object(deploy, 'postgres_enabled', return_value=False):
                with self.assertRaisesRegex(ValueError, 'renewal-compatible'):
                    deploy.require_billing_compatible({'serverBillingVersion': 2})
                deploy.require_billing_compatible({'serverBillingVersion': 2, 'subscriptionRenewalsVersion': 1})
        with patch.object(deploy, 'postgres_enabled', return_value=True), patch.object(deploy, 'postgres_info', return_value={'subscriptionRenewalsVersion': 1}):
            with self.assertRaisesRegex(ValueError, 'renewal-compatible'):
                deploy.require_billing_compatible({'postgresSchemaVersion': 1, 'serverBillingVersion': 2})

    def test_release_runs_period_migration_after_stop_and_packages_its_script(self):
        source = (Path(__file__).parent / 'upgrade_domains.py').read_text(encoding='utf-8')
        body = source[source.index('def upgrade('):]
        self.assertLess(body.index("control.run('systemctl','stop'"), body.index('migrate_renewals(release'))
        self.assertLess(body.index('migrate_renewals(release'), body.index('control.point_current(release)'))
        from release_bundle import RELEASE_SCRIPTS
        self.assertIn('subscription_renewal_deployment.py', RELEASE_SCRIPTS)
