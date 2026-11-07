import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import upgrade_domains as deploy


class PostgresReleaseTests(unittest.TestCase):
    def test_postgres_marker_blocks_sqlite_rollback_without_opening_sqlite(self):
        with tempfile.TemporaryDirectory() as folder:
            data=Path(folder)
            (data/'postgresql.enabled').write_text('1\n')
            with patch.object(deploy.control,'DATA',data), patch.object(deploy.sqlite3,'connect') as sqlite:
                with self.assertRaisesRegex(ValueError,'PostgreSQL-compatible'):
                    deploy.require_billing_compatible({'schemaVersion':5,'serverBillingVersion':2})
                deploy.require_billing_compatible({'postgresSchemaVersion':1,'serverBillingVersion':2})
                deploy.disable_feishu()
                sqlite.assert_not_called()

    def test_environment_is_parsed_without_shell_expansion_or_output(self):
        with tempfile.TemporaryDirectory() as folder:
            file=Path(folder)/'storage.env'
            file.write_text('HQIP_DATABASE_URL="postgres://user:literal$abc@127.0.0.1/hqip"\nHQIP_REDIS_URL="redis://:secret@127.0.0.1:6379"\n')
            file.chmod(0o600)
            with patch.object(deploy,'STORAGE_ENV',file):
                env=deploy.storage_environment()
                self.assertIn('literal$abc',env['HQIP_DATABASE_URL'])
                file.write_text('HQIP_DATABASE_URL=x\nPATH=/malicious\n')
                with self.assertRaisesRegex(ValueError,'Unexpected'):
                    deploy.storage_environment()

    def test_postgres_probe_uses_binary_and_does_not_leak_errors(self):
        with patch.object(deploy,'storage_environment',return_value={'HQIP_DATABASE_URL':'secret'}), patch.object(deploy.subprocess,'run') as run:
            run.return_value=SimpleNamespace(returncode=1,stderr='postgres://secret')
            with self.assertRaisesRegex(ValueError,'probe failed') as error:
                deploy.postgres_info()
            self.assertNotIn('secret',str(error.exception))
            self.assertEqual(run.call_args.kwargs['env'],{'HQIP_DATABASE_URL':'secret'})
            self.assertNotIn('secret',run.call_args.args[0])
            run.return_value=SimpleNamespace(returncode=0,stdout=json.dumps({'backend':'postgresql','postgresSchemaVersion':1,'schemaVersion':5}))
            self.assertEqual(deploy.postgres_info()['schemaVersion'],5)


if __name__=='__main__':unittest.main()
