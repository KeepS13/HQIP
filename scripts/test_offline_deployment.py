import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch
import offline_deployment as offline
import upgrade_domains


class OfflineDeploymentTests(unittest.TestCase):
    def test_migration_role_target_and_version(self):
        deploy = Mock()
        deploy.postgres_enabled.return_value = True
        deploy.postgres_info.return_value = {'offlineAdmissionVersion': 0}
        deploy.MIGRATION_ENV = Path('/migration.env')
        deploy.storage_environment.side_effect = lambda path=None: {
            'HQIP_DATABASE_URL': 'postgres://%s@db/hqip' % ('hqip_migrator' if path else 'hqip_app')}
        env = offline.prepare(deploy, {'offlineAdmissionVersion': 1})
        run = Mock(return_value=SimpleNamespace(returncode=0))
        offline.migrate(Path('/release'), env, run)
        self.assertEqual(run.call_args.args[0], [str(Path('/release/hqip')), 'migrate-offline-admission'])
        deploy.postgres_info.return_value = {'offlineAdmissionVersion': 1}
        self.assertIsNone(offline.prepare(deploy, {'offlineAdmissionVersion': 1}))

    def test_rollback_cannot_ignore_new_ledger(self):
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), patch.object(
                upgrade_domains, 'postgres_info', return_value={'offlineAdmissionVersion': 1}):
            with self.assertRaisesRegex(ValueError, 'Offline grants'):
                upgrade_domains.require_billing_compatible({'postgresSchemaVersion': 1})

    def test_migration_failure_preserves_data_and_stops_activation(self):
        run = Mock(return_value=SimpleNamespace(returncode=1))
        with self.assertRaisesRegex(RuntimeError, 'live data is preserved'):
            offline.migrate(Path('/release'), {}, run)
        self.assertEqual(run.call_count, 1)


if __name__ == '__main__':
    unittest.main()
