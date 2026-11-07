from contextlib import closing
from pathlib import Path
from unittest.mock import patch
import hashlib,json,sqlite3,tempfile,unittest
from contextlib import ExitStack
import render_domains as render
import upgrade_domains as deploy
import upgrade_control as control

class DomainReleaseTests(unittest.TestCase):
 def test_nginx_static_and_api_separation(self):
  text=render.render()
  self.assertEqual(text, (Path(__file__).resolve().parents[1]/'deploy/nginx-hqip.conf').read_text(encoding='utf-8'))
  for host in [render.USER,render.ADMIN,render.API]:self.assertEqual(text.count('server_name '+host+';'),2)
  self.assertNotIn('$request_uri\'',text)
  self.assertIn('return 410;',text)
  self.assertIn('root /opt/hqip-control/current/user-web',text)
  self.assertIn('root /opt/hqip-control/current/admin-web',text)
  self.assertIn('api/v[23]/client',text)
  self.assertIn('real_ip_header CF-Connecting-IP',text)
  self.assertNotIn('*.sharevps.online',text)
  self.assertNotIn('5202345.xyz',text)
  self.assertNotIn('http2 on;',text)
  self.assertEqual(text.count('listen 443 ssl http2;'),3)
  self.assertEqual(text.count('listen [::]:443 ssl http2;'),3)
  self.assertIn('map $request_uri $hqip_user_csp',text)
  self.assertIn('~^/(login|register|authorize)(\\?|$)',text)
 def test_official_domains_match_service_and_certificate_paths(self):
  self.assertEqual((render.USER,render.ADMIN,render.API),('hqip.sharevps.online','cms-hqip.sharevps.online','api-hqip.sharevps.online'))
  text=render.render()
  unit=(Path(__file__).resolve().parents[1]/'deploy/hqip-control.service').read_text(encoding='utf-8')
  for host in (render.USER,render.ADMIN,render.API):
   self.assertIn(f'ssl_certificate /etc/letsencrypt/live/{host}/fullchain.pem;',text)
   self.assertIn(f'ssl_certificate_key /etc/letsencrypt/live/{host}/privkey.pem;',text)
  for flag,host in [('admin-origin',render.ADMIN),('user-origin',render.USER),('public-url',render.API)]:
   self.assertIn(f'--{flag} https://{host} ',unit)
  self.assertIn('--listen 127.0.0.1:18090 --public-listen 127.0.0.1:18091',unit)
  self.assertNotIn('5202345.xyz',unit)
 def test_retiring_feishu_preserves_business_usage(self):
  with tempfile.TemporaryDirectory() as folder:
   root=Path(folder)
   with closing(sqlite3.connect(root/'hqip.db')) as d,d:
    d.executescript("CREATE TABLE feishu_settings(enabled,webhook,secret);INSERT INTO feishu_settings VALUES(1,'encrypted','encrypted');CREATE TABLE notifications(status,claim,claim_until,next_attempt);INSERT INTO notifications VALUES('retry','x',99,99);CREATE TABLE usage(bytes);INSERT INTO usage VALUES(12345);")
   with patch.object(control,'DATA',root):deploy.disable_feishu()
   with closing(sqlite3.connect(root/'hqip.db')) as d:
    self.assertEqual(d.execute('SELECT * FROM feishu_settings').fetchone(),(0,'',''))
    self.assertEqual(d.execute('SELECT status FROM notifications').fetchone(),('cancelled',))
    self.assertEqual(d.execute('SELECT bytes FROM usage').fetchone(),(12345,))
 def test_state_must_be_direct_private_backup(self):
  for path in [Path('relative'),Path('/tmp/random'),Path('/root/backups/nested/state')]:
   with self.assertRaises(ValueError):deploy.check_state(path)
 def test_subscription_upgrade_requires_settled_migration_marker(self):
  with tempfile.TemporaryDirectory() as folder:
   root=Path(folder)
   with closing(sqlite3.connect(root/'hqip.db')) as db,db:
    db.executescript("CREATE TABLE metadata(key TEXT PRIMARY KEY,value);INSERT INTO metadata VALUES('schema_version','4');CREATE TABLE portal_leases(closed);INSERT INTO portal_leases VALUES(0);")
   with patch.object(control,'DATA',root):
    with self.assertRaisesRegex(ValueError,'not been prepared'):deploy.subscription_migration_ready()
    with closing(sqlite3.connect(root/'hqip.db')) as db,db:db.execute("INSERT INTO metadata VALUES('subscriptions_reset_ready','true')")
    with self.assertRaisesRegex(ValueError,'Unsettled'):deploy.subscription_migration_ready()
    with closing(sqlite3.connect(root/'hqip.db')) as db,db:db.execute('UPDATE portal_leases SET closed=1')
    deploy.subscription_migration_ready()
    with closing(sqlite3.connect(root/'hqip.db')) as db,db:db.execute("UPDATE metadata SET value='5' WHERE key='schema_version'")
    deploy.subscription_migration_ready()
 def test_rollback_keeps_new_accounting_and_disables_robot(self):
  with tempfile.TemporaryDirectory() as folder,ExitStack() as mocks:
   root=Path(folder).resolve();data=root/'data';data.mkdir();base=root/'base';base.mkdir();backups=root/'backups';backups.mkdir();state=backups/'release';state.mkdir()
   previous=base/'previous';previous.mkdir();release=base/'release';release.mkdir();(base/'current').symlink_to(release,target_is_directory=True)
   key=data/'master.key';key.write_bytes(b'original-paired-key')
   with closing(sqlite3.connect(data/'hqip.db')) as db,db:
    db.executescript("CREATE TABLE metadata(key,value);INSERT INTO metadata VALUES('schema_version','4');CREATE TABLE usage(bytes);INSERT INTO usage VALUES(987654);CREATE TABLE feishu_settings(enabled,webhook,secret);INSERT INTO feishu_settings VALUES(1,'ciphertext','ciphertext');CREATE TABLE notifications(status,claim,claim_until,next_attempt);INSERT INTO notifications VALUES('pending','x',99,99);")
   site=root/'nginx.conf';site.write_text('new-route');unit=root/'service';unit.write_text('new-unit');(state/'nginx.before.conf').write_text('old-route');(state/'unit.before.service').write_text('old-unit')
   (state/'domains-state.json').write_text(json.dumps({'previous':str(previous),'release':str(release),'masterSHA256':hashlib.sha256(key.read_bytes()).hexdigest()}))
   for obj,name,value in [(control,'DATA',data),(control,'BASE',base),(deploy,'BACKUPS',backups),(deploy,'SITE',site),(deploy,'UNIT',unit),(deploy,'PREP',root/'prepare')]:mocks.enter_context(patch.object(obj,name,value))
   mocks.enter_context(patch.object(control,'verify_release',return_value=(previous,{})));run=mocks.enter_context(patch.object(control,'run'));mocks.enter_context(patch.object(control,'healthy'));point=mocks.enter_context(patch.object(control,'point_current'))
   deploy.rollback(state)
   point.assert_called_once_with(previous)
   self.assertEqual(site.read_text(),'old-route');self.assertEqual(unit.read_text(),'old-unit')
   with closing(sqlite3.connect(data/'hqip.db')) as db:
    self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone(),(987654,));self.assertEqual(db.execute('SELECT * FROM feishu_settings').fetchone(),(0,'',''))
   key.write_bytes(b'unexpected-key');run.reset_mock()
   with self.assertRaisesRegex(ValueError,'Master key changed'):deploy.rollback(state)
   run.assert_not_called()

 def test_rollback_refuses_schema_advance_during_stop(self):
  with tempfile.TemporaryDirectory() as folder,ExitStack() as mocks:
   root=Path(folder).resolve();base=root/'base';base.mkdir();backups=root/'backups';backups.mkdir();state=backups/'release';state.mkdir()
   previous=base/'previous';previous.mkdir();release=base/'release';release.mkdir();(base/'current').symlink_to(release,target_is_directory=True)
   (state/'domains-state.json').write_text(json.dumps({'previous':str(previous),'release':str(release),'masterSHA256':'paired-key'}))
   mocks.enter_context(patch.object(control,'BASE',base));mocks.enter_context(patch.object(deploy,'BACKUPS',backups))
   mocks.enter_context(patch.object(control,'verify_release',return_value=(previous,{'schemaVersion':4})))
   mocks.enter_context(patch.object(deploy,'data_identity',return_value='paired-key'))
   mocks.enter_context(patch.object(deploy,'schema_version',side_effect=[4,5]))
   mocks.enter_context(patch.object(deploy,'require_billing_compatible'))
   run=mocks.enter_context(patch.object(control,'run'));point=mocks.enter_context(patch.object(control,'point_current'))
   with self.assertRaisesRegex(ValueError,'Schema advanced while stopping'):deploy.rollback(state)
   point.assert_not_called()
   self.assertEqual(run.call_args_list[0].args,('systemctl','stop',control.SERVICE))
   self.assertEqual(run.call_args_list[1].args,('systemctl','start',control.SERVICE))

if __name__=='__main__':unittest.main()
