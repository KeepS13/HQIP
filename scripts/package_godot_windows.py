"""Build the Windows installer from an explicit, complete application payload.

The compiler is a local dependency, never downloaded/executed by this module.
Output is first built in a temporary directory: failed compilation cannot leave
an older installer looking like the result of the current build.
"""
from pathlib import Path
import os
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PAYLOAD = ('HQIP-UI.exe', 'HQIP-UI.pck', 'hqip-runtime.exe', 'hqip-helper.exe',
           'sing-box.exe', 'libcronet.dll', 'THIRD-PARTY-NOTICES.txt', 'sing-box-LICENSE')
INSTALLER_NAME = 'HQIP-Windows-x64-Setup.exe'


def find_compiler(explicit=None):
    """Resolve Inno Setup locally, or fail before the expensive platform build."""
    candidates = [explicit] if explicit else [
        os.environ.get('HQIP_ISCC'), shutil.which('ISCC'),
        ROOT / '.local/godot-tools/inno/ISCC.exe',
        Path(os.environ.get('ProgramFiles(x86)', 'C:/Program Files (x86)')) / 'Inno Setup 6/ISCC.exe']
    for value in candidates:
        if value and Path(value).is_file():
            return Path(value).resolve()
    raise FileNotFoundError('Inno Setup compiler missing; install Inno Setup 6 and pass --iscc PATH.')


def build_installer(install, destination, version, compiler):
    """Return one EXE; include only the launcher and the fixed update payload.

    Source directories remain untouched, including any developer-only files.
    Version input is restricted before it becomes a compiler preprocessor value.
    """
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?', version):
        raise ValueError('Invalid installer version')
    install, destination = Path(install).resolve(), Path(destination).resolve()
    for relative in ('HQIP.exe', *(f'versions/bundled/{name}' for name in PAYLOAD)):
        source = install / relative
        if not source.is_file() or source.stat().st_size == 0 or source.is_symlink():
            raise ValueError(f'Missing or invalid installer payload: {relative}')
    compiler = find_compiler(compiler)
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='hqip-setup-', dir=destination) as temporary:
        subprocess.run([str(compiler), '/Qp', '/DAppVersion=' + version,
                        '/DSourceRoot=' + str(install), '/O' + temporary,
                        str(ROOT / 'packaging/windows/hqip.iss')], check=True)
        output = Path(temporary) / INSTALLER_NAME
        if not output.is_file() or output.stat().st_size == 0:
            raise RuntimeError('Compiler did not produce the requested installer')
        result = destination / INSTALLER_NAME
        os.replace(output, result)
    return result
