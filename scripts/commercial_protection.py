"""Install only HQIP protection units and validate Cloudflare before origin locking."""
from pathlib import Path
import ipaddress
import json
import os
import shutil
import socket
import subprocess
import urllib.request

from render_domains import API, USER, ADMIN, CF_RANGES

UNITS = ('hqip-backup.service', 'hqip-backup.timer', 'hqip-backup-retry.service', 'hqip-backup-retry.timer')
UNIT_ROOT = Path('/etc/systemd/system')


def verify_cloudflare(resolve=socket.getaddrinfo, opener=None):
    networks = tuple(ipaddress.ip_network(value) for value in CF_RANGES)
    opener = opener or urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for host, path in ((USER, '/'), (ADMIN, '/'), (API, '/healthz')):
        try:
            addresses = {ipaddress.ip_address(item[4][0]) for item in resolve(host, 443, type=socket.SOCK_STREAM)}
            if not addresses or any(not any(address in network for network in networks) for address in addresses):
                raise ValueError('DNS is not proxied')
            request = urllib.request.Request('https://' + host + path,
                                             method='HEAD', headers={'User-Agent': 'HQIP-Deployment/1.0'})
            with opener.open(request, timeout=20) as response:
                if response.status != 200 or not response.headers.get('CF-Ray'):
                    raise ValueError('Cloudflare edge unavailable')
        except Exception:
            raise ValueError('Cloudflare proxy verification failed for ' + host + '; current origin configuration was preserved') from None


def prepare(release, manifest, state, run=subprocess.run):
    if manifest.get('commercialProtectionVersion') != 1:
        return False
    verify_cloudflare()
    record = {}
    for name in UNITS:
        source, target = release / 'deploy' / name, UNIT_ROOT / name
        if source.is_symlink() or not source.is_file() or target.is_symlink() or (target.exists() and not target.is_file()):
            raise ValueError('Invalid protection unit')
        previous = target.is_file()
        if previous:
            shutil.copyfile(target, state / (name + '.before'))
        record[name] = previous
    record['timerEnabled'] = run(['systemctl', 'is-enabled', 'hqip-backup.timer'], capture_output=True, check=False).returncode == 0
    (state / 'protection-before.json').write_text(json.dumps(record))
    (state / 'protection-before.json').chmod(0o600)
    return True


def install(release, execute, data=Path('/var/lib/hqip-control')):
    from automatic_backup import initialize_paths, publish_json, next_run, utc
    initialize_paths(data=data)
    for name in UNITS:
        source, target = release / 'deploy' / name, UNIT_ROOT / name
        if source.is_symlink() or not source.is_file() or target.is_symlink() or (target.exists() and not target.is_file()):
            raise ValueError('Invalid protection unit')
        shutil.copyfile(source, target)
        target.chmod(0o644)
    execute('systemctl', 'daemon-reload')
    execute('systemctl', 'enable', '--now', 'hqip-backup.timer')
    if not (data / 'backup-status.json').exists():
        publish_json(data / 'backup-status.json', {'version': 1, 'enabled': True, 'intervalHours': 6,
                     'retentionDays': 14, 'nextRunAt': next_run(utc())}, shared=True)


def rollback(state, execute):
    path = state / 'protection-before.json'
    if not path.exists():
        return
    if path.is_symlink():
        raise ValueError('Invalid protection rollback state')
    record = json.loads(path.read_text())
    timers = [name for name in ('hqip-backup.timer', 'hqip-backup-retry.timer') if (UNIT_ROOT / name).is_file()]
    if timers:
        execute('systemctl', 'stop', *timers)
    if (UNIT_ROOT / 'hqip-backup.timer').is_file():
        execute('systemctl', 'disable', 'hqip-backup.timer')
    for name in UNITS:
        target, saved = UNIT_ROOT / name, state / (name + '.before')
        if target.is_symlink() or saved.is_symlink():
            raise ValueError('Invalid protection rollback unit')
        if record.get(name):
            shutil.copyfile(saved, target)
            target.chmod(0o644)
        elif target.exists():
            target.unlink()
    execute('systemctl', 'daemon-reload')
    if record.get('timerEnabled'):
        execute('systemctl', 'enable', '--now', 'hqip-backup.timer')


def prepare_operation_logs(deploy, manifest):
    if not manifest.get('operationLogsVersion') or not deploy.postgres_enabled():
        return None
    if manifest['operationLogsVersion'] != 1:
        raise ValueError('Unsupported operation logs schema')
    if deploy.postgres_info().get('operationLogsVersion', 0) >= 1:
        return None
    from urllib.parse import urlsplit
    environment = deploy.storage_environment(deploy.MIGRATION_ENV)
    application = urlsplit(deploy.storage_environment()['HQIP_DATABASE_URL'])
    migration = urlsplit(environment['HQIP_DATABASE_URL'])
    if (application.hostname, application.port, application.path, application.query) != (migration.hostname, migration.port, migration.path, migration.query) or migration.username != 'hqip_migrator':
        raise ValueError('Operation logs migration must target the active database with the migration role')
    return environment


def migrate_operation_logs(release, environment, execute=subprocess.run):
    if environment is None:
        return
    result = execute([str(release / 'hqip'), 'migrate-operation-logs'], env=environment,
                     capture_output=True, timeout=120, check=False)
    if result.returncode:
        raise RuntimeError('Operation logs migration failed; live data is preserved')


def ensure_initial_backup(release, *, data=Path('/var/lib/hqip-control'), execute=subprocess.run):
    from automatic_backup import read_json
    manifest = json.loads((release / 'release.json').read_text())
    if manifest.get('commercialProtectionVersion') != 1:
        return
    status = data / 'backup-status.json'
    if status.exists() and read_json(status).get('lastSuccessAt'):
        return
    result = execute(['systemctl', 'start', 'hqip-backup.service'], capture_output=True,
                     timeout=2100, check=False)
    if result.returncode or not status.exists() or not read_json(status).get('lastSuccessAt'):
        raise RuntimeError('Initial automatic backup verification failed; active service and live data are preserved')


def defer_origin(release, manifest, state, site):
    if manifest.get('commercialProtectionVersion') != 1:
        return False
    current = site.read_text() if site.is_file() else ''
    if 'geo $realip_remote_addr $hqip_cloudflare_peer' in current and 'if ($hqip_cloudflare_peer = 0)' in current:
        return False
    if not site.is_file() or site.is_symlink():
        raise ValueError('An existing HQIP site is required before deferred origin activation')
    from automatic_backup import publish_json
    publish_json(state / 'origin-pending.json', {'version': 1, 'release': str(release), 'pending': True})
    return True


def complete_release(release, state, execute, *, site=Path('/etc/nginx/sites-available/hqip.conf'),
                     base=Path('/opt/hqip-control'), data=Path('/var/lib/hqip-control'), lock=None):
    from automatic_backup import deployment_lock, read_json, publish_json
    # This must run outside deploy.lock: the systemd backup acquires that lock.
    ensure_initial_backup(release, data=data)
    pending = state / 'origin-pending.json'
    if not pending.exists() or not read_json(pending).get('pending'):
        return
    with (lock or deployment_lock)(base):
        record = read_json(pending)
        if record.get('version') != 1 or record.get('release') != str(release) or (base / 'current').resolve(strict=True) != release:
            raise ValueError('Active release changed; deferred origin activation refused')
        verify_cloudflare()
        if site.is_symlink() or not site.is_file():
            raise ValueError('Invalid HQIP origin configuration')
        saved = state / 'nginx.origin-before.conf'
        if saved.exists() or saved.is_symlink():
            raise ValueError('Deferred origin state already exists; inspect before retrying')
        shutil.copyfile(site, saved)
        saved.chmod(0o600)
        try:
            shutil.copyfile(release / 'deploy/nginx-hqip.conf', site)
            site.chmod(0o644)
            execute('nginx', '-t')
            execute('systemctl', 'reload', 'nginx')
            verify_cloudflare()
        except Exception:
            shutil.copyfile(saved, site)
            site.chmod(0o644)
            execute('nginx', '-t')
            execute('systemctl', 'reload', 'nginx')
            saved.unlink()
            raise
        record['pending'] = False
        publish_json(pending, record)


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('first-backup',))
    parser.add_argument('--release', type=Path, required=True)
    arguments = parser.parse_args()
    if os.name == 'nt' or os.geteuid() != 0:
        raise PermissionError('Run backup acceptance on the control host as root')
    ensure_initial_backup(arguments.release)
