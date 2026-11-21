"""Never roll node-owned quota back into central preallocation accounting."""
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

import upgrade_node_agent
import upgrade_domains


class NodeQuotaDeploymentTests(unittest.TestCase):
    def test_node_ledger_blocks_incompatible_agent(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'state.db'
            with sqlite3.connect(path) as db:
                db.execute('CREATE TABLE offline_grants(state TEXT)')
                db.execute('INSERT INTO offline_grants VALUES(?)', (json.dumps({'grant': {'nodeQuota': {'revision': 1}, 'closed': True}}),))
            db.close()
            with self.assertRaisesRegex(ValueError, 'Node-owned quota'):
                upgrade_node_agent.require_node_quota_compatible(path, 0)
            upgrade_node_agent.require_node_quota_compatible(path, 1)

    def test_controller_blocks_incompatible_release(self):
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), patch.object(
                upgrade_domains, 'postgres_info', return_value={'nodeQuotaVersion': 1}):
            with self.assertRaisesRegex(ValueError, 'Node-owned quota'):
                upgrade_domains.require_billing_compatible({'nodeQuotaVersion': 0})
