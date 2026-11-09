"""Private, durable deployment checkpoint and exact-instance rollback."""
import base64, json, os, pathlib, re, shutil, sqlite3, subprocess, sys, tarfile, tempfile

a=json.loads(base64.b64decode(sys.argv[1])); node=a['nodeId']
if not re.fullmatch(r'[A-Za-z0-9_-]{1,80}',node): raise SystemExit('invalid-node')
backup=pathlib.Path(a['backup'])
if backup.parent != pathlib.Path('/var/backups/hqip-nodes') or not backup.name.startswith(node+'-'): raise SystemExit('invalid-backup')
config=pathlib.Path('/etc/hqip-nodes')/node; state=pathlib.Path('/var/lib/hqip-nodes')/node
ownunits=['hqip-agent-'+node+'.service','hqip-hysteria-'+node+'.service']
def command(*args):
    return subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=30)
def states(units):
    return [{'unit':u,'active':command('systemctl','is-active','--quiet',u).returncode==0,'enabled':command('systemctl','is-enabled','--quiet',u).returncode==0} for u in units]
def syncfile(p):
    with open(p,'rb') as f: os.fsync(f.fileno())
def private_archive(path,paths):
    snapshots=[]
    def stable_database(member):
        if member.name.endswith(('.db-wal','.db-shm')):
            return None
        if member.isfile() and member.name.endswith('.db'):
            fd,snapshot=tempfile.mkstemp(prefix='.sqlite-',dir=backup);os.close(fd)
            source=sqlite3.connect('file:/'+member.name+'?mode=ro',uri=True,timeout=30)
            target=sqlite3.connect(snapshot)
            try: source.backup(target)
            finally: target.close();source.close()
            snapshots.append((snapshot,member.name));return None
        return member
    with tarfile.open(path,'w:gz',dereference=False) as tar:
        for p in sorted(set(str(pathlib.Path(p)) for p in paths)):
            if os.path.lexists(p): tar.add(p,arcname=p.lstrip('/'),recursive=True,filter=stable_database)
        for snapshot,name in snapshots:
            tar.add(snapshot,arcname=name);os.unlink(snapshot)
    os.chmod(path,0o600);syncfile(path)
def restore_states(items):
    for item in items:
        command('systemctl','enable' if item['enabled'] else 'disable',item['unit'])
        command('systemctl','start' if item['active'] else 'stop',item['unit'])

if a['action']=='backup':
    backup.mkdir(mode=0o700,parents=True,exist_ok=False);os.chmod(backup.parent,0o700)
    legacy=[]; oldpaths=[]
    for candidate in a.get('candidates',[]):
        unit=candidate['unit']
        if candidate['kind']=='hqip-legacy':
            legacy.extend(['hqip-agent.service',unit]);oldpaths.extend(['/etc/hqip-node','/opt/hqip-node/agent','/opt/hqip-node/hysteria','/var/lib/hqip-agent','/var/lib/hqip-hysteria','/etc/systemd/system/hqip-agent.service','/etc/systemd/system/hqip-hysteria.service'])
        elif candidate['kind']=='native-hy2':
            legacy.append(unit);oldpaths.extend(candidate.get('files',{}).keys())
        else: raise SystemExit('unsupported-takeover')
    manifest={'ownExisted':config.exists(),'ownStates':states(ownunits),'legacyStates':states(legacy)}
    private_archive(backup/'own.tar.gz',[config,state]+[pathlib.Path('/etc/systemd/system')/u for u in ownunits])
    private_archive(backup/'legacy.tar.gz',oldpaths)
    (backup/'manifest.json').write_text(json.dumps(manifest));os.chmod(backup/'manifest.json',0o600);syncfile(backup/'manifest.json')
    directory=os.open(backup,os.O_DIRECTORY);os.fsync(directory);os.close(directory)
    print(str(backup))
elif a['action']=='stop':
    manifest=json.loads((backup/'manifest.json').read_text())
    for item in manifest['legacyStates']+manifest['ownStates']:
        # Ignore absent newly allocated units; stop/disable every existing one.
        if command('systemctl','show','--property=LoadState','--value',item['unit']).stdout.strip()!=b'loaded': continue
        if command('systemctl','stop',item['unit']).returncode: raise SystemExit('stop-failed')
        command('systemctl','disable',item['unit'])
elif a['action']=='rollback':
    manifest=json.loads((backup/'manifest.json').read_text())
    for unit in ownunits:
        command('systemctl','stop',unit);command('systemctl','disable',unit)
    if manifest['ownExisted']:
        with tarfile.open(backup/'own.tar.gz','r:gz') as tar:
            allowed={str(config).lstrip('/'),str(state).lstrip('/')}|{'etc/systemd/system/'+u for u in ownunits}
            members=tar.getmembers()
            for member in members:
                if not any(member.name==p or member.name.startswith(p+'/') for p in allowed) or '..' in pathlib.PurePosixPath(member.name).parts: raise SystemExit('invalid-archive')
            # A code/configuration rollback must never rewind the live ledger,
            # its WAL, pending final usage or certificates written after backup.
            state_prefix=str(state).lstrip('/')
            members=[m for m in members if m.name!=state_prefix and not m.name.startswith(state_prefix+'/')]
            if config.is_symlink(): config.unlink()
            elif config.exists(): shutil.rmtree(config)
            for unit in ownunits:
                (pathlib.Path('/etc/systemd/system')/unit).unlink(missing_ok=True)
            tar.extractall('/',members=members,filter='fully_trusted') if sys.version_info >= (3,12) else tar.extractall('/',members=members)
    command('systemctl','daemon-reload')
    restore_states(manifest['ownStates'] if manifest['ownExisted'] else [])
    restore_states(manifest['legacyStates'])
else: raise SystemExit('invalid-action')
