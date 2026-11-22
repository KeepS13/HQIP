"""Activation schema 2 must migrate explicitly and block period-only rollback."""
from contextlib import closing
from pathlib import Path
import json
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import subscription_renewal_deployment as renewal
import upgrade_domains as deploy


class ActivationDeploymentTests(unittest.TestCase):
    def test_version_one_requires_migration_and_version_two_is_noop(self):
        application = {'HQIP_DATABASE_URL': 'postgres://hqip_app:test@127.0.0.1/hqip'}
        migration = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator:test@127.0.0.1/hqip'}
        context = SimpleNamespace(postgres_enabled=lambda: True,
            postgres_info=lambda: {'subscriptionRenewalsVersion': 1}, MIGRATION_ENV=Path('/migration'),
            storage_environment=lambda path=None: migration if path else application)
        self.assertEqual(renewal.prepare(context, {'subscriptionRenewalsVersion': 2}), migration)
        context.postgres_info = lambda: {'subscriptionRenewalsVersion': 2}
        self.assertIsNone(renewal.prepare(context, {'subscriptionRenewalsVersion': 2}))

    def test_attempt_gate_records_activation_version(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            info = {}
            renewal.migrate(Path('/release'), {'database': 'fixture'}, state, info,
                            Mock(return_value=SimpleNamespace(returncode=0)))
            saved = json.loads((state / 'domains-state.json').read_text())
            self.assertEqual(saved['renewalTarget'], 2)
            self.assertTrue(saved['renewalMigrationCompleted'])

    def test_activation_data_cannot_roll_back_to_period_only_release(self):
        with tempfile.TemporaryDirectory() as directory:
            data = Path(directory)
            with closing(sqlite3.connect(data / 'hqip.db')) as db, db:
                db.execute('CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT)')
                db.execute("INSERT INTO metadata VALUES('subscription_renewals_version','2')")
            with patch.object(deploy.control, 'DATA', data), patch.object(deploy, 'postgres_enabled', return_value=False):
                with self.assertRaisesRegex(ValueError, 'renewal-compatible'):
                    deploy.require_billing_compatible({'serverBillingVersion': 2, 'subscriptionRenewalsVersion': 1})
                deploy.require_billing_compatible({'serverBillingVersion': 2, 'subscriptionRenewalsVersion': 2})
        with patch.object(deploy, 'postgres_enabled', return_value=True), patch.object(deploy, 'postgres_info', return_value={'subscriptionRenewalsVersion': 2}):
            with self.assertRaisesRegex(ValueError, 'renewal-compatible'):
                deploy.require_billing_compatible({'postgresSchemaVersion': 1, 'serverBillingVersion': 2, 'subscriptionRenewalsVersion': 1})
