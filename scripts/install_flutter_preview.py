"""Root-side preview installer. Never changes the control release or database."""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import urllib.request

from flutter_channel import digest, encode, extract_bundle, regular, verify_bundle
from render_domains import preview_routes

BASE = Path('/opt/hqip-flutter-preview')
SITE = Path('/etc/nginx/sites-available/hqip.conf')
BEGIN = ' # BEGIN HQIP Flutter preview\n'
END = ' # END HQIP Flutter preview\n'
ANCHOR = ' location = /api/v1/payments/fm/notify '


def patch_site(text):
    if BEGIN in text:
        if text.count(BEGIN) != 1 or text.count(END) != 1:
            raise ValueError('Ambiguous preview configuration')
        start, end = text.index(BEGIN), text.index(END) + len(END)
        return text[:start] + preview_routes() + text[end:]
    if text.count(ANCHOR) != 1:
        raise ValueError('Expected HQIP user API configuration not found')
    index = text.index(ANCHOR)
    return text[:index] + preview_routes() + text[index:]


def atomic_bytes(path, value, mode=0o644):
    path = Path(path)
    fd, name = tempfile.mkstemp(prefix='.' + path.name + '-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(value)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(name, mode)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def current_target(base):
    current = base / 'current'
    if current.is_symlink():
        target = os.readlink(current)
        if not re.fullmatch(r'versions/[1-9][0-9]*', target) or not current.is_dir():
            raise ValueError('Unsafe active preview pointer')
        return target
    if current.exists():
        raise ValueError('Preview current must be a symlink')
    return None


def switch(base, target):
    current = base / 'current'
    if target is None:
        current.unlink(missing_ok=True)
        return
    if not re.fullmatch(r'versions/[1-9][0-9]*', target) or not (base / target).is_dir():
        raise ValueError('Invalid preview activation target')
    temp = base / ('.current-' + os.urandom(8).hex())
    try:
        temp.symlink_to(target, target_is_directory=True)
        os.replace(temp, current)
    finally:
        temp.unlink(missing_ok=True)


@contextmanager
def locked(base):
    import fcntl
    base.mkdir(mode=0o755, parents=True, exist_ok=True)
    if base.is_symlink() or base.resolve() != base:
        raise ValueError('Unsafe preview root')
    with (base / '.publish.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield


def stage(bundle, base, public, sha):
    receipt, releases = verify_bundle(bundle, public, sha)
    versions = base / 'versions'
    versions.mkdir(exist_ok=True)
    sequence = receipt['sequence']
    target = versions / str(sequence)
    for old in versions.iterdir():
        if old.is_symlink() or not old.is_dir() or not re.fullmatch('[1-9][0-9]*', old.name):
            raise ValueError('Unexpected version entry')
        if int(old.name) > sequence:
            raise ValueError('Cannot publish an older preview sequence')
    if target.exists():
        if (target / 'receipt.json').read_bytes() != (bundle / 'receipt.json').read_bytes():
            raise ValueError('Published sequence cannot contain different content')
    for v in releases:
        source = bundle / 'objects' / v['sha256'] / v['filename']
        dest = base / 'objects' / v['sha256'] / v['filename']
        dest.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
        if dest.parent.is_symlink() or dest.parent.resolve() != dest.parent:
            raise ValueError('Unexpected object directory')
        if dest.exists():
            if regular(dest).stat().st_size != v['size'] or digest(dest) != v['sha256']:
                raise ValueError('Existing hash-addressed object differs')
        else:
            fd, temporary = tempfile.mkstemp(prefix='.incoming-', dir=dest.parent)
            os.close(fd)
            try:
                shutil.copyfile(source, temporary)
                os.chmod(temporary, 0o444)
                os.replace(temporary, dest)
            finally:
                if os.path.exists(temporary):
                    os.unlink(temporary)
    if not target.exists():
        temporary = Path(tempfile.mkdtemp(prefix='.version-', dir=base))
        try:
            for name in ['releases.json', 'installers.json', 'receipt.json']:
                shutil.copyfile(bundle / name, temporary / name)
                (temporary / name).chmod(0o444)
            temporary.chmod(0o755)
            os.replace(temporary, target)
        finally:
            if temporary.exists():
                shutil.rmtree(temporary)
    return receipt


def nginx_reload():
    subprocess.run(['nginx', '-t'], check=True)
    subprocess.run(['systemctl', 'reload', 'nginx'], check=True)


def activate(bundle, base, site, public, sha, run_id, reload=nginx_reload):
    if not re.fullmatch(r'[0-9]+-[0-9]+', run_id):
        raise ValueError('Invalid run identity')
    receipt = stage(bundle, base, public, sha)
    target = 'versions/' + str(receipt['sequence'])
    previous = current_target(base)
    before = regular(site).read_bytes()
    after = patch_site(before.decode()).encode()
    transaction = base / 'transactions' / run_id
    transaction.mkdir(mode=0o700, parents=True, exist_ok=False)
    (transaction / 'nginx.before').write_bytes(before)
    record = dict(sourceSHA=sha, target=target, previous=previous, siteSHA256=digest(site),
                  activatedSiteSHA256=__import__('hashlib').sha256(after).hexdigest(), phase='prepared')
    atomic_bytes(transaction / 'state.json', encode(record), 0o600)
    try:
        atomic_bytes(site, after)
        # Both catalog replacement and Nginx reload are covered by rollback below.
        switch(base, target)
        reload()
        record['phase'] = 'active'
        atomic_bytes(transaction / 'state.json', encode(record), 0o600)
    except Exception:
        switch(base, previous)
        atomic_bytes(site, before)
        reload()
        record['phase'] = 'rolled-back'
        atomic_bytes(transaction / 'state.json', encode(record), 0o600)
        raise
    return record


def rollback(base, site, run_id, reload=nginx_reload):
    if not re.fullmatch(r'[0-9]+-[0-9]+', run_id):
        raise ValueError('Invalid run identity')
    transaction = base / 'transactions' / run_id
    record = json.loads(regular(transaction / 'state.json').read_bytes())
    if record['phase'] == 'rolled-back':
        return record
    if current_target(base) != record['target'] or digest(site) != record['activatedSiteSHA256']:
        raise ValueError('Another deployment changed the active preview or Nginx configuration')
    before = regular(transaction / 'nginx.before').read_bytes()
    if __import__('hashlib').sha256(before).hexdigest() != record['siteSHA256']:
        raise ValueError('Rollback configuration checksum mismatch')
    active = site.read_bytes()
    try:
        switch(base, record['previous'])
        atomic_bytes(site, before)
        reload()
    except Exception:
        switch(base, record['target'])
        atomic_bytes(site, active)
        reload()
        raise
    record['phase'] = 'rolled-back'
    atomic_bytes(transaction / 'state.json', encode(record), 0o600)
    return record


def preflight():
    if os.geteuid() != 0:
        raise ValueError('Preview installation requires root')
    if shutil.disk_usage('/opt').free < 2 * 1024**3:
        raise ValueError('At least 2 GiB free space required')
    for port in [18090, 18091]:
        with urllib.request.urlopen(f'http://127.0.0.1:{port}/readyz', timeout=10) as r:
            if json.load(r).get('ok') is not True:
                raise ValueError('Control storage is not ready')
    subprocess.run(['nginx', '-t'], check=True)
    release = Path('/opt/hqip-control/current').resolve()
    artifacts = release / 'artifacts'
    if not artifacts.is_dir():
        raise ValueError('Official artifact directory missing')
    files = {p.relative_to(artifacts).as_posix(): {'size': regular(p).stat().st_size, 'sha256': digest(p)}
             for p in sorted(artifacts.rglob('*')) if p.is_file()}
    if not files:
        raise ValueError('Official artifact inventory is empty')
    return {'controlRelease': str(release), 'officialFiles': files,
            'controlPID': subprocess.check_output(['systemctl', 'show', 'hqip-control', '--property=MainPID', '--value'], text=True).strip()}


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=['activate', 'rollback', 'preflight'])
    p.add_argument('--archive', type=Path)
    p.add_argument('--public-key', required=True)
    p.add_argument('--sha')
    p.add_argument('--run-id', required=True)
    a = p.parse_args()
    baseline = preflight()
    if a.action == 'preflight':
        print(json.dumps(baseline))
    else:
        with locked(BASE):
            if a.action == 'rollback':
                result = rollback(BASE, SITE, a.run_id)
            else:
                with tempfile.TemporaryDirectory(prefix='hqip-preview-') as folder:
                    bundle = Path(folder) / 'bundle'
                    extract_bundle(a.archive, bundle)
                    result = activate(bundle, BASE, SITE, a.public_key, a.sha, a.run_id)
            try:
                if preflight() != baseline:
                    raise ValueError('Control service or official files changed during preview publication')
            except Exception:
                if a.action == 'activate':
                    rollback(BASE, SITE, a.run_id)
                raise
            print(json.dumps(result))
