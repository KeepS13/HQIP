"""Additive traffic-billing migration using the existing database owner."""
import subprocess
from urllib.parse import urlsplit


def prepare(deploy, manifest):
    version = manifest.get('trafficBillingVersion', 0)
    if version == 0 or not deploy.postgres_enabled():
        return None
    if version != 1:
        raise ValueError('Unsupported traffic billing schema')
    if deploy.postgres_info().get('trafficBillingVersion', 0) >= version:
        return None
    environment = deploy.storage_environment(deploy.MIGRATION_ENV)
    application = urlsplit(deploy.storage_environment()['HQIP_DATABASE_URL'])
    migration = urlsplit(environment['HQIP_DATABASE_URL'])
    if ((application.hostname, application.port, application.path, application.query) !=
            (migration.hostname, migration.port, migration.path, migration.query) or
            migration.username != 'hqip_migrator'):
        raise ValueError('Traffic billing migration must use the active database migration role')
    return environment


def migrate(release, environment, execute=subprocess.run):
    if environment is None:
        return
    result = execute([str(release / 'hqip'), 'migrate-traffic-billing'], env=environment,
                     capture_output=True, timeout=120, check=False)
    if result.returncode:
        raise RuntimeError('Traffic billing migration failed; live data is preserved')
