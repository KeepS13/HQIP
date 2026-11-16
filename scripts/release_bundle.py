"""Validate a self-contained HQIP release without consulting an older host."""
from pathlib import Path, PurePosixPath
import hashlib
import json
import re
import stat
import struct
import zipfile

NODE_ASSETS = ('agent-linux-amd64', 'agent-linux-arm64',
               'hysteria-linux-amd64', 'hysteria-linux-arm64', 'caddy-linux-amd64')
RELEASE_SCRIPTS = ('upgrade_control.py', 'upgrade_domains.py', 'release_bundle.py',
                   'fresh_deploy.py', 'prepare_storage.py', 'restore_postgres.py', 'install_ci_release.py',
                   'operations_deployment.py', 'upgrade_node_agent.py', 'upgrade_node_admission.py',
                   'automatic_backup.py', 'commercial_protection.py', 'offline_deployment.py', 'render_domains.py')
PROTECTION_FILES = {'scripts/automatic_backup.py', 'scripts/commercial_protection.py', 'scripts/render_domains.py',
                    'deploy/hqip-backup.service', 'deploy/hqip-backup.timer',
                    'deploy/hqip-backup-retry.service', 'deploy/hqip-backup-retry.timer'}
REQUIRED = {'hqip', 'artifacts/manifest.json', 'artifacts/HQIP-Windows-x64.zip',
            'artifacts/desktop-manifest.json', 'admin-web/index.html', 'user-web/index.html',
            'deploy/hqip-control.service', 'deploy/hqip-storage.service', 'deploy/nginx-hqip.conf',
            'deploy/hqip-journal-limits.conf', 'deploy/hqip-nginx-logrotate.conf',
            'deploy/storage/compose.yaml', 'deploy/storage/init-roles.sql', 'deploy/storage/redis.conf',
            *PROTECTION_FILES,
            *(f'artifacts/{name}' for name in NODE_ASSETS),
            *(f'scripts/{name}' for name in RELEASE_SCRIPTS)}
DESKTOP_BINARIES = {'HQIP.exe', 'hqip-helper.exe', 'sing-box.exe', 'libcronet.dll'}
DESKTOP_FILES = DESKTOP_BINARIES | {'sing-box-LICENSE', 'THIRD-PARTY-NOTICES.txt',
                                    'manifest.json', '使用说明.md'}


def digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def safe_name(name):
    path = PurePosixPath(name)
    if (not name or name == 'release.json' or path.is_absolute() or '..' in path.parts
            or '\\' in name or path.as_posix() != name):
        raise ValueError('Unsafe manifest path: ' + name)
    return name


def validate_manifest(manifest, *, allow_legacy=False):
    if (not re.fullmatch('[a-f0-9]{40}', manifest.get('sourceSHA', ''))
            or manifest.get('releaseKind') != 'full' or manifest.get('upgradeKind') != 'full-release'):
        raise ValueError('A complete current-source release is required')
    for key, value in {'schemaVersion': 5, 'postgresSchemaVersion': 1, 'paymentsVersion': 1,
                       'serverBillingVersion': 2, 'agentTelemetryVersion': 1}.items():
        if manifest.get(key) != value:
            raise ValueError('Unsupported release capability: ' + key)
    allocation = manifest.get('subscriptionAllocationsVersion', 0)
    if type(allocation) is not int or allocation not in ((0, 1) if allow_legacy else (1,)):
        raise ValueError('New releases require subscriptionAllocationsVersion 1')
    recovery = manifest.get('postgresRecoveryVersion', 0)
    if type(recovery) is not int or recovery not in ((0, 1) if allow_legacy else (1,)):
        raise ValueError('New releases require postgresRecoveryVersion 1')
    required = REQUIRED if recovery == 1 else REQUIRED - {'scripts/restore_postgres.py'}
    offline = manifest.get('offlineAdmissionVersion', 0)
    if type(offline) is not int or offline not in (0, 1):
        raise ValueError('Unsupported offline admission capability')
    if offline == 0:
        required = required - {'scripts/offline_deployment.py'}
    protection = manifest.get('commercialProtectionVersion', 0)
    logs = manifest.get('operationLogsVersion', 0)
    if type(protection) is not int or protection not in ((0, 1) if allow_legacy else (1,)):
        raise ValueError('New releases require commercialProtectionVersion 1')
    if type(logs) is not int or logs not in ((0, 1) if allow_legacy else (1,)):
        raise ValueError('New releases require operationLogsVersion 1')
    if protection == 0:
        required = required - PROTECTION_FILES
    operations = manifest.get('operationsVersion', 0)
    if type(operations) is not int or operations not in (0, 1):
        raise ValueError('Unsupported operations schema capability')
    if operations == 0:
        required = required - {'scripts/operations_deployment.py', 'scripts/upgrade_node_agent.py', 'scripts/upgrade_node_admission.py'}
    hashes = manifest.get('sha256', {})
    if not isinstance(hashes, dict) or not required <= hashes.keys():
        raise ValueError('Incomplete full release manifest')
    for name, checksum in hashes.items():
        safe_name(name)
        if not isinstance(checksum, str) or not re.fullmatch('[a-f0-9]{64}', checksum):
            raise ValueError('Invalid payload hash')
    return hashes


def verify_windows_binary(data, icon_group=None):
    """Check a real AMD64 PE image and, for HQIP, referenced icon resources."""
    try:
        pe = struct.unpack_from('<I', data, 0x3c)[0]
        machine, sections, _, _, _, optional_size, _ = struct.unpack_from('<HHIIIHH', data, pe + 4)
        optional = pe + 24
        if (data[:2] != b'MZ' or data[pe:pe + 4] != b'PE\0\0' or machine != 0x8664
                or not 1 <= sections <= 96 or optional_size < 136
                or struct.unpack_from('<H', data, optional)[0] != 0x20b):
            raise ValueError('Desktop binary is not a Windows AMD64 PE image')
        mappings = []
        for index in range(sections):
            _, address, size, offset = struct.unpack_from('<IIII', data, optional + optional_size + index * 40 + 8)
            if offset + size > len(data):
                raise ValueError('Truncated desktop PE section')
            mappings.append((address, size, offset))
        if icon_group is None:
            return

        def at(address, size):
            for base, length, offset in mappings:
                if base <= address and address + size <= base + length:
                    return offset + address - base
            raise ValueError('Desktop PE resource points outside the image')

        address, size = struct.unpack_from('<II', data, optional + 128)
        if not address or not size:
            raise ValueError('Desktop EXE has no Windows icon resources')
        base = at(address, size)
        resources = {}

        def walk(offset, path):
            if len(path) > 2 or offset + 16 > size:
                raise ValueError('Invalid desktop PE resource directory')
            named, ids = struct.unpack_from('<HH', data, base + offset + 12)
            count = named + ids
            if count > 256 or offset + 16 + count * 8 > size:
                raise ValueError('Invalid desktop PE resource entries')
            for index in range(count):
                name, target = struct.unpack_from('<II', data, base + offset + 16 + index * 8)
                if target & 0x80000000:
                    walk(target & 0x7fffffff, (*path, name))
                else:
                    if len(path) != 2 or target + 16 > size or len(resources) >= 1024:
                        raise ValueError('Invalid desktop PE resource leaf')
                    rva, length = struct.unpack_from('<II', data, base + target)
                    start = at(rva, length)
                    resources[(*path, name)] = data[start:start + length]

        walk(0, ())
        groups = [(key, value) for key, value in resources.items() if key[:2] == (14, icon_group)]
        if not groups:
            raise ValueError('Desktop EXE is missing its Windows icon group')
        for (_, _, language), group in groups:
            reserved, kind, count = struct.unpack_from('<HHH', group)
            if reserved or kind != 1 or not 1 <= count <= 256 or len(group) != 6 + 14 * count:
                raise ValueError('Invalid desktop Windows icon group')
            for index in range(count):
                length, resource_id = struct.unpack_from('<IH', group, 6 + 14 * index + 8)
                image = resources.get((3, resource_id, language))
                if not image or len(image) != length:
                    raise ValueError('Desktop Windows icon image is missing or truncated')
    except struct.error as error:
        raise ValueError('Truncated desktop Windows PE image') from error


def verify_desktop(release, manifest, hashes):
    archive = release / 'artifacts/HQIP-Windows-x64.zip'
    desktop = json.loads((release / 'artifacts/desktop-manifest.json').read_text(encoding='utf-8'))
    version = manifest.get('desktopVersion', '')
    if (not re.fullmatch(r'\d+\.\d+\.\d+', version)
            or desktop.get('version') != version or desktop.get('filename') != archive.name
            or type(desktop.get('size')) is not int or desktop['size'] != archive.stat().st_size
            or desktop.get('sourceSHA') != manifest['sourceSHA'] or desktop.get('dirty') is not False
            or desktop.get('sha256') != hashes['artifacts/' + archive.name]):
        raise ValueError('Desktop manifest version, file, size or provenance mismatch')
    try:
        with zipfile.ZipFile(archive) as package:
            members = package.infolist()
            names = [member.filename for member in members]
            expected = {'HQIP/' + name for name in DESKTOP_FILES}
            if (len(names) != len(set(names)) or set(names) != expected
                    or sum(member.file_size for member in members) > 512 * 1024 ** 2):
                raise ValueError('Desktop ZIP must contain exactly the required safe paths')
            for member in members:
                mode = member.external_attr >> 16
                if (member.is_dir() or member.flag_bits & 1 or member.file_size > 256 * 1024 ** 2
                        or stat.S_IFMT(mode) not in (0, stat.S_IFREG)):
                    raise ValueError('Desktop ZIP contains an unsafe or oversized member')
            if package.getinfo('HQIP/manifest.json').file_size > 1024 ** 2:
                raise ValueError('Desktop inner manifest is oversized')
            inner = json.loads(package.read('HQIP/manifest.json'))
            if (inner.get('version') != version or inner.get('sourceSHA') != manifest['sourceSHA']
                    or inner.get('dirty') is not False or set(inner.get('sha256', {})) != DESKTOP_BINARIES):
                raise ValueError('Desktop inner manifest version or provenance mismatch')
            resources = inner.get('windowsResources', {})
            if (resources.get('groupId') != 3 or not re.fullmatch('[a-f0-9]{64}', resources.get('iconSha256', ''))
                    or resources['iconSha256'] != manifest.get('desktopIconSHA256')):
                raise ValueError('Desktop Windows resource provenance mismatch')
            for member in members:
                data = package.read(member)
                name = member.filename.removeprefix('HQIP/')
                if name in DESKTOP_BINARIES:
                    if hashlib.sha256(data).hexdigest() != inner['sha256'][name]:
                        raise ValueError('Desktop inner binary checksum mismatch: ' + name)
                    verify_windows_binary(data, resources['groupId'] if name == 'HQIP.exe' else None)
    except (zipfile.BadZipFile, RuntimeError) as error:
        raise ValueError('Desktop package is not a valid ordinary ZIP') from error


def verify_full_release(release, manifest=None, *, allow_legacy=False):
    release = Path(release).resolve(strict=True)
    manifest = manifest or json.loads((release / 'release.json').read_text())
    hashes = validate_manifest(manifest, allow_legacy=allow_legacy)
    actual = set()
    for path in release.rglob('*'):
        if path.is_symlink():
            raise ValueError('Symlink in full release')
        if path.is_file() and path != release / 'release.json':
            actual.add(path.relative_to(release).as_posix())
    if actual != set(hashes):
        raise ValueError('Release payload must exactly match its manifest')
    for name, expected in hashes.items():
        if digest(release / name) != expected:
            raise ValueError('Release checksum mismatch: ' + name)
    nodes = json.loads((release / 'artifacts/manifest.json').read_text())
    if (nodes.get('sourceSHA') != manifest['sourceSHA'] or nodes.get('dirty') is not False
            or nodes.get('agentSourceSHA') != manifest['sourceSHA']
            or nodes.get('hqipAdmissionVersion') != 1 or nodes.get('agentTelemetryVersion') != 1):
        raise ValueError('Node artifact provenance does not match current source')
    for name in NODE_ASSETS:
        if nodes.get('sha256', {}).get(name) != hashes['artifacts/' + name]:
            raise ValueError('Node artifact checksum mismatch: ' + name)
    core = nodes.get('hysteriaSource', {})
    for arch in ('amd64', 'arm64'):
        if core.get('sha256', {}).get('hqip-hysteria-linux-' + arch) != hashes['artifacts/hysteria-linux-' + arch]:
            raise ValueError('Admission core provenance mismatch')
    verify_desktop(release, manifest, hashes)
    return manifest


def verify_retained_release(release, current_sha):
    # Historical full packages predate the recovery operator. Only the active
    # newly built release must advertise the new capability; historical hashes,
    # full node/client provenance and exact payload checks remain mandatory.
    return verify_full_release(release, allow_legacy=Path(release).name != current_sha)
