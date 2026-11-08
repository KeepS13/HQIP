"""Exercise the deployment boundary with real manifests and archives."""
import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

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
                         'releaseKind': 'full', 'upgradeKind': 'full-release', 'sha256': {
                             **self.previous['sha256'], 'hqip': hashlib.sha256(b'new-controller').hexdigest()}}
        self.payload = {'hqip': b'new-controller', 'artifacts/node': b'existing-node'}
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

    def test_complete_release_uses_only_packaged_files(self):
        (self.current / 'artifacts/node').write_bytes(b'corrupted-old-file')
        self.install()
        self.assertEqual((self.release / 'hqip').read_bytes(), b'new-controller')
        self.assertEqual((self.release / 'artifacts/node').read_bytes(), b'existing-node')
        self.assertEqual(json.loads((self.release / 'release.json').read_text()), self.manifest)
        self.assertEqual((self.current / 'artifacts/node').read_bytes(), b'corrupted-old-file')

    def test_changed_base_refused_before_new_directory(self):
        self.manifest['previousSourceSHA'] = 'c' * 40
        with self.assertRaisesRegex(ValueError, 'active commit'):
            self.install()
        self.assertFalse(self.release.exists())

    def test_archive_checksum_checked_before_extraction(self):
        with self.assertRaisesRegex(ValueError, 'archive checksum'):
            self.install(checksum='0' * 64)
        self.assertFalse(self.release.exists())

    def test_missing_payload_never_falls_back_to_old_files(self):
        del self.payload['artifacts/node']
        with self.assertRaisesRegex(ValueError, 'Complete archive required'):
            self.install()
        self.assertFalse(self.release.exists())

    def test_fresh_full_release_needs_no_old_directory(self):
        del self.manifest['previousSourceSHA']
        archive = self.archive()
        deploy.materialize(archive, deploy.digest(archive), 'b' * 40, None, self.release)
        self.assertEqual((self.release / 'artifacts/node').read_bytes(), b'existing-node')

    def test_fresh_rejects_release_bound_to_old_deployment(self):
        archive = self.archive()
        with self.assertRaisesRegex(ValueError, 'standalone'):
            deploy.materialize(archive, deploy.digest(archive), 'b' * 40, None, self.release)
        self.assertFalse(self.release.exists())

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

    def test_all_new_directories_are_explicitly_traversable_without_changing_parents(self):
        name = 'admin-web/assets/app.js'
        self.payload[name] = b'web-payload'
        self.manifest['sha256'][name] = hashlib.sha256(self.payload[name]).hexdigest()
        real_chmod = Path.chmod
        changed = []

        def chmod(path, mode, *args, **kwargs):
            changed.append((path, mode))
            return real_chmod(path, mode, *args, **kwargs)

        with patch.object(Path, 'chmod', chmod):
            self.install()
        directories = {self.release, self.release / 'artifacts', self.release / 'admin-web',
                       self.release / 'admin-web/assets'}
        self.assertTrue(directories <= {path for path, mode in changed if mode == 0o755})
        self.assertTrue(all(path == self.release or self.release in path.parents for path, _ in changed))

    @unittest.skipIf(os.name == 'nt', 'POSIX permission bits are enforced by the Linux CI job')
    def test_umask_077_does_not_block_application_or_web_users_after_rename(self):
        name = 'user-web/assets/app.js'
        self.payload[name] = b'web-payload'
        self.manifest['sha256'][name] = hashlib.sha256(self.payload[name]).hexdigest()
        previous_umask = os.umask(0o077)
        try:
            self.install()
        finally:
            os.umask(previous_umask)
        final = self.root / 'final-release'
        self.release.rename(final)
        for path in [final, *(item for item in final.rglob('*') if item.is_dir())]:
            self.assertEqual(path.stat().st_mode & 0o777, 0o755)
        self.assertEqual((final / name).stat().st_mode & 0o777, 0o644)
        self.assertEqual((final / 'hqip').stat().st_mode & 0o777, 0o755)

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
