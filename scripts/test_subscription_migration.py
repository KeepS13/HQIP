from contextlib import ExitStack, closing
from pathlib import Path
from unittest.mock import patch
import json
import hashlib
import sqlite3
import tempfile
import types
import unittest
import subscription_migration as migration


class SubscriptionMigrationTests(unittest.TestCase):
 def fixture(self, root):
  data=root/'data';data.mkdir();backups=root/'backups';backups.mkdir()
  (data/'master.key').write_bytes(b'paired-key')
  with closing(sqlite3.connect(data/'hqip.db')) as db,db:
   db.executescript("""CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT);
   INSERT INTO metadata VALUES('schema_version','4'),('portal_registration','true');
   CREATE TABLE portal_users(id,enabled,revision);INSERT INTO portal_users VALUES('u',1,1);
   CREATE TABLE nodes(id,lifecycle,managed);INSERT INTO nodes VALUES('n','active',1);
   CREATE TABLE portal_sessions(revoked);INSERT INTO portal_sessions VALUES(0);
   CREATE TABLE portal_authorizations(consumed);INSERT INTO portal_authorizations VALUES(0);
   CREATE TABLE portal_leases(id,node_id,upload,download,closed,revoked);INSERT INTO portal_leases VALUES('l','n',123,456,1,0);
   CREATE TABLE audit(at,action,subject);
   CREATE TABLE lifetime(upload,download);INSERT INTO lifetime VALUES(123,456);""")
  return data,backups

 def test_gate_requires_matching_node_final_and_preserves_history(self):
  with tempfile.TemporaryDirectory() as temp:
   data,backups=self.fixture(Path(temp).resolve());state=backups/'reset'
   with patch.object(migration,'DATA',data),patch.object(migration,'BACKUPS',backups):
    migration.hold(state)
    info=json.loads((state/'migration-state.json').read_text())
    receipts=[dict(nodeId='n',checkedAt=info['startedAt'],ledgerSHA256='a'*64,pendingFinals=0,openLeases=0,finals=[dict(id='l',upload=123,download=456)])]
    evidence=state/'incoming.json';evidence.write_text(json.dumps(receipts))
    receipts[0]['finals'][0]['download']=457;evidence.write_text(json.dumps(receipts))
    with self.assertRaisesRegex(ValueError,'mismatch'):migration.confirm(state,evidence)
    receipts[0]['finals'][0]['download']=456;evidence.write_text(json.dumps(receipts))
    migration.confirm(state,evidence)
    with closing(sqlite3.connect(data/'hqip.db')) as db:
     self.assertEqual(db.execute("SELECT value FROM metadata WHERE key='subscriptions_reset_ready'").fetchone(),('true',))
     self.assertEqual(db.execute('SELECT * FROM lifetime').fetchone(),(123,456))
    with closing(sqlite3.connect(state/'hqip.db')) as db:
     self.assertEqual(db.execute('SELECT enabled FROM portal_users').fetchone(),(1,))

 def test_unsettled_and_stale_receipts_are_rejected(self):
  with tempfile.TemporaryDirectory() as temp:
   data,_=self.fixture(Path(temp).resolve())
   info={'nodes':['n'],'startedAt':100}
   with closing(sqlite3.connect(data/'hqip.db')) as db:
    for receipts in [[],[dict(nodeId='n',checkedAt=99,pendingFinals=0,openLeases=0)], [dict(nodeId='n',checkedAt=101,pendingFinals=1,openLeases=0)]]:
     with self.assertRaises(ValueError):migration.validate_receipts(db,info,receipts,102)
    db.execute('UPDATE portal_leases SET closed=0');db.commit()
    with self.assertRaisesRegex(ValueError,'unsettled'):migration.validate_receipts(db,info,[dict(nodeId='n')],102)

 def test_abort_cannot_rewind_new_schema_or_accounting(self):
  with tempfile.TemporaryDirectory() as temp:
   data,backups=self.fixture(Path(temp).resolve());state=backups/'reset'
   with patch.object(migration,'DATA',data),patch.object(migration,'BACKUPS',backups):
    migration.hold(state)
    with closing(sqlite3.connect(data/'hqip.db')) as db,db:db.execute('UPDATE lifetime SET upload=200')
    migration.abort_hold(state)
    with closing(sqlite3.connect(data/'hqip.db')) as db,db:
     self.assertEqual(db.execute('SELECT upload FROM lifetime').fetchone(),(200,))
     self.assertEqual(db.execute('SELECT enabled FROM portal_users').fetchone(),(1,))
     self.assertEqual(db.execute('SELECT revoked FROM portal_sessions').fetchone(),(1,))
     db.execute("UPDATE metadata SET value='5' WHERE key='schema_version'")
    with self.assertRaisesRegex(ValueError,'Schema advanced'):migration.abort_hold(state)

 def test_old_abort_cannot_cancel_new_hold_or_changed_key(self):
  with tempfile.TemporaryDirectory() as temp:
   data,backups=self.fixture(Path(temp).resolve());state=backups/'first'
   with patch.object(migration,'DATA',data),patch.object(migration,'BACKUPS',backups):
    migration.hold(state)
    held=(state/'migration-state.json').read_text()
    (data/'master.key').write_bytes(b'other-key')
    with self.assertRaisesRegex(ValueError,'Master key changed'):migration.abort_hold(state)
    (data/'master.key').write_bytes(b'paired-key')
    migration.abort_hold(state)
    migration.hold(backups/'second')
    with self.assertRaisesRegex(ValueError,'no longer active'):migration.abort_hold(state)
    # A crash after committing abort but before persisting its local phase can
    # leave an apparently active old file. The DB generation must still win.
    (state/'migration-state.json').write_text(held)
    with self.assertRaisesRegex(ValueError,'different hold'):migration.abort_hold(state)
    with closing(sqlite3.connect(data/'hqip.db')) as db:
     self.assertEqual(db.execute('SELECT enabled FROM portal_users').fetchone(),(0,))
     self.assertEqual(db.execute("SELECT value FROM metadata WHERE key='portal_recovery_hold'").fetchone(),('true',))

 def test_confirm_requires_durable_evidence_before_ready_and_can_resume(self):
  with tempfile.TemporaryDirectory() as temp:
   data,backups=self.fixture(Path(temp).resolve());state=backups/'reset'
   with patch.object(migration,'DATA',data),patch.object(migration,'BACKUPS',backups):
    migration.hold(state)
    info=json.loads((state/'migration-state.json').read_text())
    receipts=[dict(nodeId='n',checkedAt=info['startedAt'],ledgerSHA256='a'*64,pendingFinals=0,openLeases=0,finals=[dict(id='l',upload=123,download=456)])]
    evidence=state/'incoming.json';evidence.write_text(json.dumps(receipts))
    original=migration.write_private
    def fail_evidence(path,value):
     if path.name=='node-receipts.json':raise OSError('disk full')
     original(path,value)
    with patch.object(migration,'write_private',side_effect=fail_evidence):
     with self.assertRaisesRegex(OSError,'disk full'):migration.confirm(state,evidence)
    with closing(sqlite3.connect(data/'hqip.db')) as db:
     self.assertEqual(db.execute("SELECT value FROM metadata WHERE key='subscriptions_reset_ready'").fetchone(),('false',))
    def observe_order(path,value):
     with closing(sqlite3.connect(data/'hqip.db')) as db:
      self.assertEqual(db.execute("SELECT value FROM metadata WHERE key='subscriptions_reset_ready'").fetchone(),('false',))
     original(path,value)
    with patch.object(migration,'write_private',side_effect=observe_order):migration.confirm(state,evidence)
    self.assertEqual(json.loads((state/'node-receipts.json').read_text()),receipts)
    migration.confirm(state,evidence)  # interrupted final output is safe to retry

 def node_fixture(self, root, stack):
  tools=migration.node_tools
  for name in ['CONFIG_ROOT','STATE_ROOT','UNIT_ROOT']:
   path=root/name;path.mkdir()
   stack.enter_context(patch.object(tools,name,path))
  backups=root/'backups';backups.mkdir();stack.enter_context(patch.object(migration,'BACKUPS',backups))
  node='node-a';config=tools.CONFIG_ROOT/node;state=tools.STATE_ROOT/node;config.mkdir();state.mkdir()
  cfg=dict(nodeId=node,nodeDir=str(config),managed=True,controllerId='fixture',listen='127.0.0.1:19001',statsUrl='http://127.0.0.1:19002',statePath=str(state/'state.db'))
  (config/'agent.json').write_text(json.dumps(cfg));(config/'hysteria.yaml').write_text('listen: 192.0.2.1:443\n')
  for kind,unit in tools.node_units(node).items():
   description=f'HQIP node agent {node}' if kind=='agent' else f'HQIP Hysteria node {node}'
   argument=config/('agent.json' if kind=='agent' else 'hysteria.yaml')
   (tools.UNIT_ROOT/unit).write_text(f'[Unit]\nDescription={description}\n[Service]\nExecStart=/owned/{kind} --config {argument}\n')
  def command(*args):
   self.assertEqual(args[:5],('systemctl','show','--value','-p','ExecStart'))
   return types.SimpleNamespace(stdout=(tools.UNIT_ROOT/args[-1]).read_text())
  stack.enter_context(patch.object(tools,'command',side_effect=command))
  with closing(sqlite3.connect(state/'state.db')) as db,db:
   db.executescript("""CREATE TABLE lease_usage(lease_id,upload,download,closed,dirty);
   INSERT INTO lease_usage VALUES('l',123,456,1,0);
   CREATE TABLE usage(device_id,upload,download);INSERT INTO usage VALUES('user_old',123,456);
   CREATE TABLE meta(key,value);INSERT INTO meta VALUES('baseline','keep');""")
  return node,config,state,backups,cfg

 def test_node_receipt_verifies_owned_runtime_and_preserves_exact_ledger(self):
  with tempfile.TemporaryDirectory() as temp, ExitStack() as stack:
   node,config,state,backups,_=self.node_fixture(Path(temp).resolve(),stack)
   before=(state/'state.db').read_bytes()
   receipt=migration.node_receipt(node,backups/'receipt')
   self.assertEqual((state/'state.db').read_bytes(),before)
   self.assertEqual(receipt['finals'],[dict(id='l',upload=123,download=456)])
   self.assertEqual(receipt['ledgerSHA256'],hashlib.sha256((backups/'receipt'/'state.db').read_bytes()).hexdigest())
   with closing(sqlite3.connect(backups/'receipt'/'state.db')) as db:
    self.assertEqual(db.execute('SELECT * FROM usage').fetchone(),('user_old',123,456))
    self.assertEqual(db.execute('SELECT * FROM meta').fetchone(),('baseline','keep'))

 def test_node_receipt_rejects_wrong_identity_path_runtime_and_dirty_final(self):
  with tempfile.TemporaryDirectory() as temp, ExitStack() as stack:
   node,config,state,backups,cfg=self.node_fixture(Path(temp).resolve(),stack)
   original=dict(cfg)
   cfg['nodeId']='another-node';(config/'agent.json').write_text(json.dumps(cfg))
   with self.assertRaisesRegex(ValueError,'ownership'):migration.node_receipt(node,backups/'wrong-id')
   cfg=dict(original);cfg['statePath']=str(state.parent/'another.db');(config/'agent.json').write_text(json.dumps(cfg))
   with self.assertRaisesRegex(ValueError,'ledger path'):migration.node_receipt(node,backups/'wrong-ledger')
   (config/'agent.json').write_text(json.dumps(original))
   with patch.object(migration.node_tools,'command',return_value=types.SimpleNamespace(stdout='--config /different/agent.json')):
    with self.assertRaisesRegex(ValueError,'Effective service'):migration.node_receipt(node,backups/'wrong-runtime')
   with closing(sqlite3.connect(state/'state.db')) as db,db:db.execute('UPDATE lease_usage SET dirty=1')
   with self.assertRaisesRegex(ValueError,'unacknowledged'):migration.node_receipt(node,backups/'pending')
   self.assertEqual(list(backups.iterdir()),[])

 def test_receipt_checks_snapshot_even_if_live_preflight_was_settled(self):
  with tempfile.TemporaryDirectory() as temp, ExitStack() as stack:
   node,_,state,backups,_=self.node_fixture(Path(temp).resolve(),stack)
   original=migration.backup_db
   def concurrent_final(source,target):
    with closing(sqlite3.connect(state/'state.db')) as db,db:db.execute('UPDATE lease_usage SET dirty=1')
    original(source,target)
   with patch.object(migration,'backup_db',side_effect=concurrent_final):
    with self.assertRaisesRegex(ValueError,'unacknowledged'):migration.node_receipt(node,backups/'receipt')
   self.assertFalse((backups/'receipt'/'receipt.json').exists())


if __name__=='__main__':unittest.main()
