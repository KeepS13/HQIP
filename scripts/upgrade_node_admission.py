"""Upgrade one owned HQIP node to the admission core; leave other IP nodes alone.

Run on a node as root with a verified controller artifacts directory. Backups
contain private configuration and are retained under /root/backups. No passwords
or tokens are printed. Run before activating the new account system.
"""
from __future__ import annotations
from contextlib import closing
import argparse, hashlib, json, os, pathlib, platform, re, shutil, sqlite3, subprocess, tarfile, time, urllib.request
from urllib.parse import urlsplit

CONFIG_ROOT = pathlib.Path('/etc/hqip-nodes')
STATE_ROOT = pathlib.Path('/var/lib/hqip-nodes')
RELEASE_ROOT = pathlib.Path('/opt/hqip-node/releases')
UNIT_ROOT = pathlib.Path('/etc/systemd/system')
BACKUP_ROOT = pathlib.Path('/root/backups')

def node_units(node):
    if not isinstance(node,str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,64}',node):raise ValueError('Invalid node ID')
    return {kind:f'hqip-{kind}-{node}.service' for kind in ['agent','hysteria']}

def regular(path):
    if path.is_symlink() or not path.is_file():raise ValueError('Expected an ordinary file: '+str(path))
    return path

def local_url(value):
    url=urlsplit(value)
    if url.scheme!='http' or url.hostname!='127.0.0.1' or not url.port or url.username or url.password or url.query or url.fragment or url.path not in ('','/'):
        raise ValueError('Expected a loopback Agent/core URL')
    return value.rstrip('/')

def verify_units(node,config,units,check_effective=True):
    for kind,unit in units.items():
        text=regular(UNIT_ROOT/unit).read_text()
        description=f'Description=HQIP node agent {node}' if kind=='agent' else f'Description=HQIP Hysteria node {node}'
        argument=str(config/('agent.json' if kind=='agent' else 'hysteria.yaml'))
        if description not in text.splitlines() or '--config '+argument not in text:raise ValueError('Service does not belong to this node')
        if check_effective:
            effective=command('systemctl','show','--value','-p','ExecStart',unit).stdout
            if '--config '+argument not in effective:raise ValueError('Effective service configuration points outside this node')

def digest(path):
    with path.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()

def command(*args,check=True):
    return subprocess.run(args,check=check,capture_output=True,text=True)

def owned_paths(node):
    node_units(node)
    config=CONFIG_ROOT/node
    state=STATE_ROOT/node
    if any(not p.is_dir() or p.is_symlink() or p.resolve()!=p for p in [config,state]):raise ValueError('Node directories must be real absolute directories')
    cfg=json.loads(regular(config/'agent.json').read_text())
    if cfg.get('nodeId')!=node or cfg.get('nodeDir')!=str(config) or not cfg.get('managed') or not cfg.get('controllerId'):
        raise ValueError('Node installation ownership does not match')
    local_url('http://'+cfg['listen']);local_url(cfg['statsUrl'])
    return config,state,cfg

def upgrade(node,artifacts):
    config,state,cfg=owned_paths(node)
    units=node_units(node)
    verify_units(node,config,units)
    original={kind:command('systemctl','is-active',unit,check=False).stdout.strip() for kind,unit in units.items()}
    if any(value!='active' for value in original.values()):raise ValueError('Both owned node services must be active before upgrade')
    arch={'x86_64':'amd64','aarch64':'arm64'}.get(platform.machine())
    if not arch:raise ValueError('Unsupported node architecture')
    manifest=json.loads(regular(artifacts/'manifest.json').read_text())
    if manifest.get('hqipAdmissionVersion')!=1:raise ValueError('Artifacts do not support admission')
    binaries={}
    for kind in ['agent','hysteria']:
        source=regular(artifacts/f'{kind}-linux-{arch}')
        expected=manifest['sha256'].get(source.name)
        if not expected or source.is_symlink() or digest(source)!=expected:raise ValueError('Artifact hash mismatch: '+kind)
        target=RELEASE_ROOT/expected/kind
        if target.is_symlink() or target.parent.is_symlink() or target.resolve()!=target:raise ValueError('Unsafe immutable release path')
        target.parent.mkdir(parents=True,exist_ok=True)
        if target.exists() and digest(target)!=expected:raise ValueError('Immutable release collision')
        if not target.exists():shutil.copyfile(source,target)
        if digest(target)!=expected:raise ValueError('Copied artifact checksum mismatch')
        target.chmod(0o755);binaries[kind]=target
    if BACKUP_ROOT.is_symlink() or BACKUP_ROOT.resolve()!=BACKUP_ROOT:raise ValueError('Unsafe backup root')
    backup=BACKUP_ROOT/f'hqip-node-admission-{node}-{time.time_ns()}'
    backup.mkdir(mode=0o700,parents=True)
    yaml=regular(config/'hysteria.yaml');old=yaml.read_bytes()
    overrides={}
    for kind,unit in units.items():
        path=UNIT_ROOT/(unit+'.d')/'70-hqip-admission.conf'
        if path.is_symlink() or path.parent.is_symlink():raise ValueError('Unsafe service override path')
        overrides[kind]={'path':str(path),'before':path.read_text() if path.exists() else None}
    info={'nodeId':node,'units':units,'originalStates':original,'overrides':overrides,'phase':'prepared'}
    (backup/'state.json').write_text(json.dumps(info,indent=2)+'\n');(backup/'state.json').chmod(0o600)
    (backup/'hysteria.before.yaml').write_bytes(old);(backup/'hysteria.before.yaml').chmod(0o600)
    try:
        command('systemctl','stop',units['agent'],units['hysteria'])
        with tarfile.open(backup/'node.before.tar.gz','w:gz') as tar:
            for path in [config,state]:tar.add(path,arcname=str(path).lstrip('/'),recursive=True)
        (backup/'node.before.tar.gz').chmod(0o600)
        local=local_url('http://'+cfg['listen'])
        block='hqipAdmission:\n  url: '+json.dumps(local)+'\n  secret: '+json.dumps(cfg['statsSecret'])+'\n'
        text=old.decode()
        if re.search(r'^hqipAdmission:',text,re.M):text=re.sub(r'^hqipAdmission:.*?(?=^\S|\Z)',lambda _:block,text,flags=re.M|re.S)
        else:text=block+text
        yaml.write_text(text);yaml.chmod(0o600)
        for kind,item in overrides.items():
            path=pathlib.Path(item['path']);path.parent.mkdir(exist_ok=True)
            arguments=f'--config {config / "agent.json"}' if kind=='agent' else f'server --config {yaml} --log-level warn'
            path.write_text('[Service]\nExecStart=\nExecStart='+str(binaries[kind])+' '+arguments+'\n');path.chmod(0o644)
        command('systemctl','daemon-reload');command('systemctl','start',units['agent'],units['hysteria'])
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
        for attempt in range(30):
            try:
                request=urllib.request.Request(local_url(cfg['statsUrl'])+'/hqip/capabilities',headers={'Authorization':cfg['statsSecret']})
                with opener.open(request,timeout=2) as response:capability=json.load(response)
                if isinstance(capability,dict) and capability.get('admission') is True:break
            except Exception:
                if attempt==29:raise RuntimeError('New node core did not prove admission capability')
            if attempt<29:time.sleep(1)
        else:
            raise RuntimeError('New node core did not prove admission capability')
        for unit in units.values():
            if command('systemctl','is-active',unit).stdout.strip()!='active':raise RuntimeError('Node service failed')
        info['phase']='active';(backup/'state.json').write_text(json.dumps(info,indent=2)+'\n')
    except Exception:
        rollback(backup);raise
    print(json.dumps({'nodeId':node,'backup':str(backup),'admissionVersion':1}))

def rollback(backup):
    if backup.is_symlink() or not backup.is_dir() or backup.resolve().parent!=BACKUP_ROOT:raise ValueError('Unsafe backup path')
    info=json.loads(regular(backup/'state.json').read_text());config,state,cfg=owned_paths(info['nodeId'])
    units=node_units(info['nodeId'])
    if info.get('units')!=units or set(info.get('overrides',{}))!={'agent','hysteria'}:raise ValueError('Rollback service ownership does not match')
    if info.get('originalStates')!={'agent':'active','hysteria':'active'}:raise ValueError('Missing original active service states')
    # A failed/truncated new override must not block restoring the owned base unit.
    verify_units(info['nodeId'],config,units,check_effective=False)
    old=regular(backup/'hysteria.before.yaml').read_bytes()
    yaml=regular(config/'hysteria.yaml')
    # Validate every destination before issuing a stop or modifying any file.
    for kind,item in info['overrides'].items():
        expected=UNIT_ROOT/(units[kind]+'.d')/'70-hqip-admission.conf'
        if str(expected)!=item.get('path') or expected.is_symlink() or expected.parent.is_symlink() or item.get('before') is not None and not isinstance(item['before'],str):raise ValueError('Invalid rollback override')
    command('systemctl','stop',*units.values())
    # A stopped node must age out of activation's 30-second healthy-heartbeat
    # gate before the final controller check, otherwise activation can race us.
    time.sleep(31)
    assert_pre_activation(state,cfg)
    yaml.write_bytes(old);yaml.chmod(0o600)
    for item in info['overrides'].values():
        expected=pathlib.Path(item['path'])
        if item['before'] is None:expected.unlink(missing_ok=True)
        else:expected.write_text(item['before'])
    command('systemctl','daemon-reload');command('systemctl','start',*info['units'].values())
    for unit in units.values():
        if command('systemctl','is-active',unit).stdout.strip()!='active':raise RuntimeError('Original node service did not recover')
    info['phase']='rolled-back';(backup/'state.json').write_text(json.dumps(info,indent=2)+'\n')


def assert_pre_activation(state,cfg):
    message=('Unsafe node rollback refused; both owned services remain stopped and current ledger/configuration are preserved. '
             'Account activation or unknown controller state requires coordinated manual recovery; never restore an old ledger.')
    try:
        database=pathlib.Path(cfg['statePath'])
        if not database.is_absolute() or database.resolve().parent!=state or database.is_symlink():raise ValueError('Invalid ledger location')
        regular(database)
        with closing(sqlite3.connect(database.as_uri()+'?mode=ro',uri=True,timeout=5)) as db:
            row=db.execute("SELECT value FROM meta WHERE key='credentials'").fetchone()
        if row is None:raise ValueError('No cached controller state')
        cached=json.loads(row[0])
        if not isinstance(cached,dict) or not isinstance(cached.get('portalEnabled',False),bool):raise ValueError('Invalid cached activation state')
        if cached.get('portalEnabled',False):raise ValueError('Account system activated in current ledger')
        url=urlsplit(cfg['controlUrl'])
        if url.scheme not in ('https','http') or not url.hostname or url.username or url.password or url.query or url.fragment:raise ValueError('Invalid controller URL')
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self,req,fp,code,msg,headers,newurl):return None
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
        request=urllib.request.Request(cfg['controlUrl'].rstrip('/')+'/api/v1/agents/config',headers={'Authorization':'Bearer '+cfg['token']})
        with opener.open(request,timeout=4) as response:desired=json.loads(response.read(4*1024*1024+1))
        if not isinstance(desired,dict) or not isinstance(desired.get('portalEnabled',False),bool) or not isinstance(desired.get('devices'),list) or type(desired.get('revision')) is not int:raise ValueError('Invalid controller state')
        if desired.get('portalEnabled',False):raise ValueError('Account system activated at controller')
    except (OSError,KeyError,ValueError,sqlite3.Error) as error:
        raise RuntimeError(message) from error

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action',choices=['upgrade','rollback'])
    parser.add_argument('--node-id');parser.add_argument('--artifacts',type=pathlib.Path);parser.add_argument('--backup',type=pathlib.Path)
    args=parser.parse_args()
    if os.geteuid()!=0:raise PermissionError('Root required')
    if args.action=='upgrade':upgrade(args.node_id,args.artifacts)
    else:rollback(args.backup)

if __name__=='__main__':main()
