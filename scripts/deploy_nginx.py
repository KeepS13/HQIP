"""Run on the target Ubuntu server: prepare, activate, finalize or rollback.

This replaces only the HQIP virtual host. It never installs Caddy, resets the
legacy PostgreSQL database, or stops another application. Transfer a verified
release directory first; state and secrets must remain outside the repository.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import socket
import subprocess
import urllib.error
import urllib.request

SITE = pathlib.Path('/etc/nginx/sites-available/hqip.conf')
DATA = pathlib.Path('/var/lib/hqip-control')
CURRENT = pathlib.Path('/opt/hqip-control/current')
CONFIG = pathlib.Path('/etc/hqip-control')
UNIT = pathlib.Path('/etc/systemd/system/hqip-control.service')
USER = 'hqip-control'
DOMAIN = 'hqip.5202345.xyz'


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def capture(*args):
    return subprocess.check_output(args, text=True).strip()


def digest(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def save(path, value):
    tmp = path.with_suffix('.new')
    tmp.write_text(json.dumps(value, indent=2)+'\n')
    tmp.chmod(0o600)
    tmp.replace(path)


def check_http(url, expected):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(url, timeout=15) as response:
            code = response.status
    except urllib.error.HTTPError as error:
        code = error.code
    if code != expected:
        raise RuntimeError(f'HTTP check failed: {url} returned {code}, expected {expected}')


def database_backup(path):
    with path.open('wb') as output:
        run('runuser', '-u', 'postgres', '--', 'pg_dump', '-Fc', 'hqip', stdout=output)
    path.chmod(0o600)
    with open(os.devnull, 'w') as sink:
        run('pg_restore', '--list', str(path), stdout=sink)


def validate_release(release):
    manifest = json.loads((release/'release.json').read_text())
    if not re.fullmatch(r'[0-9a-f]{40}', manifest['sourceSHA']):
        raise ValueError('Release requires a full source commit SHA')
    required = {'hqip', 'artifacts/manifest.json', 'artifacts/agent-linux-amd64',
                'artifacts/agent-linux-arm64', 'artifacts/hysteria-linux-amd64',
                'artifacts/hysteria-linux-arm64', 'deploy/hqip-control.service', 'deploy/nginx-hqip.conf'}
    if not required.issubset(manifest['sha256']):
        raise ValueError('Release is missing required files')
    for name, expected in manifest['sha256'].items():
        path = (release/name).resolve()
        if not path.is_relative_to(release) or not path.is_file() or digest(path) != expected:
            raise ValueError('Release checksum mismatch: '+name)
    return manifest


def prepare(state, release, rules):
    import pwd
    if (state/'state.json').exists() or CURRENT.exists() or (DATA/'hqip.db').exists():
        raise ValueError('A prepared deployment or database already exists; refusing to overwrite')
    manifest = validate_release(release)
    for port in (18090, 18091):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', port))
    if DOMAIN not in SITE.read_text():
        raise ValueError('Expected HQIP nginx site was not found')
    bundle = json.loads(rules.read_text())
    if len(bundle.get('rules', [])) != 1709 or not bundle.get('geoipCN', {}).get('CIDRs', bundle.get('geoipCN', {}).get('cidrs')):
        raise ValueError('Expected 1709-rule bundle with China IP data')
    previous = pathlib.Path(capture('readlink', '-f', '/var/www/hqip/current'))
    if not previous.is_relative_to('/var/www/hqip/releases'):
        raise ValueError('Unexpected legacy release path')
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    state.chmod(0o700)
    shutil.copy2(SITE, state/'nginx.before.conf')
    (state/'nginx.before.conf').chmod(0o600)
    with (state/'legacy-code-and-config.tar.gz').open('wb') as output:
        pm2_dump = pathlib.Path(pwd.getpwnam('hqip').pw_dir)/'.pm2/dump.pm2'
        run('tar', '-czf', '-', '-C', '/', str(previous).lstrip('/'), 'var/www/hqip/shared', str(pm2_dump).lstrip('/'), stdout=output)
    database_backup(state/'legacy.before.dump')
    processes = json.loads(capture('runuser', '-u', 'hqip', '--', 'pm2', 'jlist'))
    if {p['name'] for p in processes} != {'hqip-frontend','hqip-backend'}:
        raise ValueError('Unexpected legacy PM2 process set; review before replacing')
    baseline = {str(p):digest(p.resolve()) for p in pathlib.Path('/etc/nginx/sites-enabled').iterdir() if p.resolve()!=SITE}
    save(state/'state.json', {'phase':'backed-up','release':str(release),'sourceSHA':manifest['sourceSHA'],
         'previous':str(previous),'nginxSHA':digest(SITE),'otherSites':baseline,'rulesSHA':digest(rules)})
    try:
        pwd.getpwnam(USER)
    except KeyError:
        run('useradd','--system','--home-dir',str(DATA),'--shell','/usr/sbin/nologin',USER)
    run('install','-d','-m','700','-o',USER,'-g',USER,str(DATA))
    run('install','-d','-m','755',str(CONFIG),'/etc/systemd/journald@hqip-control.conf.d')
    run('install','-m','600','-o',USER,'-g',USER,str(rules),str(DATA/'initial-rules.json'))
    os.symlink(release, CURRENT)
    (release/'hqip').chmod(0o755)
    for artifact in (release/'artifacts').glob('*-linux-*'):
        artifact.chmod(0o755)
    run('runuser','-u',USER,'--',str(CURRENT/'hqip'),'init-admin','--data-dir',str(DATA),'--output',str(DATA/'bootstrap-admin.json'),'--rules',str(DATA/'initial-rules.json'))
    shutil.copy2(release/'deploy/hqip-control.service', UNIT)
    UNIT.chmod(0o644)
    shutil.copy2(release/'deploy/nginx-hqip.conf', CONFIG/'nginx.candidate.conf')
    pathlib.Path('/etc/systemd/journald@hqip-control.conf.d/limits.conf').write_text('[Journal]\nSystemMaxUse=100M\nRuntimeMaxUse=16M\nMaxRetentionSec=7day\n')
    pathlib.Path('/etc/logrotate.d/hqip-control').write_text('/var/log/nginx/hqip-control.access.log {\n daily\n rotate 7\n size 10M\n missingok\n notifempty\n compress\n copytruncate\n}\n')
    run('systemctl','daemon-reload')
    run('systemctl','start','hqip-control')
    # systemd Type=simple becomes active before listen() completes.
    import time
    for attempt in range(20):
        try:
            check_http('http://127.0.0.1:18090/healthz',200)
            check_http('http://127.0.0.1:18091/healthz',200)
            break
        except (OSError,RuntimeError):
            if attempt==19: raise
            time.sleep(1)
    check_http('http://127.0.0.1:18091/api/v1/nodes',404)
    record=json.loads((state/'state.json').read_text());record['phase']='prepared';save(state/'state.json',record)


def rollback(state, record):
    shutil.copy2(state/'nginx.before.conf', SITE)
    SITE.chmod(0o644)
    run('nginx','-t')
    run('systemctl','disable','--now','hqip-control')
    run('runuser','-u','hqip','--','pm2','start','/var/www/hqip/shared/ecosystem.config.cjs')
    run('runuser','-u','hqip','--','pm2','save')
    run('systemctl','enable','pm2-hqip')
    run('systemctl','reload','nginx')
    record['phase']='rolled-back';save(state/'state.json',record)


def activate(state, record):
    if record['phase'] != 'prepared' or digest(SITE)!=record['nginxSHA']:
        raise ValueError('Prepared state or legacy nginx changed')
    try:
        run('runuser','-u','hqip','--','pm2','stop','hqip-frontend','hqip-backend')
        database_backup(state/'legacy.final.dump')
        shutil.copy2(CONFIG/'nginx.candidate.conf', SITE)
        run('nginx','-t')
        run('systemctl','reload','nginx')
        check_http('https://'+DOMAIN+'/healthz',200)
        check_http('https://'+DOMAIN+'/',200)
        check_http('https://'+DOMAIN+'/api/v1/nodes',401)
        check_http('https://'+DOMAIN+'/api/v1/client/profile',401)
        check_http('https://'+DOMAIN+'/api/auth/me',404)
        check_http('https://'+DOMAIN+'/s/retired',410)
        record['phase']='active';save(state/'state.json',record)
    except Exception:
        rollback(state,record)
        raise


def finalize(state, record):
    if record['phase']!='active': raise ValueError('Only an accepted active deployment may be finalized')
    for path, expected in record['otherSites'].items():
        if digest(pathlib.Path(path).resolve())!=expected: raise RuntimeError('Another nginx site changed: '+path)
    check_http('https://'+DOMAIN+'/healthz',200)
    run('runuser','-u','hqip','--','pm2','delete','hqip-frontend','hqip-backend')
    run('runuser','-u','hqip','--','pm2','save','--force')
    run('systemctl','disable','--now','pm2-hqip')
    run('systemctl','enable','hqip-control')
    record['phase']='finalized';save(state/'state.json',record)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('mode',choices=['prepare','activate','finalize','rollback'])
    p.add_argument('--state',required=True,type=pathlib.Path)
    p.add_argument('--release',type=pathlib.Path)
    p.add_argument('--rules',type=pathlib.Path)
    args=p.parse_args()
    if os.geteuid()!=0: raise SystemExit('Run as root')
    os.umask(0o077)
    state=args.state.resolve()
    if state.parent!=pathlib.Path('/root/backups') or not re.fullmatch(r'hqip-control-[A-Za-z0-9-]+',state.name):
        raise ValueError('State must be /root/backups/hqip-control-<release>')
    if args.mode=='prepare':
        if not args.release or not args.rules: p.error('prepare requires --release and --rules')
        prepare(state,args.release.resolve(),args.rules.resolve())
    else:
        record=json.loads((state/'state.json').read_text())
        {'activate':activate,'finalize':finalize,'rollback':rollback}[args.mode](state,record)
    print('Deployment phase: '+json.loads((state/'state.json').read_text())['phase'])


if __name__=='__main__': main()
