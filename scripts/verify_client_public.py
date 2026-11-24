"""Acceptance gate: public catalog, signature, download identity and HTTP ranges."""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
from concurrent.futures import ThreadPoolExecutor

from client_bundle import client_digest, verify_client_bundle


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
    # A small range cannot detect a server truncating long transfers. Download
    # every delivery in parallel and hash the actual complete public response.
    transfers = [dict(item, downloadRoute='client') for item in downloads]
    for envelope in expected['releases']:
        update = json.loads(base64.b64decode(envelope['payload']))
        if update['platform'] == 'windows-amd64':
            transfers.append(dict(update, downloadRoute='godot'))
    with ThreadPoolExecutor(max_workers=4) as workers:
        list(workers.map(verify_complete_download, transfers))
    print('Accepted signed channel, three complete installers and the Windows update payload.')


def verify_complete_download(item):
    path = '/downloads/' + item['downloadRoute'] + '/' + item['sha256'] + '/' + item['filename']
    with tempfile.TemporaryDirectory(prefix='hqip-public-download-') as temporary:
        output = Path(temporary) / item['filename']
        subprocess.run(['curl', '--fail', '--silent', '--show-error', '--retry', '2', '--retry-all-errors',
            '--max-time', '600', '--max-filesize', str(item['size']), '--user-agent', 'Go-http-client/1.1', '--output', str(output),
            'https://hqip.sharevps.online' + path], check=True)
        if output.stat().st_size != item['size'] or client_digest(output) != item['sha256']:
            raise ValueError('Complete public download checksum mismatch: ' + item['filename'])


if __name__ == '__main__':
    main()
