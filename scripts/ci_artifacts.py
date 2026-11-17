"""Private, unpublished release transport when Actions artifact storage is full.

Only trusted main-branch CI may use this fallback. A draft is scoped to one run
and one platform, and retries may replace it only for the same source commit.
The receipt checks transport integrity; the existing production installer still
performs its independent complete-release checks and health/rollback gates.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tempfile
import zipfile

LINUX_FILES = ('hqip-linux-amd64', 'hqip-migrate-postgres-linux-amd64',
               'agent-linux-amd64', 'agent-linux-arm64', 'hysteria-linux-amd64',
               'hysteria-linux-arm64', 'caddy-linux-amd64', 'manifest.json')
WINDOWS_FILES = ('HQIP-Windows-x64.zip', 'desktop-manifest.json')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def identity(env):
    repo, run, sha = (env.get(key, '') for key in ('GITHUB_REPOSITORY', 'GITHUB_RUN_ID', 'GITHUB_SHA'))
    if (env.get('GITHUB_REF') != 'refs/heads/main' or env.get('GITHUB_EVENT_NAME') not in ('push', 'workflow_dispatch')
            or not re.fullmatch(r'[\w.-]+/[\w.-]+', repo) or not re.fullmatch(r'\d+', run)
            or not re.fullmatch(r'[a-f0-9]{40}', sha)):
        raise ValueError('Release transport requires trusted main-branch CI identity')
    return repo, run, sha


def allowed(kind, name):
    path = PurePosixPath(name)
    if (not name or '\\' in name or ':' in name or path.is_absolute()
            or '..' in path.parts or str(path) != name):
        return False
    if kind == 'windows-release':
        return name in WINDOWS_FILES
    return (name in {'dist/' + item for item in LINUX_FILES}
            or name.startswith(('admin-web/dist/', 'user-web/dist/')))


def verify_files(kind, root, sha):
    """Validate same-commit, clean build manifests before upload and after download."""
    folder = root / 'dist' if kind == 'linux-release' else root
    manifest = json.loads((folder / ('manifest.json' if kind == 'linux-release' else 'desktop-manifest.json')).read_text())
    if manifest.get('sourceSHA') != sha or manifest.get('dirty') is not False:
        raise ValueError('Artifact source commit or clean-build identity mismatch')
    if kind == 'linux-release':
        for name in LINUX_FILES[:-1]:
            if digest(folder / name) != manifest['sha256'].get(name):
                raise ValueError('Linux artifact checksum mismatch: ' + name)
        for site in ('admin-web', 'user-web'):
            if not (root / site / 'dist/index.html').is_file():
                raise ValueError('Missing web artifact: ' + site)
    elif (manifest.get('filename') != WINDOWS_FILES[0]
          or digest(folder / WINDOWS_FILES[0]) != manifest.get('sha256')
          or (folder / WINDOWS_FILES[0]).stat().st_size != manifest.get('size')):
        raise ValueError('Windows artifact checksum or size mismatch')


def package(kind, root, output, sha):
    # Explicit roots reproduce the existing Actions upload layout. No credentials,
    # repository working files, or unrelated generated files enter the archive.
    source = root if kind == 'linux-release' else root / 'dist'
    verify_files(kind, source, sha)
    files = [source / name for name in WINDOWS_FILES] if kind == 'windows-release' else [
        source / 'dist' / name for name in LINUX_FILES]
    if kind == 'linux-release':
        for site in ('admin-web', 'user-web'):
            files.extend(path for path in (source / site / 'dist').rglob('*') if path.is_file())
    with zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(files):
            name = path.relative_to(source).as_posix()
            if path.is_symlink() or not path.resolve().is_relative_to(source.resolve()) or not allowed(kind, name):
                raise ValueError('Unsafe artifact input')
            archive.write(path, name)


def unpack(kind, archive_path, receipt, root, run, sha):
    if (receipt.get('run') != run or receipt.get('sourceSHA') != sha or receipt.get('kind') != kind
            or receipt.get('size') != archive_path.stat().st_size
            or receipt.get('sha256') != digest(archive_path) or archive_path.stat().st_size > 512 << 20):
        raise ValueError('Artifact transfer receipt mismatch')
    with zipfile.ZipFile(archive_path) as archive:
        members = archive.infolist()
        names = [item.filename for item in members]
        if len(names) != len(set(names)) or sum(item.file_size for item in members) > 2 << 30:
            raise ValueError('Duplicate or oversized artifact archive')
        for item in members:
            if not allowed(kind, item.filename) or stat.S_ISLNK(item.external_attr >> 16) or item.is_dir():
                raise ValueError('Unsafe artifact archive member')
            destination = root / item.filename
            if not destination.resolve().is_relative_to(root.resolve()):
                raise ValueError('Artifact destination escapes workspace')
        archive.extractall(root)
    verify_files(kind, root, sha)


def gh(*args):
    return subprocess.run(['gh', *args], check=True, text=True, capture_output=True).stdout


def release_info(repo, tag):
    # REST's releases/tags endpoint omits unpublished drafts. gh resolves drafts
    # as well, so a retry can find and verify the exact release it created.
    result = subprocess.run(['gh', 'release', 'view', tag, '--repo', repo,
                             '--json', 'tagName,isDraft,targetCommitish'], text=True, capture_output=True)
    if result.returncode:
        if result.stderr.strip() == 'release not found':
            return None
        raise RuntimeError('Cannot inspect CI draft release: ' + result.stderr.strip())
    value = json.loads(result.stdout)
    return {'draft': value['isDraft'], 'tag_name': value['tagName'], 'target_commitish': value['targetCommitish']}


def owned_release(info, tag, sha):
    if info is None or info.get('draft') is not True or info.get('tag_name') != tag or info.get('target_commitish') != sha:
        raise ValueError('Refusing to use or modify an unrelated/published release')


def transfer(action, kind, root, env):
    repo, run, sha = identity(env)
    tag = f'hqip-ci-{run}-{kind}'
    info = release_info(repo, tag)
    if action == 'cleanup':
        if info is not None:
            owned_release(info, tag, sha)
            gh('release', 'delete', tag, '--repo', repo, '--yes')
        return
    with tempfile.TemporaryDirectory(prefix='hqip-ci-artifact-') as temporary:
        folder = Path(temporary)
        archive = folder / (kind + '.zip')
        receipt_path = folder / (kind + '.json')
        if action == 'upload':
            package(kind, root, archive, sha)
            receipt_path.write_text(json.dumps({'run': run, 'sourceSHA': sha, 'kind': kind,
                                               'size': archive.stat().st_size, 'sha256': digest(archive)}))
            if info is None:
                gh('release', 'create', tag, '--repo', repo, '--target', sha, '--draft',
                   '--title', 'Temporary CI transport ' + run + ' ' + kind,
                   '--notes', 'Unpublished CI transfer. Removed after successful production deployment.')
                info = release_info(repo, tag)
            owned_release(info, tag, sha)
            gh('release', 'upload', tag, str(archive), str(receipt_path), '--repo', repo, '--clobber')
        else:
            owned_release(info, tag, sha)
            gh('release', 'download', tag, '--repo', repo, '--pattern', archive.name,
               '--pattern', receipt_path.name, '--dir', str(folder))
            unpack(kind, archive, json.loads(receipt_path.read_text()), root, run, sha)
    print(f'{action}: verified {kind} for {sha}')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('upload', 'download', 'cleanup'))
    parser.add_argument('kind', choices=('linux-release', 'windows-release'))
    parser.add_argument('--root', type=Path, default=Path.cwd())
    args = parser.parse_args()
    transfer(args.action, args.kind, args.root, os.environ)
