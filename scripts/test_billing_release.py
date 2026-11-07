from contextlib import closing, ExitStack
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

import upgrade_domains as deploy


class BillingReleaseTests(unittest.TestCase):
    def test_correction_during_stop_restarts_current_release_and_refuses_downgrade(self):
        with tempfile.TemporaryDirectory() as folder, ExitStack() as mocks:
            root = Path(folder).resolve()
            data, base, backups = root/'data', root/'base', root/'backups'
            for directory in (data, base, backups):
                directory.mkdir()
            previous, release, state = base/'previous', base/'release', backups/'state'
            for directory in (previous, release, state):
                directory.mkdir()
            (base/'current').symlink_to(release, target_is_directory=True)
            (state/'domains-state.json').write_text(json.dumps({'previous': str(previous), 'release': str(release), 'masterSHA256': 'paired'}))
            with closing(sqlite3.connect(data/'hqip.db')) as db, db:
                db.executescript("CREATE TABLE server_billing(server_id,cycle_offset); INSERT INTO server_billing VALUES('keep',0);")
            for obj, name, value in [(deploy.control, 'DATA', data), (deploy.control, 'BASE', base), (deploy, 'BACKUPS', backups)]:
                mocks.enter_context(patch.object(obj, name, value))
            mocks.enter_context(patch.object(deploy.control, 'verify_release', return_value=(previous, {'schemaVersion': 5, 'serverBillingVersion': 1})))
            mocks.enter_context(patch.object(deploy, 'data_identity', return_value='paired'))
            mocks.enter_context(patch.object(deploy, 'schema_version', return_value=5))

            def complete_inflight_correction(*args):
                if args[:2] == ('systemctl', 'stop'):
                    with closing(sqlite3.connect(data/'hqip.db')) as db, db:
                        db.execute('UPDATE server_billing SET cycle_offset=1')

            run = mocks.enter_context(patch.object(deploy.control, 'run', side_effect=complete_inflight_correction))
            point = mocks.enter_context(patch.object(deploy.control, 'point_current'))
            with self.assertRaisesRegex(ValueError, 'serverBillingVersion 2'):
                deploy.rollback(state)
            point.assert_not_called()
            self.assertEqual([call.args for call in run.call_args_list], [
                ('systemctl', 'stop', deploy.control.SERVICE),
                ('systemctl', 'start', deploy.control.SERVICE),
            ])

    def test_reanchored_cycle_blocks_v1_without_rewriting_live_usage(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with closing(sqlite3.connect(root/'hqip.db')) as db, db:
                db.executescript("CREATE TABLE server_billing(server_id,cycle_offset); INSERT INTO server_billing VALUES('keep',0); CREATE TABLE usage(bytes); INSERT INTO usage VALUES(12345);")
            with patch.object(deploy.control, 'DATA', root):
                deploy.require_billing_compatible({'serverBillingVersion': 1})
                with closing(sqlite3.connect(root/'hqip.db')) as db, db:
                    db.execute('UPDATE server_billing SET cycle_offset=2')
                with self.assertRaisesRegex(ValueError, 'serverBillingVersion 2'):
                    deploy.require_billing_compatible({'serverBillingVersion': 1})
                deploy.require_billing_compatible({'serverBillingVersion': 2})
            with closing(sqlite3.connect(root/'hqip.db')) as db:
                self.assertEqual(db.execute('SELECT * FROM server_billing').fetchall(), [('keep', 2)])
                self.assertEqual(db.execute('SELECT * FROM usage').fetchall(), [(12345,)])

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
