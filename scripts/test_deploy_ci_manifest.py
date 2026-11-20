import hashlib
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from deploy_ci import copy_remote_manifest, check_public


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

    def test_public_checks_only_current_official_domains(self):
        urls = []

        def response(args, **kwargs):
            urls.append(args[-1])
            return SimpleNamespace(stdout='{"ok":true}' if args[-1].endswith(('/healthz', '/readyz')) else '<!doctype html>')

        with patch('deploy_ci.command', side_effect=response):
            check_public()
        self.assertEqual(urls, ['https://hqip.sharevps.online/',
                                'https://cms-hqip.sharevps.online/',
                                'https://hqip.sharevps.online/healthz',
                                'https://hqip.sharevps.online/readyz'])

    def test_alive_process_with_unavailable_storage_rejects_release(self):
        def response(args, **kwargs):
            return SimpleNamespace(stdout='{"ok":false}' if args[-1].endswith('/readyz') else '{"ok":true}')
        with patch('deploy_ci.command', side_effect=response):
            with self.assertRaisesRegex(ValueError, 'Public API is unhealthy'):
                check_public()

    def test_public_api_business_failure_rejects_release(self):
        with patch('deploy_ci.command', return_value=SimpleNamespace(stdout='{"ok":false}')):
            with self.assertRaisesRegex(ValueError, 'Public API is unhealthy'):
                check_public()

    def test_workflow_uses_new_environment_url_and_secret_selected_host(self):
        workflow = (Path(__file__).resolve().parents[1]/'.github/workflows/ci.yml').read_text(encoding='utf-8')
        self.assertIn('url: https://hqip.sharevps.online', workflow)
        self.assertNotIn('5202345.xyz', workflow)
        for setting in ['HOST', 'PORT', 'USER', 'SSH_KEY', 'KNOWN_HOSTS']:
            name = 'DEPLOY_' + setting
            self.assertIn(name + ': ${{ secrets.' + name + ' }}', workflow)


if __name__ == '__main__':
    unittest.main()
