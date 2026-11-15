"""Package every production file from this tested commit; never inherit old files."""
from pathlib import Path
import argparse
import ast
import json
import re
import shutil
import subprocess
import tarfile

from release_bundle import NODE_ASSETS, RELEASE_SCRIPTS, digest, verify_full_release

ROOT = Path(__file__).resolve().parents[1]


def package_release(root, sha, previous_sha=None):
    root = Path(root)
    if not re.fullmatch('[a-f0-9]{40}', sha) or (previous_sha and not re.fullmatch('[a-f0-9]{40}', previous_sha)):
        raise ValueError('Full source commits required')
    dist = root / 'dist'
    # Read the actual build parameter without importing or executing the build
    # script. This pins future versions too; the installer does not hardcode one.
    build = ast.parse((root / 'scripts/build_desktop.py').read_text(encoding='utf-8'))
    versions = [ast.literal_eval(node.value) for node in build.body if isinstance(node, ast.Assign)
                and any(isinstance(target, ast.Name) and target.id == 'DESKTOP_VERSION' for target in node.targets)]
    if len(versions) != 1 or not re.fullmatch(r'\d+\.\d+\.\d+', versions[0]):
        raise ValueError('A single semantic desktop build version is required')
    nodes = json.loads((dist / 'manifest.json').read_text(encoding='utf-8'))
    if nodes.get('sourceSHA') != sha or nodes.get('dirty') is not False:
        raise ValueError('Build node assets from the current clean source commit')
    if nodes.get('sha256', {}).get('hqip-linux-amd64') != digest(dist / 'hqip-linux-amd64'):
        raise ValueError('Controller artifact does not match this source build')
    inputs = {'hqip': dist / 'hqip-linux-amd64',
              'artifacts/HQIP-Windows-x64.zip': dist / 'HQIP-Windows-x64.zip',
              'artifacts/desktop-manifest.json': dist / 'desktop-manifest.json'}
    for name in NODE_ASSETS:
        source = dist / name
        if not source.is_file() or source.is_symlink() or nodes.get('sha256', {}).get(name) != digest(source):
            raise ValueError('Missing or unverified current node asset: ' + name)
        inputs['artifacts/' + name] = source
    for name in RELEASE_SCRIPTS:
        inputs['scripts/' + name] = root / 'scripts' / name
    for name in ('nginx-hqip.conf', 'hqip-control.service', 'hqip-storage.service',
                 'hqip-journal-limits.conf', 'hqip-nginx-logrotate.conf',
                 'hqip-backup.service', 'hqip-backup.timer', 'hqip-backup-retry.service', 'hqip-backup-retry.timer',
                 'storage/compose.yaml', 'storage/init-roles.sql', 'storage/redis.conf'):
        inputs['deploy/' + name] = root / 'deploy' / name
    for app in ('admin-web', 'user-web'):
        for source in (root / app / 'dist').rglob('*'):
            if source.is_symlink():
                raise ValueError('Symlink in built web payload')
            if source.is_file():
                inputs[app + '/' + source.relative_to(root / app / 'dist').as_posix()] = source
    out = dist / ('full-' + sha)
    out.mkdir(exist_ok=False)
    hashes = {}
    for name, source in inputs.items():
        if source.is_symlink():
            raise ValueError('Symlink in release input')
        destination = out / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
        hashes[name] = digest(destination)
    nodes['sha256'] = {name: nodes['sha256'][name] for name in NODE_ASSETS}
    nodes['agentTelemetryVersion'], nodes['agentSourceSHA'] = 1, sha
    node_manifest = out / 'artifacts/manifest.json'
    node_manifest.write_text(json.dumps(nodes, indent=2) + '\n', encoding='utf-8')
    hashes['artifacts/manifest.json'] = digest(node_manifest)
    manifest = {'sourceSHA': sha, 'releaseKind': 'full', 'upgradeKind': 'full-release',
                'schemaVersion': 5, 'postgresSchemaVersion': 1, 'paymentsVersion': 1, 'postgresRecoveryVersion': 1, 'subscriptionAllocationsVersion': 1,
                'clientApiVersion': 3, 'serverBillingVersion': 2, 'agentTelemetryVersion': 1, 'operationsVersion': 1,
                'operationLogsVersion': 1, 'commercialProtectionVersion': 1,
                'desktopVersion': versions[0], 'desktopIconSHA256': digest(root / 'desktop/assets/hqip.ico'),
                'sha256': hashes}
    if previous_sha:
        manifest['previousSourceSHA'] = previous_sha  # Deployment guard, never a source of files.
    (out / 'release.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8')
    verify_full_release(out, manifest)
    archive = dist / ('hqip-full-' + sha[:12] + '.tar.gz')
    with tarfile.open(archive, 'x:gz') as tar:
        for name in sorted([*hashes, 'release.json']):
            tar.add(out / name, arcname=name, recursive=False)
    result = {'sourceSHA': sha, 'filename': archive.name, 'size': archive.stat().st_size,
              'sha256': digest(archive), 'releaseKind': 'full'}
    (dist / 'domains-release.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--previous-source-sha')
    args = parser.parse_args()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, text=True).strip():
        raise ValueError('Commit all release inputs first')
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    print(json.dumps(package_release(ROOT, sha, args.previous_source_sha)))


if __name__ == '__main__':
    main()
