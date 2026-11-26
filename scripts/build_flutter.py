"""Build the isolated Flutter preview. No upload, deployment or legacy migration.

Requires a pinned Flutter checkout, Go, MSVC/Inno or Android SDK/NDK/gomobile,
and a separate Ed25519 seed. Android also requires the preview signing JSON.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import zipfile
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from build_billing_core import build as build_core, CORE_HASH

ROOT = Path(__file__).resolve().parents[1]
APP = ROOT / 'flutter-client'
TOOLS = ROOT / '.local/flutter-tools'
DIST = ROOT / 'dist/flutter-preview'
META = json.loads((ROOT / 'packaging/flutter-version.json').read_text())


def run(args, cwd=ROOT, env=None):
    subprocess.run([str(a) for a in args], cwd=cwd, env=env, check=True)


def digest(path):
    with Path(path).open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, separators=(',', ':')), encoding='utf-8')


def sign(package, platform, kind, key, manifest=''):
    release = dict(schema=2, product='hqip-flutter', channel='preview', version=META['version'],
                   sequence=META['sequence'], platform=platform, kind=kind,
                   filename=package.name, sha256=digest(package), size=package.stat().st_size)
    if manifest:
        release['manifestSha256'] = manifest
    raw = json.dumps(release, separators=(',', ':')).encode()
    envelope = dict(payload=base64.b64encode(raw).decode(), signature=base64.b64encode(key.sign(raw)).decode())
    dest = DIST / 'server/flutter/preview' / release['sha256'] / package.name
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(package, dest)
    return envelope


def build_windows(args, flutter, flags, public):
    run([flutter, '--suppress-analytics', 'build', 'windows', '--release',
         '--build-name', META['version'], '--build-number', str(META['sequence'])], APP)
    install = DIST / 'windows'
    payload = install / 'versions/bundled'
    if install.exists():
        # Only this script's generated staging tree is replaced, never installed state.
        shutil.rmtree(install)
    shutil.copytree(APP / 'build/windows/x64/runner/Release', payload)
    env = dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0')
    for target, output, extra in [
        ('./cmd/hqip-flutter-host', payload / 'hqip-runtime.exe', ''),
        ('./cmd/hqip-helper', payload / 'hqip-helper.exe', ' -H=windowsgui'),
        ('./cmd/hqip-flutter-launcher', install / 'HQIP Preview.exe',
         ' -H=windowsgui -X main.updateKey=' + public + ' -X main.bundledSequence=' + str(META['sequence']))]:
        run(['go', 'build', '-mod=readonly', '-trimpath', '-tags=with_utls', '-ldflags', flags + extra, '-o', output, target], env=env)
    core = ROOT / '.cache/traffic-billing-core/sing-box-repro.exe'
    if not core.exists() or digest(core) != CORE_HASH:
        build_core(core)
    shutil.copyfile(core, payload / 'sing-box.exe')
    for name in ['libcronet.dll', 'LICENSE']:
        shutil.copyfile(args.core_deps / name, payload / ('sing-box-LICENSE' if name == 'LICENSE' else name))
    shutil.copyfile(APP / 'windows/runner/resources/app_icon.ico', payload / 'hqip.ico')
    shutil.copyfile(ROOT / 'packaging/flutter-NOTICES.txt', payload / 'THIRD-PARTY-NOTICES.txt')
    shutil.copyfile(args.flutter / 'LICENSE', payload / 'Flutter-LICENSE')
    files = [dict(path=p.relative_to(payload).as_posix(), size=p.stat().st_size, sha256=digest(p))
             for p in sorted(payload.rglob('*')) if p.is_file()]
    write_json(payload / 'payload.json', dict(files=files))
    manifest = digest(payload / 'payload.json')
    package = DIST / 'HQIP-Preview-Windows-x64.zip'
    with zipfile.ZipFile(package, 'w', zipfile.ZIP_DEFLATED) as archive:
        for p in sorted(payload.rglob('*')):
            if p.is_file(): archive.write(p, p.relative_to(payload).as_posix())
    run([args.iscc, '/DSourceRoot=' + str(install), '/DOutputRoot=' + str(DIST),
         '/DProductVersion=' + META['version'], ROOT / 'packaging/flutter-preview.iss'])
    return package, DIST / 'HQIP-Preview-Windows-x64-Setup.exe', manifest


def build_android(args, flutter, flags):
    signing = json.loads(args.android_signing.read_text())
    sdk = Path(os.environ['ANDROID_HOME'])
    env = dict(os.environ, ANDROID_NDK_HOME=str(sdk / 'ndk/30.0.16248370'),
               HQIP_PREVIEW_KEYSTORE=str(Path(signing['keystore']).resolve()),
               HQIP_PREVIEW_STORE_PASSWORD=signing['storePassword'], HQIP_PREVIEW_KEY_ALIAS=signing['alias'],
               HQIP_PREVIEW_KEY_PASSWORD=signing['keyPassword'])
    env['PATH'] = str(TOOLS) + os.pathsep + env['PATH']
    aar = APP / 'android/app/libs/hqip-mobile.aar'
    aar.parent.mkdir(parents=True, exist_ok=True)
    if not args.reuse_aar:
        run([args.gomobile, 'bind', '-target', 'android/arm64,android/amd64', '-androidapi', '26',
             '-tags', 'with_gvisor,with_quic,with_utls,with_clash_api,with_v2ray_api',
             '-ldflags', flags + ' -checklinkname=0', '-o', aar, '.'], ROOT / 'native/mobile', env)
    run([flutter, '--suppress-analytics', 'build', 'apk', '--release', '--split-per-abi',
         '--target-platform', 'android-arm64,android-x64', '--build-number', str(META['sequence']),
         '--build-name', META['version']], APP, env)
    results = []
    for arch, abi, name in [('arm64', 'arm64-v8a', 'arm64'), ('amd64', 'x86_64', 'x64')]:
        package = DIST / ('HQIP-Preview-Android-' + name + '.apk')
        shutil.copyfile(APP / ('build/app/outputs/flutter-apk/app-' + abi + '-release.apk'), package)
        run([sdk / 'build-tools/36.0.0/apksigner.bat', 'verify', '--verbose', package], env=env)
        results.append((package, 'android-' + arch))
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('platform', choices=['windows', 'android'])
    parser.add_argument('--flutter', type=Path, default=ROOT / '.local/flutter-sdk')
    parser.add_argument('--key', type=Path, default=ROOT / '.local/flutter-signing/ed25519.seed')
    parser.add_argument('--android-signing', type=Path, default=ROOT / '.local/flutter-signing/android.json')
    parser.add_argument('--gomobile', type=Path, default=TOOLS / 'gomobile.exe')
    parser.add_argument('--core-deps', type=Path, default=TOOLS / 'core-deps')
    parser.add_argument('--iscc', type=Path, default=TOOLS / 'inno/ISCC.exe')
    parser.add_argument('--reuse-aar', action='store_true', help='Local iteration only; must be the same source/key/version')
    args = parser.parse_args()
    args.flutter = args.flutter.resolve()
    actual = subprocess.check_output(['git', '-C', str(args.flutter), 'rev-parse', 'HEAD'], text=True).strip()
    if actual != META['flutterCommit']: raise ValueError('Flutter SDK differs from pinned commit')
    if META['sequence'] <= 0 or META['channel'] != 'preview': raise ValueError('Invalid preview version')
    key = Ed25519PrivateKey.from_private_bytes(args.key.read_bytes())
    public = key.public_key().public_bytes_raw().hex()
    flags = ('-s -w -X github.com/KeepS13/HQIP/internal/client.AppVersion=' + META['version'] +
             ' -X github.com/KeepS13/HQIP/internal/client.PreviewSequence=' + str(META['sequence']) +
             ' -X github.com/KeepS13/HQIP/internal/client.PreviewUpdatePublicKey=' + public)
    flutter = args.flutter / ('bin/flutter.bat' if os.name == 'nt' else 'bin/flutter')
    DIST.mkdir(parents=True, exist_ok=True)
    entries = []
    if args.platform == 'windows':
        package, installer, manifest = build_windows(args, flutter, flags, public)
        entries.extend([sign(package, 'windows-amd64', 'update', key, manifest), sign(installer, 'windows-amd64', 'installer', key)])
    else:
        for package, platform in build_android(args, flutter, flags):
            entries.extend([sign(package, platform, kind, key) for kind in ['update', 'installer']])
    catalog = DIST / 'server/flutter-preview-releases.json'
    existing = json.loads(catalog.read_text())['releases'] if catalog.exists() else []
    platforms = {json.loads(base64.b64decode(e['payload']))['platform'] for e in entries}
    for e in existing:
        key.public_key().verify(base64.b64decode(e['signature']), base64.b64decode(e['payload']))
    entries += [e for e in existing if json.loads(base64.b64decode(e['payload']))['platform'] not in platforms]
    write_json(catalog, dict(releases=entries))
    (DIST / 'update-public-key.txt').write_text(public + '\n')
    print('Preview artifacts ready in ' + str(DIST) + '; nothing uploaded.')


if __name__ == '__main__': main()
