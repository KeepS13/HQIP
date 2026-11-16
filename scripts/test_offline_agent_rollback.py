import sqlite3
from contextlib import closing
import tempfile
import unittest
from pathlib import Path
import upgrade_node_agent as agent


class OfflineAgentRollbackTests(unittest.TestCase):
    def test_new_ledger_prevents_incompatible_binary(self):
        with tempfile.TemporaryDirectory() as folder:
            ledger = Path(folder) / 'state.db'
            with closing(sqlite3.connect(ledger)) as db, db:
                db.execute('CREATE TABLE offline_grants(id TEXT PRIMARY KEY,state TEXT)')
                db.execute('CREATE TABLE offline_requests(hash TEXT PRIMARY KEY,request_id TEXT)')
            agent.require_offline_compatible(ledger, 0)
            with closing(sqlite3.connect(ledger)) as db, db:
                db.execute("INSERT INTO offline_requests VALUES('credential-digest','uncertain-request')")
            with self.assertRaisesRegex(ValueError, 'compatible Agent'):
                agent.require_offline_compatible(ledger, 0)
            agent.require_offline_compatible(ledger, 1)
            with closing(sqlite3.connect(ledger)) as db, db:
                db.execute('DELETE FROM offline_requests')
                db.execute("INSERT INTO offline_grants VALUES('grant','pending-final')")
            with self.assertRaisesRegex(ValueError, 'live ledger'):
                agent.require_offline_compatible(ledger, 0)
            with closing(sqlite3.connect(ledger)) as db, db:
                self.assertEqual(db.execute('SELECT state FROM offline_grants').fetchone()[0], 'pending-final')


if __name__ == '__main__':
    unittest.main()
