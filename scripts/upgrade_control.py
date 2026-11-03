"""Upgrade an existing HQIP control service without touching Nginx or other apps.

Run as root on the Linux control host. The release must contain release.json
with sourceSHA and SHA-256 hashes of every payload file. Rollback preserves the
failed database and restores the exact stopped database/master-key snapshot.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import time
import urllib.request

BASE = Path('/opt/hqip-control')
DATA = Path('/var/lib/hqip-control')
SERVICE = 'hqip-control.service'


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout.strip()


def verify_release(release):
    release = release.resolve(strict=True)
    if release.parent != BASE / 'releases':
        raise ValueError('Release must be a direct child of /opt/hqip-control/releases')
    manifest = json.loads((release / 'release.json').read_text())
    if not re.fullmatch(r'[a-f0-9]{40}', manifest['sourceSHA']):
        raise ValueError('Full source commit required')
    expected = manifest['sha256']
    if 'hqip' not in expected or 'artifacts/manifest.json' not in expected:
        raise ValueError('Incomplete release manifest')
    for name, digest in expected.items():
        path = release / name
        if path.is_symlink() or not path.is_file() or release not in path.resolve().parents:
            raise ValueError('Unsafe release path')
        with path.open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != digest:
                raise ValueError('Release checksum mismatch: ' + name)
    for path in release.rglob('*'):
        if path.is_symlink():
            raise ValueError('Symlink in release payload')
        if path.is_file() and path.relative_to(release).as_posix() not in expected and path != release / 'release.json':
            raise ValueError('Unmanifested release file')
    return release, manifest


def point_current(release):
    pending = BASE / 'current.upgrade'
    if pending.exists() or pending.is_symlink():
        pending.unlink()
    pending.symlink_to(release, target_is_directory=True)
    pending.replace(BASE / 'current')


def healthy():
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for _ in range(30):
        try:
            for port in [18090, 18091]:
                with opener.open(f'http://127.0.0.1:{port}/healthz', timeout=2) as response:
                    if response.status != 200 or not json.load(response).get('ok'):
                        raise ValueError('Unhealthy controller')
            if run('systemctl', 'is-active', SERVICE) == 'active':
                return
        except Exception:
            time.sleep(1)
    raise RuntimeError('Both control listeners did not become healthy')


def rollback(state):
    if state.is_symlink() or not state.is_dir() or state.resolve() != state:
        raise ValueError('Unsafe backup directory')
    info = json.loads((state / 'state.json').read_text())
    previous, _ = verify_release(Path(info['previous']))
    snapshot = state / 'data.before'
    if snapshot.is_symlink() or any(p.is_symlink() for p in snapshot.rglob('*')):
        raise ValueError('Unsafe data snapshot')
    if not (snapshot / 'hqip.db').is_file() or not (snapshot / 'master.key').is_file():
        raise ValueError('Backup is incomplete')
    run('systemctl', 'stop', SERVICE)
    failure = state / ('failed-data-' + str(time.time_ns()))
    shutil.copytree(DATA, failure)
    # DATA is fixed, never derived from user input. Retain the failed copy first.
    if DATA.resolve() != Path('/var/lib/hqip-control') or DATA.is_symlink():
        raise ValueError('Unexpected data directory')
    shutil.rmtree(DATA)
    shutil.copytree(snapshot, DATA)
    run('chown', '-R', 'hqip-control:hqip-control', str(DATA))
    run('chmod', '700', str(DATA))
    point_current(previous)
    run('systemctl', 'start', SERVICE)
    healthy()
    info['phase'] = 'rolled-back'
    (state / 'state.json').write_text(json.dumps(info, indent=2) + '\n')


def upgrade(release, state):
    release, manifest = verify_release(release)
    previous = (BASE / 'current').resolve(strict=True)
    verify_release(previous)
    if previous == release:
        raise ValueError('Release already active')
    if state.exists():
        raise ValueError('Use a new backup directory for each upgrade')
    state.mkdir(parents=True, mode=0o700)
    os.chmod(state, 0o700)
    if DATA.is_symlink():
        raise ValueError('Data directory must not be a symlink')
    # Final snapshot is taken with the old service stopped, including WAL.
    run('systemctl', 'stop', SERVICE)
    try:
        shutil.copytree(DATA, state / 'data.before')
        with tarfile.open(state / 'config.before.tar.gz', 'w:gz') as archive:
            for path in [Path('/etc/hqip-control'), Path('/etc/systemd/system/hqip-control.service')]:
                if path.exists():
                    archive.add(path, arcname=str(path).lstrip('/'))
        info = {'phase': 'prepared', 'sourceSHA': manifest['sourceSHA'], 'previous': str(previous), 'release': str(release), 'createdAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
        (state / 'state.json').write_text(json.dumps(info, indent=2) + '\n')
        for path in state.rglob('*'):
            path.chmod(0o700 if path.is_dir() else 0o600)
        point_current(release)
        run('systemctl', 'start', SERVICE)
        healthy()
        info['phase'] = 'active'
        (state / 'state.json').write_text(json.dumps(info, indent=2) + '\n')
    except Exception:
        if (state / 'state.json').exists():
            rollback(state)
        else:
            point_current(previous)
            run('systemctl', 'start', SERVICE)
        raise
    print(json.dumps({'sourceSHA': manifest['sourceSHA'], 'backup': str(state), 'phase': 'active'}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['verify', 'upgrade', 'rollback'])
    parser.add_argument('--release', type=Path)
    parser.add_argument('--state', type=Path)
    args = parser.parse_args()
    if args.action == 'verify':
        _, manifest = verify_release(args.release)
        print(manifest['sourceSHA'])
        return
    if os.geteuid() != 0:
        raise PermissionError('Root required')
    if not args.state or not args.state.is_absolute() or args.state.parent != Path('/root/backups'):
        raise ValueError('State must be a direct directory inside /root/backups')
    if args.action == 'upgrade':
        upgrade(args.release, args.state)
    else:
        rollback(args.state)


if __name__ == '__main__':
    main()
