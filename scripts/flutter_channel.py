"""Signed, self-contained Flutter preview publication bundle (no private files)."""
import argparse
import base64
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PUBLIC_URL = 'https://hqip.sharevps.online'
FILENAMES = {
    ('windows-amd64', 'installer'): 'HQIP-Preview-Windows-x64-Setup.exe',
    ('windows-amd64', 'update'): 'HQIP-Preview-Windows-x64.zip',
    ('android-arm64', 'installer'): 'HQIP-Preview-Android-arm64.apk',
    ('android-arm64', 'update'): 'HQIP-Preview-Android-arm64.apk',
    ('android-amd64', 'installer'): 'HQIP-Preview-Android-x64.apk',
    ('android-amd64', 'update'): 'HQIP-Preview-Android-x64.apk',
}
MAX_FILE = 500 << 20


def digest(path):
    with Path(path).open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode()


def signed(value, key):
    raw = encode(value)
    return {'payload': base64.b64encode(raw).decode(), 'signature': base64.b64encode(key.sign(raw)).decode()}


def verified(envelope, public):
    raw = base64.b64decode(envelope['payload'], validate=True)
    if len(raw) > 65536:
        raise ValueError('Signed metadata too large')
    signature = base64.b64decode(envelope['signature'], validate=True)
    try:
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
    except ImportError:
        verify_with_openssl(raw, signature, public)
    else:
        Ed25519PublicKey.from_public_bytes(bytes.fromhex(public)).verify(signature, raw)
    return json.loads(raw)


def verify_with_openssl(raw, signature, public):
    # The production Python runtime has no third-party packages. OpenSSL 3
    # verifies the same Ed25519 bytes without changing that shared runtime.
    key = bytes.fromhex(public)
    if len(key) != 32 or len(signature) != 64:
        raise ValueError('Invalid Ed25519 key or signature length')
    with tempfile.TemporaryDirectory(prefix='hqip-verify-') as folder:
        root = Path(folder)
        (root / 'key.der').write_bytes(bytes.fromhex('302a300506032b6570032100') + key)
        (root / 'payload').write_bytes(raw)
        (root / 'signature').write_bytes(signature)
        result = subprocess.run(['openssl', 'pkeyutl', '-verify', '-pubin', '-keyform', 'DER',
                                 '-inkey', str(root / 'key.der'), '-rawin', '-in', str(root / 'payload'),
                                 '-sigfile', str(root / 'signature')], capture_output=True)
        if result.returncode:
            raise ValueError('Ed25519 signature verification failed')


def regular(path):
    path = Path(path)
    if not stat.S_ISREG(path.lstat().st_mode) or path.is_symlink():
        raise ValueError('Not a regular file: ' + path.name)
    return path


def safe_path(value):
    path = PurePosixPath(value)
    if not value or path.is_absolute() or '..' in path.parts or '\\' in value or path.as_posix() != value or ':' in value:
        raise ValueError('Unsafe bundle path')
    return path


def catalog_releases(catalog, public):
    entries = catalog.get('releases', [])
    if len(entries) != 6:
        raise ValueError('Exactly three platforms and two release kinds required')
    releases, seen = [], set()
    for entry in entries:
        v = verified(entry, public)
        pair = (v.get('platform'), v.get('kind'))
        if (pair in seen or FILENAMES.get(pair) != v.get('filename') or
                v.get('schema') != 2 or v.get('product') != 'hqip-flutter' or v.get('channel') != 'preview' or
                type(v.get('sequence')) is not int or not 0 < v['sequence'] < 2000000000 or
                not re.fullmatch(r'\d+\.\d+\.\d+-preview\.\d+', v.get('version', '')) or
                not re.fullmatch('[a-f0-9]{64}', v.get('sha256', '')) or
                type(v.get('size')) is not int or not 0 < v['size'] <= MAX_FILE):
            raise ValueError('Invalid or duplicate preview release')
        seen.add(pair)
        releases.append(v)
    if len({(v['version'], v['sequence']) for v in releases}) != 1:
        raise ValueError('Mixed preview versions')
    for platform in ['android-arm64', 'android-amd64']:
        if len({(v['sha256'], v['size']) for v in releases if v['platform'] == platform}) != 1:
            raise ValueError('Android installer and update must be identical')
    return releases


def object_path(v):
    return 'objects/' + v['sha256'] + '/' + v['filename']


def installers(releases):
    return {'releases': [dict(
        platform=v['platform'], name='HQIP Preview', filename=v['filename'],
        version=v['version'], sequence=v['sequence'], sha256=v['sha256'], size=v['size'],
        url=PUBLIC_URL + '/downloads/flutter/preview/' + v['sha256'] + '/' + v['filename'],
    ) for v in releases if v['kind'] == 'installer']}


def verify_windows_zip(path, release):
    with zipfile.ZipFile(path) as z:
        names = z.namelist()
        if len(names) != len(set(names)) or len(names) > 3000:
            raise ValueError('Duplicate or excessive ZIP entries')
        for entry in z.infolist():
            safe_path(entry.filename)
            if stat.S_ISLNK(entry.external_attr >> 16) or entry.is_dir():
                raise ValueError('ZIP must contain ordinary files only')
        raw = z.read('payload.json')
        if hashlib.sha256(raw).hexdigest() != release.get('manifestSha256'):
            raise ValueError('Windows payload manifest mismatch')
        files = json.loads(raw)['files']
        expected = {f['path'] for f in files}
        if len(files) != len(expected) or set(names) != expected | {'payload.json'}:
            raise ValueError('Incomplete Windows payload')
        if not {'hqip-ui.exe', 'hqip-runtime.exe', 'hqip-helper.exe', 'sing-box.exe', 'libcronet.dll', 'vcruntime140.dll'} <= expected:
            raise ValueError('Required Windows runtime missing')
        for item in files:
            safe_path(item['path'])
            if z.getinfo(item['path']).file_size != item['size']:
                raise ValueError('Windows payload size mismatch')
            with z.open(item['path']) as source:
                if hashlib.file_digest(source, 'sha256').hexdigest() != item['sha256']:
                    raise ValueError('Windows payload checksum mismatch')


def verify_bundle(root, public, source_sha=None, *, inspect_zip=True):
    root = Path(root)
    catalog = json.loads(regular(root / 'releases.json').read_bytes())
    releases = catalog_releases(catalog, public)
    receipt = verified(json.loads(regular(root / 'receipt.json').read_bytes()), public)
    if (receipt.get('schema') != 1 or receipt.get('product') != 'hqip-flutter' or receipt.get('channel') != 'preview' or
            not re.fullmatch('[a-f0-9]{40}', receipt.get('sourceSHA', '')) or receipt.get('dirty') is not False or
            source_sha is not None and receipt['sourceSHA'] != source_sha or
            (receipt.get('version'), receipt.get('sequence')) != (releases[0]['version'], releases[0]['sequence'])):
        raise ValueError('Invalid preview build provenance')
    expected = {'releases.json', 'installers.json', *(object_path(v) for v in releases)}
    files = receipt.get('files', {})
    if set(files) != expected:
        raise ValueError('Signed receipt file set mismatch')
    actual = set()
    for p in root.rglob('*'):
        if p.is_symlink():
            raise ValueError('Bundle links are forbidden')
        if p.is_file():
            actual.add(p.relative_to(root).as_posix())
    if actual != expected | {'receipt.json'}:
        raise ValueError('Unexpected or missing publication files')
    for name, info in files.items():
        safe_path(name)
        p = regular(root / name)
        if p.stat().st_size != info['size'] or digest(p) != info['sha256']:
            raise ValueError('Publication file checksum mismatch: ' + name)
    if json.loads((root / 'installers.json').read_bytes()) != installers(releases):
        raise ValueError('Installer URLs do not match signed releases')
    for v in releases:
        info = files[object_path(v)]
        if info != {'size': v['size'], 'sha256': v['sha256']}:
            raise ValueError('Artifact differs from signed release')
        if inspect_zip and v['platform'] == 'windows-amd64' and v['kind'] == 'update':
            verify_windows_zip(root / object_path(v), v)
    return receipt, releases


def extract_bundle(archive, destination):
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=False)
    with tarfile.open(archive, 'r:gz') as package:
        members = package.getmembers()
        names = [m.name for m in members]
        if len(names) != len(set(names)) or len(names) > 16 or sum(m.size for m in members) > 4 * MAX_FILE + 200000:
            raise ValueError('Invalid bundle limits')
        for member in members:
            safe_path(member.name)
            if not member.isfile() or member.size > MAX_FILE:
                raise ValueError('Non-file archive member')
            p = destination / member.name
            p.parent.mkdir(parents=True, exist_ok=True)
            with package.extractfile(member) as source, p.open('xb') as output:
                shutil.copyfileobj(source, output)


def package(source, output, key_path, expected_public):
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, text=True).strip():
        raise ValueError('Publication requires a clean committed checkout')
    key = Ed25519PrivateKey.from_private_bytes(Path(key_path).read_bytes())
    if key.public_key().public_bytes_raw().hex() != expected_public:
        raise ValueError('Unexpected preview signing identity')
    source, output = Path(source), Path(output)
    output.mkdir(parents=True, exist_ok=False)
    catalog = json.loads((source / 'server/flutter-preview-releases.json').read_bytes())
    releases = catalog_releases(catalog, expected_public)
    (output / 'releases.json').write_bytes(encode(catalog))
    (output / 'installers.json').write_bytes(encode(installers(releases)))
    for v in releases:
        target = output / object_path(v)
        if not target.exists():
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / v['filename'], target)
    files = {p.relative_to(output).as_posix(): {'size': p.stat().st_size, 'sha256': digest(p)} for p in sorted(output.rglob('*')) if p.is_file()}
    receipt = dict(schema=1, product='hqip-flutter', channel='preview', sourceSHA=sha, dirty=False,
                   version=releases[0]['version'], sequence=releases[0]['sequence'], files=files)
    (output / 'receipt.json').write_bytes(encode(signed(receipt, key)))
    verify_bundle(output, expected_public, sha)
    archive = output.parent / 'flutter-preview-publication.tar.gz'
    with tarfile.open(archive, 'w:gz', compresslevel=1) as tar:
        for p in sorted(output.rglob('*')):
            if p.is_file():
                tar.add(p, arcname=p.relative_to(output).as_posix(), recursive=False)
    print(json.dumps({'archive': str(archive), 'sha256': digest(archive), 'sourceSHA': sha, 'version': receipt['version'], 'sequence': receipt['sequence']}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, default=ROOT / 'dist/flutter-preview')
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/flutter-preview-publication')
    parser.add_argument('--key', type=Path, default=ROOT / '.local/flutter-signing/ed25519.seed')
    parser.add_argument('--public-key', type=Path, default=ROOT / 'packaging/flutter-update-public-key.txt')
    a = parser.parse_args()
    package(a.source, a.output, a.key, a.public_key.read_text().strip())
