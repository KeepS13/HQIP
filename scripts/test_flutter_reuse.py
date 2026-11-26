import json
import os
import unittest
from unittest.mock import patch

from deploy_flutter_preview import reusable_source


class ReuseTests(unittest.TestCase):
    def test_only_green_main_builds_in_current_history_are_reusable(self):
        run = dict(event='workflow_dispatch', head_branch='main', path='.github/workflows/flutter-preview.yml',
                   status='completed', head_sha='a' * 40)
        jobs = {'jobs': [dict(name='build', conclusion='success')]}
        ci = {'workflow_runs': [dict(head_sha='a' * 40, head_branch='main', conclusion='success')]}
        with patch.dict(os.environ, GITHUB_REPOSITORY='KeepS13/HQIP'):
            for bad in [None, 'fork', 'failed-build', 'unrelated', 'red-ci']:
                source = dict(run)
                if bad == 'fork':
                    source['event'] = 'pull_request'
                replies = [source, {'jobs': []} if bad == 'failed-build' else jobs,
                           {'status': 'diverged' if bad == 'unrelated' else 'ahead'},
                           {'workflow_runs': []} if bad == 'red-ci' else ci]
                with patch('subprocess.check_output', side_effect=[json.dumps(r) for r in replies]):
                    if bad:
                        with self.assertRaises(ValueError):
                            reusable_source('123', 'b' * 40)
                    else:
                        self.assertEqual(reusable_source('123', 'b' * 40), 'a' * 40)


if __name__ == '__main__':
    unittest.main()
