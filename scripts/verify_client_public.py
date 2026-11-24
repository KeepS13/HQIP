"""Acceptance gate: public catalog, signature, download identity and HTTP ranges."""
import json
import os
from pathlib import Path
import subprocess

from client_bundle import verify_client_bundle


def fetch(path, *, byte_range=False):
    args = ['curl', '--fail', '--silent', '--show-error', '--retry', '3', '--retry-all-errors',
            '--max-time', '45', '--user-agent', 'Go-http-client/1.1']
    if byte_range:
        args += ['--range', '0-31']
    return subprocess.check_output([*args, 'https://hqip.sharevps.online' + path])


def main():
    root = Path('dist/client-release')
    verify_client_bundle(root, os.environ['GITHUB_SHA'], signatures=True)
    expected = json.loads((root / 'godot-releases.json').read_text(encoding='utf-8'))
    if json.loads(fetch('/api/v3/client/releases')) != expected:
        raise ValueError('Public update catalog differs from the tested release')
    delivered = json.loads(fetch('/api/v1/user/installers'))['releases']
    downloads = json.loads((root / 'godot-downloads.json').read_text(encoding='utf-8'))['releases']
    if len(delivered) != len(downloads):
        raise ValueError('Public download platform set mismatch')
    for item in downloads:
        match = [r for r in delivered if r['platform'] == item['platform']]
        path = '/downloads/client/' + item['sha256'] + '/' + item['filename']
        if len(match) != 1 or any(match[0].get(k) != item[k] for k in ('sha256', 'size', 'version', 'filename')):
            raise ValueError('Public installer metadata mismatch')
        if match[0]['url'] != 'https://hqip.sharevps.online' + path:
            raise ValueError('Unexpected installer URL')
        source = root / 'godot' / item['sha256'] / item['filename']
        with source.open('rb') as stream:
            expected_bytes = stream.read(32)
        if fetch(path, byte_range=True) != expected_bytes:
            raise ValueError('Public installer range does not match verified payload')
    print('Accepted public signed update channel and all three installer downloads.')


if __name__ == '__main__':
    main()
