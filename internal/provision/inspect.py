"""Read-only host inspection. No credential/config contents leave the host."""
import base64, hashlib, ipaddress, json, os, pathlib, re, shutil, subprocess, sys

args = json.loads(base64.b64decode(sys.argv[1]))
def command(*argv):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=15)
    return p.stdout.decode(errors='replace').strip()
def read(path):
    try:
        return pathlib.Path(path).read_text()
    except (OSError, UnicodeError):
        return ''
def digest(path):
    try:
        return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
    except OSError:
        return ''
def properties(unit):
    out = command('systemctl', 'show', unit, '--property=Id,LoadState,ActiveState,UnitFileState,FragmentPath,ExecStart,MainPID,DropInPaths')
    return dict(line.split('=', 1) for line in out.splitlines() if '=' in line)
def process_unit(pid):
    for line in read('/proc/' + str(pid) + '/cgroup').splitlines():
        units = re.findall(r'(?:^|/)([^/]+\.service)(?:/|$)', line)
        if units:
            return units[-1]
    return ''

if os.geteuid() != 0:
    raise SystemExit('root-required')
for name in ('systemctl', 'ss', 'ip', 'tar', 'flock', 'curl'):
    if not shutil.which(name):
        raise SystemExit('missing-tool-' + name)
osrel = {}
for line in read('/etc/os-release').splitlines():
    if '=' in line:
        k,v = line.split('=',1); osrel[k] = v.strip('"')
interfaces = json.loads(command('ip', '-j', '-4', 'address', 'show'))
addresses = sorted(set(a['local'] for i in interfaces for a in i.get('addr_info',[]) if a.get('family') == 'inet'))
sockets = []
for line in command('ss', '-H', '-ltnup').splitlines():
    fields = line.split()
    if len(fields) < 6:
        continue
    proto, local = fields[0], fields[4]
    if ':' not in local:
        continue
    host, port = local.rsplit(':', 1)
    if not port.isdigit():
        continue
    pids = [int(x) for x in re.findall(r'pid=(\d+)', line)]
    units = sorted(set(process_unit(pid) for pid in pids))
    sockets.append({'protocol': proto, 'host': host.strip('[]'), 'port': int(port), 'units': units})
nodeid = args.get('nodeId','')
configdir = '/etc/hqip-nodes/' + nodeid if nodeid else ''
owner = None
if configdir and pathlib.Path(configdir + '/owner.json').exists():
    try: owner = json.loads(read(configdir + '/owner.json'))
    except ValueError: owner = {'invalid': True}
legacyowner = read('/etc/hqip-node/owner').strip()
units = set()
units.update(('hysteria.service','hysteria-server.service','hy2.service'))
if (legacyowner or pathlib.Path('/etc/hqip-node/agent.json').exists()) and not pathlib.Path('/etc/hqip-node/retired-by').exists():
    units.add('hqip-hysteria.service')
target = args.get('publicIp','')
targetport = args.get('port',443)
for socket in sockets:
    if socket['protocol'] == 'udp' and socket['port'] == targetport and socket['host'] in (target,'0.0.0.0','::','*'):
        units.update(u for u in socket['units'] if u)
if nodeid:
    units.add('hqip-hysteria-' + nodeid + '.service')
    units.add('hqip-agent-' + nodeid + '.service')
resultunits = []
for unit in sorted(units):
    p = properties(unit)
    if p.get('LoadState') != 'loaded':
        continue
    exe = re.search(r'path=([^ ;]+)',p.get('ExecStart',''))
    executable = exe.group(1) if exe else ''
    config = re.search(r'(?:--config(?:=|\s+)|(?:^|\s)-c\s+)(/[^ ;"\']+)', p.get('ExecStart',''))
    configpath = config.group(1) if config else ''
    unittext = command('systemctl','cat',unit)
    tracked = [p.get('FragmentPath',''), configpath, executable]
    # Include referenced standalone TLS files in the snapshot/backup without
    # returning the private key (or any config text) to the controller.
    for line in read(configpath).splitlines():
        match = re.match(r'\s*(?:cert|key):\s*["\']?(/[^"\'\r\n]+)', line)
        if match: tracked.append(match.group(1).strip())
    tracked.extend(p.get('DropInPaths','').split())
    files = {path: digest(path) for path in tracked if path.startswith('/') and pathlib.Path(path).is_file()}
    executable_ok = pathlib.Path(executable).name.startswith('hysteria') and 'h-ui' not in executable.lower() and not executable.startswith('/var/lib/docker/')
    legacy = unit == 'hqip-hysteria.service' and configpath == '/etc/hqip-node/hysteria.yaml' and bool(legacyowner)
    native = (unit in ('hysteria.service','hysteria-server.service','hy2.service') and executable_ok and configpath.startswith('/etc/') and ';' not in configpath)
    if native:
        listen = re.search(r'^listen:\s*["\']?([^\s"\'#]+)',read(configpath),re.MULTILINE)
        endpoint = listen.group(1) if listen else ':443'
        try: listenhost,listenport=endpoint.rsplit(':',1)
        except ValueError: listenhost,listenport='', '443'
        if target and (listenport!=str(targetport) or listenhost not in ('',target,'0.0.0.0','::','[::]','*')):
            continue
    own = nodeid and unit in ('hqip-hysteria-'+nodeid+'.service','hqip-agent-'+nodeid+'.service')
    resultunits.append({'unit':unit,'configPath':configpath,'binaryPath':executable,'active':p.get('ActiveState')=='active','enabled':p.get('UnitFileState') in ('enabled','enabled-runtime'),'kind':'hqip-legacy' if legacy else ('native-hy2' if native else ('hqip-instance' if own else 'unsupported')),'unitHash':hashlib.sha256(unittext.encode()).hexdigest(),'files':files})
result = {'machineId':read('/etc/machine-id').strip(),'platform':osrel.get('ID','')+' '+osrel.get('VERSION_ID',''),'architecture':os.uname().machine,'boundIps':addresses,'freeBytes':shutil.disk_usage('/').free,'sockets':sorted(sockets,key=lambda x:(x['protocol'],x['host'],x['port'])),'units':resultunits,'owner':owner,'legacyOwner':legacyowner}
if legacyowner:
    result['legacyHashes'] = {path:digest(path) for path in ('/etc/hqip-node/owner','/etc/hqip-node/agent.json','/etc/hqip-node/hysteria.yaml')}
print(json.dumps(result,sort_keys=True,separators=(',',':')))
