"""Explicit PostgreSQL disaster recovery; never called by ordinary deployment."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import sys
import tarfile
import tempfile
from urllib.parse import unquote, urlsplit

from release_bundle import digest, verify_full_release

SAFE_ENV = {'PATH': '/usr/lib/postgresql/18/bin:/usr/bin:/bin', 'LANG': 'C.UTF-8', 'GOMAXPROCS': '2'}
ROLES = ('hqip_migrator', 'hqip_app')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def ordinary(path):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path and not path.is_symlink(),
            'Use absolute paths without symlinks')
    return path


def run(args, *, env=None, sql=None, timeout=1800):
    try:
        result = subprocess.run(list(map(str, args)), input=sql, env=env or SAFE_ENV,
                                capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise ValueError('Recovery command unavailable or timed out; preserve the target for inspection') from None
    require(result.returncode == 0, 'Recovery command failed: ' + Path(str(args[0])).name)
    return result.stdout.strip()


def validate_database(database):
    require(isinstance(database, str) and re.fullmatch(r'hqip(?:_[a-z0-9_]{1,48})?', database),
            'Specify the exact HQIP recovery database name')
    return database


def config(address, role, database):
    validate_database(database)
    parsed = urlsplit(address)
    require(role in ROLES and parsed.scheme == 'postgres' and parsed.username == role
            and parsed.hostname == '127.0.0.1' and parsed.port == 15432 and parsed.password
            and parsed.path == '/' + database and parsed.query == 'sslmode=disable' and not parsed.fragment,
            'Recovery requires the matching local database and restricted role')
    values = {'host': '127.0.0.1', 'port': '15432', 'dbname': database, 'user': role,
              'password': unquote(parsed.password), 'sslmode': 'disable', 'connect_timeout': '5'}
    require(all(value.strip() == value and not any(c in value for c in '\r\n\x00') for value in values.values()),
            'Invalid PostgreSQL service setting')
    return values


def environment(path, role, database):
    path = ordinary(path)
    require(path.is_file() and (os.name == 'nt' or stat.S_IMODE(path.stat().st_mode) & 0o077 == 0),
            'Recovery environment must be a private ordinary file')
    values = {}
    for line in path.read_text().splitlines():
        fields = shlex.split(line, comments=True)
        if not fields:
            continue
        require(len(fields) == 1 and '=' in fields[0], 'Invalid recovery environment')
        name, value = fields[0].split('=', 1)
        require(name in ('HQIP_DATABASE_URL', 'HQIP_REDIS_URL', 'HQIP_REDIS_NAMESPACE')
                and name not in values and value, 'Unexpected recovery environment setting')
        values[name] = value
    address = values.get('HQIP_DATABASE_URL', '')
    config(address, role, database)
    return address


def sql(address, role, database, statement):
    values = config(address, role, database)
    with tempfile.TemporaryDirectory(prefix='hqip-recovery-pg-') as directory:
        service = Path(directory) / 'service.conf'
        service.write_text('[hqip]\n' + ''.join(key + '=' + value + '\n' for key, value in values.items()))
        service.chmod(0o600)
        return run(['/usr/lib/postgresql/18/bin/psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1'], sql=statement,
                   env={**SAFE_ENV, 'PGSERVICEFILE': str(service), 'PGSERVICE': 'hqip',
                        'PGHOST': '127.0.0.1'}, timeout=90)


def target_facts(address, database):
    return json.loads(sql(address, 'hqip_migrator', database, """BEGIN READ ONLY;
      SELECT json_build_object('role',current_user,'database',current_database(),
        'owner',(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database()),
        'schemas',(SELECT json_agg(nspname ORDER BY nspname) FROM pg_namespace
          WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema'),
        'schemaOwner',(SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='public'),
        'objects',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public')+(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='public'),
        'foreignOwners',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public' AND c.relowner<>'hqip_migrator'::regrole),
        'unsafeRoles',(SELECT count(*) FROM pg_roles WHERE rolname IN ('hqip_app','hqip_migrator')
          AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)),
        'appMember',pg_has_role('hqip_app','hqip_migrator','MEMBER'),
        'appDatabaseCreate',has_database_privilege('hqip_app',current_database(),'CREATE'),
        'appDatabaseTemp',has_database_privilege('hqip_app',current_database(),'TEMP'));
      COMMIT;"""))


def validate_target(value, database):
    validate_database(database)
    require(value.get('role') == 'hqip_migrator' and value.get('database') == database
            and value.get('owner') == 'hqip_migrator' and value.get('schemas') == ['public']
            and value.get('schemaOwner') in ('hqip_migrator', 'pg_database_owner')
            and value.get('foreignOwners') == 0 and value.get('unsafeRoles') == 0
            and value.get('appMember') is False and value.get('appDatabaseCreate') is False
            and value.get('appDatabaseTemp') is False,
            'Target schema ownership or application role isolation is unsafe')


def permission_sql():
    # Never grant role membership, CREATE, TEMP, TRUNCATE or sequence UPDATE.
    return """BEGIN;
      REVOKE ALL ON SCHEMA public FROM PUBLIC,hqip_app;
      GRANT USAGE ON SCHEMA public TO hqip_app;
      REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC,hqip_app;
      GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO hqip_app;
      REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC,hqip_app;
      GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO hqip_app;
      ALTER DEFAULT PRIVILEGES FOR ROLE hqip_migrator IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC,hqip_app;
      ALTER DEFAULT PRIVILEGES FOR ROLE hqip_migrator IN SCHEMA public GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO hqip_app;
      ALTER DEFAULT PRIVILEGES FOR ROLE hqip_migrator IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC,hqip_app;
      ALTER DEFAULT PRIVILEGES FOR ROLE hqip_migrator IN SCHEMA public GRANT USAGE,SELECT ON SEQUENCES TO hqip_app;
      COMMIT;"""


def restore_permissions(address, database, schema=5, payments_schema=1):
    value = target_facts(address, database)
    validate_target(value, database)
    require(value.get('objects', 0) > 0, 'Restore the database before applying application grants')
    identity = json.loads(sql(address, 'hqip_migrator', database, """BEGIN READ ONLY;
      SELECT json_build_object('schema',(SELECT value FROM metadata WHERE key='schema_version'),
        'payments',(SELECT value FROM metadata WHERE key='payments_schema_version'),
        'recoveryHold',(SELECT value FROM metadata WHERE key='portal_recovery_hold'));
      COMMIT;"""))
    require(identity == {'schema': str(schema), 'payments': str(payments_schema), 'recoveryHold': 'true'},
            'Expected a supported restored schema with recovery hold enabled')
    sql(address, 'hqip_migrator', database, permission_sql())
    effective = json.loads(sql(address, 'hqip_migrator', database, """BEGIN READ ONLY;
      SELECT json_build_object('usage',has_schema_privilege('hqip_app','public','USAGE'),
        'create',has_schema_privilege('hqip_app','public','CREATE'),
        'missingTables',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f') AND NOT (
            has_table_privilege('hqip_app',c.oid,'SELECT') AND has_table_privilege('hqip_app',c.oid,'INSERT')
            AND has_table_privilege('hqip_app',c.oid,'UPDATE') AND has_table_privilege('hqip_app',c.oid,'DELETE'))),
        'unsafeTables',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f') AND (
            has_table_privilege('hqip_app',c.oid,'TRUNCATE') OR has_table_privilege('hqip_app',c.oid,'REFERENCES')
            OR has_table_privilege('hqip_app',c.oid,'TRIGGER'))),
        'unsafeSequences',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public' AND c.relkind='S' AND (NOT has_sequence_privilege('hqip_app',c.oid,'USAGE')
            OR NOT has_sequence_privilege('hqip_app',c.oid,'SELECT') OR has_sequence_privilege('hqip_app',c.oid,'UPDATE'))),
        'defaults',(SELECT coalesce(json_agg(json_build_array(d.defaclobjtype,a.privilege_type)), '[]')
          FROM pg_default_acl d JOIN pg_namespace n ON n.oid=d.defaclnamespace,
            LATERAL aclexplode(d.defaclacl) a WHERE n.nspname='public'
            AND d.defaclrole='hqip_migrator'::regrole AND a.grantee='hqip_app'::regrole));
      COMMIT;"""))
    require(effective.get('usage') is True and effective.get('create') is False
            and all(effective.get(key) == 0 for key in ('missingTables','unsafeTables','unsafeSequences'))
            and set(map(tuple, effective.get('defaults', []))) == {
                ('r','SELECT'),('r','INSERT'),('r','UPDATE'),('r','DELETE'),('S','USAGE'),('S','SELECT')},
            'Restored application grants are incomplete or excessive')
    return identity


def archive_key(archive):
    seen, key_hash = set(), None
    with tarfile.open(archive, 'r|gz') as source:
        for member in source:
            require(member.name in ('hqip.pg.dump','master.key','manifest.json') and member.name not in seen
                    and member.isfile() and 0 < member.size <= 4 * 1024 ** 3, 'Invalid PostgreSQL backup archive')
            seen.add(member.name)
            if member.name == 'master.key':
                require(member.size == 32, 'Invalid backup master key')
                with source.extractfile(member) as stream:
                    key_hash = hashlib.sha256(stream.read()).hexdigest()
    require(seen == {'hqip.pg.dump','master.key','manifest.json'}, 'Incomplete PostgreSQL backup')
    return key_hash


def restore(release, backup, backup_sha, data, migration_file, application_file, database):
    release, backup, data = map(ordinary, (release, backup, data))
    require(re.fullmatch('[a-f0-9]{64}', backup_sha) and digest(backup) == backup_sha, 'Backup checksum mismatch')
    require(data != Path('/var/lib/hqip-control') and data.parent.is_dir()
            and (not data.exists() or (data.is_dir() and not any(data.iterdir()))),
            'Restore into an independent empty directory; preserve the active data directory')
    manifest = verify_full_release(release)
    migration = environment(migration_file, 'hqip_migrator', database)
    application = environment(application_file, 'hqip_app', database)
    for tool in ('/usr/lib/postgresql/18/bin/pg_dump','/usr/lib/postgresql/18/bin/pg_restore'):
        require(' 18.' in run([tool,'--version']), 'PostgreSQL 18 backup tools are required')
    if database == 'hqip':
        for unit in ('hqip-control.service','hqip-control-prepare.service'):
            status = subprocess.run(['/usr/bin/systemctl','is-active',unit], capture_output=True, text=True, env=SAFE_ENV)
            require(status.stdout.strip() in ('inactive','failed','unknown'), 'Stop the target controller before disaster recovery')
    before = target_facts(migration, database)
    validate_target(before, database)
    require(before.get('objects') == 0, 'PostgreSQL recovery target must be empty')
    key_hash = archive_key(backup)
    binary = release / 'hqip'
    run([binary,'restore','--input',backup,'--data-dir',data], env={**SAFE_ENV,'HQIP_DATABASE_URL':migration})
    restore_permissions(migration, database, manifest['schemaVersion'], manifest['paymentsVersion'])
    info = json.loads(run([binary,'storage-info','--data-dir',data], env={**SAFE_ENV,'HQIP_DATABASE_URL':application}))
    require(info.get('backend') == 'postgresql' and info.get('masterSHA256') == key_hash
            and info.get('schemaVersion') == manifest['schemaVersion']
            and info.get('subscriptionRenewalsVersion') == manifest['subscriptionRenewalsVersion'],
            'Restored application identity or subscription period capability mismatch')
    with tempfile.TemporaryDirectory(prefix='hqip-recovery-check-') as directory:
        verification = Path(directory) / 'verified.tar.gz'
        run([binary,'backup','--data-dir',data,'--output',verification], env={**SAFE_ENV,'HQIP_DATABASE_URL':application})
        require(archive_key(verification) == key_hash, 'Application backup key mismatch')
    require(digest(backup) == backup_sha, 'Source backup changed during recovery')
    import pwd
    account = pwd.getpwnam('hqip-control')
    require({item.name for item in data.iterdir()} == {'master.key','postgresql.enabled'}, 'Unexpected restored data files')
    for path in (data, data / 'master.key', data / 'postgresql.enabled'):
        ordinary(path)
        path.chmod(0o700 if path.is_dir() else 0o600)
        os.chown(path, account.pw_uid, account.pw_gid)
    return {'ok': True, 'database': database, 'sourceSHA': manifest['sourceSHA'], 'backupSHA256': backup_sha,
            'masterSHA256': key_hash, 'schemaVersion': info['schemaVersion'], 'recoveryHold': True,
            'applicationGrantsVerified': True, 'applicationBackupVerified': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('release','backup','data-dir','migration-env','app-env'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--backup-sha256', required=True)
    parser.add_argument('--database', required=True)
    args = parser.parse_args()
    require(sys.platform == 'linux' and os.geteuid() == 0, 'Run offline recovery as root on the target Linux host')
    os.umask(0o077)
    print(json.dumps(restore(args.release, args.backup, args.backup_sha256, args.data_dir,
                             args.migration_env, args.app_env, args.database)))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError):
        # Paths/DSNs/SQL may appear in nested exceptions. Retain the target for
        # offline inspection and never turn those exceptions into log output.
        print(json.dumps({'ok': False, 'error': 'Recovery failed; keep the target and backup for offline inspection'}))
        raise SystemExit(1)
