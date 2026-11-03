import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import deploy_nginx as deploy


class DeploymentSafetyTests(unittest.TestCase):
    def test_release_verifies_every_artifact_and_rejects_path_escape(self):
        with tempfile.TemporaryDirectory() as folder:
            root = pathlib.Path(folder)
            names = ['hqip', 'artifacts/manifest.json', 'artifacts/agent-linux-amd64',
                     'artifacts/agent-linux-arm64', 'artifacts/hysteria-linux-amd64',
                     'artifacts/hysteria-linux-arm64', 'deploy/hqip-control.service', 'deploy/nginx-hqip.conf']
            for name in names:
                target = root/name
                target.parent.mkdir(exist_ok=True, parents=True)
                target.write_bytes(b'release-artifact')
            manifest = {'sourceSHA':'a'*40, 'sha256':{n:deploy.digest(root/n) for n in names}}
            (root/'release.json').write_text(json.dumps(manifest))
            self.assertEqual(deploy.validate_release(root)['sourceSHA'], 'a'*40)
            (root/'hqip').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'checksum'): deploy.validate_release(root)
            (root/'hqip').write_bytes(b'release-artifact')
            manifest['sha256']['../outside']='b'*64
            (root/'release.json').write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'checksum'): deploy.validate_release(root)

    def test_activation_failure_runs_rollback(self):
        with tempfile.TemporaryDirectory() as folder:
            root=pathlib.Path(folder)
            site=root/'site.conf';site.write_text('legacy')
            record={'phase':'prepared','nginxSHA':deploy.digest(site)}
            with patch.object(deploy,'SITE',site), patch.object(deploy,'run'), patch.object(deploy,'database_backup',side_effect=RuntimeError('backup failed')), patch.object(deploy,'rollback') as rollback:
                with self.assertRaisesRegex(RuntimeError,'backup failed'): deploy.activate(root,record)
                rollback.assert_called_once_with(root,record)
            self.assertEqual(site.read_text(),'legacy')

    def test_changed_site_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as folder:
            site=pathlib.Path(folder)/'site';site.write_text('changed')
            with patch.object(deploy,'SITE',site), patch.object(deploy,'run') as command:
                with self.assertRaises(ValueError): deploy.activate(pathlib.Path(folder),{'phase':'prepared','nginxSHA':'old'})
                command.assert_not_called()


if __name__=='__main__': unittest.main()
