"""Exercise the deployment boundary with real manifests and archives."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import install_ci_release as deploy


class CIReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.current = self.root / 'current'
        self.current.mkdir()
        (self.current / 'artifacts').mkdir()
        (self.current / 'artifacts/node').write_bytes(b'existing-node')
        self.previous = {'sourceSHA': 'a' * 40, 'sha256': {
            'artifacts/node': hashlib.sha256(b'existing-node').hexdigest()}}
        (self.current / 'release.json').write_text(json.dumps(self.previous))
        self.manifest = {'sourceSHA': 'b' * 40, 'previousSourceSHA': 'a' * 40,
                         'upgradeKind': 'domains-alerts', 'sha256': {
                             **self.previous['sha256'], 'hqip': hashlib.sha256(b'new-controller').hexdigest()}}
        self.payload = {'hqip': b'new-controller'}
        self.release = self.root / 'release'

    def archive(self, extra=None):
        path = self.root / 'release.tar.gz'
        with tarfile.open(path, 'w:gz') as package:
            for name, data in {**self.payload, 'release.json': json.dumps(self.manifest).encode()}.items():
                member = tarfile.TarInfo(name)
                member.size = len(data)
                package.addfile(member, io.BytesIO(data))
            if extra:
                package.addfile(extra)
        return path

    def install(self, path=None, checksum=None):
        path = path or self.archive()
        deploy.materialize(path, checksum or deploy.digest(path), 'b' * 40, self.current, self.release)

    def test_overlay_reuses_only_verified_artifacts(self):
        self.install()
        self.assertEqual((self.release / 'hqip').read_bytes(), b'new-controller')
        self.assertEqual((self.release / 'artifacts/node').read_bytes(), b'existing-node')
        self.assertEqual(json.loads((self.release / 'release.json').read_text()), self.manifest)
        self.assertEqual((self.current / 'artifacts/node').read_bytes(), b'existing-node')

    def test_changed_base_refused_before_new_directory(self):
        self.manifest['previousSourceSHA'] = 'c' * 40
        with self.assertRaisesRegex(ValueError, 'active commit'):
            self.install()
        self.assertFalse(self.release.exists())

    def test_archive_checksum_checked_before_extraction(self):
        with self.assertRaisesRegex(ValueError, 'archive checksum'):
            self.install(checksum='0' * 64)
        self.assertFalse(self.release.exists())

    def test_corrupted_inherited_file_refused(self):
        (self.current / 'artifacts/node').write_bytes(b'corrupted')
        with self.assertRaisesRegex(ValueError, 'unverified production file'):
            self.install()

    def test_corrupted_uploaded_file_refused(self):
        self.payload['hqip'] = b'corrupted'
        with self.assertRaisesRegex(ValueError, 'Payload checksum'):
            self.install()

    def test_existing_release_is_never_overwritten(self):
        self.release.mkdir()
        (self.release / 'important').write_bytes(b'keep')
        with self.assertRaises(FileExistsError):
            self.install()
        self.assertEqual((self.release / 'important').read_bytes(), b'keep')

    def test_path_escape_is_rejected(self):
        for name in ['../escape', '/absolute', 'a/../../escape', 'a\\escape', './hqip']:
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'Unsafe payload path'):
                self.install(self.archive(tarfile.TarInfo(name)))
        self.assertFalse(self.release.exists())

    def test_symlink_and_duplicate_archive_members_are_rejected(self):
        for member in [tarfile.TarInfo('hqip'), tarfile.TarInfo('link')]:
            if member.name == 'link':
                member.type = tarfile.SYMTYPE
                member.linkname = '/etc/passwd'
            with self.subTest(name=member.name), self.assertRaisesRegex(ValueError, 'unique ordinary'):
                self.install(self.archive(member))
        self.assertFalse(self.release.exists())


if __name__ == '__main__':
    unittest.main()
