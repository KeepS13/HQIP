"""Only add/delete exact HQIP-owned UFW allowances; never change policy."""
import base64,json,pathlib,re,shlex,shutil,subprocess,sys

a=json.loads(base64.b64decode(sys.argv[1]));node=a['nodeId']
if not re.fullmatch(r'[A-Za-z0-9_-]{1,80}',node): raise SystemExit('invalid-node')
marker='hqip:'+node
config=pathlib.Path('/etc/hqip-nodes')/node
manifest=config/'firewall.json'
def command(*args):
    return subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,timeout=25)
def rules():
    result=command('ufw','show','added')
    if result.returncode: raise RuntimeError('cannot-read-ufw-rules')
    found=[]
    for line in result.stdout.splitlines():
        try: parts=shlex.split(line)
        except ValueError: continue
        if parts[:2]!=['ufw','allow']: continue
        values={}
        for key in ('to','port','proto','comment'):
            if key in parts:
                i=parts.index(key)
                if i+1<len(parts): values[key]=parts[i+1]
        if all(k in values for k in ('to','port','proto')):
            found.append(values)
    return found
def spec_equal(rule,spec):
    return rule.get('to')==spec['ip'] and rule.get('port')==str(spec['port']) and rule.get('proto')==spec['proto']
def save(owned):
    if config.exists():
        tmp=manifest.with_suffix('.json.new');tmp.write_text(json.dumps(owned));tmp.chmod(0o600);tmp.replace(manifest)

if not shutil.which('ufw'):
    print('[]');raise SystemExit(0)
try: owned=json.loads(manifest.read_text())
except FileNotFoundError: owned=[]
existing=rules()
if a['action']=='inspect':
    # Used before a durable upgrade mutation. Any matching existing rule,
    # regardless of owner, must be excluded from rollback's deletion intent.
    print(json.dumps([spec for spec in a['rules'] if any(spec_equal(r,spec) for r in existing)]))
elif a['action']=='apply':
    if not command('ufw','status').stdout.startswith('Status: active'):
        print(json.dumps({'added':[],'error':False}));raise SystemExit(0)
    added=[]
    try:
        for spec in a['rules']:
            matches=[r for r in existing if spec_equal(r,spec)]
            if matches:
                if any(r.get('comment')==marker for r in matches) and spec not in owned:
                    owned.append(spec);save(owned)
                continue
            result=command('ufw','allow','to',spec['ip'],'port',str(spec['port']),'proto',spec['proto'],'comment',marker)
            if result.returncode: raise RuntimeError('ufw-add-failed')
            added.append(spec)
            if spec not in owned: owned.append(spec)
            save(owned)
    except Exception:
        print(json.dumps({'added':added,'error':True}));raise SystemExit(0)
    print(json.dumps({'added':added,'error':False}))
elif a['action']=='cleanup':
    selected=a.get('rules',owned)
    for spec in selected:
        matches=[r for r in existing if spec_equal(r,spec)]
        # A modified comment/rule is not ours to delete.
        if not any(r.get('comment')==marker for r in matches): continue
        result=command('ufw','--force','delete','allow','to',spec['ip'],'port',str(spec['port']),'proto',spec['proto'],'comment',marker)
        if result.returncode: raise RuntimeError('ufw-delete-failed')
        owned=[r for r in owned if r!=spec]
        save(owned)
    print('[]')
else: raise SystemExit('invalid-action')
