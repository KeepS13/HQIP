"""Create a verified overlay on an existing production release manifest."""
from pathlib import Path
import argparse,hashlib,json,shutil,subprocess,tarfile
ROOT=Path(__file__).resolve().parents[1]
def digest(p):
 with p.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--base-manifest',required=True,type=Path);a=p.parse_args()
 if subprocess.check_output(['git','status','--porcelain','--untracked-files=normal'],cwd=ROOT,text=True).strip():raise ValueError('Commit all release inputs first')
 sha=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
 base=json.loads(a.base_manifest.read_text(encoding='utf-8'))
 desktop=json.loads((ROOT/'dist/desktop-manifest.json').read_text(encoding='utf-8'))
 if desktop['sourceSHA']!=sha or desktop['dirty'] or digest(ROOT/'dist/HQIP-Windows-x64.zip')!=desktop['sha256']:raise ValueError('Desktop release must match current clean commit')
 out=ROOT/'dist'/('domains-'+sha);out.mkdir(exist_ok=False)
 inputs={'hqip':ROOT/'dist/hqip-domains-linux-amd64','artifacts/HQIP-Windows-x64.zip':ROOT/'dist/HQIP-Windows-x64.zip','artifacts/desktop-manifest.json':ROOT/'dist/desktop-manifest.json'}
 for name in ['upgrade_control.py','upgrade_domains.py','migrate_agent_api.py','upgrade_node_admission.py','subscription_migration.py']:inputs['scripts/'+name]=ROOT/'scripts'/name
 for name in ['nginx-hqip.conf','hqip-control.service']:inputs['deploy/'+name]=ROOT/'deploy'/name
 for app in ['admin-web','user-web']:
  for f in (ROOT/app/'dist').rglob('*'):
   if f.is_file():inputs[app+'/'+f.relative_to(ROOT/app/'dist').as_posix()]=f
 hashes=dict(base['sha256'])
 for name,source in inputs.items():
  dest=out/name;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source,dest);hashes[name]=digest(dest)
 manifest={'sourceSHA':sha,'previousSourceSHA':base['sourceSHA'],'upgradeKind':'domains-alerts','schemaVersion':5,'clientApiVersion':3,'sha256':hashes}
 (out/'release.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
 archive=ROOT/'dist'/('hqip-domains-'+sha[:12]+'.tar.gz')
 with tarfile.open(archive,'w:gz') as tar:
  for name in [*inputs,'release.json']:tar.add(out/name,arcname=name,recursive=False)
 result={'sourceSHA':sha,'filename':archive.name,'size':archive.stat().st_size,'sha256':digest(archive),'overlayFiles':list(inputs)}
 (ROOT/'dist/domains-release.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
 print(json.dumps({k:v for k,v in result.items() if k!='overlayFiles'}))
if __name__=='__main__':main()
