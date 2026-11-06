import hashlib
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from deploy_ci import copy_remote_manifest


class ManifestTransferTests(unittest.TestCase):
    def test_windows_built_manifest_keeps_original_hash_over_ssh(self):
        raw = b'{\r\n  "sha256": {"agent": "pinned"}\r\n}\r\n'
        remote = Mock(return_value=SimpleNamespace(stdout=raw))
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder)/'manifest.json'
            copy_remote_manifest(remote, '/release/manifest.json', output)
            self.assertEqual(hashlib.sha256(output.read_bytes()).digest(), hashlib.sha256(raw).digest())
        remote.assert_called_once_with(['cat', '/release/manifest.json'], capture_output=True)

    def test_text_transport_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder)/'manifest.json'
            with self.assertRaises(TypeError):
                copy_remote_manifest(Mock(return_value=SimpleNamespace(stdout='{}\n')), '/manifest', output)
            self.assertFalse(output.exists())


if __name__ == '__main__':
    unittest.main()
