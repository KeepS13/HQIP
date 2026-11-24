"""Prepare pinned Windows build tools and an isolated CI signing directory.

Only trusted main pushes receive the persistent identities. Pull requests build
with disposable keys; no signing material belongs in artifacts or tool caches.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import urllib.request
import zipfile

from client_bundle import CLIENT_PUBLIC_KEY, client_digest

ROOT = Path(__file__).resolve().parents[1]
TOOLS = ROOT / '.local/godot-tools'
GODOT_BASE = 'https://github.com/godotengine/godot-builds/releases/download/4.7.2-stable/'


def download(name, url, checksum):
    path = TOOLS / name
    if not path.is_file() or client_digest(path) != checksum:
        with urllib.request.urlopen(url, timeout=180) as source, path.with_suffix('.part').open('wb') as output:
            shutil.copyfileobj(source, output)
        if client_digest(path.with_suffix('.part')) != checksum:
            raise ValueError('Build tool checksum mismatch: ' + name)
        path.with_suffix('.part').replace(path)
    return path


def extract(archive, names, destination):
    with zipfile.ZipFile(archive) as package:
        for name in names:
            target = destination / Path(name).name
            target.parent.mkdir(parents=True, exist_ok=True)
            with package.open(name) as source, target.open('wb') as output:
                shutil.copyfileobj(source, output)


def prepare_tools(platform):
    TOOLS.mkdir(parents=True, exist_ok=True)
    editor = download('Godot_v4.7.2-stable_win64.exe.zip', GODOT_BASE + 'Godot_v4.7.2-stable_win64.exe.zip',
                      '731980f9608d61333e5baf54a2ef17210acc7a538446c0cb9969f002aca1e953')
    extract(editor, ['Godot_v4.7.2-stable_win64.exe', 'Godot_v4.7.2-stable_win64_console.exe'], TOOLS)
    templates = download('Godot_v4.7.2-stable_export_templates.tpz', GODOT_BASE + 'Godot_v4.7.2-stable_export_templates.tpz',
                         'f298490b8d44d934be425a5a65a51bf15f422428b229a06a6e11d9ffea248011')
    if platform == 'windows-amd64':
        extract(templates, ['templates/windows_release_x86_64.exe', 'templates/windows_debug_x86_64.exe'], TOOLS)
        installer = download('innosetup-6.7.3.exe',
            'https://github.com/jrsoftware/issrc/releases/download/is-6_7_3/innosetup-6.7.3.exe',
            '9c73c3bae7ed48d44112a0f48e66742c00090bdb5bef71d9d3c056c66e97b732')
        subprocess.run([str(installer), '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART',
                        '/CURRENTUSER', '/DIR=' + str(TOOLS / 'inno')], check=True)
        from build_desktop import VERSION, ZIP_HASH
        core = download('sing-box-' + VERSION + '.zip',
            f'https://github.com/SagerNet/sing-box/releases/download/v{VERSION}/sing-box-{VERSION}-windows-amd64.zip', ZIP_HASH)
        dependencies = ROOT / '.local/reality-desktop'
        extract(core, [f'sing-box-{VERSION}-windows-amd64/libcronet.dll', f'sing-box-{VERSION}-windows-amd64/LICENSE'], dependencies)
        shutil.copyfile(dependencies / 'LICENSE', dependencies / 'sing-box-LICENSE')
    else:
        extract(templates, ['templates/android_source.zip', 'templates/android_debug.apk', 'templates/android_release.apk'], TOOLS)
        extract(TOOLS / 'android_source.zip', ['libs/release/godot-lib.template_release.aar'], TOOLS / 'android-template/libs/release')
        gradle = download('gradle-8.11.1-bin.zip', 'https://services.gradle.org/distributions/gradle-8.11.1-bin.zip',
                          'f397b287023acdba1e9f6fc5ea72d22dd63669d59ed4a289a29b1a76eee151c6')
        # This archive is checksum-pinned before extraction and contains only build tools.
        with zipfile.ZipFile(gradle) as archive:
            archive.extractall(TOOLS)
        environment = dict(os.environ, GOBIN=str(TOOLS))
        for name in ('gomobile', 'gobind'):
            subprocess.run(['go', 'install', f'github.com/sagernet/gomobile/cmd/{name}@v0.1.12'], env=environment, check=True)


def prepare_signing(platform):
    signing = ROOT / '.local/ci-signing'
    signing.mkdir(exist_ok=False)
    trusted = os.environ.get('GITHUB_REF') == 'refs/heads/main' and os.environ.get('GITHUB_EVENT_NAME') in ('push', 'workflow_dispatch')
    seed = base64.b64decode(os.environ['HQIP_CLIENT_UPDATE_SEED'], validate=True) if trusted else os.urandom(32)
    if len(seed) != 32:
        raise ValueError('Invalid update identity')
    (signing / 'ed25519.seed').write_bytes(seed)
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
    key = Ed25519PrivateKey.from_private_bytes(seed).public_key().public_bytes_raw().hex()
    if trusted and key != CLIENT_PUBLIC_KEY:
        raise ValueError('CI update identity differs from installed clients')
    if platform == 'windows-amd64':
        return
    keystore = signing / 'android.jks'
    if trusted:
        keystore.write_bytes(base64.b64decode(os.environ['HQIP_ANDROID_KEYSTORE'], validate=True))
        values = json.loads(os.environ['HQIP_ANDROID_SIGNING_JSON'])
    else:
        values = dict(alias='hqip-test', storePassword='disposable-ci-only', keyPassword='disposable-ci-only')
        subprocess.run(['keytool', '-genkeypair', '-keystore', str(keystore), '-storepass', values['storePassword'],
            '-keypass', values['keyPassword'], '-alias', values['alias'], '-keyalg', 'RSA', '-keysize', '2048',
            '-validity', '7', '-dname', 'CN=HQIP CI test'], check=True)
    values['keystore'] = str(keystore)
    (signing / 'android.json').write_text(json.dumps(values), encoding='utf-8')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('tools', 'signing'))
    parser.add_argument('platform', choices=('windows-amd64', 'android-arm64', 'android-amd64'))
    args = parser.parse_args()
    if args.action == 'tools':
        prepare_tools(args.platform)
    else:
        prepare_signing(args.platform)
