from pathlib import Path
import hashlib
import tempfile
import unittest
from urllib.parse import urlparse

from prepare_storage import prepare


class StoragePreparationTests(unittest.TestCase):
    def test_separate_credentials_and_acl_are_consistent_without_overwrite(self):
        with tempfile.TemporaryDirectory() as root:
            path=prepare(Path(root)/'storage')
            app=dict(line.split('=',1) for line in (path/'storage.env').read_text().splitlines())
            migration=dict(line.split('=',1) for line in (path/'migration.env').read_text().splitlines())
            self.assertNotEqual(app['HQIP_DATABASE_URL'],migration['HQIP_DATABASE_URL'])
            url=urlparse(app['HQIP_REDIS_URL'].strip('"'))
            acl=(path/'secrets/redis-acl').read_text()
            self.assertIn(hashlib.sha256(url.password.encode()).hexdigest(),acl)
            self.assertNotIn(url.password,acl)
            self.assertIn('-@all',acl)
            self.assertIn('~hqip:rate:*',acl)
            with self.assertRaises(FileExistsError):prepare(path)
            self.assertEqual(app['HQIP_REDIS_URL'],dict(line.split('=',1) for line in (path/'storage.env').read_text().splitlines())['HQIP_REDIS_URL'])


if __name__=='__main__':unittest.main()
