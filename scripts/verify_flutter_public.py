"""Public preview acceptance without login, installation or VPN operations."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import time
import urllib.error
import urllib.request

from flutter_channel import PUBLIC_URL, catalog_releases, installers, object_path, verify_bundle


def request(path, headers=None):
    return urllib.request.Request(PUBLIC_URL + path, headers={'User-Agent': 'Go-http-client/1.1', 'Cache-Control': 'no-cache', **(headers or {})})


def get_json(path):
    with urllib.request.urlopen(request(path), timeout=60) as r:
        if r.status != 200:
            raise ValueError('Public endpoint failed')
        return json.load(r)


def baseline():
    return {'updates': get_json('/api/v3/client/releases'), 'installers': get_json('/api/v1/user/installers')}


def range_bytes(path, start, end, total):
    for attempt in range(2):
        try:
            with urllib.request.urlopen(request(path, {'Range': f'bytes={start}-{end}'}), timeout=180) as r:
                body = r.read(end - start + 2)
                if r.status != 206 or r.headers.get('Content-Range') != f'bytes {start}-{end}/{total}' or len(body) != end - start + 1:
                    raise ValueError('Public Range response differs from requested bytes')
                return body
        except (OSError, TimeoutError):
            if attempt:
                raise


def verify_public(bundle, public, before, *, full=True):
    _, releases = verify_bundle(bundle, public)
    if baseline() != before:
        raise ValueError('Official channel changed during preview publication')
    if get_json('/api/v3/client/releases?client=flutter&channel=preview') != json.loads((bundle / 'releases.json').read_bytes()):
        raise ValueError('Public preview catalog mismatch')
    if get_json('/api/v1/user/installers?client=flutter&channel=preview') != installers(releases):
        raise ValueError('Public preview installers mismatch')
    for path in ['/', '/healthz', '/readyz']:
        with urllib.request.urlopen(request(path), timeout=60) as r:
            if r.status != 200 or path != '/' and json.load(r).get('ok') is not True:
                raise ValueError('Public service health failed')
    with urllib.request.urlopen('https://cms-hqip.sharevps.online/', timeout=60) as r:
        if r.status != 200:
            raise ValueError('Admin website unavailable')
    try:
        get_json('/api/v3/client/releases?client=flutter&channel=invalid')
    except urllib.error.HTTPError as e:
        if e.code != 404:
            raise
    else:
        raise ValueError('Unknown preview channel must return 404')
    completed, seen = [], set()
    for v in releases:
        if v['sha256'] in seen:
            continue
        seen.add(v['sha256'])
        path = '/downloads/flutter/preview/' + v['sha256'] + '/' + v['filename']
        size = v['size']
        local = bundle / object_path(v)
        for start, end in [(0, min(31, size - 1)), (max(0, size - 32), size - 1)]:
            data = range_bytes(path, start, end, size)
            with local.open('rb') as f:
                f.seek(start)
                if f.read(len(data)) != data:
                    raise ValueError('Public file range content mismatch')
        if full:
            ranges = [(i, min(i + (16 << 20), size) - 1) for i in range(0, size, 16 << 20)]
            checksum = hashlib.sha256()
            with ThreadPoolExecutor(max_workers=3) as pool:
                for data in pool.map(lambda span: range_bytes(path, *span, size), ranges):
                    checksum.update(data)
            if checksum.hexdigest() != v['sha256']:
                raise ValueError('Public full download checksum mismatch')
        completed.append({'filename': v['filename'], 'sha256': v['sha256'], 'size': size, 'fullDownload': full})
    if baseline() != before:
        raise ValueError('Official channel changed during acceptance')
    return {'checkedAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'files': completed, 'officialChannelUnchanged': True}


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--bundle', type=Path, required=True)
    p.add_argument('--public-key', type=Path, default=Path('packaging/flutter-update-public-key.txt'))
    p.add_argument('--baseline', type=Path, required=True)
    a = p.parse_args()
    print(json.dumps(verify_public(a.bundle, a.public_key.read_text().strip(), json.loads(a.baseline.read_bytes())), indent=2))
