"""Build HQIP's pinned, protocol-compatible Hysteria 2 server extension."""
from __future__ import annotations
import argparse, hashlib, json, os, pathlib, subprocess

ROOT=pathlib.Path(__file__).resolve().parents[1]
SOURCE=ROOT/'runtime/hysteria'

def digest(path):
    with path.open('rb') as stream:return hashlib.file_digest(stream,'sha256').hexdigest()

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--platform',action='append',choices=['linux-amd64','linux-arm64','windows-amd64'])
    parser.add_argument('--test',action='store_true')
    args=parser.parse_args()
    env=dict(os.environ,CGO_ENABLED='0')
    if args.test:
        subprocess.run(['go','test','./core/server','./core/internal/integration_tests','./app/cmd'],cwd=SOURCE,env=env,check=True)
    out=ROOT/'dist';out.mkdir(exist_ok=True)
    builds={}
    for platform in args.platform or ['linux-amd64','linux-arm64']:
        goos,arch=platform.split('-');suffix='.exe' if goos=='windows' else ''
        target=out/('hqip-hysteria-'+platform+suffix)
        subprocess.run(['go','build','-trimpath','-buildvcs=false','-ldflags=-s -w','-o',str(target),'./app'],cwd=SOURCE,env=dict(env,GOOS=goos,GOARCH=arch),check=True)
        builds[target.name]=digest(target)
    inputs={str(p.relative_to(SOURCE)).replace('\\','/'):digest(p) for p in SOURCE.rglob('*') if p.is_file() and p.suffix in ('.go','.mod','.sum','.md','.json','.work')}
    source_hash=hashlib.sha256(json.dumps(inputs,sort_keys=True,separators=(',',':')).encode()).hexdigest()
    manifest={'upstream':'app/v2.13.0','hqipAdmissionVersion':1,'sourceSHA256':source_hash,'sha256':builds,'license':'runtime/hysteria/LICENSE.md'}
    (out/'hqip-hysteria-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(manifest,indent=2))

if __name__=='__main__':main()
