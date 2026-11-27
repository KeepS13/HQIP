from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

import announcements_deployment as announcements


class AnnouncementsDeploymentTests(unittest.TestCase):
    def test_checks_role_database_and_existing_version(self):
        app = {'HQIP_DATABASE_URL': 'postgres://hqip_app:test@127.0.0.1/hqip'}
        migration = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator:test@127.0.0.1/hqip'}
        deploy = SimpleNamespace(postgres_enabled=lambda: True, postgres_info=lambda: {},
                                 MIGRATION_ENV=Path('/migration'), storage_environment=lambda path=None: migration if path else app)
        self.assertEqual(announcements.prepare(deploy, {'announcementsVersion': 1}), migration)
        for invalid in ['postgres://hqip_app:test@127.0.0.1/hqip', 'postgres://hqip_migrator:test@127.0.0.1/other']:
            migration['HQIP_DATABASE_URL'] = invalid
            with self.assertRaises(ValueError):
                announcements.prepare(deploy, {'announcementsVersion': 1})
        deploy.postgres_info = lambda: {'announcementsVersion': 1}
        self.assertIsNone(announcements.prepare(deploy, {'announcementsVersion': 1}))

    def test_migration_is_noop_or_checked_command(self):
        run = Mock(return_value=SimpleNamespace(returncode=0))
        announcements.migrate(Path('/release'), None, run)
        run.assert_not_called()
        announcements.migrate(Path('/release'), {'test': 'environment'}, run)
        self.assertEqual(run.call_args.args[0], [str(Path('/release/hqip')), 'migrate-announcements'])
        run.return_value.returncode = 1
        with self.assertRaisesRegex(RuntimeError, 'live data is preserved'):
            announcements.migrate(Path('/release'), {'test': 'environment'}, run)

    def test_release_packages_and_migrates_before_switch(self):
        from release_bundle import RELEASE_SCRIPTS
        self.assertIn('announcements_deployment.py', RELEASE_SCRIPTS)
        source = Path(__file__).with_name('upgrade_domains.py').read_text(encoding='utf-8')
        body = source[source.index('def upgrade('):]
        self.assertLess(body.index('migrate_announcements(release'), body.index('control.point_current(release)'))
