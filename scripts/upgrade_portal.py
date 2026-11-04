"""Upgrade HQIP's controller and this virtual host, with a recoverable snapshot.

Does not activate account migration or change node services. First upgrade and
verify all owned node cores, then activate through the authenticated admin API.
"""
from __future__ import annotations
import argparse, hashlib, json, os, pathlib, shutil, subprocess
import upgrade_control as control

SITE=pathlib.Path('/etc/nginx/sites-available/hqip.conf')

def run(*args):
    subprocess.run(args,check=True,capture_output=True)

def upgrade(release,state):
    release,manifest=control.verify_release(release)
    candidate=release/'deploy/nginx-hqip.conf'
    if 'deploy/nginx-hqip.conf' not in manifest['sha256'] or not candidate.is_file():
        raise ValueError('Release requires a verified HQIP Nginx configuration')
    if SITE.is_symlink() or not SITE.is_file() or 'server_name hqip.5202345.xyz;' not in SITE.read_text():
        raise ValueError('Unexpected virtual host; refusing to replace')
    old=SITE.read_bytes()
    others={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in SITE.parent.iterdir() if p.is_file() and p!=SITE}
    control.upgrade(release,state)
    try:
        backup=state/'nginx.before.conf';backup.write_bytes(old);backup.chmod(0o600)
        pending=SITE.with_suffix('.hqip-new');pending.write_bytes(candidate.read_bytes());pending.chmod(0o644);pending.replace(SITE)
        run('nginx','-t');run('systemctl','reload','nginx')
        control.healthy()
        for filename,digest in others.items():
            if hashlib.sha256(pathlib.Path(filename).read_bytes()).hexdigest()!=digest:raise RuntimeError('Another site changed')
        (state/'portal-stage.json').write_text(json.dumps({'phase':'prepared','activated':False,'otherSites':others},indent=2)+'\n')
        (state/'portal-stage.json').chmod(0o600)
    except Exception:
        # The controller guard reads the real DB after stopping it. If activation
        # happened, preserve the new routes/data and require manual recovery.
        control.rollback(state)
        SITE.write_bytes(old);run('nginx','-t');run('systemctl','reload','nginx')
        raise
    print('Controller and user portal ready; account migration remains explicitly gated.')

def rollback(state):
    backup=state/'nginx.before.conf'
    if not backup.is_file() or backup.is_symlink():raise ValueError('Missing original virtual host')
    control.rollback(state)
    SITE.write_bytes(backup.read_bytes());SITE.chmod(0o644)
    run('nginx','-t');run('systemctl','reload','nginx')

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action',choices=['upgrade','rollback'])
    parser.add_argument('--release',type=pathlib.Path)
    parser.add_argument('--state',type=pathlib.Path,required=True)
    args=parser.parse_args()
    if os.geteuid()!=0:raise PermissionError('Root required')
    if not args.state.is_absolute() or args.state.parent!=pathlib.Path('/root/backups') or args.state.is_symlink():raise ValueError('Use a direct /root/backups directory')
    if args.action=='upgrade':upgrade(args.release,args.state)
    else:rollback(args.state)

if __name__=='__main__':main()
