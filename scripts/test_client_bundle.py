"""Real files exercise source mixing, tampering and exact client payload limits."""
import base64
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
import client_bundle as bundle


def client_fixture(root, sha, platforms=None, key=None):
    root.mkdir(parents=True, exist_ok=True)
    downloads, envelopes = [], []
    public = key.public_key().public_bytes_raw().hex() if key else bundle.CLIENT_PUBLIC_KEY
    for platform in platforms or bundle.CLIENT_PLATFORMS:
        update_name, download_name, label = bundle.CLIENT_PLATFORMS[platform]
        items = []
        for name in (update_name, download_name):
            data = ('binary fixture:' + name).encode()
            checksum = bundle.hashlib.sha256(data).hexdigest()
            target = root / 'godot' / checksum / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
            items.append(dict(platform=platform, filename=name, version='5.0.0-preview.17', sequence=5000017,
                              sha256=checksum, size=len(data)))
        update, delivery = items
        update.update(schema=1, apiMin=3, apiMax=3)
        raw = json.dumps(update).encode()
        signature = key.sign(raw) if key else b'0' * 64
        envelopes.append(dict(payload=base64.b64encode(raw).decode(), signature=base64.b64encode(signature).decode()))
        downloads.append(dict(delivery, name=label))
    (root / 'godot-downloads.json').write_text(json.dumps(dict(sourceSHA=sha, dirty=False, publicKey=public, releases=downloads)), encoding='utf-8')
    (root / 'godot-releases.json').write_text(json.dumps(dict(releases=envelopes)), encoding='utf-8')
    return downloads


class ClientBundleTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.sha = 'a' * 40
        self.key = Ed25519PrivateKey.generate()
        self.items = client_fixture(self.root, self.sha, key=self.key)
        public = patch.object(bundle, 'CLIENT_PUBLIC_KEY', self.key.public_key().public_bytes_raw().hex())
        public.start()
        self.addCleanup(public.stop)

    def test_signed_complete_release_and_same_source(self):
        self.assertEqual(len(bundle.verify_client_bundle(self.root, self.sha, signatures=True)), 6)
        with self.assertRaisesRegex(ValueError, 'source'):
            bundle.verify_client_bundle(self.root, 'b' * 40)

    def test_tampering_and_extra_files_fail(self):
        (self.root / 'unexpected-private-file').write_text('must not ship')
        with self.assertRaisesRegex(ValueError, 'exactly'):
            bundle.verify_client_bundle(self.root, self.sha)
        (self.root / 'unexpected-private-file').unlink()
        item = self.items[0]
        (self.root / 'godot' / item['sha256'] / item['filename']).write_bytes(b'x' * item['size'])
        with self.assertRaisesRegex(ValueError, 'checksum'):
            bundle.verify_client_bundle(self.root, self.sha)

    def test_signature_and_mixed_platform_versions_fail(self):
        path = self.root / 'godot-releases.json'
        catalog = json.loads(path.read_text())
        catalog['releases'][0]['signature'] = base64.b64encode(b'x' * 64).decode()
        path.write_text(json.dumps(catalog))
        from cryptography.exceptions import InvalidSignature
        with self.assertRaises(InvalidSignature):
            bundle.verify_client_bundle(self.root, self.sha, signatures=True)
        path = self.root / 'godot-downloads.json'
        manifest = json.loads(path.read_text())
        manifest['releases'][0]['sequence'] += 1
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'version mismatch'):
            bundle.verify_client_bundle(self.root, self.sha)

    def test_shard_transport_round_trip_checks_signature(self):
        from ci_artifacts import package, unpack, digest
        source = self.root / 'workspace'
        shard = source / 'dist/client-android-arm64'
        client_fixture(shard, self.sha, ['android-arm64'], self.key)
        archive = self.root / 'transport.zip'
        package('client-android-arm64', source, archive, self.sha)
        receipt = dict(run='12', sourceSHA=self.sha, kind='client-android-arm64', size=archive.stat().st_size, sha256=digest(archive))
        unpack('client-android-arm64', archive, receipt, self.root / 'received', '12', self.sha)


if __name__ == '__main__':
    unittest.main()
