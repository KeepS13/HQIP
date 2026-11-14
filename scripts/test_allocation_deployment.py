import json
from pathlib import Path
from types import SimpleNamespace
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import upgrade_domains as deploy


class AllocationDeploymentTests(unittest.TestCase):
    def test_migration_environment_requires_same_database_and_migration_role(self):
        app = {'HQIP_DATABASE_URL': 'postgres://hqip_app:test@127.0.0.1:15432/hqip?sslmode=disable'}
        migration = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator:test@127.0.0.1:15432/hqip?sslmode=disable'}
        with patch.object(deploy, 'postgres_enabled', return_value=True), patch.object(deploy, 'postgres_info', return_value={}):
            with patch.object(deploy, 'storage_environment', side_effect=[migration, app]):
                self.assertEqual(deploy.allocation_migration_environment({'subscriptionAllocationsVersion': 1}), migration)
            for bad in ['postgres://hqip_migrator:test@127.0.0.1:15432/other?sslmode=disable', app['HQIP_DATABASE_URL']]:
                with patch.object(deploy, 'storage_environment', side_effect=[{'HQIP_DATABASE_URL': bad}, app]):
                    with self.assertRaisesRegex(ValueError, 'active PostgreSQL'):
                        deploy.allocation_migration_environment({'subscriptionAllocationsVersion': 1})

    def test_current_schema_does_not_require_migration_credentials(self):
        with patch.object(deploy, 'postgres_enabled', return_value=True), patch.object(deploy, 'postgres_info', return_value={'subscriptionAllocationsVersion': 1}), patch.object(deploy, 'storage_environment') as read:
            self.assertIsNone(deploy.allocation_migration_environment({'subscriptionAllocationsVersion': 1}))
            read.assert_not_called()

    def test_attempt_is_durable_before_command_and_errors_do_not_leak_credentials(self):
        for result in [SimpleNamespace(returncode=0), SimpleNamespace(returncode=1), subprocess.TimeoutExpired('migration', 330)]:
            with self.subTest(result=result), tempfile.TemporaryDirectory() as folder:
                state, info = Path(folder), {}
                def run(*args, **kwargs):
                    self.assertTrue(json.loads((state/'domains-state.json').read_text())['allocationMigrationAttempted'])
                    self.assertEqual(args[0], [str(Path('/release/hqip')), 'migrate-subscription-allocations'])
                    self.assertEqual(kwargs['env'], {'HQIP_DATABASE_URL': 'private-value'})
                    self.assertTrue(kwargs['capture_output'])
                    if isinstance(result, Exception):
                        raise result
                    return result
                with patch.object(deploy.subprocess, 'run', side_effect=run):
                    if isinstance(result, Exception) or result.returncode:
                        with self.assertRaisesRegex(RuntimeError, 'controller remains stopped'):
                            deploy.migrate_allocations(Path('/release'), {'HQIP_DATABASE_URL': 'private-value'}, state, info)
                    else:
                        deploy.migrate_allocations(Path('/release'), {'HQIP_DATABASE_URL': 'private-value'}, state, info)
                        self.assertTrue(info['allocationMigrationCompleted'])

    def test_rollback_refuses_old_controller_after_attempt_even_before_switch(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            state, previous = root/'backup', root/'previous'
            state.mkdir()
            previous.mkdir()
            (root/'current').mkdir()
            info = {'previous': str(root/'current'), 'release': str(previous), 'allocationMigrationAttempted': True, 'allocationTarget': 1}
            (state/'domains-state.json').write_text(json.dumps(info))
            with patch.object(deploy, 'check_state'), patch.object(deploy.control, 'BASE', root), patch.object(deploy.control, 'verify_release', return_value=(previous, {})), patch.object(deploy.control, 'run') as run:
                with self.assertRaisesRegex(ValueError, 'prevents rollback'):
                    deploy.rollback(state)
                run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
