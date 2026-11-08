"""Recovery safety: exact target/role, private credentials and post-restore ACLs."""
from pathlib import Path
import json
import os
import stat
import tempfile
import unittest
from unittest.mock import patch

import restore_postgres as recovery

DATABASE = 'hqip_recovery_test'
DSN = 'postgres://hqip_migrator:test-only@127.0.0.1:15432/' + DATABASE + '?sslmode=disable'


def safe_target():
    return {'role': 'hqip_migrator', 'database': DATABASE, 'owner': 'hqip_migrator',
            'schemas': ['public'], 'schemaOwner': 'hqip_migrator', 'objects': 60,
            'foreignOwners': 0, 'unsafeRoles': 0, 'appMember': False,
            'appDatabaseCreate': False, 'appDatabaseTemp': False}


def effective_permissions():
    return {'usage': True, 'create': False, 'missingTables': 0, 'unsafeTables': 0, 'unsafeSequences': 0,
            'defaults': [['r',name] for name in ('SELECT','INSERT','UPDATE','DELETE')] + [['S','USAGE'],['S','SELECT']]}


class PostgresRecoveryTests(unittest.TestCase):
    def test_endpoint_and_explicit_database_must_match_restricted_role(self):
        self.assertEqual(recovery.config(DSN, 'hqip_migrator', DATABASE)['dbname'], DATABASE)
        for address, role, database in ((DSN, 'hqip_app', DATABASE), (DSN, 'hqip_migrator', 'hqip'),
                (DSN.replace('127.0.0.1','remote.example'), 'hqip_migrator', DATABASE),
                (DSN.replace('hqip_migrator','postgres'), 'postgres', DATABASE),
                (DSN + '&options=arbitrary', 'hqip_migrator', DATABASE),
                (DSN, 'hqip_migrator', "hqip';DROP DATABASE hqip;")):
            with self.subTest(role=role, database=database), self.assertRaises(ValueError):
                recovery.config(address, role, database)

    def test_psql_credentials_use_private_service_file_not_arguments_or_environment(self):
        files = []

        def run(arguments, **options):
            self.assertEqual(arguments[0], '/usr/bin/psql')
            self.assertNotIn('test-only', repr(arguments))
            self.assertNotIn('test-only', repr(options['env']))
            self.assertNotIn('HQIP_DATABASE_URL', options['env'])
            self.assertNotIn('PGPASSWORD', options['env'])
            service = Path(options['env']['PGSERVICEFILE'])
            files.append(service)
            self.assertIn('user=hqip_migrator\n', service.read_text())
            self.assertIn('password=test-only\n', service.read_text())
            if os.name != 'nt':
                self.assertEqual(stat.S_IMODE(service.stat().st_mode), 0o600)
            return '1'

        with patch.object(recovery, 'run', side_effect=run):
            self.assertEqual(recovery.sql(DSN, 'hqip_migrator', DATABASE, 'SELECT 1;'), '1')
        self.assertTrue(files)
        self.assertTrue(all(not file.exists() for file in files))

    def test_private_environment_rejects_duplicates_and_unrelated_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve() / 'migration.env'
            path.write_text('HQIP_DATABASE_URL=' + DSN + '\n')
            path.chmod(0o600)
            self.assertEqual(recovery.environment(path, 'hqip_migrator', DATABASE), DSN)
            for extra in ('HQIP_DATABASE_URL=' + DSN, 'PGPASSWORD=not-allowed', 'PATH=/malicious'):
                path.write_text('HQIP_DATABASE_URL=' + DSN + '\n' + extra + '\n')
                with self.assertRaises(ValueError):
                    recovery.environment(path, 'hqip_migrator', DATABASE)

    def test_schema_and_role_safety_rejected_before_any_grant(self):
        for key, bad in (('role','postgres'),('database','hqip'),('owner','postgres'),
                ('schemas',['public','other']),('schemaOwner','other'),('foreignOwners',1),
                ('unsafeRoles',1),('appMember',True),('appDatabaseCreate',True),('appDatabaseTemp',True)):
            value = {**safe_target(), key: bad}
            with self.subTest(field=key), patch.object(recovery, 'target_facts', return_value=value), \
                    patch.object(recovery, 'sql') as sql, self.assertRaises(ValueError):
                recovery.restore_permissions(DSN, DATABASE)
            sql.assert_not_called()

    def test_expected_schema_and_recovery_hold_are_required(self):
        for value in ({'schema':'5','payments':'1','recoveryHold':'false'},
                      {'schema':'4','payments':'1','recoveryHold':'true'},
                      {'schema':'5','payments':'0','recoveryHold':'true'}):
            with patch.object(recovery, 'target_facts', return_value=safe_target()), \
                    patch.object(recovery, 'sql', return_value=json.dumps(value)) as sql, self.assertRaises(ValueError):
                recovery.restore_permissions(DSN, DATABASE)
            self.assertEqual(sql.call_count, 1)
            self.assertNotIn('GRANT', sql.call_args.args[3])

    def test_restored_schema_gets_existing_and_future_grants_under_migrator(self):
        identity = {'schema':'5','payments':'1','recoveryHold':'true'}
        with patch.object(recovery, 'target_facts', return_value=safe_target()), \
                patch.object(recovery, 'sql', side_effect=[json.dumps(identity), '', json.dumps(effective_permissions())]) as sql:
            self.assertEqual(recovery.restore_permissions(DSN, DATABASE), identity)
        self.assertEqual(sql.call_count, 3)
        grant = sql.call_args_list[1].args
        self.assertEqual(grant[:3], (DSN, 'hqip_migrator', DATABASE))
        self.assertIn('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC,hqip_app;', grant[3])
        self.assertIn('GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO hqip_app;', grant[3])
        self.assertIn('GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO hqip_app;', grant[3])
        self.assertIn('ALTER DEFAULT PRIVILEGES FOR ROLE hqip_migrator', grant[3])
        self.assertNotIn('ALTER ROLE', grant[3])
        self.assertNotIn('GRANT ALL', grant[3])

    def test_excessive_or_missing_effective_grants_fail_verification(self):
        identity = {'schema':'5','payments':'1','recoveryHold':'true'}
        for key, bad in (('usage',False),('create',True),('missingTables',1),('unsafeTables',1),
                         ('unsafeSequences',1),('defaults',[])):
            with self.subTest(field=key), patch.object(recovery, 'target_facts', return_value=safe_target()), \
                    patch.object(recovery, 'sql', side_effect=[json.dumps(identity), '', json.dumps({**effective_permissions(), key:bad})]), \
                    self.assertRaises(ValueError):
                recovery.restore_permissions(DSN, DATABASE)

    def test_nonempty_data_and_checksum_mismatch_stop_before_external_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            backup = root / 'backup.tar.gz'
            backup.write_bytes(b'preserve-source')
            data = root / 'data'
            data.mkdir()
            (data / 'master.key').write_bytes(b'existing-key')
            with patch.object(recovery, 'run') as run, patch.object(recovery, 'verify_full_release') as verify:
                for checksum in ('0' * 64, recovery.digest(backup)):
                    with self.assertRaises(ValueError):
                        recovery.restore(root, backup, checksum, data, root/'migration.env', root/'app.env', DATABASE)
                run.assert_not_called()
                verify.assert_not_called()
            self.assertEqual((data/'master.key').read_bytes(), b'existing-key')
            self.assertEqual(backup.read_bytes(), b'preserve-source')


if __name__ == '__main__':
    unittest.main()
