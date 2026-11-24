"""Validate immutable client deliveries and same-source CI receipts.

This module uses only the standard library so offline installation and rollback
can validate the exact payload. CI additionally verifies Ed25519 signatures.
"""
import base64
import hashlib
import json
from pathlib import Path
import re

CLIENT_PUBLIC_KEY = '611cdf0e186c76eb55e6fefce30c6c9af5bdfe6e34bbc034086d2c7ecad2ba85'
CLIENT_PLATFORMS = {
    'windows-amd64': ('HQIP-Godot-Windows-x64.zip', 'HQIP-Windows-x64-Setup.exe', 'Windows 64 位安装包'),
    'android-arm64': ('HQIP-Android-arm64.apk', 'HQIP-Android-arm64.apk', 'Android ARM64'),
    'android-amd64': ('HQIP-Android-x64.apk', 'HQIP-Android-x64.apk', 'Android x64'),
}


def client_digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def client_file(root, item, expected_name):
    if (item.get('filename') != expected_name or not re.fullmatch('[a-f0-9]{64}', item.get('sha256', ''))
            or type(item.get('size')) is not int or not 0 < item['size'] <= 500 << 20):
        raise ValueError('Invalid client artifact metadata')
    relative = 'godot/' + item['sha256'] + '/' + expected_name
    path = root / relative
    if (path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(root.resolve())
            or path.stat().st_size != item['size'] or client_digest(path) != item['sha256']):
        raise ValueError('Client artifact checksum or size mismatch: ' + relative)
    return relative


def verify_client_bundle(root, sha, platforms=None, *, signatures=False, exact=True):
    """Return the exact permitted files; reject mixed commits, ABIs and versions."""
    root = Path(root)
    platforms = set(platforms or CLIENT_PLATFORMS)
    downloads = json.loads((root / 'godot-downloads.json').read_text(encoding='utf-8'))
    catalog = json.loads((root / 'godot-releases.json').read_text(encoding='utf-8'))
    if (downloads.get('sourceSHA') != sha or downloads.get('dirty') is not False
            or not re.fullmatch('[a-f0-9]{40}', sha) or downloads.get('publicKey') != CLIENT_PUBLIC_KEY
            or len(downloads.get('releases', [])) != len(platforms)
            or len(catalog.get('releases', [])) != len(platforms)):
        raise ValueError('Client release source or platform set mismatch')
    files = {'godot-downloads.json', 'godot-releases.json'}
    updates = {}
    for envelope in catalog['releases']:
        raw = base64.b64decode(envelope['payload'], validate=True)
        signature = base64.b64decode(envelope['signature'], validate=True)
        item = json.loads(raw)
        platform = item.get('platform')
        if (platform not in platforms or platform in updates or len(signature) != 64
                or item.get('schema') != 1 or item.get('apiMin') != 3 or item.get('apiMax') != 3
                or type(item.get('sequence')) is not int or not 0 < item['sequence'] <= 2100000000
                or not re.fullmatch(r'\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?', item.get('version', ''))):
            raise ValueError('Invalid signed client release')
        if signatures:
            from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
            Ed25519PublicKey.from_public_bytes(bytes.fromhex(CLIENT_PUBLIC_KEY)).verify(signature, raw)
        files.add(client_file(root, item, CLIENT_PLATFORMS[platform][0]))
        updates[platform] = item
    seen = set()
    identities = set()
    for item in downloads['releases']:
        platform = item.get('platform')
        if platform not in updates or platform in seen:
            raise ValueError('Duplicate or unknown client delivery')
        update = updates[platform]
        if (item.get('version'), item.get('sequence')) != (update['version'], update['sequence']):
            raise ValueError('Installer and updater version mismatch')
        if platform.startswith('android-') and (item.get('sha256'), item.get('size')) != (update['sha256'], update['size']):
            raise ValueError('APK differs from authenticated update')
        files.add(client_file(root, item, CLIENT_PLATFORMS[platform][1]))
        seen.add(platform)
        identities.add((item['version'], item['sequence']))
    if seen != platforms or len(identities) != 1:
        raise ValueError('Mixed client versions or missing platform')
    actual = {p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()}
    if not exact:
        actual = {name for name in actual if name.startswith('godot/') or name in {'godot-downloads.json', 'godot-releases.json'}}
    if actual != files or any(p.is_symlink() for p in root.rglob('*')):
        raise ValueError('Client bundle must contain exactly its declared files')
    return files
