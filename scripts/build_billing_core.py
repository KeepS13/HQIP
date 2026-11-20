"""Reproducible proxy-outbound statistics core. No third-party source patches."""
from pathlib import Path
import hashlib
import json
import os
import subprocess

VERSION = '1.14.4'
TOOLCHAIN = 'go1.26.10'
MODULE = 'github.com/sagernet/sing-box'
MODULE_SUM = 'h1:KV6J8v1zMUu7BZA93nD9HGMmQnNC46VTuYaKEHkP+gA='
TAGS = 'with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_v2ray_api'
CORE_HASH = '1fc5e70e367621aa95bcc70847f504a819f40f775a859ac95be98e83016c80a0'


def build(output):
    output = Path(output).resolve()
    environment = dict(os.environ, CGO_ENABLED='0', GOOS='windows', GOARCH='amd64',
                       GOTOOLCHAIN=TOOLCHAIN, GOFLAGS='')
    module = json.loads(subprocess.check_output(
        ['go', 'mod', 'download', '-json', MODULE + '@v' + VERSION], env=environment, text=True))
    if module.get('Sum') != MODULE_SUM:
        raise ValueError('sing-box source checksum mismatch')
    output.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(['go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
                    '-tags', TAGS, '-ldflags', '-s -w -X ' + MODULE + '/constant.Version=' + VERSION,
                    '-o', str(output), './cmd/sing-box'], cwd=module['Dir'], env=environment, check=True)
    with output.open('rb') as source:
        if hashlib.file_digest(source, 'sha256').hexdigest() != CORE_HASH:
            raise ValueError('Billing-capable sing-box reproducibility check failed')


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument('output', type=Path)
    build(parser.parse_args().output)
