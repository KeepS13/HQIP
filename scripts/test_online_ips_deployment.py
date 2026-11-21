"""Online-counting contracts trigger migration and reject incompatible rollback."""
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import Mock, patch

import offline_deployment
import upgrade_domains
import upgrade_node_agent


class OnlineIPDeploymentTests(unittest.TestCase):
    def test_existing_ip_schema_migrates_instance_counting(self):
        deploy = Mock()
        deploy.postgres_enabled.return_value = True
        deploy.postgres_info.return_value = {'offlineAdmissionVersion': 1, 'connectionSlotsVersion': 2}
        deploy.MIGRATION_ENV = Path('/migration.env')
        deploy.storage_environment.return_value = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator@db/hqip'}
        manifest = {'offlineAdmissionVersion': 1, 'connectionSlotsVersion': 3}
        self.assertIsNotNone(offline_deployment.prepare(deploy, manifest))
        deploy.postgres_info.return_value['connectionSlotsVersion'] = 3
        self.assertIsNone(offline_deployment.prepare(deploy, manifest))

    def test_instance_counting_refuses_version_two_rollback(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = Path(directory) / 'ledger.db'
            with closing(sqlite3.connect(ledger)) as db:
                db.execute('CREATE TABLE offline_grants(state TEXT)')
                db.execute('INSERT INTO offline_grants VALUES(?)', (json.dumps({'grant': {'connectionSlotsVersion': 3}}),))
                db.commit()
            with self.assertRaisesRegex(ValueError, 'Connection slots'):
                upgrade_node_agent.require_connection_slots_compatible(ledger, 2)
            upgrade_node_agent.require_connection_slots_compatible(ledger, 3)
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), patch.object(
                upgrade_domains, 'postgres_info', return_value={'connectionSlotsVersion': 3}):
            with self.assertRaisesRegex(ValueError, 'Connection slots'):
                upgrade_domains.require_billing_compatible({'connectionSlotsVersion': 2})
            upgrade_domains.require_billing_compatible({
                'connectionSlotsVersion': 3, 'postgresSchemaVersion': 1, 'serverBillingVersion': 2})

    def test_existing_offline_schema_still_migrates_ip_column(self):
        deploy = Mock()
        deploy.postgres_enabled.return_value = True
        deploy.postgres_info.return_value = {'offlineAdmissionVersion': 1, 'connectionSlotsVersion': 1}
        deploy.MIGRATION_ENV = Path('/migration.env')
        deploy.storage_environment.side_effect = lambda path=None: {
            'HQIP_DATABASE_URL': 'postgres://%s@db/hqip' % ('hqip_migrator' if path else 'hqip_app')}
        manifest = {'offlineAdmissionVersion': 1, 'connectionSlotsVersion': 2}
        self.assertIsNotNone(offline_deployment.prepare(deploy, manifest))
        deploy.postgres_info.return_value['connectionSlotsVersion'] = 2
        self.assertIsNone(offline_deployment.prepare(deploy, manifest))

    def test_node_cannot_roll_back_from_ip_aware_ledger(self):
        for grant in ({'connectionSlotsVersion': 2}, {'clientIP': '198.51.100.1'}):
            with self.subTest(grant=grant), tempfile.TemporaryDirectory() as directory:
                ledger = Path(directory) / 'ledger.db'
                with closing(sqlite3.connect(ledger)) as db:
                    db.execute('CREATE TABLE offline_grants(state TEXT)')
                    db.execute('INSERT INTO offline_grants VALUES(?)', (json.dumps({'grant': grant}),))
                    db.commit()
                with self.assertRaisesRegex(ValueError, 'Connection slots'):
                    upgrade_node_agent.require_connection_slots_compatible(ledger, 1)
                upgrade_node_agent.require_connection_slots_compatible(ledger, 2)

    def test_controller_cannot_roll_back_from_ip_aware_schema(self):
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), patch.object(
                upgrade_domains, 'postgres_info', return_value={'connectionSlotsVersion': 2}):
            with self.assertRaisesRegex(ValueError, 'Connection slots'):
                upgrade_domains.require_billing_compatible({'connectionSlotsVersion': 1})
            upgrade_domains.require_billing_compatible({
                'connectionSlotsVersion': 2, 'postgresSchemaVersion': 1, 'serverBillingVersion': 2})

    def test_node_marker_preserves_slot_version_two(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            binary = root / 'agent'
            binary.write_bytes(b'test-agent')
            marker = root / 'connection-slots-capability.json'
            override = root / 'override.conf'
            override.write_text('ExecStart="' + binary.as_posix() + '"\n')
            with patch.object(upgrade_node_agent.node_ops, 'RELEASE_ROOT', root):
                for version in (2, 3):
                    marker.write_text(json.dumps({'sha256': upgrade_node_agent.node_ops.digest(binary), 'version': version}))
                    self.assertEqual(upgrade_node_agent.previous_offline_version(override, 'connection-slots'), version)


if __name__ == '__main__':
    unittest.main()
