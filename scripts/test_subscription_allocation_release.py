"""An older controller must not widen allocated subscription rights on rollback."""
from pathlib import Path
from contextlib import closing
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

import upgrade_domains as deploy


class SubscriptionAllocationReleaseTests(unittest.TestCase):
    def test_postgres_allocation_marker_blocks_older_release(self):
        old = {'postgresSchemaVersion': 1, 'serverBillingVersion': 2, 'paymentsVersion': 1}
        with patch.object(deploy, 'postgres_enabled', return_value=True), patch.object(deploy, 'postgres_info', return_value={'subscriptionAllocationsVersion': 1}):
            with self.assertRaisesRegex(ValueError, 'Subscription allocations'):
                deploy.require_billing_compatible(old)
            deploy.require_billing_compatible({**old, 'subscriptionAllocationsVersion': 1})

    def test_sqlite_allocation_marker_blocks_older_release_without_payments(self):
        with tempfile.TemporaryDirectory() as folder:
            data = Path(folder)
            with closing(sqlite3.connect(data / 'hqip.db')) as db, db:
                db.executescript("CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT); INSERT INTO metadata VALUES('subscription_allocations_version','1');")
            with patch.object(deploy.control, 'DATA', data):
                with self.assertRaisesRegex(ValueError, 'Subscription allocations'):
                    deploy.require_billing_compatible({'serverBillingVersion': 2})
                deploy.require_billing_compatible({'serverBillingVersion': 2, 'subscriptionAllocationsVersion': 1})
