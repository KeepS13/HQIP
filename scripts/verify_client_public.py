"""Bounded public acceptance; complete artifacts are hashed before network probes."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from concurrent.futures import ThreadPoolExecutor

from client_bundle import verify_client_bundle

PUBLIC_BASE = 'https://hqip.sharevps.online'
RANGE_CHUNK = 16 << 20


def fetch(path):
    args = ['curl', '--fail', '--silent', '--show-error', '--retry', '3', '--retry-all-errors',
            '--max-time', '45', '--user-agent', 'Go-http-client/1.1']
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
        if len(match) != 1 or any(match[0].get(k) != item[k] for k in ('sha256', 'size', 'version', 'sequence', 'filename')):
            raise ValueError('Public installer metadata mismatch')
        if match[0]['url'] != 'https://hqip.sharevps.online' + path:
            raise ValueError('Unexpected installer URL')
    # All four full payloads were verified above. Probe both ends of every
    # public route, then transfer one whole installer. Re-downloading both large
    # APKs and the internal ZIP adds hundreds of MB without another code path.
    transfers = [dict(item, downloadRoute='client') for item in downloads]
    for envelope in expected['releases']:
        update = json.loads(base64.b64decode(envelope['payload']))
        if update['platform'] == 'windows-amd64':
            transfers.append(dict(update, downloadRoute='godot'))
    with ThreadPoolExecutor(max_workers=4) as workers:
        list(workers.map(lambda item: verify_boundaries(root, item), transfers))
    verify_complete_download(next(item for item in transfers if item['filename'].endswith('.exe')))
    print('Accepted signed channel, all payload boundaries and complete Windows installer.', flush=True)


def verify_boundaries(root, item):
    source = root / 'godot' / item['sha256'] / item['filename']
    for start, end in [(0, 31), (item['size'] - 32, item['size'] - 1)]:
        with source.open('rb') as stream:
            stream.seek(start)
            expected_bytes = stream.read(end - start + 1)
        if download_range(download_url(item), start, end, item['size']) != expected_bytes:
            raise ValueError('Public range differs from verified artifact: ' + item['filename'])


def verify_complete_download(item):
    # Independent bounded ranges survive edge connection resets without starting
    # over. The HTTP slow-stream regression separately guards the 30s total-time
    # bug; every byte of this installer is still downloaded and hashed here.
    ranges = [(start, min(start + RANGE_CHUNK, item['size']) - 1) for start in range(0, item['size'], RANGE_CHUNK)]
    digest = hashlib.sha256()
    with ThreadPoolExecutor(max_workers=4) as workers:
        for body in workers.map(lambda span: download_range(download_url(item), *span, item['size']), ranges):
            digest.update(body)
    if digest.hexdigest() != item['sha256']:
        raise ValueError('Complete public download checksum mismatch: ' + item['filename'])
    print('Verified complete public installer: ' + item['filename'], flush=True)


def download_url(item):
    return PUBLIC_BASE + '/downloads/' + item['downloadRoute'] + '/' + item['sha256'] + '/' + item['filename']


def download_range(url, start, end, total):
    """Accept only an exact 206 range; never concatenate an ignored/wrong range."""
    with tempfile.TemporaryDirectory(prefix='hqip-public-download-') as temporary:
        output, headers = Path(temporary) / 'part', Path(temporary) / 'headers'
        budget = '120' if end - start + 1 > 32 else '20'
        try:
            result = subprocess.run(['curl', '--fail', '--silent', '--show-error', '--retry', '1', '--retry-all-errors',
                '--max-time', budget, '--max-filesize', str(end - start + 1), '--user-agent', 'Go-http-client/1.1',
                '--range', f'{start}-{end}', '--dump-header', str(headers), '--output', str(output),
                '--write-out', '%{http_code}', url], check=True, capture_output=True)
        except subprocess.CalledProcessError as error:
            print(error.stderr.decode(errors='replace'), flush=True)
            raise
        values = re.findall(rb'(?im)^content-range:\s*([^\r\n]+)', headers.read_bytes())
        if result.stdout.strip() != b'206' or not values or values[-1] != f'bytes {start}-{end}/{total}'.encode():
            raise ValueError('Public download returned an unacknowledged range')
        body = output.read_bytes()
        if len(body) != end - start + 1:
            raise ValueError('Incomplete public range')
        return body


if __name__ == '__main__':
    main()
