from contextlib import ExitStack
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
import json
import tempfile
import unittest

import operations_deployment as operations
import upgrade_domains as deploy
import fresh_deploy


class OperationsDeploymentTests(unittest.TestCase):
    def test_migration_requires_correct_database_and_migration_role(self):
        app = {'HQIP_DATABASE_URL': 'postgres://app:secret@127.0.0.1/hqip'}
        migration = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator:secret@127.0.0.1/hqip'}
        fake = SimpleNamespace(postgres_enabled=lambda: True, postgres_info=lambda: {},
                               MIGRATION_ENV='migration', storage_environment=lambda path=None: migration if path else app)
        self.assertEqual(operations.prepare(fake, {'operationsVersion': 1}), migration)
        migration['HQIP_DATABASE_URL'] = 'postgres://hqip_migrator:secret@127.0.0.1/other'
        with self.assertRaises(ValueError):
            operations.prepare(fake, {'operationsVersion': 1})

    def run_upgrade(self, failure=None):
        with tempfile.TemporaryDirectory() as folder, ExitStack() as mocks:
            root = Path(folder).resolve()
            previous, release, data, backups = [root / name for name in ['previous','release','data','backups']]
            for path in [previous, release, data, backups]: path.mkdir()
            (release / 'deploy').mkdir()
            for name in ['hqip-control.service','nginx-hqip.conf']: (release / 'deploy' / name).write_text('new')
            site, unit = root / 'site.conf', root / 'unit.service'
            site.write_text('old'); unit.write_text('old')
            state = backups / 'upgrade'
            steps = []
            for name, value in [('BACKUPS',backups),('SITE',site),('UNIT',unit),('PREP',root/'absent')]: mocks.enter_context(patch.object(deploy,name,value))
            mocks.enter_context(patch.object(deploy.control,'DATA',data))
            for name, value in [('verify',(release,{'sourceSHA':'a'*40,'schemaVersion':5,'operationsVersion':1},previous)),('allocation_migration_environment',None),('data_identity','same-key'),('schema_version',5),('postgres_enabled',True),('storage_environment',{})]:
                mocks.enter_context(patch.object(deploy,name,return_value=value))
            for name in ['subscription_migration_ready','disable_feishu','migrate_allocations','require_free_space']:
                mocks.enter_context(patch.object(deploy,name))
            mocks.enter_context(patch.object(operations,'prepare',return_value={}))
            def migration(*_):
                steps.append('migration')
                if failure == 'migration': raise RuntimeError('migration failed')
            mocks.enter_context(patch.object(operations,'migrate',side_effect=migration))
            mocks.enter_context(patch.object(fresh_deploy,'install_log_limits',side_effect=lambda *_: steps.append('prepare-logs')))
            def backup(*args,**kwargs):
                steps.append('backup')
                self.assertNotIn('stop',steps)
                return SimpleNamespace(returncode=1 if failure == 'backup' else 0)
            mocks.enter_context(patch.object(deploy.subprocess,'run',side_effect=backup))
            mocks.enter_context(patch.object(deploy.control,'run',side_effect=lambda *args: steps.append(args[1])))
            mocks.enter_context(patch.object(deploy.control,'point_current',side_effect=lambda *_:steps.append('switch')))
            mocks.enter_context(patch.object(deploy.control,'healthy'))
            rollback=mocks.enter_context(patch.object(deploy,'rollback'))
            if failure:
                with self.assertRaises(RuntimeError): deploy.upgrade(release,state)
                self.assertNotIn('stop',steps)
                rollback.assert_not_called()
                self.assertEqual(json.loads((state/'domains-state.json').read_text())['phase'],'preparation-failed-service-preserved')
            else:
                deploy.upgrade(release,state)
                self.assertLess(steps.index('backup'),steps.index('stop'))
                self.assertLess(steps.index('migration'),steps.index('stop'))
                self.assertLess(steps.index('prepare-logs'),steps.index('stop'))
                self.assertLess(steps.index('stop'),steps.index('switch'))

    def test_online_preparation_finishes_before_service_stop(self): self.run_upgrade()
    def test_backup_failure_keeps_current_service_running(self): self.run_upgrade('backup')
    def test_migration_failure_keeps_current_service_running(self): self.run_upgrade('migration')


if __name__ == '__main__': unittest.main()
