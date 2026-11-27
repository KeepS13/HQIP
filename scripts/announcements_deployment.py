"""Prepare additive user-announcement tables before switching the release."""
import subprocess
from urllib.parse import urlsplit


def prepare(deploy, manifest):
    """Resolve only the active database's migration role; never expose its URL."""
    version = manifest.get('announcementsVersion', 0)
    if not version or not deploy.postgres_enabled():
        return None
    if version != 1:
        raise ValueError('Unsupported announcements schema')
    if deploy.postgres_info().get('announcementsVersion', 0) >= version:
        return None
    environment = deploy.storage_environment(deploy.MIGRATION_ENV)
    application = urlsplit(deploy.storage_environment()['HQIP_DATABASE_URL'])
    migration = urlsplit(environment['HQIP_DATABASE_URL'])
    if ((application.hostname, application.port, application.path, application.query) !=
            (migration.hostname, migration.port, migration.path, migration.query) or
            migration.username != 'hqip_migrator'):
        raise ValueError('Announcements migration must target the active database migration role')
    return environment


def migrate(release, environment, execute=subprocess.run):
    """An idempotent additive migration may be retried; preserve live data on error."""
    if environment is None:
        return
    result = execute([str(release / 'hqip'), 'migrate-announcements'], env=environment,
                     capture_output=True, timeout=120, check=False)
    if result.returncode:
        raise RuntimeError('Announcements migration failed; live data is preserved')
