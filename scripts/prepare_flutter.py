"""Prepare only preview build tools or an isolated preview signing identity."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import urllib.request
import zipfile
from build_desktop import VERSION, ZIP_HASH

ROOT = Path(__file__).resolve().parents[1]
TOOLS = ROOT / '.local/flutter-tools'


def fetch(name, url, checksum):
    path = TOOLS / name
    if not path.exists() or hashlib.sha256(path.read_bytes()).hexdigest() != checksum:
        temporary = path.with_suffix('.part')
        with urllib.request.urlopen(url, timeout=180) as source, temporary.open('wb') as output:
            import shutil
            shutil.copyfileobj(source, output)
        if hashlib.sha256(temporary.read_bytes()).hexdigest() != checksum:
            raise ValueError('Tool hash mismatch: ' + name)
        temporary.replace(path)
    return path


def tools(platform):
    TOOLS.mkdir(parents=True, exist_ok=True)
    if platform == 'windows':
        installer = fetch('innosetup-6.7.3.exe', 'https://github.com/jrsoftware/issrc/releases/download/is-6_7_3/innosetup-6.7.3.exe',
                          '9c73c3bae7ed48d44112a0f48e66742c00090bdb5bef71d9d3c056c66e97b732')
        subprocess.run([str(installer), '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/CURRENTUSER', '/DIR=' + str(TOOLS / 'inno')], check=True)
        archive = fetch('sing-box-' + VERSION + '.zip', f'https://github.com/SagerNet/sing-box/releases/download/v{VERSION}/sing-box-{VERSION}-windows-amd64.zip', ZIP_HASH)
        deps = TOOLS / 'core-deps'; deps.mkdir(exist_ok=True)
        with zipfile.ZipFile(archive) as z:
            for name in ['libcronet.dll', 'LICENSE']:
                (deps / name).write_bytes(z.read(f'sing-box-{VERSION}-windows-amd64/' + name))
    else:
        for tool in ['gomobile', 'gobind']:
            subprocess.run(['go', 'install', f'github.com/sagernet/gomobile/cmd/{tool}@v0.1.12'], env=dict(os.environ, GOBIN=str(TOOLS)), check=True)


def signing():
    directory = ROOT / '.local/flutter-signing'
    directory.mkdir(parents=True, exist_ok=False)
    seed = os.environ.get('HQIP_FLUTTER_UPDATE_SEED', '')
    raw = base64.b64decode(seed, validate=True) if seed else secrets.token_bytes(32)
    if len(raw) != 32: raise ValueError('Preview Ed25519 seed must be 32 bytes')
    (directory / 'ed25519.seed').write_bytes(raw)
    keystore = directory / 'android.jks'
    supplied = os.environ.get('HQIP_FLUTTER_ANDROID_KEYSTORE', '')
    if supplied:
        keystore.write_bytes(base64.b64decode(supplied, validate=True))
        values = json.loads(os.environ['HQIP_FLUTTER_ANDROID_SIGNING_JSON'])
    else:
        password = secrets.token_hex(24)
        values = dict(alias='hqip-preview', storePassword=password, keyPassword=password)
        subprocess.run(['keytool', '-genkeypair', '-keystore', str(keystore), '-storepass:env', 'HQIP_TEMP_STORE_PASS',
                        '-keypass:env', 'HQIP_TEMP_STORE_PASS', '-alias', values['alias'], '-keyalg', 'RSA', '-keysize', '3072',
                        '-validity', '10000', '-dname', 'CN=HQIP Flutter Preview'],
                       env=dict(os.environ, HQIP_TEMP_STORE_PASS=password), check=True)
    values['keystore'] = str(keystore)
    (directory / 'android.json').write_text(json.dumps(values), encoding='utf-8')
    print('Created isolated preview signing files. Retain them for future compatible updates; never upload them.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['tools', 'signing'])
    parser.add_argument('--platform', choices=['windows', 'android'], default='windows')
    args = parser.parse_args()
    tools(args.platform) if args.action == 'tools' else signing()
