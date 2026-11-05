"""Prepare the one-time development-account reset; never erase node ledgers.

Run hold/confirm on the controller, node-receipt on each owned active node.
Only confirm creates the v4->v5 migration gate consumed by the new controller.
"""
from contextlib import closing
from pathlib import Path
import argparse
import hashlib
import json
import os
import re
import secrets
import shutil
import sqlite3
import time
import upgrade_node_admission as node_tools

DATA = Path('/var/lib/hqip-control')
BACKUPS = Path('/root/backups')


def private_state(path, create=False):
    path = Path(path)
    if not path.is_absolute() or path.parent != BACKUPS or path.is_symlink():
        raise ValueError('Use a direct private backup directory')
    if create:
        path.mkdir(mode=0o700, exist_ok=False)
    elif not path.is_dir():
        raise ValueError('Backup directory is missing')
    return path


def write_private(path, value):
    path = Path(path)
    if path.is_symlink():
        raise ValueError('Refuse to replace a symlink')
    pending = path.with_name(path.name + '.' + secrets.token_hex(8) + '.pending')
    try:
        with os.fdopen(os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as stream:
            stream.write(json.dumps(value, indent=2) + '\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(pending, path)
        if os.name != 'nt':
            descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
    finally:
        pending.unlink(missing_ok=True)


def backup_db(source, target):
    if source.is_symlink() or not source.is_file():
        raise ValueError('An ordinary database file is required')
    with closing(sqlite3.connect(source.resolve().as_uri()+'?mode=ro', uri=True, timeout=10)) as db, closing(sqlite3.connect(target)) as copy:
        db.backup(copy)
        if copy.execute('PRAGMA quick_check').fetchone() != ('ok',):
            raise ValueError('Backup integrity check failed')
    target.chmod(0o600)


def hold(state):
    state = private_state(state, create=True)
    backup_db(DATA/'hqip.db', state/'hqip.db')
    shutil.copyfile(DATA/'master.key', state/'master.key')
    (state/'master.key').chmod(0o600)
    with closing(sqlite3.connect(DATA/'hqip.db', timeout=10)) as db, db:
        db.execute('BEGIN IMMEDIATE')
        if db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone() != ('4',):
            raise ValueError('Only the old development schema can be reset')
        flags = dict(db.execute("SELECT key,value FROM metadata WHERE key IN ('portal_registration','portal_recovery_hold')"))
        if flags.get('portal_recovery_hold') == 'true':
            raise ValueError('Existing recovery hold needs explicit reconciliation')
        users = list(db.execute('SELECT id,enabled FROM portal_users'))
        nodes = [r[0] for r in db.execute("SELECT id FROM nodes WHERE lifecycle='active' AND managed=1")]
        info = {'phase':'held','holdId':secrets.token_hex(16),'startedAt':int(time.time()),'flags':flags,'users':users,'nodes':nodes,
                'masterSHA256':hashlib.sha256((DATA/'master.key').read_bytes()).hexdigest()}
        # Persist abort information before committing the operational hold.
        write_private(state/'migration-state.json', info)
        db.executemany("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                       [('portal_registration','false'),('portal_recovery_hold','true'),('subscriptions_reset_ready','false'),
                        ('subscriptions_migration_id',info['holdId']),
                        ('subscriptions_registration_after_reset',flags.get('portal_registration','false'))])
        db.execute('UPDATE portal_users SET enabled=0,revision=revision+1')
        db.execute('UPDATE portal_sessions SET revoked=1')
        db.execute('UPDATE portal_authorizations SET consumed=1')
        db.execute('UPDATE portal_leases SET revoked=1 WHERE closed=0')
        db.execute("INSERT INTO audit(at,action,subject) VALUES(?,'subscriptions.migration_hold','development-account-reset')", (info['startedAt'],))
    return {'phase':'held','backup':str(state),'nodes':nodes}


def node_receipt(node_id, state):
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,64}', node_id):
        raise ValueError('Invalid node ID')
    config, node_state, cfg = node_tools.owned_paths(node_id)
    node_tools.verify_units(node_id, config, node_tools.node_units(node_id))
    ledger = node_tools.regular(node_state/'state.db')
    if Path(cfg.get('statePath', '')) != ledger:
        raise ValueError('Agent ledger path does not belong to this node')
    if config.is_symlink() or not config.is_dir() or any(p.is_symlink() for p in config.rglob('*')):
        raise ValueError('Owned node configuration must be an ordinary directory')
    closed_finals(ledger)
    state = private_state(state, create=True)
    backup_db(ledger, state/'state.db')
    # The receipt describes the exact immutable backup, not an earlier read of
    # the live WAL. A dirty final in that snapshot prevents readiness.
    rows = closed_finals(state/'state.db')
    shutil.copytree(config, state/'config')
    for p in state.rglob('*'):
        p.chmod(0o700 if p.is_dir() else 0o600)
    receipt = {'nodeId':node_id,'checkedAt':int(time.time()),'backup':str(state),
               'ledgerSHA256':hashlib.sha256((state/'state.db').read_bytes()).hexdigest(),
               'pendingFinals':0,'openLeases':0,'finals':[{'id':r[0],'upload':r[1],'download':r[2]} for r in rows]}
    write_private(state/'receipt.json', receipt)
    return receipt


def closed_finals(database):
    with closing(sqlite3.connect(database.resolve().as_uri()+'?mode=ro', uri=True, timeout=10)) as db:
        rows = db.execute('SELECT lease_id,upload,download,closed,dirty FROM lease_usage').fetchall()
        if any(not r[3] or r[4] for r in rows):
            raise ValueError('Node has open or unacknowledged final leases; wait for reconciliation')
        return rows


def validate_hold(db, info):
    if info.get('phase') not in ('held', 'ready'):
        raise ValueError('Hold state is no longer active')
    if not re.fullmatch(r'[a-f0-9]{32}', info.get('holdId', '')):
        raise ValueError('Missing hold identity')
    if db.execute("SELECT value FROM metadata WHERE key='subscriptions_migration_id'").fetchone() != (info['holdId'],):
        raise ValueError('A different hold is now active')
    if db.execute("SELECT value FROM metadata WHERE key='portal_recovery_hold'").fetchone() != ('true',):
        raise ValueError('Operational hold is no longer active')
    if hashlib.sha256((DATA/'master.key').read_bytes()).hexdigest() != info['masterSHA256']:
        raise ValueError('Master key changed')


def validate_receipts(db, info, receipts, now):
    if set(r.get('nodeId') for r in receipts) != set(info['nodes']) or len(receipts) != len(info['nodes']):
        raise ValueError('Every active managed node needs its own receipt')
    if db.execute('SELECT COUNT(*) FROM portal_leases WHERE closed=0').fetchone()[0]:
        raise ValueError('Controller still has unsettled leases')
    for receipt in receipts:
        if receipt.get('pendingFinals') != 0 or receipt.get('openLeases') != 0:
            raise ValueError('Unsettled node receipt')
        checked = receipt.get('checkedAt',0)
        if checked < info['startedAt'] or checked > now+30 or now-checked > 900:
            raise ValueError('Node receipt is stale or predates the hold')
        if not re.fullmatch(r'[a-f0-9]{64}',receipt.get('ledgerSHA256','')):
            raise ValueError('Missing node backup digest')
        for final in receipt.get('finals',[]):
            row = db.execute('SELECT upload,download,closed FROM portal_leases WHERE id=? AND node_id=?', (final['id'],receipt['nodeId'])).fetchone()
            if row != (final['upload'],final['download'],1):
                raise ValueError('Controller/node final accounting mismatch')


def confirm(state, receipts_path):
    state = private_state(state)
    info = json.loads((state/'migration-state.json').read_text())
    receipts = json.loads(Path(receipts_path).read_text())
    with closing(sqlite3.connect(DATA/'hqip.db', timeout=10)) as db, db:
        db.execute('BEGIN IMMEDIATE')
        if db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone() != ('4',):
            raise ValueError('Already migrated; never repeat the reset')
        validate_hold(db, info)
        current = [r[0] for r in db.execute("SELECT id FROM nodes WHERE lifecycle='active' AND managed=1")]
        if set(current) != set(info['nodes']):
            raise ValueError('Active node set changed during preparation')
        validate_receipts(db,info,receipts,int(time.time()))
        write_private(state/'node-receipts.json',receipts)
        info['phase']='ready';write_private(state/'migration-state.json',info)
        db.execute("INSERT INTO metadata(key,value) VALUES('subscriptions_reset_ready','true') ON CONFLICT(key) DO UPDATE SET value='true'")
    return {'phase':'ready','backup':str(state),'nodesVerified':len(receipts)}


def abort_hold(state):
    state=private_state(state)
    info=json.loads((state/'migration-state.json').read_text())
    with closing(sqlite3.connect(DATA/'hqip.db',timeout=10)) as db,db:
        db.execute('BEGIN IMMEDIATE')
        if db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone() != ('4',):
            raise ValueError('Schema advanced; refuse to restore old account state')
        validate_hold(db, info)
        db.executemany('UPDATE portal_users SET enabled=?,revision=revision+1 WHERE id=?',[(enabled,id) for id,enabled in info['users']])
        db.executemany("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",[(key,info['flags'].get(key,'false')) for key in ['portal_registration','portal_recovery_hold']])
        db.execute("DELETE FROM metadata WHERE key IN ('subscriptions_reset_ready','subscriptions_registration_after_reset','subscriptions_migration_id')")
    info['phase']='aborted';write_private(state/'migration-state.json',info)
    return {'phase':'aborted','sessionsRequireLogin':True}


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('action',choices=['hold','node-receipt','confirm','abort-hold'])
    p.add_argument('--state',required=True,type=Path);p.add_argument('--node-id');p.add_argument('--receipts',type=Path)
    a=p.parse_args()
    if os.geteuid()!=0:raise PermissionError('Root required')
    os.umask(0o077)
    if a.action=='hold':result=hold(a.state)
    elif a.action=='node-receipt':result=node_receipt(a.node_id,a.state)
    elif a.action=='confirm':result=confirm(a.state,a.receipts)
    else:result=abort_hold(a.state)
    print(json.dumps(result))
