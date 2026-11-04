"""Change only one owned Agent controller URL; preserve its core and ledger."""
from __future__ import annotations
import argparse,json,os,shutil,sqlite3,sys,time,urllib.request
from pathlib import Path
sys.dont_write_bytecode=True
import upgrade_node_admission as node_ops

OLD='https://hqip.5202345.xyz'
NEW='https://api-hqip.5202345.xyz'

def write_config(path,value):
 temporary=path.with_suffix('.domain-new')
 with temporary.open('x') as f:json.dump(value,f,indent=2)
 temporary.chmod(0o600);temporary.replace(path)

def migrate(node):
 config,state,cfg=node_ops.owned_paths(node)
 units=node_ops.node_units(node);node_ops.verify_units(node,config,units)
 if cfg['controlUrl']==NEW:print(json.dumps({'node':node,'alreadyMigrated':True}));return
 if cfg['controlUrl']!=OLD:raise ValueError('Unexpected controller origin')
 opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
 # Exercise the same edge policy as the Go Agent; Cloudflare can reject
 # urllib's default user agent before the request reaches the application.
 request=urllib.request.Request(NEW+'/healthz',headers={'User-Agent':'Go-http-client/1.1'})
 with opener.open(request,timeout=15) as response:
  if response.status!=200 or not json.load(response).get('ok'):raise ValueError('New API not healthy')
 backup=Path('/root/backups')/('hqip-agent-domain-'+node+'-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime()))
 backup.mkdir(mode=0o700,parents=True,exist_ok=False)
 shutil.copytree(config,backup/'config')
 with sqlite3.connect('file:'+str(state/'state.db')+'?mode=ro',uri=True) as src,sqlite3.connect(backup/'state.db') as dest:src.backup(dest)
 for p in backup.rglob('*'):p.chmod(0o700 if p.is_dir() else 0o600)
 cfg['controlUrl']=NEW
 try:
  write_config(config/'agent.json',cfg)
  node_ops.command('systemctl','restart',units['agent'])
  node_ops.command('systemctl','is-active',units['agent'])
 except Exception:
  shutil.copyfile(backup/'config/agent.json',config/'agent.json')
  node_ops.command('systemctl','restart',units['agent']);raise
 (backup/'state.json').write_text(json.dumps({'node':node,'old':OLD,'new':NEW}));(backup/'state.json').chmod(0o600)
 print(json.dumps({'node':node,'backup':str(backup),'controlUrl':NEW}))

def rollback(backup):
 if backup.parent!=Path('/root/backups') or backup.is_symlink():raise ValueError('Invalid backup')
 info=json.loads((backup/'state.json').read_text());config,_,cfg=node_ops.owned_paths(info['node'])
 if cfg['controlUrl']!=NEW:raise ValueError('Controller origin already changed')
 cfg['controlUrl']=OLD;write_config(config/'agent.json',cfg)
 node_ops.command('systemctl','restart',node_ops.node_units(info['node'])['agent'])

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['migrate','rollback']);p.add_argument('--node-id');p.add_argument('--backup',type=Path);a=p.parse_args()
 if os.geteuid()!=0:raise PermissionError('Root required')
 if a.action=='migrate':migrate(a.node_id)
 else:rollback(a.backup)
