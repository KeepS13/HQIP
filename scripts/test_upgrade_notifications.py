"""Verify notification rollback never rewinds active portal accounting."""
from contextlib import closing
import json
import sqlite3
from unittest.mock import patch

import upgrade_notifications as notifications
import upgrade_control as control
import test_upgrade_control as base_tests


class NotificationsUpgradeTests(base_tests.UpgradeSafetyTests):
    # Reuse only the isolated filesystem fixture; inherited old-tool tests
    # remain useful regressions and run without touching real host services.
    def setUp(self):
        super().setUp()
        self.stack.enter_context(patch.object(notifications, 'BACKUPS', self.backups))
        self.stack.enter_context(patch.object(notifications, 'Path', self.safe_path))
        with closing(sqlite3.connect(self.data / 'hqip.db')) as db, db:
            db.execute("INSERT INTO metadata VALUES('schema_version','4')")
            db.execute("INSERT INTO metadata VALUES('portal_enabled','true')")
            db.execute('CREATE TABLE usage (bytes INTEGER)')
            db.execute('INSERT INTO usage VALUES(100)')
        old = json.loads((self.previous / 'release.json').read_text())
        new = json.loads((self.new / 'release.json').read_text())
        for name, digest in old['sha256'].items():
            if name.startswith('artifacts/'):
                (self.new / name).write_bytes((self.previous / name).read_bytes())
                new['sha256'][name] = digest
        new.update(upgradeKind='notifications-additive', previousSourceSHA=old['sourceSHA'])
        (self.new / 'release.json').write_text(json.dumps(new))
        self.old_database = (self.data / 'hqip.db').read_bytes()

    def test_notification_failure_preserves_new_usage_and_schema(self):
        checks = []
        def healthy():
            checks.append(1)
            if len(checks) == 1:
                with closing(sqlite3.connect(self.data / 'hqip.db')) as db, db:
                    db.execute('UPDATE usage SET bytes=250')
                    db.execute('CREATE TABLE notification_example (id TEXT)')
                raise RuntimeError('new code fails')
        with patch.object(control, 'healthy', side_effect=healthy):
            with self.assertRaisesRegex(RuntimeError, 'new code fails'):
                notifications.upgrade(self.new, self.state)
        self.assertTrue(self.running)
        self.assertEqual((self.base / 'current').resolve(), self.previous)
        with closing(sqlite3.connect(self.data / 'hqip.db')) as db:
            self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone()[0], 250)
            self.assertEqual(db.execute("SELECT value FROM metadata WHERE key='portal_enabled'").fetchone()[0], 'true')
            db.execute('SELECT * FROM notification_example')
        self.assertEqual((self.state / 'data.before/hqip.db').read_bytes(), self.old_database)

    def test_notification_backup_failure_restarts_previous(self):
        with patch.object(notifications.shutil, 'copytree', side_effect=OSError('full')):
            with self.assertRaisesRegex(OSError, 'full'):
                notifications.upgrade(self.new, self.state)
        self.assertTrue(self.running)
        self.assertEqual((self.base / 'current').resolve(), self.previous)

    def test_notification_assets_and_base_are_pinned(self):
        manifest = json.loads((self.new / 'release.json').read_text())
        manifest['previousSourceSHA'] = 'c' * 40
        (self.new / 'release.json').write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'current verified'):
            notifications.upgrade(self.new, self.state)
        self.assertEqual(self.commands, [])

    def test_notification_success_rollback_keeps_current_data(self):
        notifications.upgrade(self.new, self.state)
        if __import__('os').name != 'nt':
            self.assertEqual(self.state.stat().st_mode & 0o777, 0o700)
            for path in self.state.rglob('*'):
                self.assertEqual(path.stat().st_mode & 0o777, 0o700 if path.is_dir() else 0o600)
        with closing(sqlite3.connect(self.data / 'hqip.db')) as db, db:
            db.execute('UPDATE usage SET bytes=400')
        notifications.rollback(self.state)
        with closing(sqlite3.connect(self.data / 'hqip.db')) as db:
            self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone()[0], 400)
        self.assertTrue(self.running)

    def test_notification_rejects_changed_node_asset_before_stop(self):
        import hashlib
        name = 'artifacts/agent-linux-amd64'
        (self.new / name).write_bytes(b'changed-agent')
        manifest = json.loads((self.new / 'release.json').read_text())
        manifest['sha256'][name] = hashlib.sha256(b'changed-agent').hexdigest()
        (self.new / 'release.json').write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'assets must remain unchanged'):
            notifications.upgrade(self.new, self.state)
        self.assertEqual(self.commands, [])


# The base suite targets destructive preactivation rollback. It must not be
# inherited with this active-portal fixture; only the fixture helpers are reused.
for _name in list(vars(base_tests.UpgradeSafetyTests)):
    if _name.startswith('test_'):
        setattr(NotificationsUpgradeTests, _name, None)
