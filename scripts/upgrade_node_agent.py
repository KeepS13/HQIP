"""Upgrade one owned Agent, preserving the running core and all live ledgers."""
from __future__ import annotations

import argparse
from contextlib import closing
import json
import os
from pathlib import Path
import platform
import shutil
import sqlite3
import tempfile
import time

import upgrade_node_admission as node_ops


def override_path(units):
    return node_ops.UNIT_ROOT / (units['agent'] + '.d') / '80-hqip-telemetry.conf'


def atomic_override(path, text):
    if path.is_symlink() or path.parent.is_symlink():
        raise ValueError('Unsafe Agent override')
    path.parent.mkdir(exist_ok=True)
    descriptor, filename = tempfile.mkstemp(prefix=path.name + '.', suffix='.new', dir=path.parent)
    temporary = Path(filename)
    try:
        with os.fdopen(descriptor, 'w') as output:
            output.write(text)
            output.flush()
            os.fsync(output.fileno())
        temporary.chmod(0o644)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def verify_running(unit, binary, ledger):
    for attempt in range(20):
        active = node_ops.command('systemctl', 'is-active', unit, check=False).stdout.strip() == 'active'
        effective = node_ops.command('systemctl', 'show', '--value', '-p', 'ExecStart', unit).stdout
        with closing(sqlite3.connect(ledger.as_uri() + '?mode=ro', uri=True)) as db:
            initialized = db.execute("SELECT 1 FROM sqlite_master WHERE name='hourly_state'").fetchone()
        if active and str(binary) in effective and initialized:
            return
        if attempt < 19:
            time.sleep(.5)
    raise RuntimeError('Agent telemetry initialization failed')


def upgrade(node, artifacts):
    config, state, cfg = node_ops.owned_paths(node)
    units = node_ops.node_units(node)
    node_ops.verify_units(node, config, units)
    ledger = Path(cfg['statePath'])
    if ledger.is_symlink() or ledger.resolve().parent != state:
        raise ValueError('Agent ledger does not belong to node')
    node_ops.regular(ledger)
    for unit in units.values():
        if node_ops.command('systemctl', 'is-active', unit).stdout.strip() != 'active':
            raise ValueError('Owned services must be running before Agent upgrade')
    invocation = node_ops.command('systemctl', 'show', '--value', '-p', 'InvocationID', units['hysteria']).stdout.strip()
    if not invocation:
        raise ValueError('Cannot verify core invocation')
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    if not arch:
        raise ValueError('Unsupported architecture')
    manifest = json.loads(node_ops.regular(artifacts / 'manifest.json').read_text())
    if manifest.get('agentTelemetryVersion') != 1:
        raise ValueError('Native hourly Agent release required')
    source = node_ops.regular(artifacts / ('agent-linux-' + arch))
    expected = manifest['sha256'].get(source.name)
    if not expected or node_ops.digest(source) != expected:
        raise ValueError('Agent checksum mismatch')
    target = node_ops.RELEASE_ROOT / expected / 'agent'
    if target.is_symlink() or target.parent.is_symlink() or target.resolve() != target:
        raise ValueError('Unsafe immutable Agent location')
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        if node_ops.digest(target) != expected:
            raise ValueError('Immutable Agent collision')
    else:
        shutil.copyfile(source, target)
    target.chmod(0o755)
    override = override_path(units)
    if override.is_symlink() or override.parent.is_symlink():
        raise ValueError('Unsafe Agent override')
    if node_ops.BACKUP_ROOT.is_symlink() or node_ops.BACKUP_ROOT.resolve() != node_ops.BACKUP_ROOT:
        raise ValueError('Unsafe backup root')
    backup = node_ops.BACKUP_ROOT / ('hqip-agent-telemetry-' + node + '-' + str(time.time_ns()))
    backup.mkdir(mode=0o700, parents=True, exist_ok=False)
    text = '[Service]\nExecStart=\nExecStart=' + str(target) + ' --config ' + str(config / 'agent.json') + '\n'
    info = {'nodeId': node, 'override': str(override), 'before': override.read_text() if override.exists() else None,
            'installed': text, 'agentSHA256': expected, 'sourceSHA': manifest.get('agentSourceSHA'),
            'coreInvocation': invocation, 'phase': 'prepared'}
    save_state(backup, info)
    # Only the Agent stops. The core keeps forwarding on its existing leases.
    node_ops.command('systemctl', 'stop', units['agent'])
    try:
        shutil.copytree(config, backup / 'config')
        with closing(sqlite3.connect(ledger.as_uri() + '?mode=ro', uri=True)) as src:
            with closing(sqlite3.connect(backup / 'state.db')) as dest:
                src.backup(dest)
        for path in backup.rglob('*'):
            path.chmod(0o700 if path.is_dir() else 0o600)
        atomic_override(override, text)
        node_ops.command('systemctl', 'daemon-reload')
        node_ops.command('systemctl', 'start', units['agent'])
        verify_running(units['agent'], target, ledger)
        current = node_ops.command('systemctl', 'show', '--value', '-p', 'InvocationID', units['hysteria']).stdout.strip()
        if current != invocation:
            raise RuntimeError('Core invocation changed during Agent-only upgrade')
    except Exception:
        rollback(backup)
        raise
    info['phase'] = 'active'
    save_state(backup, info)
    result = {'nodeId': node, 'backup': str(backup), 'agentSHA256': expected, 'coreInvocationUnchanged': True}
    print(json.dumps(result))
    return result


def save_state(backup, info):
    path = backup / 'state.json'
    path.write_text(json.dumps(info, indent=2) + '\n')
    path.chmod(0o600)


def rollback(backup):
    if backup.is_symlink() or backup.resolve().parent != node_ops.BACKUP_ROOT:
        raise ValueError('Unsafe backup location')
    info = json.loads(node_ops.regular(backup / 'state.json').read_text())
    config, _, _ = node_ops.owned_paths(info['nodeId'])
    units = node_ops.node_units(info['nodeId'])
    node_ops.verify_units(info['nodeId'], config, units, check_effective=False)
    override = override_path(units)
    if str(override) != info['override'] or override.is_symlink() or override.parent.is_symlink():
        raise ValueError('Rollback override ownership mismatch')
    current = override.read_text() if override.exists() else None
    if current not in (info['installed'], info['before']):
        raise ValueError('Refusing to overwrite a newer Agent override')
    node_ops.command('systemctl', 'stop', units['agent'])
    if info['before'] is None:
        override.unlink(missing_ok=True)
    else:
        atomic_override(override, info['before'])
    # Never restore state.db or the core configuration from the backup.
    node_ops.command('systemctl', 'daemon-reload')
    node_ops.command('systemctl', 'start', units['agent'])
    if node_ops.command('systemctl', 'is-active', units['agent']).stdout.strip() != 'active':
        raise RuntimeError('Previous Agent did not recover')
    info['phase'] = 'rolled-back-ledger-preserved'
    save_state(backup, info)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['upgrade', 'rollback'])
    parser.add_argument('--node-id')
    parser.add_argument('--artifacts', type=Path)
    parser.add_argument('--backup', type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise PermissionError('Root required')
    if args.action == 'upgrade':
        upgrade(args.node_id, args.artifacts)
    else:
        rollback(args.backup)
