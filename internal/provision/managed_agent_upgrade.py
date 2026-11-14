"""Run one durable Agent-only upgrade independently of the SSH connection."""
from pathlib import Path
import contextlib
import fcntl
import io
import json
import os
import signal
import sys

folder = Path(__file__).resolve().parent
request = json.loads((folder / 'request.json').read_text())
receipt = folder / 'result.json'


def publish(value):
    temp = folder / 'result.new'
    temp.write_text(json.dumps(value))
    temp.chmod(0o600)
    temp.replace(receipt)


def interrupted(*_):
    raise RuntimeError('Upgrade interrupted')


def run():
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGHUP, interrupted)
    sys.path.insert(0, str(folder / 'scripts'))
    import upgrade_node_agent as upgrade
    root = Path('/root/backups') / ('hqip-managed-' + request['operationId'])
    if root.is_symlink():
        raise ValueError('Unsafe backup directory')
    root.mkdir(mode=0o700, exist_ok=True)
    upgrade.node_ops.BACKUP_ROOT = root
    with open('/run/lock/hqip-nodes.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        # A host reboot may have interrupted the old process after stopping the
        # Agent. Restore only its service override, never the live ledger.
        for state in root.glob('*/state.json'):
            previous = json.loads(state.read_text())
            if previous.get('phase') == 'prepared':
                upgrade.rollback(state.parent)
        publish({**request, 'state': 'running'})
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                result = upgrade.upgrade(request['nodeId'], folder / 'artifacts')
        except Exception:
            # Includes interruption immediately after systemctl stop, before
            # the upgrade helper enters its own rollback handler.
            for state in root.glob('*/state.json'):
                if json.loads(state.read_text()).get('phase') == 'prepared':
                    upgrade.rollback(state.parent)
            raise
        publish({**request, **result, 'state': 'succeeded'})
        # Keep the receipt and private recovery backup, not another copy of the
        # large immutable binary already installed under /opt/hqip-node.
        with contextlib.suppress(OSError):
            for item in (folder / 'artifacts').iterdir():
                if item.is_file() and not item.is_symlink():
                    item.unlink()
            (folder / 'artifacts').rmdir()


try:
    run()
except Exception:
    publish({**request, 'state': 'failed'})
    sys.exit(1)
