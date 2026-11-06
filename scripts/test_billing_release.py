from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

import upgrade_domains as deploy


class BillingReleaseTests(unittest.TestCase):
    def test_even_empty_schema_blocks_old_protocol_before_first_agent_report(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with closing(sqlite3.connect(root/'hqip.db')) as db, db:
                db.executescript('CREATE TABLE node_hourly_coverage(node_id);')
            with patch.object(deploy.control, 'DATA', root):
                with self.assertRaisesRegex(ValueError, 'billing-compatible'):
                    deploy.require_billing_compatible({'schemaVersion': 5})
                deploy.require_billing_compatible({'schemaVersion': 5, 'serverBillingVersion': 1})

    def test_configured_cycles_block_legacy_rollback_without_changing_data(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with closing(sqlite3.connect(root/'hqip.db')) as db, db:
                db.executescript("CREATE TABLE server_billing(server_id); INSERT INTO server_billing VALUES('keep');")
            with patch.object(deploy.control, 'DATA', root):
                with self.assertRaisesRegex(ValueError, 'billing-compatible'):
                    deploy.require_billing_compatible({})
                deploy.require_billing_compatible({'serverBillingVersion': 1})
            with closing(sqlite3.connect(root/'hqip.db')) as db:
                self.assertEqual(db.execute('SELECT server_id FROM server_billing').fetchall(), [('keep',)])

    def test_telemetry_release_cannot_change_core_or_other_artifact_pins(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            old, new = root/'old', root/'new'
            for p in [old, new]:
                (p/'artifacts').mkdir(parents=True)
            previous = {'hqipAdmissionVersion': 1, 'sha256': {'agent-linux-amd64': 'old1', 'agent-linux-arm64': 'old2', 'hysteria-linux-amd64': 'core'}}
            current = {'hqipAdmissionVersion': 1, 'agentTelemetryVersion': 1, 'agentSourceSHA': 'sha',
                       'sha256': {**previous['sha256'], 'agent-linux-amd64': 'new1', 'agent-linux-arm64': 'new2'}}
            manifest = {'sourceSHA': 'sha', 'agentTelemetryVersion': 1, 'sha256': {'artifacts/agent-linux-amd64': 'new1', 'artifacts/agent-linux-arm64': 'new2'}}
            (old/'artifacts/manifest.json').write_text(json.dumps(previous))
            (new/'artifacts/manifest.json').write_text(json.dumps(current))
            self.assertEqual(len(deploy.verify_agent_assets(new, old, manifest)), 3)
            current['sha256']['hysteria-linux-amd64'] = 'changed'
            (new/'artifacts/manifest.json').write_text(json.dumps(current))
            with self.assertRaisesRegex(ValueError, 'Only Agent'):
                deploy.verify_agent_assets(new, old, manifest)


if __name__ == '__main__':
    unittest.main()
