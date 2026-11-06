"""Deploy this CI run's tested controller, web sites and Windows download."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def command(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def main():
    sha = os.environ['GITHUB_SHA']
    if not re.fullmatch('[a-f0-9]{40}', sha):
        raise ValueError('Full source commit required')
    # A slower, older CI run must not deploy after a newer main commit.
    latest = command(['gh', 'api', f'repos/{os.environ["GITHUB_REPOSITORY"]}/commits/main',
                      '--jq', '.sha'], capture_output=True, text=True).stdout.strip()
    if latest != sha:
        print('Skipped superseded main commit: ' + sha)
        return
    required = ['DEPLOY_HOST', 'DEPLOY_PORT', 'DEPLOY_USER', 'DEPLOY_SSH_KEY', 'DEPLOY_KNOWN_HOSTS']
    for name in required:
        if not os.environ.get(name, '').strip():
            raise ValueError('Missing repository secret: ' + name)
    host, port, user = (os.environ[name] for name in required[:3])
    if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9.-]*', host):
        raise ValueError('Invalid deployment host')
    if not port.isdecimal() or not 1 <= int(port) <= 65535:
        raise ValueError('Invalid SSH port')
    if not re.fullmatch(r'[a-z_][a-z0-9_-]*', user):
        raise ValueError('Invalid deployment user')
    run_id = os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT']
    if not re.fullmatch(r'\d+-\d+', run_id):
        raise ValueError('Invalid CI run ID')
    with tempfile.TemporaryDirectory(prefix='hqip-ssh-') as folder:
        key, known = Path(folder) / 'key', Path(folder) / 'known_hosts'
        key.write_text(os.environ['DEPLOY_SSH_KEY'].strip() + '\n')
        key.chmod(0o600)
        known.write_text(os.environ['DEPLOY_KNOWN_HOSTS'].strip() + '\n')
        common = ['-i', str(key), '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes',
                  '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + str(known),
                  '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=15',
                  '-o', 'ServerAliveCountMax=4']
        ssh = ['ssh', *common, '-p', port, f'{user}@{host}']
        root = [] if user == 'root' else ['sudo', '-n']

        def remote(args, **kwargs):
            return command([*ssh, shlex.join(root + args)], **kwargs)

        base = remote(['cat', '/opt/hqip-control/current/release.json'],
                      capture_output=True, text=True).stdout
        if json.loads(base)['sourceSHA'] == sha:
            print('Commit already active: ' + sha)
            check_public()
            return
        dist = ROOT / 'dist'
        (dist / 'base-manifest.json').write_text(base)
        node_manifest = remote(['cat', '/opt/hqip-control/current/artifacts/manifest.json'],
                               capture_output=True, text=True).stdout
        (dist / 'base-node-manifest.json').write_text(node_manifest)
        shutil.copyfile(dist / 'hqip-linux-amd64', dist / 'hqip-domains-linux-amd64')
        command([sys.executable, 'scripts/package_domains.py', '--base-manifest', 'dist/base-manifest.json',
                 '--base-node-manifest', 'dist/base-node-manifest.json'])
        info = json.loads((dist / 'domains-release.json').read_text())
        archive = dist / info['filename']
        if hashlib.sha256(archive.read_bytes()).hexdigest() != info['sha256']:
            raise ValueError('Archive checksum mismatch')
        # Private temporary directory, owned by the SSH user; clean only this run.
        incoming = command([*ssh, 'mktemp -d /tmp/hqip-ci.XXXXXXXXXX'], capture_output=True, text=True).stdout.strip()
        if not re.fullmatch(r'/tmp/hqip-ci\.[a-zA-Z0-9]{10}', incoming):
            raise ValueError('Unexpected remote temporary directory')
        state = f'/root/backups/hqip-ci-{sha}-{run_id}'
        try:
            command(['scp', *common, '-P', port, str(archive), f'{user}@{host}:{incoming}/release.tar.gz'])
            script = (ROOT / 'scripts/install_ci_release.py').read_text()
            remote(['python3', '-', sha, incoming + '/release.tar.gz', info['sha256'], run_id],
                   input=script, text=True)
            try:
                check_public()
            except Exception:
                print('Public health check failed; rolling back code and preserving live data.', flush=True)
                remote(['python3', '-B', f'/opt/hqip-control/releases/{sha}/scripts/upgrade_domains.py',
                        'rollback', '--state', state])
                raise
        finally:
            remote(['rm', '-rf', '--', incoming])
        summary = f'Deployed `{sha}` to https://hqip.5202345.xyz\n\nBackup: `{state}`\n'
        print(summary)
        if os.environ.get('GITHUB_STEP_SUMMARY'):
            with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
                output.write(summary)


def check_public():
    # curl retries transient edge failures; inspect JSON as well as HTTP status.
    for url in ['https://hqip.5202345.xyz/', 'https://cms-hqip.5202345.xyz/',
                'https://api-hqip.5202345.xyz/healthz']:
        result = command(['curl', '--fail', '--silent', '--show-error', '--retry', '5',
                          '--retry-delay', '2', '--retry-all-errors', '--max-time', '20',
                          '--user-agent', 'Go-http-client/1.1', url], capture_output=True, text=True)
        if url.endswith('/healthz') and json.loads(result.stdout).get('ok') is not True:
            raise ValueError('Public API is unhealthy')


if __name__ == '__main__':
    main()
