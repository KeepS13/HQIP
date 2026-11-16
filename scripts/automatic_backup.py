"""Root-only scheduled local backups. Never deletes release or operator backups."""
from __future__ import annotations

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import hashlib
import gzip
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import uuid

BASE = Path('/opt/hqip-control')
DATA = Path('/var/lib/hqip-control')
ROOT = Path('/var/backups/hqip-control/automatic')
STATUS = DATA / 'backup-status.json'
EVENTS = DATA / 'backup-events'
ENVIRONMENT = Path('/etc/hqip-control/storage.env')
NAME = re.compile(r'hqip-(\d{8}T\d{6}Z)-([a-f0-9]{16})')
RUN_ID = re.compile(r'\d{8}T\d{6}Z-[a-f0-9]{16}')
BEIJING = timezone(timedelta(hours=8))
SPACE_RESERVE = 256 * 1024 ** 2


def utc(value=None):
    return value or datetime.now(timezone.utc)


def stamp(value):
    return value.astimezone(timezone.utc).isoformat(timespec='seconds').replace('+00:00', 'Z')


def parse_time(value):
    result = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.tzinfo is None:
        raise ValueError('Invalid backup time')
    return result.astimezone(timezone.utc)


def next_run(now):
    local = now.astimezone(BEIJING)
    start = local.replace(hour=(local.hour // 6) * 6, minute=0, second=0, microsecond=0)
    return stamp(start + timedelta(hours=6))


def directory(path):
    path = Path(path)
    for item in (path, *path.parents):
        if item.is_symlink():
            raise ValueError('Unsafe backup directory')
    if not path.is_dir():
        raise ValueError('Missing backup directory')
    return path


def regular(path):
    info = Path(path).lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError('Unsafe backup file')
    return info


def read_json(path):
    info = regular(path)
    if info.st_size > 65536:
        raise ValueError('Oversized backup state')
    descriptor = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(descriptor, 'r', encoding='utf-8') as stream:
        opened = os.fstat(stream.fileno())
        if not stat.S_ISREG(opened.st_mode) or opened.st_size > 65536 or opened.st_nlink != 1:
            raise ValueError('Invalid opened backup state')
        value = json.load(stream)
    if not isinstance(value, dict):
        raise ValueError('Invalid backup state')
    return value


def application_group():
    if os.name == 'nt':
        return None
    import grp
    return grp.getgrnam('hqip-control').gr_gid


def sync_directory(path):
    if os.name == 'nt':
        return
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def publish_json(path, value, shared=False):
    directory(path.parent)
    raw = (json.dumps(value, sort_keys=True, separators=(',', ':')) + '\n').encode()
    if len(raw) > 65536:
        raise ValueError('Oversized backup state')
    fd, temporary = tempfile.mkstemp(prefix='.backup-', dir=path.parent)
    try:
        os.fchmod(fd, 0o640 if shared else 0o600) if hasattr(os, 'fchmod') else os.chmod(temporary, 0o600)
        if shared and os.name != 'nt':
            os.fchown(fd, 0, application_group())
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        sync_directory(path.parent)
    finally:
        if os.path.lexists(temporary):
            os.unlink(temporary)


def initialize_paths(root=ROOT, data=DATA):
    directory(root.parent.parent)
    for path in (root.parent, root):
        if not path.exists():
            path.mkdir(mode=0o700)
        directory(path)
        path.chmod(0o700)
        if os.name != 'nt':
            os.chown(path, 0, 0)
    directory(data)
    events = data / 'backup-events'
    if not events.exists():
        events.mkdir(mode=0o750)
    directory(events)
    events.chmod(0o750)
    if os.name != 'nt':
        os.chown(events, 0, application_group())


@contextmanager
def deployment_lock(base=BASE):
    import fcntl
    directory(base)
    fd = os.open(base / 'deploy.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ValueError('Invalid deployment lock')
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(fd)


@contextmanager
def backup_lock(root=ROOT):
    import fcntl
    descriptor = os.open(root / 'backup.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise ValueError('Invalid backup lock')
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(descriptor)


def checksum(path):
    regular(path)
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def validate_archive(path):
    regular(path)
    # Read through the gzip trailer as well as tar members to verify its CRC.
    with gzip.open(path, 'rb') as compressed:
        while compressed.read(1024 * 1024):
            pass
    expected = {'hqip.pg.dump', 'master.key', 'manifest.json'}
    with tarfile.open(path, 'r:gz') as archive:
        members = archive.getmembers()
        if len(members) != 3 or {m.name for m in members} != expected:
            raise ValueError('Invalid backup archive members')
        for member in members:
            if not member.isfile() or member.size <= 0:
                raise ValueError('Invalid backup archive file')
            stream = archive.extractfile(member)
            if member.name == 'master.key' and member.size != 32:
                raise ValueError('Invalid backup key')
            if member.name == 'manifest.json':
                if member.size > 65536:
                    raise ValueError('Invalid backup manifest')
                manifest = json.load(stream)
                if manifest.get('version') != 1 or manifest.get('backend') != 'postgresql' or manifest.get('containsSecrets') is not True:
                    raise ValueError('Invalid backup manifest')
            elif member.name == 'hqip.pg.dump':
                if stream.read(5) != b'PGDMP':
                    raise ValueError('Invalid PostgreSQL archive')
                # Read the whole compressed stream so truncated payloads fail now.
                while stream.read(1024 * 1024):
                    pass


def owned_backups(root):
    directory(root)
    entries = []
    for folder in sorted(root.iterdir()):
        match = NAME.fullmatch(folder.name)
        if not match:
            continue
        directory(folder)
        if {p.name for p in folder.iterdir()} != {'archive.tar.gz', 'metadata.json'}:
            raise ValueError('Invalid owned backup contents')
        metadata = read_json(folder / 'metadata.json')
        size = regular(folder / 'archive.tar.gz').st_size
        created = parse_time(metadata['createdAt'])
        if metadata.get('version') != 1 or metadata.get('runId') != folder.name.removeprefix('hqip-') or created.strftime('%Y%m%dT%H%M%SZ') != match[1]:
            raise ValueError('Invalid owned backup metadata')
        entries.append((created, folder, size, metadata))
    return sorted(entries, key=lambda entry: (entry[0], entry[1].name))


def verify_saved_backup(entry):
    _, folder, size, metadata = entry
    archive = folder / 'archive.tar.gz'
    validate_archive(archive)
    if metadata.get('sizeBytes') != size or metadata.get('sha256') != checksum(archive):
        raise ValueError('Saved backup checksum mismatch')


def delete_saved_backup(root, entry):
    _, folder, _, metadata = entry
    if folder.parent != root or not NAME.fullmatch(folder.name):
        raise ValueError('Invalid owned backup path')
    directory(folder)
    if {path.name for path in folder.iterdir()} != {'archive.tar.gz', 'metadata.json'} or read_json(folder / 'metadata.json') != metadata:
        raise ValueError('Owned backup changed before cleanup')
    regular(folder / 'archive.tar.gz')
    # Only these two validated regular files in this exact owned folder.
    (folder / 'archive.tar.gz').unlink()
    (folder / 'metadata.json').unlink()
    folder.rmdir()
    sync_directory(root)


def inventory(root, now, prune=True):
    entries = owned_backups(root)
    expired = [entry for entry in entries[:-1] if entry[0] <= now - timedelta(days=14)]
    if not prune or not expired:
        return len(entries), 0
    # Keep a verified recovery point even when every archive has expired.
    verify_saved_backup(entries[-1])
    for entry in expired:
        delete_saved_backup(root, entry)
    return len(entries) - len(expired), len(expired)


def reclaim_backup_space(root, data):
    entries = owned_backups(root)
    # Budget for the dump, the final archive, growth and a fixed safety reserve.
    # Release installation keeps its separate, larger disk-space gate.
    required = SPACE_RESERVE + 4 * max((entry[2] for entry in entries), default=0)

    def enough():
        return shutil.disk_usage(root).free >= required and shutil.disk_usage(data).free >= required

    if enough():
        return 0, True
    # Removing archives cannot free space on a separate, full data filesystem.
    if root.stat().st_dev != data.stat().st_dev and shutil.disk_usage(data).free < required:
        return 0, False
    if not entries:
        return 0, False
    verify_saved_backup(entries[-1])
    deleted = 0
    for entry in entries[:-1]:
        delete_saved_backup(root, entry)
        deleted += 1
        if enough():
            return deleted, True
    return deleted, enough()


def discard_partial(root, folder):
    if folder.parent != root or not folder.name.startswith('.partial-') or not RUN_ID.fullmatch(folder.name.removeprefix('.partial-')):
        raise ValueError('Invalid incomplete backup path')
    directory(folder)
    files = list(folder.iterdir())
    if any(path.name not in {'archive.tar.gz', 'metadata.json'} for path in files):
        raise ValueError('Invalid incomplete backup contents')
    for path in files:
        regular(path)
    for path in files:
        path.unlink()
    folder.rmdir()


def prune_incomplete(root, now):
    directory(root)
    for folder in root.iterdir():
        identity = folder.name.removeprefix('.partial-')
        if not folder.name.startswith('.partial-') or not RUN_ID.fullmatch(identity):
            continue
        created = datetime.strptime(identity[:16], '%Y%m%dT%H%M%SZ').replace(tzinfo=timezone.utc)
        if created <= now - timedelta(days=14):
            discard_partial(root, folder)


def prune_events(events, now):
    directory(events)
    for path in events.iterdir():
        if not re.fullmatch(r'\d{8}T\d{6}Z-[a-f0-9]{16}-[123]-(completed|failed|pruned)\.json', path.name):
            continue
        event = read_json(path)
        if event.get('version') != 1 or event.get('eventId') != path.stem:
            raise ValueError('Invalid backup event')
        if parse_time(event['at']) <= now - timedelta(days=14):
            path.unlink()


def event(events, run, action, now, **details):
    suffix = action.removeprefix('backup.')
    identity = f"{run['runId']}-{run['attempt']}-{suffix}"
    publish_json(events / (identity + '.json'), {'version': 1, 'eventId': identity,
                 'runId': run['runId'], 'attempt': run['attempt'], 'action': action,
                 'at': stamp(now), 'result': 'failure' if suffix == 'failed' else 'success', **details}, shared=True)


def create_archive(root, base, data, run, execute=subprocess.run):
    from upgrade_domains import storage_environment
    destination = root / ('hqip-' + run['runId'])
    if destination.exists() or destination.is_symlink():
        directory(destination)
        if {path.name for path in destination.iterdir()} != {'archive.tar.gz', 'metadata.json'}:
            raise ValueError('Invalid published backup')
        metadata = read_json(destination / 'metadata.json')
        archive = destination / 'archive.tar.gz'
        validate_archive(archive)
        if metadata.get('runId') != run['runId'] or metadata.get('createdAt') != run['startedAt'] or metadata.get('sha256') != checksum(archive) or metadata.get('sizeBytes') != archive.stat().st_size:
            raise ValueError('Published backup identity mismatch')
        return metadata['sizeBytes'], metadata['sha256']
    directory(base / 'releases')
    release = (base / 'current').resolve(strict=True)
    if release.parent != (base / 'releases').resolve() or not re.fullmatch('[a-f0-9]{40}', release.name):
        raise ValueError('Invalid active release')
    regular(release / 'hqip')
    env = storage_environment(ENVIRONMENT)
    from urllib.parse import urlsplit
    if urlsplit(env['HQIP_DATABASE_URL']).username != 'hqip_app':
        raise ValueError('Backup requires application database role')
    stage = root / ('.partial-' + run['runId'])
    if stage.exists() or stage.is_symlink():
        discard_partial(root, stage)
    stage.mkdir(mode=0o700)
    try:
        archive = stage / 'archive.tar.gz'
        outcome = execute([str(release / 'hqip'), 'backup', '--data-dir', str(data), '--output', str(archive)],
                          env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, timeout=1900, check=False)
        if outcome.returncode:
            raise RuntimeError('backup_command_failed')
        validate_archive(archive)
        archive.chmod(0o600)
        digest, size = checksum(archive), archive.stat().st_size
        metadata = {'version': 1, 'runId': run['runId'], 'createdAt': run['startedAt'],
                    'sourceSHA': release.name, 'sha256': digest, 'sizeBytes': size}
        publish_json(stage / 'metadata.json', metadata)
        if destination.exists() or destination.is_symlink():
            raise ValueError('Backup destination already exists')
        stage.rename(destination)
        sync_directory(root)
        return size, digest
    finally:
        if stage.exists():
            for name in ('archive.tar.gz', 'metadata.json'):
                path = stage / name
                if path.exists():
                    regular(path)
                    path.unlink()
            stage.rmdir()


def retry_timer(enable, execute=subprocess.run):
    result = execute(['/usr/bin/systemctl', 'restart' if enable else 'stop', 'hqip-backup-retry.timer'],
                     stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     timeout=20, check=False)
    if result.returncode:
        raise RuntimeError('retry_timer_failed')


def record_abnormal_exit(*, data=DATA, now=None, timer=retry_timer):
    status_path = data / 'backup-status.json'
    if not status_path.exists():
        return 0
    state = read_json(status_path)
    attempt = state.get('lastRun', {})
    if attempt.get('status') != 'running' or not RUN_ID.fullmatch(attempt.get('runId', '')):
        return 0
    now = utc(now)
    attempt.update(status='failed', finishedAt=stamp(now), errorCode='backup_interrupted')
    if attempt.get('attempt', 3) < 3:
        state['retryAt'] = stamp(now + timedelta(minutes=10))
    event(data / 'backup-events', attempt, 'backup.failed', now, errorCode='backup_interrupted')
    publish_json(status_path, state, shared=True)
    timer(attempt.get('attempt', 3) < 3)
    return 0


def run_backup(retry=False, *, root=ROOT, base=BASE, data=DATA, now=None,
               archive_writer=create_archive, timer=retry_timer, lock=deployment_lock):
    fixed_now = now
    now = utc(now)
    initialize_paths(root, data)
    status_path, events = data / 'backup-status.json', data / 'backup-events'
    state = read_json(status_path) if status_path.exists() else {}
    previous = state.get('lastRun', {})
    if retry:
        if previous.get('status') != 'failed' or previous.get('attempt', 3) >= 3 or not RUN_ID.fullmatch(previous.get('runId', '')):
            return 0
        if parse_time(state['retryAt']) > now:
            return 0
        attempt = {**previous, 'attempt': previous['attempt'] + 1, 'status': 'running'}
        for key in ('finishedAt', 'errorCode'):
            attempt.pop(key, None)
    else:
        attempt = {'runId': now.strftime('%Y%m%dT%H%M%SZ') + '-' + uuid.uuid4().hex[:16],
                   'startedAt': stamp(now), 'attempt': 1, 'status': 'running'}
    state = {'version': 1, 'enabled': True, 'intervalHours': 6, 'retentionDays': 14,
             'nextRunAt': next_run(now), 'lastRun': attempt,
             **({'lastSuccessAt': state['lastSuccessAt']} if state.get('lastSuccessAt') else {})}
    publish_json(status_path, state, shared=True)
    try:
        with lock(base):
            # Expiry and low-space cleanup always preserve the newest backup.
            retained, deleted = inventory(root, now)
            prune_incomplete(root, now)
            prune_events(events, now)
            reclaimed, enough = reclaim_backup_space(root, data)
            deleted += reclaimed
            attempt['deletedCount'] = deleted
            if deleted:
                event(events, attempt, 'backup.pruned', now, deletedCount=deleted)
            if not enough:
                raise RuntimeError('insufficient_backup_space')
            size, digest = archive_writer(root, base, data, attempt)
            finished = utc(fixed_now)
            retained, _ = inventory(root, now, prune=False)
            attempt.update(status='success', finishedAt=stamp(finished), sizeBytes=size,
                           sha256=digest, retainedCount=retained, deletedCount=deleted)
            state['lastSuccessAt'] = stamp(finished)
            event(events, attempt, 'backup.completed', finished, sizeBytes=size, sha256=digest)
    except Exception as error:
        finished = utc(fixed_now)
        code = 'deployment_busy' if isinstance(error, BlockingIOError) else 'backup_failed'
        if isinstance(error, RuntimeError) and str(error) in {'backup_command_failed', 'insufficient_backup_space'}:
            code = str(error)
        attempt.update(status='failed', finishedAt=stamp(finished), errorCode=code)
        event(events, attempt, 'backup.failed', finished, errorCode=code)
        if attempt['attempt'] < 3:
            state['retryAt'] = stamp(finished + timedelta(minutes=10))
    publish_json(status_path, state, shared=True)
    try:
        timer(attempt['status'] == 'failed' and attempt['attempt'] < 3)
    except Exception:
        state['retryErrorCode'] = 'retry_timer_failed'
        publish_json(status_path, state, shared=True)
        return 1
    return 0 if attempt['status'] == 'success' else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--retry', action='store_true')
    parser.add_argument('--record-abnormal', action='store_true')
    args = parser.parse_args()
    if os.name == 'nt' or os.geteuid() != 0:
        raise PermissionError('Scheduled backup requires root on the control host')
    os.umask(0o077)
    try:
        initialize_paths()
        # Serialize calendar and retry units before reading or updating state.
        with backup_lock():
            if args.record_abnormal:
                return record_abnormal_exit()
            return run_backup(args.retry)
    except BlockingIOError:
        return 0
    except Exception:
        # Underlying errors may contain database configuration or file contents.
        print('HQIP automatic backup failed; inspect the private backup status')
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
