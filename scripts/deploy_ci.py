"""Deploy this CI run's tested controller, web sites and Windows download."""
from __future__ import annotations

import hashlib
import inspect
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
PRODUCTION_TARGET = ('107.151.244.106', '22', 'root')
MIN_FREE_BYTES = 3 * 1024 ** 3


def command(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def copy_remote_manifest(remote, source, destination):
    # Production artifacts can contain CRLF from a Windows release builder.
    # text=True normalizes them and breaks their pinned byte-level checksum.
    raw = remote(['cat', source], capture_output=True).stdout
    if not isinstance(raw, bytes):
        raise TypeError('Manifest transfer must preserve bytes')
    destination.write_bytes(raw)


def validate_target(host, port, user):
    if (host, port, user) != PRODUCTION_TARGET:
        raise ValueError('Deployment target must be 107.151.244.106:22/root')


def check_ci_space(archive_bytes=0, expanded_bytes=0, requirements=None):
    """Reserve upload and extraction bytes together when they share a filesystem."""
    if any(type(value) is not int or value < 0 for value in (archive_bytes, expanded_bytes)):
        raise ValueError('Invalid release disk budget')
    if requirements is None:
        requirements = [('/tmp', archive_bytes), ('/opt/hqip-control/releases', expanded_bytes),
                        ('/root/backups', 0), ('/var/lib/hqip-control', 0),
                        ('/etc/hqip-storage', 0), ('/var/lib/docker', 0)]
    filesystems = {}
    for location, budget in requirements:
        path = Path(location)
        while not path.exists():
            if path.parent == path:
                raise ValueError('Cannot locate deployment filesystem')
            path = path.parent
        device = path.stat().st_dev
        item = filesystems.setdefault(device, {'paths': [], 'reservedBytes': 0,
                                              'freeBytes': shutil.disk_usage(path).free})
        item['paths'].append(str(location))
        item['reservedBytes'] += budget
        item['freeBytes'] = min(item['freeBytes'], shutil.disk_usage(path).free)
    for item in filesystems.values():
        item['requiredBytes'] = MIN_FREE_BYTES + item['reservedBytes']
        if item['freeBytes'] < item['requiredBytes']:
            raise ValueError('Deployment requires 3 GiB free after upload/extraction on: ' + ', '.join(item['paths']))
    return list(filesystems.values())


def retain_releases(base, current_sha, previous_sha, verify_release, audit):
    """Delete only validated older full releases; keep the active rollback pair."""
    base = Path(base)
    releases = base / 'releases'
    if (not base.is_absolute() or base.resolve(strict=True) != base
            or releases.is_symlink() or not releases.is_dir() or releases.resolve() != releases):
        raise ValueError('Unexpected release root')
    if (not re.fullmatch('[a-f0-9]{40}', current_sha)
            or previous_sha is not None and (not re.fullmatch('[a-f0-9]{40}', previous_sha) or previous_sha == current_sha)):
        raise ValueError('Invalid retained release identity')
    current = releases / current_sha

    def active_unchanged():
        if not (base / 'current').is_symlink() or (base / 'current').resolve(strict=True) != current:
            raise ValueError('Active release changed; retention refused')

    def ordinary_tree(path):
        if path.is_symlink() or not path.is_dir() or path.resolve() != path or os.path.ismount(path):
            raise ValueError('Not an ordinary release directory')
        identity = path.stat()
        for parent, directories, files in os.walk(path, followlinks=False):
            for name in directories + files:
                item = Path(parent) / name
                mode = item.lstat()
                if (not (stat.S_ISREG(mode.st_mode) or stat.S_ISDIR(mode.st_mode))
                        or mode.st_dev != identity.st_dev or os.path.ismount(item)):
                    raise ValueError('Release contains links, mounts or special files')
        return identity

    active_unchanged()
    current_stat = ordinary_tree(current)
    manifest = verify_release(current)
    if manifest['sourceSHA'] != current_sha or manifest.get('previousSourceSHA') != previous_sha:
        raise ValueError('Current manifest does not match the retained pair')
    keep = {current_sha}
    if previous_sha:
        previous = releases / previous_sha
        ordinary_tree(previous)
        if verify_release(previous)['sourceSHA'] != previous_sha:
            raise ValueError('Previous release identity mismatch')
        keep.add(previous_sha)
    report = {'current': current_sha, 'previous': previous_sha, 'deleted': [], 'skipped': []}
    for candidate in sorted(releases.iterdir()):
        name = candidate.name
        if name in keep:
            audit({'action': 'keep', 'release': name})
            continue
        reason = None
        identity = None
        try:
            if not re.fullmatch('[a-f0-9]{40}', name):
                raise ValueError('Unexpected release directory name')
            identity = ordinary_tree(candidate)
            if identity.st_mtime_ns > current_stat.st_mtime_ns:
                raise ValueError('Release is newer than the active release')
            if verify_release(candidate)['sourceSHA'] != name:
                raise ValueError('Release manifest identity mismatch')
        except (ValueError, OSError, KeyError, TypeError) as error:
            reason = str(error)
        if reason:
            report['skipped'].append({'release': name, 'reason': reason})
            audit({'action': 'skip', 'release': name, 'reason': reason})
            continue
        active_unchanged()
        latest = candidate.lstat()
        if (candidate.is_symlink() or not stat.S_ISDIR(latest.st_mode)
                or (latest.st_dev, latest.st_ino) != (identity.st_dev, identity.st_ino)):
            raise ValueError('Release directory changed during validation')
        audit({'action': 'delete-start', 'release': name})
        shutil.rmtree(candidate)
        report['deleted'].append(name)
        audit({'action': 'deleted', 'release': name})
    active_unchanged()
    return report


def remote_space_script():
    return ('import json, os, shutil, sys\nfrom pathlib import Path\n'
            + 'MIN_FREE_BYTES = ' + str(MIN_FREE_BYTES) + '\n'
            + inspect.getsource(check_ci_space)
            + '\nprint(json.dumps({"diskPreflight":check_ci_space(int(sys.argv[1]),int(sys.argv[2]))}))\n')


def remote_retention_script():
    # Run the CI checkout's verifier, not executable code from deletion candidates.
    return ((ROOT / 'scripts/release_bundle.py').read_text(encoding='utf-8')
            + '\nimport fcntl, os, shutil, stat, sys, time, urllib.request\n'
            + 'MIN_FREE_BYTES = ' + str(MIN_FREE_BYTES) + '\n'
            + inspect.getsource(check_ci_space) + '\n' + inspect.getsource(retain_releases)
            + '''
base = Path('/opt/hqip-control')
if os.geteuid() != 0 or base.resolve(strict=True) != base:
    raise ValueError('Root and an ordinary production directory are required')
if not shutil.rmtree.avoids_symlink_attacks:
    raise ValueError('Symlink-safe release deletion is unavailable')
current_sha, previous_sha = sys.argv[1], sys.argv[2] or None
lock_fd = os.open(base / 'deploy.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
with os.fdopen(lock_fd, 'a') as lock:
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for port in (18090, 18091):
        with opener.open('http://127.0.0.1:%d/readyz' % port, timeout=10) as response:
            if response.status != 200 or json.load(response).get('ok') is not True:
                raise ValueError('Local storage readiness failed; retention refused')
    check_ci_space()
    audit_fd = os.open(base / 'release-retention.jsonl', os.O_APPEND | os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(audit_fd, 'a') as output:
        def audit(event):
            record = dict(event, at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), current=current_sha, previous=previous_sha)
            line = json.dumps(record, ensure_ascii=True)
            output.write(line + '\\n')
            output.flush()
            os.fsync(output.fileno())
            print(line, flush=True)
        result = retain_releases(base, current_sha, previous_sha,
                                 lambda path: verify_retained_release(path, current_sha), audit)
        result['filesystems'] = check_ci_space()
        audit(dict(result, action='complete'))
''')


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
    validate_target(host, port, user)
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
                  '-o', 'GlobalKnownHostsFile=/dev/null', '-o', 'UpdateHostKeys=no',
                  '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=15',
                  '-o', 'ServerAliveCountMax=4']
        ssh = ['ssh', *common, '-p', port, f'{user}@{host}']
        root = [] if user == 'root' else ['sudo', '-n']

        def remote(args, **kwargs):
            return command([*ssh, shlex.join(root + args)], **kwargs)

        base = remote(['cat', '/opt/hqip-control/current/release.json'],
                      capture_output=True, text=True).stdout
        previous = json.loads(base)
        if previous['sourceSHA'] == sha:
            print('Commit already active: ' + sha)
            check_public()
            remote(['python3', '-B', '-', sha, previous.get('previousSourceSHA') or ''],
                   input=remote_retention_script(), text=True)
            return
        dist = ROOT / 'dist'
        command([sys.executable, 'scripts/package_domains.py',
                 '--previous-source-sha', previous['sourceSHA']])
        info = json.loads((dist / 'domains-release.json').read_text())
        archive = dist / info['filename']
        if hashlib.sha256(archive.read_bytes()).hexdigest() != info['sha256']:
            raise ValueError('Archive checksum mismatch')
        with tarfile.open(archive, 'r:gz') as package:
            expanded_bytes = sum(member.size for member in package.getmembers())
        remote(['python3', '-B', '-', str(archive.stat().st_size), str(expanded_bytes)],
               input=remote_space_script(), text=True)
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
                remote(['python3', '-B', '-', sha, previous['sourceSHA']],
                       input=remote_retention_script(), text=True)
            except Exception:
                print('Post-deployment acceptance failed; rolling back code and preserving live data.', flush=True)
                remote(['python3', '-B', f'/opt/hqip-control/releases/{sha}/scripts/upgrade_domains.py',
                        'rollback', '--state', state])
                raise
        finally:
            remote(['rm', '-rf', '--', incoming])
        summary = (f'Deployed `{sha}` to https://hqip.sharevps.online\n\nBackup: `{state}`\n'
                   f'Release retention: current `{sha}`, previous `{previous["sourceSHA"]}`. '
                   'Audit: `/opt/hqip-control/release-retention.jsonl` (skipped directories require manual review).\n')
        print(summary)
        if os.environ.get('GITHUB_STEP_SUMMARY'):
            with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
                output.write(summary)


def check_public():
    # curl retries transient edge failures; inspect JSON as well as HTTP status.
    for url in ['https://hqip.sharevps.online/', 'https://cms-hqip.sharevps.online/',
                'https://api-hqip.sharevps.online/healthz',
                'https://api-hqip.sharevps.online/readyz']:
        result = command(['curl', '--fail', '--silent', '--show-error', '--retry', '5',
                          '--retry-delay', '2', '--retry-all-errors', '--max-time', '20',
                          '--user-agent', 'Go-http-client/1.1', url], capture_output=True, text=True)
        if url.endswith(('/healthz', '/readyz')) and json.loads(result.stdout).get('ok') is not True:
            raise ValueError('Public API is unhealthy')


if __name__ == '__main__':
    main()
