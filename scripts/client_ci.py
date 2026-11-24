"""Produce isolated, same-commit client receipts and assemble verified CI shards."""
import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import subprocess

from client_bundle import CLIENT_PLATFORMS, CLIENT_PUBLIC_KEY, client_digest, verify_client_bundle

ROOT = Path(__file__).resolve().parents[1]


def receipt(platform):
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, text=True).strip():
        raise ValueError('Client CI receipts require a clean source tree')
    dist = ROOT / 'dist'
    summary = json.loads((dist / 'godot-build.json').read_text(encoding='utf-8'))
    if summary.get('sourceSHA') != sha or summary.get('dirty') is not False:
        raise ValueError('Client must be built from this clean commit')
    catalog = json.loads((dist / 'godot-channel/godot-releases.json').read_text(encoding='utf-8'))
    matches = [(e, json.loads(base64.b64decode(e['payload']))) for e in catalog['releases']]
    matches = [(e, r) for e, r in matches if r['platform'] == platform and r['sequence'] == summary['sequence']]
    if len(matches) != 1:
        raise ValueError('Expected exactly one current signed platform release')
    envelope, update = matches[0]
    output = dist / ('client-' + platform)
    output.mkdir(exist_ok=False)
    name = CLIENT_PLATFORMS[platform][1]
    source = dist / name
    built = [item for item in summary['artifacts'] if item.get('file') == name]
    if (len(built) != 1 or built[0]['sha256'] != client_digest(source)
            or built[0]['size'] != source.stat().st_size):
        raise ValueError('Installer changed after the recorded source build')
    delivery = dict(platform=platform, filename=name, version=summary['version'], sequence=summary['sequence'],
                    size=source.stat().st_size, sha256=client_digest(source), name=CLIENT_PLATFORMS[platform][2])
    for item, file in [(update, dist / 'godot-channel/godot' / update['sha256'] / update['filename']), (delivery, source)]:
        target = output / 'godot' / item['sha256'] / item['filename']
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(file, target)
    (output / 'godot-releases.json').write_text(json.dumps({'releases': [envelope]}) + '\n', encoding='utf-8')
    (output / 'godot-downloads.json').write_text(json.dumps(dict(sourceSHA=sha, dirty=False,
        publicKey=CLIENT_PUBLIC_KEY, releases=[delivery]), ensure_ascii=False) + '\n', encoding='utf-8')
    verify_client_bundle(output, sha, [platform], signatures=True)


def assemble(sha):
    dist = ROOT / 'dist'
    output = dist / 'client-release'
    output.mkdir(exist_ok=False)
    downloads, envelopes = [], []
    for platform in CLIENT_PLATFORMS:
        source = dist / ('client-' + platform)
        files = verify_client_bundle(source, sha, [platform], signatures=True)
        downloads.extend(json.loads((source / 'godot-downloads.json').read_text(encoding='utf-8'))['releases'])
        envelopes.extend(json.loads((source / 'godot-releases.json').read_text(encoding='utf-8'))['releases'])
        for name in files - {'godot-downloads.json', 'godot-releases.json'}:
            target = output / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / name, target)
    (output / 'godot-downloads.json').write_text(json.dumps(dict(sourceSHA=sha, dirty=False,
        publicKey=CLIENT_PUBLIC_KEY, releases=downloads), ensure_ascii=False) + '\n', encoding='utf-8')
    (output / 'godot-releases.json').write_text(json.dumps({'releases': envelopes}) + '\n', encoding='utf-8')
    verify_client_bundle(output, sha, signatures=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('receipt', 'assemble'))
    parser.add_argument('--platform', choices=CLIENT_PLATFORMS)
    args = parser.parse_args()
    if args.action == 'receipt':
        receipt(args.platform)
    else:
        assemble(os.environ['GITHUB_SHA'])
