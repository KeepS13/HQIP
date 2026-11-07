"""Payment callbacks and live ledgers must survive controlled release changes."""
import sqlite3
from contextlib import closing
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import render_domains
import upgrade_domains as deploy


class PaymentReleaseTests(unittest.TestCase):
    def test_exact_callback_uses_public_listener(self):
        site = render_domains.render()
        self.assertIn('location = /api/v1/payments/fm/notify { client_max_body_size 16k; proxy_pass http://127.0.0.1:18091; }', site)
        self.assertIn('location /api/ { proxy_pass http://127.0.0.1:18090; }', site)
        self.assertNotIn('$args', site)
        self.assertNotIn('$request_body', site)

    def test_sqlite_rollback_refuses_existing_orders_or_enabled_checkout(self):
        with tempfile.TemporaryDirectory() as folder:
            data = Path(folder)
            with closing(sqlite3.connect(data / 'hqip.db')) as db, db:
                db.executescript('CREATE TABLE payment_orders(id TEXT); CREATE TABLE payment_settings(enabled INTEGER); INSERT INTO payment_settings VALUES(0);')
            old = {'serverBillingVersion': 2}
            with patch.object(deploy.control, 'DATA', data):
                deploy.require_billing_compatible(old)
                with closing(sqlite3.connect(data / 'hqip.db')) as db, db:
                    db.execute('UPDATE payment_settings SET enabled=1')
                with self.assertRaisesRegex(ValueError, 'Payment orders'):
                    deploy.require_billing_compatible(old)
                with closing(sqlite3.connect(data / 'hqip.db')) as db, db:
                    db.execute('UPDATE payment_settings SET enabled=0')
                    db.execute("INSERT INTO payment_orders VALUES('paid-order')")
                with self.assertRaisesRegex(ValueError, 'Payment orders'):
                    deploy.require_billing_compatible(old)
                deploy.require_billing_compatible({**old, 'paymentsVersion': 1})

    def test_postgres_rollback_uses_payment_ledger_probe(self):
        with patch.object(deploy, 'postgres_enabled', return_value=True), patch.object(deploy, 'postgres_info', return_value={'paymentsRequired': True}):
            with self.assertRaisesRegex(ValueError, 'Payment orders'):
                deploy.require_billing_compatible({'postgresSchemaVersion': 1, 'serverBillingVersion': 2})
            deploy.require_billing_compatible({'postgresSchemaVersion': 1, 'serverBillingVersion': 2, 'paymentsVersion': 1})
