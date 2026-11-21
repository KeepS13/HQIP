"""Released slots and pending intents must survive every deployment path."""
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

import upgrade_domains
import upgrade_node_agent


class ConnectionSlotsDeploymentTests(unittest.TestCase):
    def test_node_refuses_old_agent_for_cached_or_pending_slot_state(self):
        states = [
            {'grant': {'connectionSlotsVersion': 1, 'closed': True}},
            {'grant': {'slotReleased': True}},
            {'slotAction': 'release'},
            {'pending': {'slotAction': 'acquire'}},
        ]
        for state in states:
            with self.subTest(state=state), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / 'state.db'
                with closing(sqlite3.connect(path)) as db:
                    db.execute('CREATE TABLE offline_grants(state TEXT)')
                    db.execute('INSERT INTO offline_grants VALUES(?)', (json.dumps(state),))
                    db.commit()
                with self.assertRaisesRegex(ValueError, 'Connection slots'):
                    upgrade_node_agent.require_connection_slots_compatible(path, 0)
                upgrade_node_agent.require_connection_slots_compatible(path, 1)

    def test_controller_refuses_old_postgres_release(self):
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), patch.object(
                upgrade_domains, 'postgres_info', return_value={'connectionSlotsVersion': 1}):
            with self.assertRaisesRegex(ValueError, 'Connection slots'):
                upgrade_domains.require_billing_compatible({'connectionSlotsVersion': 0})
            upgrade_domains.require_billing_compatible({
                'connectionSlotsVersion': 1, 'postgresSchemaVersion': 1, 'serverBillingVersion': 2})

    def test_controller_refuses_old_sqlite_release(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            with closing(sqlite3.connect(path / 'hqip.db')) as db:
                db.execute('CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT)')
                db.execute("INSERT INTO metadata VALUES('connection_slots_version','1')")
                db.commit()
            with patch.object(upgrade_domains, 'postgres_enabled', return_value=False), patch.object(
                    upgrade_domains.control, 'DATA', path):
                with self.assertRaisesRegex(ValueError, 'Connection slots'):
                    upgrade_domains.require_billing_compatible({'connectionSlotsVersion': 0})
                upgrade_domains.require_billing_compatible({'connectionSlotsVersion': 1})
