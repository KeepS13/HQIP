"""Build the Windows desktop bundle with pinned, verified sing-box binaries."""
from __future__ import annotations
import argparse,hashlib,json,os,pathlib,subprocess,urllib.request,zipfile,shutil

ROOT=pathlib.Path(__file__).resolve().parents[1]
OUT=ROOT/'dist/desktop'
VERSION='1.14.4'
ZIP_HASH='99185d75d314a07dbba4a130c8ad986cbeb629a07927975636b341ca61f991df'
CORE_HASH='8f30682ee198855440d2a7fe070d5d3653793b2d01bb8c85eea88f93bbe7c8b9'
PACKAGE_FILES=(
    'HQIP.exe', 'hqip-helper.exe', 'sing-box.exe', 'libcronet.dll',
    'sing-box-LICENSE', 'THIRD-PARTY-NOTICES.txt', 'manifest.json', '使用说明.md',
)
def sha(p):
    with p.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def run(args,cwd=ROOT):subprocess.run(args,cwd=cwd,check=True)
def main():
    p=argparse.ArgumentParser();p.add_argument('--skip-web',action='store_true');p.add_argument('--skip-download',action='store_true');args=p.parse_args()
    OUT.mkdir(parents=True,exist_ok=True)
    archive=OUT/'sing-box.zip'
    if not args.skip_download and (not archive.exists() or sha(archive)!=ZIP_HASH):
        url=f'https://github.com/SagerNet/sing-box/releases/download/v{VERSION}/sing-box-{VERSION}-windows-amd64.zip'
        with urllib.request.urlopen(url,timeout=120) as r,archive.with_suffix('.part').open('wb') as f:
            while b:=r.read(1024*1024):f.write(b)
        archive.with_suffix('.part').replace(archive)
    if sha(archive)!=ZIP_HASH:raise ValueError('sing-box archive checksum mismatch')
    with zipfile.ZipFile(archive) as z:
        for filename in ['sing-box.exe','libcronet.dll','LICENSE']:
            target=OUT/('sing-box-LICENSE' if filename=='LICENSE' else filename)
            target.write_bytes(z.read(f'sing-box-{VERSION}-windows-amd64/{filename}'))
    if sha(OUT/'sing-box.exe')!=CORE_HASH:raise ValueError('sing-box executable checksum mismatch')
    npm='npm.cmd' if os.name=='nt' else 'npm'
    if not args.skip_web:
        run([npm,'ci'],ROOT/'desktop/frontend');run([npm,'run','build'],ROOT/'desktop/frontend')
    env=dict(os.environ,GOOS='windows',GOARCH='amd64',CGO_ENABLED='0')
    subprocess.run(['go','build','-trimpath','-ldflags=-s -w -H windowsgui','-o',str(OUT/'HQIP.exe'),'.'],cwd=ROOT/'desktop',env=env,check=True)
    subprocess.run(['go','build','-trimpath','-ldflags=-s -w -H windowsgui','-o',str(OUT/'hqip-helper.exe'),'./cmd/hqip-helper'],cwd=ROOT,env=env,check=True)
    sources={'HQIP':'https://github.com/KeepS13/HQIP','sing-box':f'https://github.com/SagerNet/sing-box/tree/v{VERSION}','Wails':'https://github.com/wailsapp/wails/tree/v3.1.0'}
    (OUT/'THIRD-PARTY-NOTICES.txt').write_text('HQIP Windows client\n\nsing-box '+VERSION+' (GPL-3.0-or-later): see sing-box-LICENSE.\nWails v3.1.0 (MIT).\nSource revisions:\n'+json.dumps(sources,indent=2)+'\nRouting data attribution is included in each authenticated rules bundle.\n',encoding='utf-8')
    manifest={'version':'2.0.0','singBox':VERSION,'wails':'3.1.0','sources':sources,'sha256':{n:sha(OUT/n) for n in ['HQIP.exe','hqip-helper.exe','sing-box.exe','libcronet.dll']}}
    (OUT/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
    readme=ROOT/'docs/client.md'
    if readme.exists():shutil.copyfile(readme,OUT/'使用说明.md')
    missing=[name for name in PACKAGE_FILES if not (OUT/name).is_file()]
    if missing:raise FileNotFoundError('Missing package files: '+', '.join(missing))
    with zipfile.ZipFile(ROOT/'dist/HQIP-Windows-x64.zip','w',zipfile.ZIP_DEFLATED) as z:
        for name in PACKAGE_FILES:z.write(OUT/name,'HQIP/'+name)
    print('Built dist/HQIP-Windows-x64.zip with verified sing-box core.')
if __name__=='__main__':main()
