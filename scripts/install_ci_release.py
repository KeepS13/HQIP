"""Run over pinned SSH; install a complete release without inheriting production files."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import sys
import tarfile


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def safe_name(name):
    path = PurePosixPath(name)
    if not name or path.is_absolute() or '..' in path.parts or '\\' in name or path.as_posix() != name:
        raise ValueError('Unsafe payload path: ' + name)
    return name


def materialize(archive, expected, sha, current, release):
    if digest(archive) != expected:
        raise ValueError('Uploaded archive checksum mismatch')
    previous = json.loads((current / 'release.json').read_text()) if current else None
    with tarfile.open(archive, 'r:gz') as package:
        members = package.getmembers()
        names = [safe_name(m.name) for m in members]
        if len(names) != len(set(names)) or any(not m.isfile() for m in members):
            raise ValueError('Only unique ordinary files are allowed')
        manifest = json.load(package.extractfile('release.json'))
        if (manifest['sourceSHA'] != sha or manifest.get('releaseKind') != 'full'
                or manifest.get('upgradeKind') != 'full-release'
                or previous and manifest.get('previousSourceSHA') != previous['sourceSHA']):
            raise ValueError('Release is not based on the active commit')
        if not previous and manifest.get('previousSourceSHA'):
            raise ValueError('Fresh deployment requires a standalone full release')
        hashes = manifest['sha256']
        for name, checksum in hashes.items():
            safe_name(name)
            if not re.fullmatch('[a-f0-9]{64}', checksum):
                raise ValueError('Invalid payload hash')
        if set(names) != set(hashes) | {'release.json'}:
            raise ValueError('Complete archive required; missing or unmanifested files')
        # This helper is deliberately incapable of overwriting an existing release.
        release.mkdir(mode=0o755, exist_ok=False)
        for name, expected_hash in hashes.items():
            destination = release / name
            destination.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
            with package.extractfile(name) as source, destination.open('xb') as output:
                shutil.copyfileobj(source, output)
            if digest(destination) != expected_hash:
                raise ValueError('Payload checksum mismatch: ' + name)
            destination.chmod(0o755 if name in {'hqip','hqip-migrate-postgres'} or name.startswith('artifacts/') and '-linux-' in name else 0o644)
        (release / 'release.json').write_text(json.dumps(manifest, indent=2) + '\n')
        (release / 'release.json').chmod(0o644)
        # mkdir's mode is reduced by a restrictive deployment umask. Only this
        # newly owned tree is widened; shared parents and old releases are not.
        # chmod before rename keeps both fresh and CI final paths traversable.
        release.chmod(0o755)
        for directory in release.rglob('*'):
            if directory.is_dir():
                if directory.is_symlink():
                    raise ValueError('Unexpected symlink in owned release tree')
                directory.chmod(0o755)


def main():
    import fcntl
    import os
    import subprocess
    fresh = len(sys.argv) == 5 and sys.argv[1] == '--fresh'
    if fresh:
        _, sha, archive, checksum = sys.argv[1:]
        run_id = 'fresh'
    else:
        sha, archive, checksum, run_id = sys.argv[1:]
    if os.geteuid() != 0 or not re.fullmatch('[a-f0-9]{40}', sha) or (not fresh and not re.fullmatch(r'\d+-\d+', run_id)):
        raise ValueError('Root and valid CI identifiers required')
    base = Path('/opt/hqip-control')
    for directory in (base, base / 'releases'):
        if directory.is_symlink():
            raise ValueError('Unexpected release parent symlink')
        try:
            directory.mkdir(mode=0o755)
        except FileExistsError:
            if not directory.is_dir() or directory.stat().st_mode & 0o011 != 0o011:
                raise ValueError('Existing release parent must be traversable by application and web users')
        else:
            directory.chmod(0o755)  # Only a directory this invocation created.
    with (base / 'deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if fresh:
            # This explicit invocation only unpacks. It never initializes a
            # database or changes current; fresh_deploy owns those later steps.
            release = base / 'releases' / sha
            if release.exists() or release.is_symlink():
                raise ValueError('Release directory already exists')
            staging = base / 'releases' / ('.fresh-' + sha)
            materialize(Path(archive), checksum, sha, None, staging)
            subprocess.run(['python3', '-B', str(staging / 'scripts/fresh_deploy.py'),
                            'verify-release', '--release', str(staging)], check=True)
            staging.rename(release)
            print(json.dumps({'release': str(release), 'sourceSHA': sha, 'phase': 'unpacked'}))
            return
        current = (base / 'current').resolve(strict=True)
        if current.parent != base / 'releases':
            raise ValueError('Unexpected active release location')
        # Import the existing verifier only after pinning its release location.
        sys.dont_write_bytecode = True
        sys.path.insert(0, str(current / 'scripts'))
        import upgrade_control as control
        control.verify_release(current)
        release = base / 'releases' / sha
        if release.exists() or release.is_symlink():
            raise ValueError('Release directory already exists; inspect it before retrying')
        staging = base / 'releases' / f'.ci-{sha}-{run_id}'
        materialize(Path(archive), checksum, sha, current, staging)
        control.verify_release(staging)
        # Run only a file already verified against the uploaded manifest. It
        # validates required assets and current-commit node/client provenance.
        subprocess.run(['python3', '-B', str(staging / 'scripts/fresh_deploy.py'),
                        'verify-release', '--release', str(staging)], check=True)
        staging.rename(release)
        state = '/root/backups/hqip-ci-' + sha + '-' + run_id
        subprocess.run(['python3', '-B', str(release / 'scripts/upgrade_domains.py'),
                        'upgrade', '--release', str(release), '--state', state], check=True)


if __name__ == '__main__':
    main()
