"""Additive offline ledger migration. Never resets usage or enables nodes."""
import subprocess
from urllib.parse import urlsplit


def prepare(deploy, manifest):
    version = manifest.get('offlineAdmissionVersion', 0)
    if version == 0 or not deploy.postgres_enabled():
        return None
    if version != 1:
        raise ValueError('Unsupported offline admission schema')
    info = deploy.postgres_info()
    # The existing additive command also adds ingress IPs. An already-installed
    # offline ledger must not cause this newer migration to be skipped.
    if (info.get('offlineAdmissionVersion', 0) >= version
            and info.get('connectionSlotsVersion', 0) >= manifest.get('connectionSlotsVersion', 0)):
        return None
    environment = deploy.storage_environment(deploy.MIGRATION_ENV)
    application = urlsplit(deploy.storage_environment()['HQIP_DATABASE_URL'])
    migration = urlsplit(environment['HQIP_DATABASE_URL'])
    if ((application.hostname, application.port, application.path, application.query) !=
            (migration.hostname, migration.port, migration.path, migration.query) or
            migration.username != 'hqip_migrator'):
        raise ValueError('Offline migration must use the active database migration role')
    return environment


def migrate(release, environment, execute=subprocess.run):
    if environment is None:
        return
    result = execute([str(release / 'hqip'), 'migrate-offline-admission'], env=environment,
                     capture_output=True, timeout=120, check=False)
    if result.returncode:
        raise RuntimeError('Offline ledger migration failed; live data is preserved')
