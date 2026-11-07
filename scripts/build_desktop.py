"""Build the Windows desktop bundle with pinned, verified sing-box binaries."""
from __future__ import annotations
import argparse,hashlib,json,os,pathlib,subprocess,tempfile,urllib.request,zipfile,shutil

ROOT=pathlib.Path(__file__).resolve().parents[1]
OUT=ROOT/'dist/desktop'
VERSION='1.14.4'
ZIP_HASH='99185d75d314a07dbba4a130c8ad986cbeb629a07927975636b341ca61f991df'
CORE_HASH='8f30682ee198855440d2a7fe070d5d3653793b2d01bb8c85eea88f93bbe7c8b9'
RESOURCE_TOOL=ROOT/'desktop/build/windows'
PACKAGE_FILES=(
    'HQIP.exe', 'hqip-helper.exe', 'sing-box.exe', 'libcronet.dll',
    'sing-box-LICENSE', 'THIRD-PARTY-NOTICES.txt', 'manifest.json', '使用说明.md',
)
def sha(p):
    with p.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def run(args,cwd=ROOT):subprocess.run(args,cwd=cwd,check=True)
def main():
    p=argparse.ArgumentParser();p.add_argument('--skip-web',action='store_true');p.add_argument('--skip-download',action='store_true');p.add_argument('--output-dir',type=pathlib.Path,default=OUT,help='Bundle directory; choose a separate directory while the existing client is running.');args=p.parse_args()
    out=args.output_dir.resolve()
    out.mkdir(parents=True,exist_ok=True)
    archive=out/'sing-box.zip'
    cached_archive=OUT/'sing-box.zip'
    if not archive.exists() and archive!=cached_archive and cached_archive.exists() and sha(cached_archive)==ZIP_HASH:
        shutil.copyfile(cached_archive,archive)
    if not args.skip_download and (not archive.exists() or sha(archive)!=ZIP_HASH):
        url=f'https://github.com/SagerNet/sing-box/releases/download/v{VERSION}/sing-box-{VERSION}-windows-amd64.zip'
        with urllib.request.urlopen(url,timeout=120) as r,archive.with_suffix('.part').open('wb') as f:
            while b:=r.read(1024*1024):f.write(b)
        archive.with_suffix('.part').replace(archive)
    if sha(archive)!=ZIP_HASH:raise ValueError('sing-box archive checksum mismatch')
    with zipfile.ZipFile(archive) as z:
        for filename in ['sing-box.exe','libcronet.dll','LICENSE']:
            target=out/('sing-box-LICENSE' if filename=='LICENSE' else filename)
            target.write_bytes(z.read(f'sing-box-{VERSION}-windows-amd64/{filename}'))
    if sha(out/'sing-box.exe')!=CORE_HASH:raise ValueError('sing-box executable checksum mismatch')
    npm='npm.cmd' if os.name=='nt' else 'npm'
    if not args.skip_web:
        run([npm,'ci'],ROOT/'desktop/frontend');run([npm,'run','build'],ROOT/'desktop/frontend')
    env=dict(os.environ,GOOS='windows',GOARCH='amd64',CGO_ENABLED='0')
    icon=ROOT/'desktop/assets/hqip.ico'
    resource_path=ROOT/'desktop/hqip_brand_windows_amd64.syso'
    if resource_path.exists():raise FileExistsError(f'Resource file already exists; another build may be active: {resource_path}')
    with tempfile.TemporaryDirectory(prefix='hqip-resources-') as temporary:
        generated=pathlib.Path(temporary)/'hqip_windows_amd64.syso'
        run(['go','run','-mod=readonly','.', '-icon',str(icon),'-out',str(generated)],RESOURCE_TOOL)
        resource_created=False
        try:
            with resource_path.open('xb') as resource:
                resource_created=True
                resource.write(generated.read_bytes())
            subprocess.run(['go','build','-trimpath','-ldflags=-s -w -H windowsgui','-o',str(out/'HQIP.exe'),'.'],cwd=ROOT/'desktop',env=env,check=True)
        finally:
            if resource_created:resource_path.unlink()
    run(['go','run','-mod=readonly','.', '-icon',str(icon),'-verify',str(out/'HQIP.exe')],RESOURCE_TOOL)
    subprocess.run(['go','build','-trimpath','-ldflags=-s -w -H windowsgui','-o',str(out/'hqip-helper.exe'),'./cmd/hqip-helper'],cwd=ROOT,env=env,check=True)
    source_sha=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
    dirty=bool(subprocess.check_output(['git','status','--porcelain','--untracked-files=normal'],cwd=ROOT,text=True).strip())
    sources={'HQIP':f'https://github.com/KeepS13/HQIP/tree/{source_sha}','sing-box':f'https://github.com/SagerNet/sing-box/tree/v{VERSION}','Wails':'https://github.com/wailsapp/wails/tree/v3.1.0'}
    (out/'THIRD-PARTY-NOTICES.txt').write_text('HQIP Windows client\n\nsing-box '+VERSION+' (GPL-3.0-or-later): see sing-box-LICENSE.\nWails v3.1.0 (MIT).\nSource revisions:\n'+json.dumps(sources,indent=2)+'\nRouting data attribution is included in each authenticated rules bundle.\n',encoding='utf-8')
    manifest={'version':'4.2.0','sourceSHA':source_sha,'dirty':dirty,'singBox':VERSION,'wails':'3.1.0','sources':sources,'windowsResources':{'generator':'github.com/tc-hib/winres@v0.3.1','iconSha256':sha(icon),'groupId':3},'sha256':{n:sha(out/n) for n in ['HQIP.exe','hqip-helper.exe','sing-box.exe','libcronet.dll']}}
    (out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
    readme=ROOT/'docs/client.md'
    if readme.exists():shutil.copyfile(readme,out/'使用说明.md')
    missing=[name for name in PACKAGE_FILES if not (out/name).is_file()]
    if missing:raise FileNotFoundError('Missing package files: '+', '.join(missing))
    (ROOT/'dist').mkdir(parents=True,exist_ok=True)
    with zipfile.ZipFile(ROOT/'dist/HQIP-Windows-x64.zip','w',zipfile.ZIP_DEFLATED) as z:
        for name in PACKAGE_FILES:z.write(out/name,'HQIP/'+name)
    package=ROOT/'dist/HQIP-Windows-x64.zip'
    release={'version':'4.2.0','sourceSHA':source_sha,'dirty':dirty,'filename':package.name,'sha256':sha(package),'size':package.stat().st_size}
    (ROOT/'dist/desktop-manifest.json').write_text(json.dumps(release,indent=2)+'\n',encoding='utf-8')
    print('Built dist/HQIP-Windows-x64.zip with verified sing-box core.')
if __name__=='__main__':main()
