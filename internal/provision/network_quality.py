"""Run the bundled diagnostic as an unprivileged transient systemd unit.

payload_b64 is supplied by HQIP, never by an API caller. Only the validated
node address and embedded, fixed source files are accepted. No credentials
are passed to the diagnostic process.
"""
import base64,ipaddress,json,os,pathlib,pwd,re,shutil,subprocess,tempfile,time

def diagnostic_source(target):
    """Select an owned source address and prove its NAT exit without changing networking.

    Direct hosts retain the old path. NAT adds one bounded HTTPS query; the
    unprivileged script independently checks the exit again before measuring.
    A route guess or another public exit must never become the node's report.
    """
    try:
        result=subprocess.run(['ip','-j','-4','addr','show'],capture_output=True,timeout=10)
        if result.returncode:return ''
        addresses={a.get('local') for iface in json.loads(result.stdout) for a in iface.get('addr_info',[])}
        if target in addresses:return target
        route=subprocess.run(['ip','-j','-4','route','get','1.1.1.1'],capture_output=True,timeout=10)
        if route.returncode:return ''
        routes=json.loads(route.stdout)
        source=routes[0].get('prefsrc',routes[0].get('src',''))
        ip=ipaddress.IPv4Address(source)
        if source not in addresses or ip.is_unspecified or ip.is_loopback or ip.is_multicast or ip.is_link_local:return ''
        observed=subprocess.run(['curl','-4','--noproxy','*','--fail','--silent','--connect-timeout','3','--max-time','8','--interface',source,'https://api.ipify.org'],capture_output=True,text=True,timeout=10)
        if observed.returncode or str(ipaddress.IPv4Address(observed.stdout.strip()))!=target:return ''
        return source
    except (ValueError,TypeError,OSError,IndexError,KeyError,subprocess.TimeoutExpired):
        return ''

def run(payload):
    target=str(ipaddress.IPv4Address(payload['ip']))
    if not re.fullmatch('[a-f0-9]{24}',payload['runId']):return {'error':'sandbox'}
    # Use only fixed OS packages, no downloaded installer and no OS upgrade.
    packages=[package for command,package in [('jq','jq'),('bc','bc'),('dig','dnsutils')] if not shutil.which(command)]
    if packages and shutil.which('apt-get'):
        try:subprocess.run(['apt-get','-y','--no-install-recommends','-o','DPkg::Lock::Timeout=15','install',*packages],env=dict(os.environ,DEBIAN_FRONTEND='noninteractive'),stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=60)
        except subprocess.TimeoutExpired:return {'error':'dependencies'}
    needed=['bash','curl','jq','bc','dig','ip','timeout','systemd-run','systemctl','awk','sed','grep','head','cut','tr','sort','xargs','date','sleep','dirname','cat','gzip','gunzip']
    if any(not shutil.which(name) for name in needed):return {'error':'dependencies'}
    try:user=pwd.getpwnam('nobody')
    except KeyError:return {'error':'platform'}
    source=diagnostic_source(target)
    if not source:return {'error':'source'}
    root=pathlib.Path('/var/lib/hqip-quality-tmp')
    if root.is_symlink():return {'error':'sandbox'}
    root.mkdir(mode=0o755,exist_ok=True)
    work=pathlib.Path(tempfile.mkdtemp(prefix='scan-',dir=root));work.chmod(0o755)
    output=work/'output';output.mkdir(mode=0o700);os.chown(output,user.pw_uid,user.pw_gid)
    unit='hqip-quality-'+payload['runId']
    try:
        for name,encoded in payload['files'].items():
            namepath=pathlib.PurePosixPath(name)
            if namepath.is_absolute() or '..' in namepath.parts or not re.fullmatch(r'[A-Za-z0-9_./-]+',name):return {'error':'sandbox'}
            destination=work/name;destination.parent.mkdir(mode=0o755,parents=True,exist_ok=True)
            destination.write_bytes(base64.b64decode(encoded,validate=True));destination.chmod(0o444)
        report=output/'report.json'
        report_ansi=output/'report.ansi'
        properties=['User='+str(user.pw_uid),'Group='+str(user.pw_gid),'NoNewPrivileges=yes','ProtectSystem=strict','ProtectHome=yes','PrivateTmp=yes','PrivateDevices=yes','ProtectKernelTunables=yes','ProtectControlGroups=yes','RestrictSUIDSGID=yes','CapabilityBoundingSet=','ReadWritePaths='+str(output),'MemoryMax=192M','TasksMax=80','CPUQuota=50%','LimitFSIZE=1048576','LimitNOFILE=256','RuntimeMaxSec=300','TimeoutStopSec=5','KillMode=control-group','Nice=10','UMask=0077','StandardOutput=null','StandardError=null']
        command=['systemd-run','--quiet','--unit='+unit,'--service-type=exec']
        for prop in properties:command+=['--property='+prop]
        command+=['/usr/bin/env','-i','PATH=/usr/bin:/bin','HOME='+str(output),'LANG=C.UTF-8','TERM=dumb','HQIP_EXPECTED_IP='+target,'HQIP_REPORT_ANSI='+str(report_ansi),'bash',str(work/'ip.sh'),'-i',source,'-4','-n','-p','-f','-j','-o',str(report)]
        launched=subprocess.run(command,capture_output=True,timeout=20)
        if launched.returncode:return {'error':'sandbox'}
        deadline=time.monotonic()+315
        while time.monotonic()<deadline:
            state=subprocess.run(['systemctl','show',unit,'--property=ActiveState','--value'],capture_output=True,text=True,timeout=10).stdout.strip()
            if state not in ('active','activating','deactivating'):break
            time.sleep(1)
        else:return {'error':'timeout'}
        result=subprocess.run(['systemctl','show',unit,'--property=Result','--value'],capture_output=True,text=True,timeout=10).stdout.strip()
        if result in ('timeout','signal','resources','oom-kill'):return {'error':'timeout' if result=='timeout' else 'execution'}
        if result!='success' or not report.is_file() or report.is_symlink():return {'error':'execution'}
        if report.stat().st_size>192*1024:return {'error':'invalid'}
        try:doc=json.loads(report.read_text())
        except (ValueError,UnicodeError):return {'error':'invalid'}
        if not isinstance(doc,dict) or not isinstance(doc.get('Head'),dict) or doc['Head'].get('IP')!=target:return {'error':'invalid'}
        answer={'report':doc}
        # Image output is optional; a missing/invalid image cannot discard valid
        # measurement data. The controller can render the factual JSON instead.
        if report_ansi.is_file() and not report_ansi.is_symlink() and report_ansi.stat().st_size<=64*1024:
            try:answer['ansi']=report_ansi.read_text(encoding='utf-8')
            except (OSError,UnicodeError):pass
        return answer
    finally:
        try:
            subprocess.run(['systemctl','stop',unit],capture_output=True,timeout=15)
        finally:
            try:subprocess.run(['systemctl','reset-failed',unit],capture_output=True,timeout=10)
            finally:shutil.rmtree(work)

if __name__=='__main__':
    try:answer=run(json.loads(base64.b64decode(payload_b64)))
    except Exception:answer={'error':'execution'}
    print(json.dumps(answer,ensure_ascii=True))
