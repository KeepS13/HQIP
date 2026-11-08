"""Explicit empty-station deployment. Ordinary CI never invokes this workflow."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlparse

sys.dont_write_bytecode = True
from release_bundle import digest, verify_full_release

BASE = Path('/opt/hqip-control')
DATA = Path('/var/lib/hqip-control')
STAGE = Path('/var/lib/hqip-control-fresh')
STORAGE = Path('/etc/hqip-storage')
CONFIG = Path('/etc/hqip-control')
BACKUPS = Path('/root/backups')
UNIT = Path('/etc/systemd/system/hqip-control.service')
STORAGE_UNIT = Path('/etc/systemd/system/hqip-storage.service')
PREP_UNIT = Path('/etc/systemd/system/hqip-control-prepare.service')
SITE = Path('/etc/nginx/sites-available/hqip.conf')
ENABLED_SITE = Path('/etc/nginx/sites-enabled/hqip.conf')
JOURNAL_LIMITS = Path('/etc/systemd/journald@hqip-control.conf.d/limits.conf')
LOGROTATE_CONFIG = Path('/etc/logrotate.d/hqip-control')
NGINX_LOG_DIR = Path('/var/log/nginx/hqip-control')
DOMAINS = ('hqip.sharevps.online', 'cms-hqip.sharevps.online', 'api-hqip.sharevps.online')
MIN_FREE_BYTES = 3 * 1024 ** 3
EMPTY_TABLES = ('portal_users', 'portal_subscriptions', 'portal_templates', 'payment_orders',
                'servers', 'nodes', 'portal_leases', 'cloudflare_settings', 'credentials', 'devices')


def run(*args, env=None, timeout=180):
    result = subprocess.run(list(map(str, args)), env=env, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        # Tool stderr can contain connection settings. Keep failures out of logs.
        raise RuntimeError('Deployment command failed: ' + Path(str(args[0])).name)
    return result.stdout.strip()


def ordinary_directory(path):
    path = Path(path)
    if not path.is_absolute() or path.is_symlink() or path.resolve() != path:
        raise ValueError('Directory must be an absolute path without symlinks')
    return path


def require_free_space(*paths):
    # Measure the real filesystems after extraction/backup, not archive sizes.
    for path in paths:
        existing = Path(path)
        while not existing.exists():
            if existing.parent == existing:
                raise ValueError('Cannot locate deployment filesystem')
            existing = existing.parent
        if shutil.disk_usage(existing).free < MIN_FREE_BYTES:
            raise ValueError('Deployment requires at least 3 GiB of actual free space on every target filesystem')


def inventory(path):
    path = ordinary_directory(path)
    if not path.is_dir():
        raise ValueError('Missing source data directory')
    records = []
    for item in sorted(path.rglob('*')):
        if item.is_symlink() or not (item.is_dir() or item.is_file()):
            raise ValueError('Unsupported object in source data directory')
        if item.is_file():
            records.append([item.relative_to(path).as_posix(), item.stat().st_size, digest(item)])
    return hashlib.sha256(json.dumps(records, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest()


def environment(path, role):
    if path.is_symlink() or not path.is_file() or (os.name != 'nt' and path.stat().st_mode & 0o077):
        raise ValueError('Storage environment must be a private ordinary file')
    value = {'PATH': '/usr/bin:/bin'}
    for line in path.read_text().splitlines():
        fields = shlex.split(line, comments=True)
        if not fields:
            continue
        if len(fields) != 1 or '=' not in fields[0]:
            raise ValueError('Invalid storage environment')
        name, setting = fields[0].split('=', 1)
        if name not in {'HQIP_DATABASE_URL', 'HQIP_REDIS_URL', 'HQIP_REDIS_NAMESPACE'} or not setting:
            raise ValueError('Unexpected storage setting')
        value[name] = setting
    pg = urlparse(value.get('HQIP_DATABASE_URL', ''))
    redis = urlparse(value.get('HQIP_REDIS_URL', ''))
    if (pg.scheme != 'postgres' or pg.hostname != '127.0.0.1' or pg.port != 15432
            or pg.username != role or pg.path != '/hqip' or redis.hostname != '127.0.0.1'
            or redis.port != 16379 or redis.username != 'hqip'):
        raise ValueError('Fresh deployment requires the private generated storage roles')
    return value


def save(state, record):
    pending = state / 'fresh-state.tmp'
    with pending.open('w', encoding='utf-8') as output:
        json.dump(record, output, indent=2)
        output.write('\n')
    pending.chmod(0o600)
    pending.replace(state / 'fresh-state.json')


def load(state):
    ordinary_directory(state)
    if state.parent != BACKUPS or not state.is_dir():
        raise ValueError('State must be a direct private backup directory')
    record = json.loads((state / 'fresh-state.json').read_text())
    if record.get('version') != 1 or record.get('stage') != str(STAGE):
        raise ValueError('Unexpected fresh deployment state')
    return record


def sql(query):
    return run('docker', 'compose', '-f', STORAGE / 'compose.yaml', 'exec', '-T', '--user', 'postgres',
               'postgres', 'psql', '-X', '-U', 'postgres', '-d', 'hqip', '-At', '-v', 'ON_ERROR_STOP=1', '-c', query)


def empty_inventory():
    fields = ','.join("'%s',(SELECT count(*) FROM %s)" % (name, name) for name in EMPTY_TABLES)
    return json.loads(sql("SELECT json_build_object(" + fields +
        ",'admins',(SELECT count(*) FROM admin),'mustChange',(SELECT must_change FROM admin WHERE id=1)"
        ",'paymentEnabled',(SELECT enabled FROM payment_settings WHERE id=1)"
        ",'portalEnabled',(SELECT value FROM metadata WHERE key='portal_enabled')"
        ",'registrationEnabled',(SELECT value FROM metadata WHERE key='portal_registration')"
        ",'controllerID',(SELECT value FROM metadata WHERE key='controller_id'))"))


def assert_empty(value):
    if (any(value.get(name) != 0 for name in EMPTY_TABLES) or value.get('admins') != 1
            or value.get('paymentEnabled') != 0 or value.get('portalEnabled') != 'false'
            or value.get('registrationEnabled') != 'false' or not value.get('controllerID')):
        raise ValueError('Fresh station is not empty or business features are enabled')


def assert_database_empty():
    if sql("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'") != '0':
        raise ValueError('Fresh PostgreSQL schema is not empty; initialization refused')


def ready(ports):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for attempt in range(30):
        try:
            for port in ports:
                with opener.open('http://127.0.0.1:%d/readyz' % port, timeout=2) as response:
                    if response.status != 200 or json.load(response).get('ok') is not True:
                        raise ValueError('Storage dependencies are not ready')
            return
        except Exception:
            if attempt == 29:
                raise RuntimeError('Controller and both storage dependencies did not become ready')
            time.sleep(1)


def initialize_admin(release, migration_env, rules=None):
    # Preserve only the explicitly parsed environment, never the operator's
    # ambient credentials. This also avoids relying on runuser's PAM defaults.
    command = ['/usr/sbin/runuser', '--preserve-environment', '-u', 'hqip-control', '--',
               release / 'hqip', 'init-admin', '--initialize-postgres', '--data-dir', STAGE,
               '--output', STAGE / 'bootstrap-admin.json']
    if rules:
        command += ['--rules', rules]
    run(*command, env=migration_env)


def install_log_limits(release):
    """Install only HQIP-owned policies; shared journal/nginx services stay up."""
    sources = ((release / 'deploy/hqip-journal-limits.conf', JOURNAL_LIMITS),
               (release / 'deploy/hqip-nginx-logrotate.conf', LOGROTATE_CONFIG))
    for source, target in sources:
        ordinary_directory(target.parent)
        if source.is_symlink() or not source.is_file() or target.is_symlink():
            raise ValueError('Log policy must use ordinary release/configuration files')
        if target.exists() and not target.is_file():
            raise ValueError('Unexpected HQIP log policy destination')
    ordinary_directory(NGINX_LOG_DIR)
    # A full deployment is allowed to create these dedicated directories, not
    # modify shared /var/log/nginx or systemd/logrotate directory ownership.
    for directory in (JOURNAL_LIMITS.parent, NGINX_LOG_DIR):
        if not directory.exists():
            directory.mkdir(mode=0o755)
            directory.chmod(0o755)
        elif not directory.is_dir():
            raise ValueError('Unexpected HQIP log directory')
    run('chown', 'root:adm', NGINX_LOG_DIR)
    if (NGINX_LOG_DIR / 'access.log').is_symlink():
        raise ValueError('HQIP access log must not be a symlink')
    changed = not JOURNAL_LIMITS.is_file() or digest(JOURNAL_LIMITS) != digest(sources[0][0])
    # The config is parsed in dry-run mode before replacing the installed copy.
    # No logs or global logrotate state are modified by this check.
    run('/usr/sbin/logrotate', '--debug', '--state', '/dev/null', sources[1][0])
    for source, target in sources:
        shutil.copyfile(source, target)
        target.chmod(0o644)
    if changed:
        run('systemctl', 'try-restart', 'systemd-journald@hqip-control.service')


def prepare(release, state, rules=None):
    release = ordinary_directory(release)
    if release.parent != BASE / 'releases':
        raise ValueError('Use a verified release directly under the release directory')
    manifest = verify_full_release(release)
    ordinary_directory(state)
    ordinary_directory(STAGE)
    if state.parent != BACKUPS or state.exists() or STAGE.exists() or PREP_UNIT.exists():
        raise ValueError('Fresh preparation refuses existing state, staging data or preparation service')
    require_free_space(release, STAGE, state, STORAGE)
    ordinary_directory(STORAGE)
    for name in ('compose.yaml', 'init-roles.sql', 'redis.conf'):
        if (STORAGE / name).is_symlink() or digest(STORAGE / name) != digest(release / 'deploy/storage' / name):
            raise ValueError('Generated storage configuration does not match this release')
    app_env = environment(STORAGE / 'storage.env', 'hqip_app')
    migration_env = environment(STORAGE / 'migration.env', 'hqip_migrator')
    for name in ('pg_dump', 'pg_restore'):
        if ' 18.' not in run(name, '--version'):
            raise ValueError('PostgreSQL 18 backup tools are required')
    # Existing website TLS remains valid; business DNS credentials are not copied.
    for host in DOMAINS:
        for name in ('fullchain.pem', 'privkey.pem'):
            if not (Path('/etc/letsencrypt/live') / host / name).is_file():
                raise ValueError('Missing existing website certificate: ' + host)
    shutil.copyfile(release / 'deploy/hqip-storage.service', STORAGE_UNIT)
    STORAGE_UNIT.chmod(0o644)
    run('systemctl', 'daemon-reload')
    run('systemctl', 'enable', '--now', 'hqip-storage.service')
    assert_database_empty()
    install_log_limits(release)
    state.mkdir(mode=0o700)
    STAGE.mkdir(mode=0o700)
    import pwd
    try:
        pwd.getpwnam('hqip-control')
    except KeyError:
        run('useradd', '--system', '--home-dir', DATA, '--shell', '/usr/sbin/nologin', 'hqip-control')
    run('chown', 'hqip-control:hqip-control', STAGE)
    record = {'version': 1, 'phase': 'initializing', 'release': str(release),
              'sourceSHA': manifest['sourceSHA'], 'stage': str(STAGE)}
    save(state, record)
    initialize_admin(release, migration_env, rules)
    info = empty_inventory()
    assert_empty(info)
    if info['mustChange'] != 1:
        raise ValueError('Initial administrator must change password')
    record.update(controllerID=info['controllerID'], masterSHA256=digest(STAGE / 'master.key'))
    unit = (release / 'deploy/hqip-control.service').read_text()
    unit = unit.replace('/opt/hqip-control/current', str(release)).replace(str(DATA), str(STAGE))
    unit = unit.replace('/etc/hqip-control/storage.env', str(STORAGE / 'storage.env'))
    unit = unit.replace('127.0.0.1:18090', '127.0.0.1:18190').replace('127.0.0.1:18091', '127.0.0.1:18191')
    PREP_UNIT.write_text(unit)
    PREP_UNIT.chmod(0o644)
    run('systemctl', 'daemon-reload')
    run('systemctl', 'start', 'hqip-control-prepare.service')
    ready((18190, 18191))
    # The app role is used here, proving initialization grants are effective.
    probe = json.loads(run(release / 'hqip', 'storage-info', '--data-dir', STAGE, env=app_env))
    if probe.get('backend') != 'postgresql' or probe.get('masterSHA256') != record['masterSHA256']:
        raise ValueError('Prepared storage identity mismatch')
    require_free_space(release, STAGE, state, STORAGE)
    record['phase'] = 'prepared'
    save(state, record)
    return record


def validate_retirement_receipt(receipt):
    if receipt.is_symlink() or not receipt.is_file() or (os.name != 'nt' and receipt.stat().st_mode & 0o077):
        raise ValueError('Final archive receipt must be a private ordinary file')
    value = json.loads(receipt.read_text())
    archive = Path(value.get('archive', ''))
    if (value.get('version') != 1 or value.get('verified') is not True
            or value.get('sourceDataDir') != str(DATA) or not archive.is_absolute()
            or not archive.is_file() or archive.is_symlink() or BACKUPS not in archive.resolve().parents):
        raise ValueError('Verified final HQIP archive receipt required')
    if value.get('sourceInventorySHA256') != inventory(DATA) or value.get('archiveSHA256') != digest(archive):
        raise ValueError('Final archive no longer matches stopped source data')
    return value


def activate(state, receipt):
    record = load(state)
    if record['phase'] != 'prepared':
        raise ValueError('Only a verified prepared station can be activated once')
    release = Path(record['release'])
    verify_full_release(release)
    require_free_space(release, STAGE, state, DATA, STORAGE)
    assert_empty(empty_inventory())
    ready((18190, 18191))
    if digest(STAGE / 'master.key') != record['masterSHA256']:
        raise ValueError('Prepared master key changed')
    status = subprocess.run(['systemctl', 'is-active', 'hqip-control.service'], capture_output=True, text=True)
    if status.stdout.strip() not in ('inactive', 'failed', 'unknown'):
        raise ValueError('Stop the retired controller and verify its final archive before activation')
    validate_retirement_receipt(receipt)
    if (state / 'final-retired').exists():
        raise ValueError('Retired data destination already exists')
    ordinary_directory(CONFIG)
    previous = (BASE / 'current').resolve(strict=True)
    if previous.parent != BASE / 'releases':
        raise ValueError('Unexpected prior release location')
    if len({DATA.stat().st_dev, STAGE.stat().st_dev, state.stat().st_dev}) != 1:
        raise ValueError('Atomic activation requires staging, data and retirement backup on one filesystem')
    pending = BASE / 'current.fresh'
    if pending.exists() or pending.is_symlink():
        raise ValueError('Unexpected pending release link')
    if ENABLED_SITE.is_symlink():
        if ENABLED_SITE.resolve() != SITE:
            raise ValueError('Unexpected enabled site target')
    elif ENABLED_SITE.exists():
        raise ValueError('Unexpected ordinary enabled site')
    for path, name in ((SITE, 'nginx.before.conf'), (UNIT, 'unit.before.service'),
                       (CONFIG / 'storage.env', 'storage.before.env')):
        if path.is_symlink():
            raise ValueError('Unexpected shared configuration symlink')
        if path.is_file():
            shutil.copyfile(path, state / name)
            (state / name).chmod(0o600)
    record['previousRelease'] = str(previous)
    record['finalArchiveVerifiedReceipt'] = str(receipt)
    record['phase'] = 'activating'
    save(state, record)
    run('systemctl', 'stop', 'hqip-control-prepare.service')
    # Recheck after stopping the staging service, immediately before moving data.
    validate_retirement_receipt(receipt)
    require_free_space(release, STAGE, state, DATA, STORAGE)
    DATA.rename(state / 'final-retired')
    STAGE.rename(DATA)
    CONFIG.mkdir(mode=0o700, exist_ok=True)
    shutil.copyfile(STORAGE / 'storage.env', CONFIG / 'storage.env')
    (CONFIG / 'storage.env').chmod(0o600)
    shutil.copyfile(release / 'deploy/hqip-control.service', UNIT)
    shutil.copyfile(release / 'deploy/nginx-hqip.conf', SITE)
    UNIT.chmod(0o644)
    SITE.chmod(0o644)
    install_log_limits(release)
    if not ENABLED_SITE.is_symlink():
        ENABLED_SITE.symlink_to(SITE)
    pending.symlink_to(release)
    pending.replace(BASE / 'current')
    run('nginx', '-t')
    run('systemctl', 'daemon-reload')
    run('systemctl', 'enable', '--now', 'hqip-control.service')
    ready((18090, 18091))
    run('systemctl', 'reload', 'nginx')
    record['phase'] = 'active'
    save(state, record)
    return record


def verify(state, public=False):
    record = load(state)
    if record['phase'] not in ('prepared', 'active', 'finalized'):
        raise ValueError('Deployment is incomplete; preserve both datasets and fix forward')
    verify_full_release(Path(record['release']))
    active = record['phase'] in ('active', 'finalized')
    directory = DATA if active else STAGE
    if digest(directory / 'master.key') != record['masterSHA256'] or not (directory / 'postgresql.enabled').is_file():
        raise ValueError('Fresh PostgreSQL data identity changed')
    if active and (BASE / 'current').resolve() != Path(record['release']):
        raise ValueError('A different release is active')
    value = empty_inventory()
    if record['phase'] != 'finalized':
        assert_empty(value)
    if value['controllerID'] != record['controllerID']:
        raise ValueError('Fresh database identity changed')
    ready((18090, 18091) if active else (18190, 18191))
    if public:
        if not active:
            raise ValueError('Public verification requires activation')
        for url in ('https://' + DOMAINS[0] + '/', 'https://' + DOMAINS[1] + '/',
                    'https://' + DOMAINS[2] + '/healthz',
                    'https://' + DOMAINS[2] + '/readyz'):
            with urllib.request.urlopen(url, timeout=20) as response:
                if response.status != 200 or (url.endswith(('/healthz', '/readyz')) and json.load(response).get('ok') is not True):
                    raise ValueError('Public station verification failed')
    return record


def finalize(state):
    record = verify(state, public=True)
    if record['phase'] != 'active':
        raise ValueError('Only the accepted active station can be finalized once')
    backup = state / 'initial-postgres.tar.gz'
    run(Path(record['release']) / 'hqip', 'backup', '--data-dir', DATA, '--output', backup,
        env=environment(CONFIG / 'storage.env', 'hqip_app'), timeout=1900)
    record['initialBackupSHA256'] = digest(backup)
    run('systemctl', 'disable', '--now', 'hqip-control-prepare.service')
    PREP_UNIT.unlink()
    run('systemctl', 'daemon-reload')
    record['phase'] = 'finalized'
    save(state, record)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('verify-release', 'inventory', 'prepare', 'activate', 'verify', 'finalize'))
    parser.add_argument('--release', type=Path)
    parser.add_argument('--state', type=Path)
    parser.add_argument('--rules', type=Path)
    parser.add_argument('--retirement-receipt', type=Path)
    parser.add_argument('--public', action='store_true')
    args = parser.parse_args()
    if args.action == 'verify-release':
        print(json.dumps({'sourceSHA': verify_full_release(args.release)['sourceSHA'], 'releaseKind': 'full'}))
        return
    if os.geteuid() != 0:
        raise PermissionError('Run the explicit new-station workflow as root')
    if args.action == 'inventory':
        print(json.dumps({'sourceDataDir': str(DATA), 'sourceInventorySHA256': inventory(DATA)}))
        return
    if not args.state:
        parser.error('--state is required')
    import fcntl
    with (BASE / 'deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.action == 'prepare':
            if not args.release:
                parser.error('--release is required')
            result = prepare(args.release, args.state, args.rules)
        elif args.action == 'activate':
            if not args.retirement_receipt:
                parser.error('--retirement-receipt is required')
            result = activate(args.state, args.retirement_receipt)
        elif args.action == 'verify':
            result = verify(args.state, args.public)
        else:
            result = finalize(args.state)
    print(json.dumps({key: result[key] for key in ('phase', 'sourceSHA')}))


if __name__ == '__main__':
    main()
