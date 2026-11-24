"""Build local Windows/Android previews; never uploads or changes a running app.

Tools and signing identities are explicit inputs. Reuse the same signing keys
for every release so installed clients can authenticate and install updates.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import zipfile

from build_billing_core import build as build_core, CORE_HASH
from package_godot_windows import PAYLOAD, build_installer, find_compiler

ROOT = Path(__file__).resolve().parents[1]
TOOLS = ROOT / '.local/godot-tools'
DIST = ROOT / 'dist'


def run(args, cwd=ROOT, env=None):
    subprocess.run([str(a) for a in args], cwd=cwd, env=env, check=True)


def digest(path):
    with path.open('rb') as file:
        return hashlib.file_digest(file, 'sha256').hexdigest()


def go_build(target, output, flags, tags='with_utls'):
    env = dict(os.environ, CGO_ENABLED='0', GOOS='windows', GOARCH='amd64')
    run(['go', 'build', '-mod=readonly', '-trimpath', '-tags', tags,
         '-ldflags', flags, '-o', output, target], env=env)


def windows(args, flags, public):
    install = DIST / 'godot-windows'
    payload = install / 'versions/bundled'
    payload.mkdir(parents=True, exist_ok=True)
    run([args.godot, '--headless', '--path', ROOT / 'godot-client', '--export-release',
         'Windows', payload / 'HQIP-UI.exe'])
    go_build('./cmd/hqip-runtime', payload / 'hqip-runtime.exe', flags)
    go_build('./cmd/hqip-launcher', install / 'HQIP.exe',
             '-s -w -H windowsgui -X main.updateKey=' + public)
    go_build('./cmd/hqip-helper', payload / 'hqip-helper.exe', '-s -w -H windowsgui')
    core = payload / 'sing-box.exe'
    if not core.exists() or digest(core) != CORE_HASH:
        build_core(core)
    # Runtime binaries from the existing verified desktop dependency cache.
    for name in ('libcronet.dll', 'sing-box-LICENSE'):
        shutil.copyfile(args.core_deps / name, payload / name)
    (payload / 'THIRD-PARTY-NOTICES.txt').write_text(
        'HQIP Godot client\nGodot 4.7.2 (MIT), https://godotengine.org/license/\n'
        'sing-box 1.14.4 (GPL-3.0-or-later), see sing-box-LICENSE.\n'
        'https://github.com/SagerNet/sing-box/tree/v1.14.4\n'
        'HQIP source: https://github.com/KeepS13/HQIP\n'
        'Routing data attribution is included in authenticated rules bundles.\n'
        'Noto Sans CJK SC (SIL Open Font License 1.1):\n' +
        (ROOT / 'godot-client/assets/fonts/LICENSE.txt').read_text(encoding='utf-8'), encoding='utf-8')
    # ZIP is private update transport. End users receive one installer containing
    # the stable launcher, shortcuts and a complete offline-capable first version.
    package = DIST / 'HQIP-Godot-Windows-x64.zip'
    with zipfile.ZipFile(package, 'w', zipfile.ZIP_DEFLATED) as archive:
        for name in PAYLOAD:
            archive.write(payload / name, name)
    download = build_installer(install, DIST, args.version, args.iscc)
    return package, download


def android(args, flags):
    env = dict(os.environ)
    sdk = Path(env.get('ANDROID_HOME', Path(os.environ['LOCALAPPDATA']) / 'Android/Sdk'))
    env['ANDROID_HOME'] = str(sdk)
    env.setdefault('ANDROID_NDK_HOME', str(sdk / 'ndk/30.0.16248370'))
    env['PATH'] = str(TOOLS) + os.pathsep + env['PATH']
    signing = json.loads(args.android_signing.read_text(encoding='utf-8'))
    env.update(HQIP_ANDROID_KEYSTORE=str(Path(signing['keystore']).resolve()),
               HQIP_ANDROID_STORE_PASSWORD=signing['storePassword'],
               HQIP_ANDROID_KEY_PASSWORD=signing['keyPassword'],
               HQIP_ANDROID_KEY_ALIAS=signing['alias'])
    out = DIST / 'godot-android'
    out.mkdir(parents=True, exist_ok=True)
    run([TOOLS / 'gomobile.exe', 'bind', '-target', 'android/' + args.android_arch, '-androidapi', '26',
         '-tags', 'with_gvisor,with_quic,with_utls,with_clash_api,with_v2ray_api',
         '-ldflags', flags + ' -checklinkname=0', '-o', out / 'hqip-mobile.aar', '.'],
        ROOT / 'native/mobile', env)
    assets_zip = ROOT / '.local/godot-android-assets.zip'
    run([args.godot, '--headless', '--path', ROOT / 'godot-client',
         '--export-pack', 'Android resources', assets_zip])
    assets = ROOT / '.local/godot-android-assets'
    # Only the generated asset directory is replaced; no application data lives here.
    with tempfile.TemporaryDirectory(dir=ROOT / '.local', prefix='godot-assets-') as temp:
        with zipfile.ZipFile(assets_zip) as archive:
            for entry in archive.infolist():
                target = (Path(temp) / entry.filename).resolve()
                if not target.is_relative_to(Path(temp).resolve()):
                    raise ValueError('Unsafe Godot resource path')
            archive.extractall(temp)
        if assets.exists():
            shutil.rmtree(assets)
        shutil.copytree(temp, assets)
    run([TOOLS / 'gradle-8.11.1/bin/gradle.bat', '-p', 'native/android',
         'assembleRelease', '--no-daemon', '-PhqipVersion=' + args.version,
         '-PhqipSequence=' + str(args.sequence),
         '-PhqipABI=' + ('arm64-v8a' if args.android_arch == 'arm64' else 'x86_64')], env=env)
    package = DIST / ('HQIP-Android-' + ('arm64' if args.android_arch == 'arm64' else 'x64') + '.apk')
    shutil.copyfile(ROOT / 'native/android/build/outputs/apk/release/HQIP-release.apk', package)
    run([sdk / 'build-tools/37.0.0/apksigner.bat', 'verify', '--verbose', package])
    return package, package


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--platform', choices=['windows', 'android', 'all'], default='all')
    parser.add_argument('--godot', default=shutil.which('godot') or str(Path.home() / 'Desktop/Godot/godot.cmd'))
    parser.add_argument('--version', required=True)
    parser.add_argument('--sequence', type=int, required=True)
    parser.add_argument('--iscc', type=Path, help='Inno Setup compiler for the Windows EXE installer')
    parser.add_argument('--android-arch', choices=['arm64', 'amd64'], default='arm64')
    parser.add_argument('--key', type=Path, default=ROOT / '.local/godot-signing/ed25519.seed')
    parser.add_argument('--android-signing', type=Path, default=ROOT / '.local/godot-signing/android.json')
    parser.add_argument('--core-deps', type=Path, default=ROOT / '.local/reality-desktop')
    args = parser.parse_args()
    source_sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, text=True).strip())
    if os.environ.get('CI') and dirty:
        raise ValueError('CI client builds require clean committed source')
    if not args.key.is_file():
        parser.error('Initialize and back up the Ed25519 key with hqip-client-release first.')
    if args.sequence <= 0 or args.sequence > 2100000000:
        parser.error('Sequence must be an Android-compatible positive versionCode.')
    if args.platform != 'windows' and not args.android_signing.is_file():
        parser.error('Provide a persistent Android signing identity; see docs/applications/godot-client.md.')
    if args.platform != 'android':
        args.iscc = find_compiler(args.iscc)
    public = subprocess.check_output(['go', 'run', './cmd/hqip-client-release', '-key', str(args.key)],
                                     cwd=ROOT, text=True).strip()
    if len(public) != 64:
        raise ValueError('Invalid public update key')
    flags = ('-s -w -X github.com/KeepS13/HQIP/internal/client.AppVersion=' + args.version
             + ' -X github.com/KeepS13/HQIP/internal/client.AppSequence=' + str(args.sequence)
             + ' -X github.com/KeepS13/HQIP/internal/client.UpdatePublicKey=' + public)
    DIST.mkdir(exist_ok=True)
    run([args.godot, '--headless', '--path', ROOT / 'godot-client', '--editor', '--import', '--quit'])
    # Separate ABI builds belong to one release. Retain only same-version files
    # whose on-disk contents still match, so stale summaries never bless old APKs.
    summary_path = DIST / 'godot-build.json'
    previous = json.loads(summary_path.read_text(encoding='utf-8')) if summary_path.exists() else {}
    artifacts = []
    if previous.get('version') == args.version and previous.get('sequence') == args.sequence:
        for artifact in previous.get('artifacts', []):
            if artifact['file'].endswith('-Portable.zip'):
                continue
            existing = DIST / Path(artifact['file']).name
            if existing.is_file() and digest(existing) == artifact['sha256']:
                artifacts.append(artifact)
    for platform in ('windows', 'android'):
        if args.platform not in (platform, 'all'):
            continue
        package, download = windows(args, flags, public) if platform == 'windows' else android(args, flags)
        run(['go', 'run', './cmd/hqip-client-release', '-key', args.key, '-artifact', package,
             '-platform', 'windows-amd64' if platform == 'windows' else 'android-' + args.android_arch,
             '-version', args.version, '-sequence', args.sequence, '-output', DIST / 'godot-channel'])
        artifacts = [a for a in artifacts if a['file'] != download.name]
        artifacts.append({'file':download.name, 'sha256':digest(download), 'size':download.stat().st_size})
    summary_path.write_text(json.dumps({'sourceSHA':source_sha, 'dirty':dirty, 'version':args.version, 'sequence':args.sequence,
        'artifacts':artifacts}, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print('Built local previews. No upload or production deployment performed.')


if __name__ == '__main__':
    main()
