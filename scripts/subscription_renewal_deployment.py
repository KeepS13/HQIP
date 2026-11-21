"""Migrate subscription periods only after the controller has stopped."""
import json
import subprocess
from urllib.parse import urlsplit


def prepare(deploy, manifest):
    version = manifest.get('subscriptionRenewalsVersion', 0)
    if not version or not deploy.postgres_enabled():
        return None
    if version != 1:
        raise ValueError('Unsupported subscription renewal schema')
    if deploy.postgres_info().get('subscriptionRenewalsVersion', 0) >= version:
        return None
    environment = deploy.storage_environment(deploy.MIGRATION_ENV)
    application = urlsplit(deploy.storage_environment()['HQIP_DATABASE_URL'])
    migration = urlsplit(environment['HQIP_DATABASE_URL'])
    if ((application.hostname, application.port, application.path, application.query) !=
            (migration.hostname, migration.port, migration.path, migration.query) or
            migration.username != 'hqip_migrator'):
        raise ValueError('Subscription renewal migration must target the active database migration role')
    return environment


def migrate(release, environment, state, info, execute=subprocess.run):
    if environment is None:
        return
    # A lost result is not proof of rollback. Persist the gate before execution.
    info['renewalMigrationAttempted'] = True
    info['renewalTarget'] = 1
    (state / 'domains-state.json').write_text(json.dumps(info, indent=2))
    try:
        result = execute([str(release / 'hqip'), 'migrate-subscription-renewals'],
                         env=environment, capture_output=True, timeout=330, check=False)
    except subprocess.TimeoutExpired:
        raise RuntimeError('Subscription renewal migration result is uncertain; keep controller stopped and fix forward') from None
    if result.returncode:
        raise RuntimeError('Subscription renewal migration failed; live ledgers are preserved')
    info['renewalMigrationCompleted'] = True
    (state / 'domains-state.json').write_text(json.dumps(info, indent=2))
