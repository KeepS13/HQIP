"""Control-host runtime and PG18 selection on hosts with older system tools."""
from pathlib import Path
import shlex
import tempfile
import unittest
from unittest.mock import patch

import fresh_deploy
import restore_postgres


class DeploymentRuntimeTests(unittest.TestCase):
    def test_scheduled_retry_and_abnormal_exit_use_independent_python(self):
        root = Path(__file__).resolve().parents[1]
        for unit, options in (('hqip-backup.service', []), ('hqip-backup-retry.service', ['--retry'])):
            with self.subTest(unit=unit):
                settings = dict(line.split('=', 1) for line in (root / 'deploy' / unit).read_text().splitlines()
                                if line.startswith(('ExecStart=', 'ExecStopPost=')))
                command = ['/opt/hqip-runtime/bin/python3', '-B',
                           '/opt/hqip-control/current/scripts/automatic_backup.py']
                self.assertEqual(shlex.split(settings['ExecStart']), command + options)
                self.assertEqual(shlex.split(settings['ExecStopPost']), command + ['--record-abnormal'])

    def test_fresh_backup_environment_selects_pg18_without_ambient_credentials(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'storage.env'
            path.write_text('HQIP_DATABASE_URL=postgres://hqip_app:test@127.0.0.1:15432/hqip\n'
                            'HQIP_REDIS_URL=redis://hqip:test@127.0.0.1:16379\n')
            path.chmod(0o600)
            with patch.dict(fresh_deploy.os.environ, {'PATH': '/untrusted', 'PGPASSWORD': 'unrelated'}):
                env = fresh_deploy.environment(path, 'hqip_app')
            self.assertEqual(env['PATH'], '/usr/lib/postgresql/18/bin:/usr/bin:/bin')
            self.assertNotIn('PGPASSWORD', env)

    def test_recovery_checks_explicit_pg18_tools_before_target_access(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            backup = root / 'backup.tar.gz'
            backup.write_bytes(b'test archive')
            with patch.object(restore_postgres, 'verify_full_release', return_value={}), \
                    patch.object(restore_postgres, 'environment', return_value='test-only'), \
                    patch.object(restore_postgres, 'run', side_effect=['pg_dump (PostgreSQL) 18.1',
                                                                     'pg_restore (PostgreSQL) 18.1']) as run, \
                    patch.object(restore_postgres, 'target_facts', side_effect=ValueError('target gate')):
                with self.assertRaisesRegex(ValueError, 'target gate'):
                    restore_postgres.restore(root, backup, restore_postgres.digest(backup), root / 'data',
                                             root / 'migration.env', root / 'storage.env', 'hqip_recovery_test')
            self.assertEqual([call.args[0] for call in run.call_args_list],
                             [['/usr/lib/postgresql/18/bin/pg_dump', '--version'],
                              ['/usr/lib/postgresql/18/bin/pg_restore', '--version']])
            self.assertFalse((root / 'data').exists())


if __name__ == '__main__':
    unittest.main()
