"""Prepare the additive managed-operations schema with the migration role."""
import subprocess
from urllib.parse import urlsplit


def prepare(deploy, manifest):
    if not manifest.get('operationsVersion') or not deploy.postgres_enabled():
        return None
    if manifest['operationsVersion'] != 1:
        raise ValueError('Unsupported operations schema')
    if deploy.postgres_info().get('operationsVersion', 0) >= 1:
        return None
    env = deploy.storage_environment(deploy.MIGRATION_ENV)
    app = urlsplit(deploy.storage_environment()['HQIP_DATABASE_URL'])
    migration = urlsplit(env['HQIP_DATABASE_URL'])
    if (app.hostname, app.port, app.path, app.query) != (migration.hostname, migration.port, migration.path, migration.query) or migration.username != 'hqip_migrator':
        raise ValueError('Operations migration must target the active database with the migration role')
    return env


def migrate(release, env):
    if env is None:
        return
    try:
        result = subprocess.run([str(release / 'hqip'), 'migrate-operations'], env=env,
                                capture_output=True, timeout=120, check=False)
    except subprocess.TimeoutExpired:
        raise RuntimeError('Operations migration timed out; live data is preserved') from None
    if result.returncode:
        raise RuntimeError('Operations migration failed; live data is preserved')
