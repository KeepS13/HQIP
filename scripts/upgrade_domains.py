"""Atomic three-domain upgrade. Rollback restores code/routes, never accounting."""
from __future__ import annotations
import argparse,hashlib,json,os,shlex,shutil,sqlite3,subprocess,sys,time
from pathlib import Path
from contextlib import closing
from urllib.parse import urlsplit
sys.dont_write_bytecode=True
import upgrade_control as control
from fresh_deploy import require_free_space

SITE=Path('/etc/nginx/sites-available/hqip.conf')
UNIT=Path('/etc/systemd/system/hqip-control.service')
BACKUPS=Path('/root/backups')
PREP=Path('/etc/nginx/sites-enabled/hqip-domains-prepare.conf')
STORAGE_ENV=Path('/etc/hqip-control/storage.env')
MIGRATION_ENV=Path('/etc/hqip-storage/migration.env')

def postgres_enabled():
 return (control.DATA/'postgresql.enabled').is_file()

def storage_environment(path=None):
 # A deliberately small EnvironmentFile subset: one quoted KEY=value per line,
 # no shell evaluation, interpolation, includes or inherited PG credentials.
 # The probe/backup binary needs only these explicitly parsed settings. Do not
 # forward the deployer's ambient environment (which may contain unrelated
 # cloud or database credentials) into a long-lived child process.
 # pg_dump/pg_restore are child processes of the backup binary. Supply only
 # the provisioned PG18 tools and trusted system directories, never the
 # caller's potentially writable PATH or the coexisting PG14 tool selection.
 env={'PATH':'/usr/lib/postgresql/18/bin:/usr/bin:/bin'}
 if path is None:path=STORAGE_ENV
 if path.is_symlink() or not path.is_file():raise ValueError('PostgreSQL storage environment is missing')
 if os.name!='nt' and path.stat().st_mode & 0o077:raise ValueError('Storage environment must have mode 0600')
 for line in path.read_text().splitlines():
  parts=shlex.split(line,comments=True,posix=True)
  if not parts:continue
  if len(parts)!=1 or '=' not in parts[0]:raise ValueError('Invalid storage environment line')
  key,value=parts[0].split('=',1)
  if key not in {'HQIP_DATABASE_URL','HQIP_REDIS_URL','HQIP_REDIS_NAMESPACE'} or not value:raise ValueError('Unexpected or empty storage setting')
  env[key]=value
 if not env.get('HQIP_DATABASE_URL') or not env.get('HQIP_REDIS_URL'):raise ValueError('Both PostgreSQL and Redis configuration are required')
 return env

def allocation_migration_environment(manifest):
 target=manifest.get('subscriptionAllocationsVersion',0)
 if not target or not postgres_enabled():return None
 if target!=1:raise ValueError('Unsupported subscription allocation migration')
 if postgres_info().get('subscriptionAllocationsVersion',0)>=target:return None
 env=storage_environment(MIGRATION_ENV)
 app=urlsplit(storage_environment()['HQIP_DATABASE_URL'])
 migration=urlsplit(env['HQIP_DATABASE_URL'])
 if (app.hostname,app.port,app.path,app.query)!=(migration.hostname,migration.port,migration.path,migration.query) or migration.username!='hqip_migrator':
  raise ValueError('Migration role must target the active PostgreSQL database')
 return env

def migrate_allocations(release,env,state,info):
 if env is None:return
 # Record the attempt before starting: a timeout or lost response cannot prove
 # the transaction rolled back, so an old controller must not restart.
 info['allocationMigrationAttempted']=True
 info['allocationTarget']=1
 (state/'domains-state.json').write_text(json.dumps(info,indent=2))
 try:
  process=subprocess.run([str(release/'hqip'),'migrate-subscription-allocations'],env=env,capture_output=True,timeout=330,check=False)
 except subprocess.TimeoutExpired:
  raise RuntimeError('Subscription allocation migration result is uncertain; controller remains stopped') from None
 if process.returncode:raise RuntimeError('Subscription allocation migration failed; controller remains stopped')
 info['allocationMigrationCompleted']=True
 (state/'domains-state.json').write_text(json.dumps(info,indent=2))

def postgres_info():
 process=subprocess.run([str(control.BASE/'current/hqip'),'storage-info','--data-dir',str(control.DATA)],env=storage_environment(),capture_output=True,text=True,timeout=30,check=False)
 if process.returncode:raise ValueError('PostgreSQL deployment probe failed; live storage was not changed')
 value=json.loads(process.stdout)
 if value.get('backend')!='postgresql' or value.get('postgresSchemaVersion')!=1:raise ValueError('Unsupported PostgreSQL schema')
 return value

def schema_version():
 if postgres_enabled():return postgres_info()['schemaVersion']
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
 database,key=control.DATA/('postgresql.enabled' if postgres_enabled() else 'hqip.db'),control.DATA/'master.key'
 if any(p.is_symlink() or not p.is_file() for p in (database,key)):raise ValueError('Database and paired key required')
 schema_version()
 return hashlib.sha256(key.read_bytes()).hexdigest()

def disable_feishu():
 if postgres_enabled():return
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
 if postgres_enabled():
  if manifest.get('connectionSlotsVersion',0)<postgres_info().get('connectionSlotsVersion',0):raise ValueError('Connection slots require a compatible release; preserve live accounting and fix forward')
  if manifest.get('nodeQuotaVersion',0)<postgres_info().get('nodeQuotaVersion',0):raise ValueError('Node-owned quota requires a compatible release; preserve usage and fix forward')
  if manifest.get('subscriptionRenewalsVersion',0)<postgres_info().get('subscriptionRenewalsVersion',0):raise ValueError('Subscription periods require a renewal-compatible release; live usage is preserved')
  if manifest.get('trafficBillingVersion',0)<postgres_info().get('trafficBillingVersion',0):raise ValueError('Traffic billing ledger requires a compatible release; fix forward without repricing usage')
  if manifest.get('postgresSchemaVersion')!=1:raise ValueError('PostgreSQL data requires a PostgreSQL-compatible release; SQLite rollback refused')
  if manifest.get('offlineAdmissionVersion',0)<postgres_info().get('offlineAdmissionVersion',0):raise ValueError('Offline grants require a compatible release; reservations and unsettled usage are preserved')
  if manifest.get('vlessRealityVersion',0)<postgres_info().get('vlessRealityVersion',0):raise ValueError('Reality nodes require a compatible controller; fix forward without changing live usage')
  if manifest.get('serverBillingVersion',0)<2:raise ValueError('PostgreSQL requires serverBillingVersion 2')
  if manifest.get('subscriptionAllocationsVersion',0)<postgres_info().get('subscriptionAllocationsVersion',0):raise ValueError('Subscription allocations require a compatible release; node grants and reservations are preserved')
  if manifest.get('paymentsVersion',0)<1 and postgres_info().get('paymentsRequired',False):raise ValueError('Payment orders require a payment-compatible release; live orders are preserved')
  return
 with closing(sqlite3.connect((control.DATA/'hqip.db').resolve().as_uri()+'?mode=ro',uri=True)) as db:
  has_metadata=db.execute("SELECT 1 FROM sqlite_master WHERE name='metadata'").fetchone()
  slots_version=db.execute("SELECT value FROM metadata WHERE key='connection_slots_version'").fetchone() if has_metadata else None
  if slots_version and manifest.get('connectionSlotsVersion',0)<int(slots_version[0]):raise ValueError('Connection slots require a compatible release; preserve live accounting and fix forward')
  quota_version=db.execute("SELECT value FROM metadata WHERE key='node_quota_version'").fetchone() if has_metadata else None
  if quota_version and manifest.get('nodeQuotaVersion',0)<int(quota_version[0]):raise ValueError('Node-owned quota requires a compatible release; preserve usage and fix forward')
  renewal_version=db.execute("SELECT value FROM metadata WHERE key='subscription_renewals_version'").fetchone() if has_metadata else None
  if renewal_version and manifest.get('subscriptionRenewalsVersion',0)<int(renewal_version[0]):raise ValueError('Subscription periods require a renewal-compatible release; live usage is preserved')
  billing_version=db.execute("SELECT value FROM metadata WHERE key='traffic_billing_version'").fetchone() if has_metadata else None
  if billing_version and manifest.get('trafficBillingVersion',0)<int(billing_version[0]):raise ValueError('Traffic billing ledger requires a compatible release; live usage is preserved')
  reality_version=db.execute("SELECT value FROM metadata WHERE key='vless_reality_version'").fetchone() if has_metadata else None
  if reality_version and manifest.get('vlessRealityVersion',0)<int(reality_version[0]):raise ValueError('Reality nodes require a compatible controller; fix forward without changing live usage')
  offline_version=db.execute("SELECT value FROM metadata WHERE key='offline_admission_version'").fetchone() if has_metadata else None
  if offline_version and manifest.get('offlineAdmissionVersion',0)<int(offline_version[0]):raise ValueError('Offline grants require a compatible release; reservations and unsettled usage are preserved')
  allocation_version=db.execute("SELECT value FROM metadata WHERE key='subscription_allocations_version'").fetchone() if has_metadata else None
  if allocation_version and manifest.get('subscriptionAllocationsVersion',0)<int(allocation_version[0]):raise ValueError('Subscription allocations require a compatible release; node grants and reservations are preserved')
  payment_table=db.execute("SELECT 1 FROM sqlite_master WHERE name='payment_orders'").fetchone()
  if payment_table and manifest.get('paymentsVersion',0)<1:
   if db.execute('SELECT 1 FROM payment_orders LIMIT 1').fetchone() or db.execute('SELECT 1 FROM payment_settings WHERE enabled<>0 LIMIT 1').fetchone():raise ValueError('Payment orders require a payment-compatible release; live orders are preserved')
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
 if release==old or manifest.get('upgradeKind')!='full-release' or manifest.get('previousSourceSHA')!=previous['sourceSHA']:raise ValueError('Release is not based on active version')
 from release_bundle import verify_full_release
 verify_full_release(release,manifest)
 if not postgres_enabled():raise ValueError('Normal releases require the initialized PostgreSQL station; use the separate fresh deployment workflow')
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
 if info.get('renewalMigrationAttempted') and previous_manifest.get('subscriptionRenewalsVersion',0)<info.get('renewalTarget',0):
  raise ValueError('Subscription renewal migration prevents rollback to an older controller; fix forward with current data')
 if info.get('allocationMigrationAttempted') and previous_manifest.get('subscriptionAllocationsVersion',0)<info.get('allocationTarget',0):
  raise ValueError('Subscription allocation migration prevents rollback to an older controller; fix forward with current data')
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
 from commercial_protection import rollback as rollback_protection
 rollback_protection(state,control.run)
 control.run('nginx','-t');control.run('systemctl','daemon-reload');control.run('systemctl','start',control.SERVICE);control.healthy();control.run('systemctl','reload','nginx')
 info['phase']='rolled-back-data-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))

def upgrade(release,state,defer_protection=False):
 check_state(state);release,manifest,previous=verify(release)
 migration_env=allocation_migration_environment(manifest)
 from operations_deployment import prepare as prepare_operations, migrate as migrate_operations
 operations_env=prepare_operations(sys.modules[__name__],manifest)
 from commercial_protection import prepare_operation_logs, migrate_operation_logs, prepare as prepare_protection, install as install_protection, defer_origin
 logs_env=prepare_operation_logs(sys.modules[__name__],manifest)
 from offline_deployment import prepare as prepare_offline, migrate as migrate_offline
 offline_env=prepare_offline(sys.modules[__name__],manifest)
 from traffic_billing_deployment import prepare as prepare_billing, migrate as migrate_billing
 billing_env=prepare_billing(sys.modules[__name__],manifest)
 from subscription_renewal_deployment import prepare as prepare_renewals, migrate as migrate_renewals
 renewal_env=prepare_renewals(sys.modules[__name__],manifest)
 if SITE.is_symlink() or UNIT.is_symlink():raise ValueError('Unexpected configuration symlink')
 state.mkdir(mode=0o700,exist_ok=False)
 shutil.copyfile(SITE,state/'nginx.before.conf');shutil.copyfile(UNIT,state/'unit.before.service')
 info={'previous':str(previous),'release':str(release),'sourceSHA':manifest['sourceSHA'],'masterSHA256':data_identity(),'schemaBefore':schema_version(),'phase':'preparing'}
 if PREP.is_symlink():info['prepareTarget']=str(PREP.resolve(strict=True))
 elif PREP.exists():raise ValueError('Unexpected preparation site')
 (state/'domains-state.json').write_text(json.dumps(info,indent=2))
 protection=prepare_protection(release,manifest,state)
 origin_pending=defer_origin(release,manifest,state,SITE)
 # pg_dump takes a transactionally consistent online snapshot. Back up and
 # prepare additive schema while APIs still serve lease renewals; rollback
 # always keeps the live database rather than restoring this older snapshot.
 try:
  if any(p.is_symlink() for p in control.DATA.rglob('*')):raise ValueError('Symlink in live data')
  if postgres_enabled():
   process=subprocess.run([str(previous/'hqip'),'backup','--data-dir',str(control.DATA),'--output',str(state/'postgres.before.tar.gz')],env=storage_environment(),capture_output=True,timeout=1900,check=False)
   if process.returncode:raise RuntimeError('PostgreSQL backup failed; release activation refused')
   info['backupMode']='online-consistent-snapshot'
  for p in state.rglob('*'):p.chmod(0o700 if p.is_dir() else 0o600)
  require_free_space(release,state,control.DATA,Path('/etc/hqip-storage'),Path('/var/lib/docker'))
  migrate_offline(release,offline_env)
  migrate_billing(release,billing_env)
  migrate_operations(release,operations_env)
  if logs_env is not None:
   info['operationLogsMigrationAttempted']=True
   (state/'domains-state.json').write_text(json.dumps(info,indent=2))
  migrate_operation_logs(release,logs_env)
  if logs_env is not None:
   info['operationLogsMigrationCompleted']=True
   (state/'domains-state.json').write_text(json.dumps(info,indent=2))
  from fresh_deploy import install_log_limits
  install_log_limits(release)
 except Exception:
  info['phase']='preparation-failed-service-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
  raise
 info['phase']='prepared';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
 control.run('systemctl','stop',control.SERVICE)
 try:
  if data_identity()!=info['masterSHA256']:raise ValueError('Stopped data identity changed')
  if manifest.get('schemaVersion',4)==5:subscription_migration_ready()
  if not postgres_enabled():
   shutil.copytree(control.DATA,state/'data.before')
   for p in state.rglob('*'):p.chmod(0o700 if p.is_dir() else 0o600)
  disable_feishu()
  # Backups can be larger than the uploaded release. Check actual remaining
  # space after backup and before changing the active code or service files.
  require_free_space(release,state,control.DATA,Path('/etc/hqip-storage'),Path('/var/lib/docker'))
  migrate_renewals(release,renewal_env,state,info)
  migrate_allocations(release,migration_env,state,info)
  control.point_current(release)
  shutil.copyfile(release/'deploy/hqip-control.service',UNIT);UNIT.chmod(0o644)
  if not origin_pending:
   shutil.copyfile(release/'deploy/nginx-hqip.conf',SITE);SITE.chmod(0o644)
  if info.get('prepareTarget'):PREP.unlink()
  control.run('nginx','-t');control.run('systemctl','daemon-reload');control.run('systemctl','start',control.SERVICE);control.healthy();control.run('systemctl','reload','nginx')
  if protection:install_protection(release,control.run,control.DATA)
  if data_identity()!=info['masterSHA256']:raise ValueError('Live data identity changed')
 except Exception:
  if info.get('renewalMigrationAttempted'):
   info['phase']='failed-renewal-migration-data-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
   raise RuntimeError('Subscription renewal migration was attempted; refusing an incompatible controller rollback. Fix forward with this data.') from None
  if info.get('allocationMigrationAttempted'):
   info['phase']='failed-allocation-migration-data-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
   raise RuntimeError('Subscription allocation migration was attempted; refusing an incompatible controller rollback. Fix forward with this data.') from None
  if schema_version()>info['schemaBefore']:
   info['phase']='failed-schema-advanced-data-preserved';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
   raise RuntimeError('Schema migration completed; refusing incompatible old binary. Fix forward with this data or deploy a compatible v5 release.')
  rollback(state);raise
 info['phase']='active-awaiting-protection' if origin_pending else 'active';(state/'domains-state.json').write_text(json.dumps(info,indent=2))
 if protection and not defer_protection:complete_protection(state)
 print(json.dumps({'backup':str(state),'sourceSHA':manifest['sourceSHA'],'phase':json.loads((state/'domains-state.json').read_text())['phase']}))


def complete_protection(state):
 check_state(state,True)
 info=json.loads((state/'domains-state.json').read_text())
 release=Path(info['release'])
 if (control.BASE/'current').resolve(strict=True)!=release or info['phase'] not in ('active','active-awaiting-protection'):
  raise ValueError('Active release changed; protection acceptance refused')
 control.verify_release(release)
 from commercial_protection import complete_release
 complete_release(release,state,control.run,site=SITE,base=control.BASE,data=control.DATA)
 info['phase']='active';(state/'domains-state.json').write_text(json.dumps(info,indent=2))

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['verify','upgrade','rollback','complete']);p.add_argument('--release',type=Path);p.add_argument('--state',type=Path);p.add_argument('--defer-protection',action='store_true');a=p.parse_args()
 if a.action=='verify':print(verify(a.release)[1]['sourceSHA'])
 else:
  if os.geteuid()!=0:raise PermissionError('Root required')
  if a.action=='upgrade':
   if a.defer_protection:upgrade(a.release,a.state,True)
   else:
    from automatic_backup import deployment_lock
    with deployment_lock(control.BASE):upgrade(a.release,a.state,True)
    complete_protection(a.state)
  elif a.action=='complete':complete_protection(a.state)
  else:
   from automatic_backup import deployment_lock
   with deployment_lock(control.BASE):rollback(a.state)
