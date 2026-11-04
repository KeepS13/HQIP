"""Package a clean, committed HQIP controller, node artifacts and desktop release.

Build both scripts/build.py and scripts/build_desktop.py at the same clean commit
first. The result is suitable for scripts/upgrade_portal.py on the Nginx host.
Runtime databases, master keys and private credentials are never included.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, text=True).strip():
        raise ValueError('Commit all release inputs before packaging')
    dist = ROOT / 'dist'
    core = json.loads((dist / 'manifest.json').read_text(encoding='utf-8'))
    if core.get('hqipAdmissionVersion') != 1:
        raise ValueError('Admission-enabled node release required')
    desktop = json.loads((dist / 'desktop-manifest.json').read_text(encoding='utf-8'))
    if desktop.get('sourceSHA') != sha or desktop.get('dirty') or desktop.get('filename') != 'HQIP-Windows-x64.zip':
        raise ValueError('Desktop must be built from this exact clean commit')
    archive = dist / desktop['filename']
    if digest(archive) != desktop['sha256'] or archive.stat().st_size != desktop['size']:
        raise ValueError('Desktop package hash/size mismatch')
    names = [f'{kind}-linux-{arch}' for kind in ['agent', 'hysteria'] for arch in ['amd64', 'arm64']]
    if (dist / 'caddy-linux-amd64').is_file():
        names.append('caddy-linux-amd64')
    for name in ['hqip-linux-amd64'] + names:
        if digest(dist / name) != core['sha256'].get(name):
            raise ValueError('Node/controller hash mismatch: ' + name)
    output = dist / ('portal-' + sha)
    output.mkdir(exist_ok=False)
    (output / 'artifacts').mkdir()
    (output / 'scripts').mkdir()
    (output / 'deploy').mkdir()
    shutil.copyfile(dist / 'hqip-linux-amd64', output / 'hqip')
    for name in names + ['manifest.json', 'hqip-hysteria-manifest.json', 'HQIP-Windows-x64.zip', 'desktop-manifest.json']:
        shutil.copyfile(dist / name, output / 'artifacts' / name)
    for name in ['upgrade_control.py', 'upgrade_portal.py', 'upgrade_node_admission.py']:
        shutil.copyfile(ROOT / 'scripts' / name, output / 'scripts' / name)
    shutil.copyfile(ROOT / 'deploy/nginx-hqip.conf', output / 'deploy/nginx-hqip.conf')
    hashes = {p.relative_to(output).as_posix(): digest(p) for p in output.rglob('*') if p.is_file()}
    (output / 'release.json').write_text(json.dumps({'sourceSHA': sha, 'sha256': hashes}, indent=2) + '\n', encoding='utf-8')
    tarpath = dist / ('hqip-portal-' + sha[:12] + '.tar.gz')
    with tarfile.open(tarpath, 'w:gz') as tar:
        for path in sorted(output.rglob('*')):
            if path.is_file():
                tar.add(path, arcname=path.relative_to(output).as_posix(), recursive=False)
    result = {'sourceSHA': sha, 'filename': tarpath.name, 'size': tarpath.stat().st_size, 'sha256': digest(tarpath)}
    (dist / 'portal-release.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
