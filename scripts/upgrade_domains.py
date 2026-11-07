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

def schema_version():
 with closing(sqlite3.connect((control.DATA/'hqip.db').resolve().as_uri()+'?mode=ro',uri=True)) as db:
  row=db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone()
  if row not in [('4',),('5',)]:raise ValueError('Unexpected schema version')
  return int(row[0])

def subscription_migration_ready():
 if schema_version()==5:return
 with closing(sqlite3.connect((control.DATA/'hqip.db').resolve().as_uri()+'?mode=ro',uri=True)) as db:
  marker=db.execute("SELECT value FROM metadata WHERE key='subscriptions_reset_ready'").fetchone()
  if marker!=('true',):raise ValueError('Subscription migration has not been prepared; finish node settlement and backups first')
  if db.execute('SELECT COUNT(*) FROM portal_leases WHERE closed=0').fetchone()[0]:raise ValueError('Unsettled legacy leases prevent subscription migration')

def data_identity():
 if control.DATA.is_symlink() or not control.DATA.is_dir():raise ValueError('Invalid live data directory')
 database,key=control.DATA/'hqip.db',control.DATA/'master.key'
 if any(p.is_symlink() or not p.is_file() for p in (database,key)):raise ValueError('Database and paired key required')
 schema_version()
 return hashlib.sha256(key.read_bytes()).hexdigest()

def disable_feishu():
 with closing(sqlite3.connect(control.DATA/'hqip.db')) as db,db:
  if db.execute("SELECT 1 FROM sqlite_master WHERE name='feishu_settings'").fetchone():
   db.execute("UPDATE feishu_settings SET enabled=0,webhook='',secret=''")
   db.execute("UPDATE notifications SET status='cancelled',claim='',claim_until=0,next_attempt=0 WHERE status IN ('pending','retry','sending')")

def verify_agent_assets(release,old,manifest):
 if manifest.get('agentTelemetryVersion')!=1:return set()
 names={'agent-linux-amd64','agent-linux-arm64'}
 previous=json.loads((old/'artifacts/manifest.json').read_text())
 current=json.loads((release/'artifacts/manifest.json').read_text())
 if current.get('agentTelemetryVersion')!=1 or current.get('agentSourceSHA')!=manifest['sourceSHA']:raise ValueError('Agent telemetry provenance does not match release')
 for value in [previous,current]:
  if value.get('hqipAdmissionVersion')!=1:raise ValueError('Admission core must be preserved')
 if {k:v for k,v in previous['sha256'].items() if k not in names}!={k:v for k,v in current['sha256'].items() if k not in names}:raise ValueError('Only Agent artifacts may change in a telemetry release')
 if {k:v for k,v in previous.items() if k not in ['sha256','agentTelemetryVersion','agentSourceSHA']}!={k:v for k,v in current.items() if k not in ['sha256','agentTelemetryVersion','agentSourceSHA']}:raise ValueError('Pinned node provenance changed')
 for name in names:
  if current['sha256'].get(name)!=manifest['sha256'].get('artifacts/'+name):raise ValueError('Agent artifact manifest mismatch')
 return {'artifacts/manifest.json',*('artifacts/'+name for name in names)}

def require_billing_compatible(manifest):
 with closing(sqlite3.connect((control.DATA/'hqip.db').resolve().as_uri()+'?mode=ro',uri=True)) as db:
  initialized=db.execute("SELECT 1 FROM sqlite_master WHERE name IN ('server_billing','node_hourly_coverage') LIMIT 1").fetchone()
  # The schema gate also covers an upgraded Agent before its first report. Old
  # controllers strictly reject new report fields even without configured cycles.
  if initialized and manifest.get('serverBillingVersion',0)<1:raise ValueError('Initialized telemetry schema requires a billing-compatible release; current accounting and Agent reports are preserved')
  columns={row[1] for row in db.execute("PRAGMA table_info(server_billing)")}
  # A date correction can retain the cycle number while moving its anchor.
  # Version 1 ignores that offset and would reuse old cycle IDs / alert keys.
  if 'cycle_offset' in columns and manifest.get('serverBillingVersion',0)<2:
   if db.execute('SELECT 1 FROM server_billing WHERE cycle_offset<>0 LIMIT 1').fetchone():
    raise ValueError('Reanchored billing cycles require serverBillingVersion 2; current cycle identities and accounting are preserved')

def verify(release):
 release,manifest=control.verify_release(release)
 for path in ['admin-web/index.html','user-web/index.html','deploy/nginx-hqip.conf','deploy/hqip-control.service','artifacts/HQIP-Windows-x64.zip','artifacts/desktop-manifest.json']:
  if path not in manifest['sha256']:raise ValueError('Missing domain release asset: '+path)
 old=(control.BASE/'current').resolve(strict=True)
 _,previous=control.verify_release(old)
 if release==old or manifest.get('upgradeKind')!='domains-alerts' or manifest.get('previousSourceSHA')!=previous['sourceSHA']:raise ValueError('Release is not based on active version')
 allowed=verify_agent_assets(release,old,manifest)|{'artifacts/HQIP-Windows-x64.zip','artifacts/desktop-manifest.json'}
 for name,digest in previous['sha256'].items():
  if name.startswith('artifacts/') and name not in allowed and manifest['sha256'].get(name)!=digest:raise ValueError('Unexpected node artifact change')
 data_identity()
 require_billing_compatible(manifest)
 if manifest.get('schemaVersion',4)<schema_version():raise ValueError('Release cannot read the current database schema; downgrade refused')
 if manifest.get('schemaVersion',4)==5:subscription_migration_ready()
 return release,manifest,old

def check_state(state,exists=False):
 if not state.is_absolute() or state.parent!=BACKUPS or state.is_symlink() or (exists and not state.is_dir()):raise ValueError('Invalid backup directory')

def rollback(state):
 check_state(state,True);info=json.loads((state/'domains-state.json').read_text())
 active=(control.BASE/'current').resolve(strict=True)
 if active not in [Path(info['previous']),Path(info['release'])]:raise ValueError('Refusing rollback over a later release')
 _,previous_manifest=control.verify_release(Path(info['previous']))
 require_billing_compatible(previous_manifest)
 if schema_version()>previous_manifest.get('schemaVersion',4):raise ValueError('Rollback binary cannot read the current subscription schema; current data and release are preserved')
 if data_identity()!=info['masterSHA256']:raise ValueError('Master key changed; manual recovery required')
 control.run('systemctl','stop',control.SERVICE)
 if data_identity()!=info['masterSHA256']:raise ValueError('Stopped data identity changed')
 try:require_billing_compatible(previous_manifest)
 except Exception:
  control.run('systemctl','start',control.SERVICE)
  raise
 if schema_version()>previous_manifest.get('schemaVersion',4):
  control.run('systemctl','start',control.SERVICE)
  raise ValueError('Schema advanced while stopping; current compatible release restarted, downgrade refused')
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
 info={'previous':str(previous),'release':str(release),'sourceSHA':manifest['sourceSHA'],'masterSHA256':data_identity(),'schemaBefore':schema_version(),'phase':'preparing'}
 if PREP.is_symlink():info['prepareTarget']=str(PREP.resolve(strict=True))
 elif PREP.exists():raise ValueError('Unexpected preparation site')
 (state/'domains-state.json').write_text(json.dumps(info,indent=2))
 control.run('systemctl','stop',control.SERVICE)
 try:
  if data_identity()!=info['masterSHA256']:raise ValueError('Stopped data identity changed')
  if manifest.get('schemaVersion',4)==5:subscription_migration_ready()
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
  if schema_version()>info['schemaBefore']:
   info['phase']='failed-schema-advanced-data-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
   raise RuntimeError('Schema migration completed; refusing incompatible old binary. Fix forward with this data or deploy a compatible v5 release.')
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
