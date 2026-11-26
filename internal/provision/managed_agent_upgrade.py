"""Run one durable Agent-only upgrade independently of the SSH connection."""
from pathlib import Path
import contextlib
import errno
import io
import json
import signal
import sys


def publish(folder, value):
    temp = folder / 'result.new'
    temp.write_text(json.dumps(value))
    temp.chmod(0o600)
    temp.replace(folder / 'result.json')


def interrupted(*_):
    raise RuntimeError('Upgrade interrupted')


def run(folder, request, backup_parent=Path('/root/backups'), lock_path=Path('/run/lock/hqip-nodes.lock')):
    import fcntl

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGHUP, interrupted)
    sys.path.insert(0, str(folder / 'scripts'))
    import upgrade_node_agent as upgrade
    root = backup_parent / ('hqip-managed-' + request['operationId'])
    stage = 'backup_directory'
    try:
        # First-time upgrades must not depend on an earlier manual backup.
        # Reject redirected paths before creating any directories or backups.
        if (backup_parent.is_symlink() or backup_parent.resolve() != backup_parent
                or root.is_symlink() or root.resolve().parent != backup_parent):
            raise ValueError('Unsafe backup directory')
        backup_parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        root.mkdir(mode=0o700, exist_ok=True)
        root.chmod(0o700)
        upgrade.node_ops.BACKUP_ROOT = root
        stage = 'host_lock'
        with open(lock_path, 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            stage = 'rollback_failed'
            recover(upgrade, root)
            publish(folder, {**request, 'state': 'running'})
            stage = 'upgrade_failed'
            try:
                with contextlib.redirect_stdout(io.StringIO()):
                    result = upgrade.upgrade(request['nodeId'], folder / 'artifacts')
            except Exception:
                # Also covers interruption immediately after stopping Agent.
                stage = 'rollback_failed'
                recover(upgrade, root)
                stage = 'upgrade_failed'
                raise
            publish(folder, {**request, **result, 'state': 'succeeded'})
    except Exception as error:
        publish(folder, {**request, 'state': 'failed', 'errorCode': error_code(error, stage)})
        return 1
    # Keep the receipt and private recovery backup, not another binary copy.
    with contextlib.suppress(OSError):
        for item in (folder / 'artifacts').iterdir():
            if item.is_file() and not item.is_symlink():
                item.unlink()
        (folder / 'artifacts').rmdir()
    return 0


def recover(upgrade, root):
    # A host reboot may have interrupted the old process after stopping the
    # Agent. Restore only its service override, never the live ledger.
    for state in root.glob('*/state.json'):
        previous = json.loads(state.read_text())
        if previous.get('phase') == 'prepared':
            upgrade.rollback(state.parent)


def error_code(error, stage):
    # Only publish a controlled code: exception text can include command output.
    if stage == 'rollback_failed':
        return stage
    if isinstance(error, OSError) and error.errno == errno.ENOSPC:
        return 'disk_full'
    if isinstance(error, PermissionError):
        return 'permission_denied'
    if isinstance(error, BlockingIOError) and stage == 'host_lock':
        return 'host_busy'
    known = {
        'NAT nodes require a compatible Agent': 'nat_unsupported',
        'Agent checksum mismatch': 'artifact_checksum',
        'Agent telemetry initialization failed': 'agent_start_failed',
        'Reality ingress initialization failed': 'reality_start_failed',
        'Reality firewall initialization failed': 'reality_firewall_failed',
        'No free Reality TCP port': 'reality_port_busy',
        'Owned services must be running before Agent upgrade': 'inactive_service',
        'Core invocation changed during Agent-only upgrade': 'core_changed',
        'Upgrade interrupted': 'interrupted',
    }
    return known.get(str(error), stage)


if __name__ == '__main__':
    folder = Path(__file__).resolve().parent
    request = json.loads((folder / 'request.json').read_text())
    sys.exit(run(folder, request))
