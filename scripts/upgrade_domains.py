"""Atomic three-domain upgrade. Rollback restores code/routes, never accounting."""
from __future__ import annotations
import argparse,hashlib,json,os,shutil,sqlite3,sys,time
from pathlib import Path
from contextlib import closing
sys.dont_write_bytecode=True
import upgrade_control as control

SITE=Path('/etc/nginx/sites-available/hqip.conf')
UNIT=Path('/etc/systemd/system/hqip-control.service')
BACKUPS=Path('/root/backups')
PREP=Path('/etc/nginx/sites-enabled/hqip-domains-prepare.conf')

def data_identity():
 if control.DATA.is_symlink() or not control.DATA.is_dir():raise ValueError('Invalid live data directory')
 database,key=control.DATA/'hqip.db',control.DATA/'master.key'
 if any(p.is_symlink() or not p.is_file() for p in (database,key)):raise ValueError('Database and paired key required')
 with closing(sqlite3.connect(database.resolve().as_uri()+'?mode=ro',uri=True)) as db:
  if db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone()!=('4',):raise ValueError('Unexpected schema version')
 return hashlib.sha256(key.read_bytes()).hexdigest()

def disable_feishu():
 with closing(sqlite3.connect(control.DATA/'hqip.db')) as db,db:
  if db.execute("SELECT 1 FROM sqlite_master WHERE name='feishu_settings'").fetchone():
   db.execute("UPDATE feishu_settings SET enabled=0,webhook='',secret=''")
   db.execute("UPDATE notifications SET status='cancelled',claim='',claim_until=0,next_attempt=0 WHERE status IN ('pending','retry','sending')")

def verify(release):
 release,manifest=control.verify_release(release)
 for path in ['admin-web/index.html','user-web/index.html','deploy/nginx-hqip.conf','deploy/hqip-control.service','artifacts/HQIP-Windows-x64.zip','artifacts/desktop-manifest.json']:
  if path not in manifest['sha256']:raise ValueError('Missing domain release asset: '+path)
 old=(control.BASE/'current').resolve(strict=True)
 _,previous=control.verify_release(old)
 if release==old or manifest.get('upgradeKind')!='domains-alerts' or manifest.get('previousSourceSHA')!=previous['sourceSHA']:raise ValueError('Release is not based on active version')
 for name,digest in previous['sha256'].items():
  if name.startswith('artifacts/') and name not in ['artifacts/HQIP-Windows-x64.zip','artifacts/desktop-manifest.json'] and manifest['sha256'].get(name)!=digest:raise ValueError('Unexpected node artifact change')
 data_identity()
 return release,manifest,old

def check_state(state,exists=False):
 if not state.is_absolute() or state.parent!=BACKUPS or state.is_symlink() or (exists and not state.is_dir()):raise ValueError('Invalid backup directory')

def rollback(state):
 check_state(state,True);info=json.loads((state/'domains-state.json').read_text())
 active=(control.BASE/'current').resolve(strict=True)
 if active not in [Path(info['previous']),Path(info['release'])]:raise ValueError('Refusing rollback over a later release')
 control.verify_release(Path(info['previous']))
 if data_identity()!=info['masterSHA256']:raise ValueError('Master key changed; manual recovery required')
 control.run('systemctl','stop',control.SERVICE)
 if data_identity()!=info['masterSHA256']:raise ValueError('Stopped data identity changed')
 disable_feishu()
 shutil.copyfile(state/'nginx.before.conf',SITE);SITE.chmod(0o644)
 shutil.copyfile(state/'unit.before.service',UNIT);UNIT.chmod(0o644)
 if info.get('prepareTarget') and not PREP.exists():PREP.symlink_to(info['prepareTarget'])
 control.point_current(Path(info['previous']))
 control.run('nginx','-t');control.run('systemctl','daemon-reload');control.run('systemctl','start',control.SERVICE);control.healthy();control.run('systemctl','reload','nginx')
 info['phase']='rolled-back-data-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))

def upgrade(release,state):
 check_state(state);release,manifest,previous=verify(release)
 if SITE.is_symlink() or UNIT.is_symlink():raise ValueError('Unexpected configuration symlink')
 state.mkdir(mode=0o700,exist_ok=False)
 shutil.copyfile(SITE,state/'nginx.before.conf');shutil.copyfile(UNIT,state/'unit.before.service')
 info={'previous':str(previous),'release':str(release),'sourceSHA':manifest['sourceSHA'],'masterSHA256':data_identity(),'phase':'preparing'}
 if PREP.is_symlink():info['prepareTarget']=str(PREP.resolve(strict=True))
 elif PREP.exists():raise ValueError('Unexpected preparation site')
 (state/'domains-state.json').write_text(json.dumps(info,indent=2))
 control.run('systemctl','stop',control.SERVICE)
 try:
  if data_identity()!=info['masterSHA256']:raise ValueError('Stopped data identity changed')
  if any(p.is_symlink() for p in control.DATA.rglob('*')):raise ValueError('Symlink in live data')
  shutil.copytree(control.DATA,state/'data.before')
  for p in state.rglob('*'):p.chmod(0o700 if p.is_dir() else 0o600)
  disable_feishu()
  control.point_current(release)
  shutil.copyfile(release/'deploy/hqip-control.service',UNIT);UNIT.chmod(0o644)
  shutil.copyfile(release/'deploy/nginx-hqip.conf',SITE);SITE.chmod(0o644)
  if info.get('prepareTarget'):PREP.unlink()
  control.run('nginx','-t');control.run('systemctl','daemon-reload');control.run('systemctl','start',control.SERVICE);control.healthy();control.run('systemctl','reload','nginx')
  if data_identity()!=info['masterSHA256']:raise ValueError('Live data identity changed')
 except Exception:
  rollback(state);raise
 info['phase']='active';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
 print(json.dumps({'backup':str(state),'sourceSHA':manifest['sourceSHA'],'phase':'active'}))

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['verify','upgrade','rollback']);p.add_argument('--release',type=Path);p.add_argument('--state',type=Path);a=p.parse_args()
 if a.action=='verify':print(verify(a.release)[1]['sourceSHA'])
 else:
  if os.geteuid()!=0:raise PermissionError('Root required')
  if a.action=='upgrade':upgrade(a.release,a.state)
  else:rollback(a.state)
