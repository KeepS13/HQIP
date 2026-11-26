"""Same-run, private draft fallback when Actions artifact storage is unavailable."""
import argparse
import json
import os
from pathlib import Path
import subprocess


def gh(*args):
    return subprocess.check_output(['gh', *args], text=True).strip()


def transfer(action):
    if os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        raise ValueError('Preview drafts are restricted to explicitly dispatched main builds')
    sha = os.environ['GITHUB_SHA']
    tag = 'ci-flutter-preview-' + os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT']
    repo = os.environ['GITHUB_REPOSITORY']
    if action == 'upload':
        gh('release', 'create', tag, '--repo', repo, '--draft', '--target', sha, '--title', tag,
           '--notes', 'Temporary private transport for the signed Flutter preview; removed after acceptance.')
        gh('release', 'upload', tag, 'dist/flutter-preview-publication.tar.gz', '--repo', repo)
    else:
        info = json.loads(gh('release', 'view', tag, '--repo', repo, '--json', 'isDraft,targetCommitish,assets'))
        if not info['isDraft'] or info['targetCommitish'] != sha:
            raise ValueError('Draft provenance mismatch')
        if action == 'download':
            Path('dist').mkdir(exist_ok=True)
            gh('release', 'download', tag, '--repo', repo, '--pattern', 'flutter-preview-publication.tar.gz', '--dir', 'dist')
        else:
            gh('release', 'delete', tag, '--repo', repo, '--yes')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['upload', 'download', 'cleanup'])
    transfer(parser.parse_args().action)
