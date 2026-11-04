"""Deploy the additive notifications release; rollback switches code, never data.

Unlike the pre-portal migration tool, this operates on an already active account
system. It never rewinds usage, leases, passwords or authorizations. Node assets,
Windows downloads and Nginx remain exactly those in the previous release.
"""
from __future__ import annotations

import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import sys
import tarfile
import time

sys.dont_write_bytecode = True
import upgrade_control as control

BACKUPS = Path('/root/backups')


def data_identity():
    root = control.DATA
    if root.is_symlink() or not root.is_dir():
        raise ValueError('Unexpected data directory')
    database, key = root / 'hqip.db', root / 'master.key'
    if any(p.is_symlink() or not p.is_file() for p in (database, key)):
        raise ValueError('Missing ordinary database or paired key')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True)) as db:
        version = db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone()
        if version != ('4',):
            raise ValueError('Only additive schema version 4 releases are supported')
    return hashlib.sha256(key.read_bytes()).hexdigest()


def state_path(path, existing=False):
    if not path.is_absolute() or path.parent != BACKUPS or path.is_symlink():
        raise ValueError('Use a direct ordinary backup directory')
    if existing and not path.is_dir():
        raise ValueError('Missing backup state')


def record(state, info):
    path = state / 'notifications-state.json'
    path.write_text(json.dumps(info, indent=2) + '\n')
    path.chmod(0o600)


def verify_upgrade(release):
    release, manifest = control.verify_release(release)
    previous = (control.BASE / 'current').resolve(strict=True)
    _, old = control.verify_release(previous)
    if release == previous:
        raise ValueError('Release already active')
    if manifest.get('upgradeKind') != 'notifications-additive' or manifest.get('previousSourceSHA') != old['sourceSHA']:
        raise ValueError('Release is not based on the current verified controller')
    before = {p: h for p, h in old['sha256'].items() if p.startswith('artifacts/')}
    after = {p: h for p, h in manifest['sha256'].items() if p.startswith('artifacts/')}
    if not before or before != after:
        raise ValueError('Node and desktop assets must remain unchanged')
    data_identity()
    return release, manifest, previous


def rollback(state):
    state_path(state, existing=True)
    info = json.loads((state / 'notifications-state.json').read_text())
    if info.get('upgradeKind') != 'notifications-additive':
        raise ValueError('Not a notification-only backup')
    previous, _ = control.verify_release(Path(info['previous']))
    active = (control.BASE / 'current').resolve(strict=True)
    if active not in (previous, Path(info['release'])):
        raise ValueError('A later release is active; refusing stale rollback')
    if data_identity() != info['masterSHA256']:
        raise ValueError('Master key changed; manual recovery required')
    control.run('systemctl', 'stop', control.SERVICE)
    try:
        if data_identity() != info['masterSHA256']:
            raise ValueError('Stopped data identity changed')
        # Keep current accounting and all newly added notification tables.
        control.point_current(previous)
        control.run('systemctl', 'start', control.SERVICE)
        control.healthy()
    except Exception:
        # Never restore an old SQLite snapshot over live account accounting.
        control.point_current(active)
        control.run('systemctl', 'start', control.SERVICE)
        raise
    info['phase'] = 'code-rolled-back-data-preserved'
    record(state, info)


def upgrade(release, state):
    state_path(state)
    release, manifest, previous = verify_upgrade(release)
    state.mkdir(mode=0o700, exist_ok=False)
    state.chmod(0o700)
    control.run('systemctl', 'stop', control.SERVICE)
    info = None
    try:
        key_hash = data_identity()
        if any(p.is_symlink() for p in control.DATA.rglob('*')):
            raise ValueError('Symlink in controller data')
        shutil.copytree(control.DATA, state / 'data.before')
        with tarfile.open(state / 'config.before.tar.gz', 'w:gz') as archive:
            for path in (Path('/etc/hqip-control'), Path('/etc/systemd/system/hqip-control.service'), Path('/etc/nginx/sites-available/hqip.conf')):
                if path.exists():
                    archive.add(path, arcname=str(path).lstrip('/'))
        for path in state.rglob('*'):
            path.chmod(0o700 if path.is_dir() else 0o600)
        info = {'upgradeKind': 'notifications-additive', 'phase': 'prepared',
                'previous': str(previous), 'release': str(release),
                'sourceSHA': manifest['sourceSHA'], 'masterSHA256': key_hash,
                'createdAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
        record(state, info)
        control.point_current(release)
        control.run('systemctl', 'start', control.SERVICE)
        control.healthy()
        if data_identity() != key_hash:
            raise ValueError('Data identity changed during additive migration')
        info['phase'] = 'active'
        record(state, info)
    except Exception:
        if info is not None:
            rollback(state)
        else:
            control.point_current(previous)
            control.run('systemctl', 'start', control.SERVICE)
        raise
    print(json.dumps({'sourceSHA': manifest['sourceSHA'], 'backup': str(state), 'phase': 'active'}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['verify', 'upgrade', 'rollback'])
    parser.add_argument('--release', type=Path)
    parser.add_argument('--state', type=Path)
    args = parser.parse_args()
    if args.action == 'verify':
        _, manifest, _ = verify_upgrade(args.release)
        print(manifest['sourceSHA'])
        return
    if os.geteuid() != 0:
        raise PermissionError('Root required')
    if args.action == 'upgrade':
        upgrade(args.release, args.state)
    else:
        rollback(args.state)


if __name__ == '__main__':
    main()
