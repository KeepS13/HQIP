"""Prepare a new private PostgreSQL/Redis configuration; never starts services."""
from pathlib import Path
import argparse
import hashlib
import secrets
import shutil

ROOT=Path(__file__).resolve().parents[1]


def prepare(directory):
    directory=Path(directory).absolute()
    directory.mkdir(mode=0o700,exist_ok=False)
    secret_dir=directory/'secrets'
    secret_dir.mkdir(mode=0o700)
    passwords={key:secrets.token_urlsafe(40) for key in ('postgres','app','migration','redis')}
    for key in ('postgres','app','migration'):
        file=secret_dir/(key+'-password')
        file.write_text(passwords[key]+'\n')
        file.chmod(0o600)
    # Redis receives only the password hash, and only the commands/keys used by
    # the limiter. No CONFIG, FLUSH, ACL mutation, replication, or arbitrary keys.
    acl='user default off\nuser hqip on #'+hashlib.sha256(passwords['redis'].encode()).hexdigest()+' ~hqip:rate:* -@all +ping +hello +client|setinfo +client|setname +select +eval +evalsha +script|load +time +zremrangebyscore +zcard +zadd +pexpire\n'
    file=secret_dir/'redis-acl';file.write_text(acl);file.chmod(0o600)
    for file in ('compose.yaml','init-roles.sql','redis.conf'):
        shutil.copyfile(ROOT/'deploy/storage'/file,directory/file)
    for filename,role,key in (('storage.env','hqip_app','app'),('migration.env','hqip_migrator','migration')):
        text=f'HQIP_DATABASE_URL="postgres://{role}:{passwords[key]}@127.0.0.1:15432/hqip?sslmode=disable"\n'
        text+=f'HQIP_REDIS_URL="redis://hqip:{passwords["redis"]}@127.0.0.1:16379/0"\nHQIP_REDIS_NAMESPACE="hqip"\n'
        file=directory/filename;file.write_text(text);file.chmod(0o600)
    return directory


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    print('Prepared private configuration directory: '+str(prepare(args.output)))
