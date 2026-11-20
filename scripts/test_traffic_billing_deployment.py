import sqlite3
import tempfile
import unittest
from contextlib import closing
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch
import traffic_billing_deployment as billing
import upgrade_domains
import upgrade_node_agent


class TrafficBillingDeploymentTests(unittest.TestCase):
    def test_migration_role_and_rollback_guard(self):
        deploy = Mock()
        deploy.postgres_enabled.return_value = True
        deploy.postgres_info.return_value = {'trafficBillingVersion': 0}
        deploy.MIGRATION_ENV = Path('/migration.env')
        deploy.storage_environment.side_effect = lambda path=None: {
            'HQIP_DATABASE_URL': 'postgres://%s@localhost/hqip' % ('hqip_migrator' if path else 'hqip_app')}
        env = billing.prepare(deploy, {'trafficBillingVersion': 1})
        execute = Mock(return_value=SimpleNamespace(returncode=0))
        billing.migrate(Path('/release'), env, execute)
        self.assertEqual(execute.call_args.args[0], [str(Path('/release/hqip')), 'migrate-traffic-billing'])
        execute.return_value.returncode = 1
        with self.assertRaisesRegex(RuntimeError, 'preserved'):
            billing.migrate(Path('/release'), env, execute)
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), patch.object(
                upgrade_domains, 'postgres_info', return_value={'trafficBillingVersion': 1}):
            with self.assertRaisesRegex(ValueError, 'Traffic billing ledger'):
                upgrade_domains.require_billing_compatible({'postgresSchemaVersion': 1})

    def test_agent_downgrade_preserves_segment_ledger(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'state.db'
            with closing(sqlite3.connect(path)) as db, db:
                db.execute('CREATE TABLE lease_billing_reports(lease_id TEXT,body TEXT)')
            with self.assertRaisesRegex(ValueError, 'Traffic billing ledger'):
                upgrade_node_agent.require_traffic_billing_compatible(path, 0)
            upgrade_node_agent.require_traffic_billing_compatible(path, 1)


if __name__ == '__main__':
    unittest.main()
