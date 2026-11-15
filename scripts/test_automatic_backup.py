from contextlib import nullcontext
from datetime import datetime, timedelta, timezone
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

import automatic_backup as backup


class AutomaticBackupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / 'backups' / 'automatic'
        self.data = self.base / 'data'
        self.data.mkdir()
        self.now = datetime(2026, 11, 15, 4, tzinfo=timezone.utc)
        self.group = patch.object(backup, 'application_group', return_value=None)
        self.group.start()
        self.addCleanup(self.group.stop)
        # Tests run on Windows and Linux without the production OS account.
        self.owner = patch.object(backup.os, 'fchown', create=True)
        self.owner.start()
        self.addCleanup(self.owner.stop)
        self.chown = patch.object(backup.os, 'chown', create=True)
        self.chown.start()
        self.addCleanup(self.chown.stop)

    def fixture(self, at, suffix='a' * 16):
        run = at.strftime('%Y%m%dT%H%M%SZ') + '-' + suffix
        path = self.root / ('hqip-' + run)
        path.mkdir(parents=True)
        (path / 'archive.tar.gz').write_bytes(b'fixture')
        (path / 'metadata.json').write_text(json.dumps({'version': 1, 'runId': run, 'createdAt': backup.stamp(at)}))
        return path

    def test_schedule_is_beijing_and_retention_is_exact_time(self):
        self.assertEqual(backup.next_run(self.now), '2026-11-15T10:00:00Z')
        old = self.fixture(self.now - timedelta(days=14))
        kept = self.fixture(self.now - timedelta(days=14) + timedelta(seconds=1))
        manual = self.root / 'manual'
        manual.mkdir()
        (manual / 'precious').write_text('keep')
        outside = self.root.parent / 'hqip-ci-release'
        outside.mkdir()
        self.assertEqual(backup.inventory(self.root, self.now), (1, 1))
        self.assertFalse(old.exists())
        self.assertTrue(kept.exists())
        self.assertTrue((manual / 'precious').exists())
        self.assertTrue(outside.exists())

    def test_owned_directory_extra_contents_are_never_deleted(self):
        path = self.fixture(self.now - timedelta(days=15))
        (path / 'unexpected').write_text('keep')
        with self.assertRaisesRegex(ValueError, 'contents'):
            backup.inventory(self.root, self.now)
        self.assertTrue((path / 'archive.tar.gz').exists())

    def test_symlink_in_owned_backup_is_rejected(self):
        path = self.fixture(self.now - timedelta(days=15))
        target = self.base / 'outside'
        target.write_text('keep')
        (path / 'archive.tar.gz').unlink()
        (path / 'archive.tar.gz').symlink_to(target)
        with self.assertRaises(ValueError):
            backup.inventory(self.root, self.now)
        self.assertEqual(target.read_text(), 'keep')

    def run_backup(self, writer, retry=False, now=None, lock=lambda _: nullcontext()):
        self.timers = []
        result = backup.run_backup(retry, root=self.root, base=self.base, data=self.data,
                                   now=now or self.now, archive_writer=writer,
                                   timer=self.timers.append, lock=lock)
        state = json.loads((self.data / 'backup-status.json').read_text())
        return result, state

    def test_success_publishes_safe_receipt_and_cancels_retry(self):
        result, state = self.run_backup(lambda *_: (123, 'a' * 64))
        self.assertEqual(result, 0)
        self.assertEqual(state['lastRun']['status'], 'success')
        self.assertEqual(state['lastRun']['retainedCount'], 0)
        self.assertEqual(self.timers, [False])
        event = json.loads(next((self.data / 'backup-events').glob('*.json')).read_text())
        self.assertEqual(event['action'], 'backup.completed')
        self.assertNotIn('path', event)
        self.assertNotIn('password', json.dumps(state))

    def test_failure_retries_twice_at_ten_minutes_without_sensitive_error(self):
        def fail(*_):
            raise RuntimeError('postgres://secret@example/private')
        result, first = self.run_backup(fail)
        self.assertEqual(result, 1)
        self.assertEqual(first['retryAt'], backup.stamp(self.now + timedelta(minutes=10)))
        self.assertEqual(self.timers, [True])
        for number in (2, 3):
            result, state = self.run_backup(fail, retry=True, now=self.now + timedelta(minutes=10 * (number - 1)))
            self.assertEqual(state['lastRun']['attempt'], number)
            self.assertEqual(state['lastRun']['runId'], first['lastRun']['runId'])
        self.assertEqual(self.timers, [False])
        self.assertNotIn('secret', (self.data / 'backup-status.json').read_text())
        count = len(list((self.data / 'backup-events').glob('*.json')))
        backup.run_backup(True, root=self.root, base=self.base, data=self.data,
                          now=self.now + timedelta(hours=1), archive_writer=fail,
                          timer=lambda _: self.fail('unexpected retry'), lock=lambda _: nullcontext())
        self.assertEqual(len(list((self.data / 'backup-events').glob('*.json'))), count)

    def test_deployment_lock_contention_defers_without_running_dump(self):
        def busy(_):
            raise BlockingIOError('busy')
        result, state = self.run_backup(lambda *_: self.fail('dump must not run'), lock=busy)
        self.assertEqual(result, 1)
        self.assertEqual(state['lastRun']['errorCode'], 'deployment_busy')

    def test_corrupt_archive_and_wrong_key_rejected(self):
        archive = self.base / 'archive.tar.gz'
        payload = {'hqip.pg.dump': b'PGDMPdatabase', 'master.key': b'k' * 32,
                   'manifest.json': json.dumps({'version': 1, 'backend': 'postgresql', 'containsSecrets': True}).encode()}
        def write():
            with tarfile.open(archive, 'w:gz') as out:
                for name, raw in payload.items():
                    member = tarfile.TarInfo(name)
                    member.size = len(raw)
                    out.addfile(member, io.BytesIO(raw))
        write()
        backup.validate_archive(archive)
        raw = bytearray(archive.read_bytes())
        raw[-8] ^= 0xff
        archive.write_bytes(raw)
        with self.assertRaises(Exception):
            backup.validate_archive(archive)
        payload['master.key'] = b'invalid'
        write()
        with self.assertRaisesRegex(ValueError, 'key'):
            backup.validate_archive(archive)
        payload['master.key'] = b'k' * 32
        payload['hqip.pg.dump'] = b'not postgres'
        write()
        with self.assertRaisesRegex(ValueError, 'PostgreSQL'):
            backup.validate_archive(archive)

    def test_archive_writer_publishes_only_complete_archive_and_safe_metadata(self):
        release = self.base / 'releases' / ('a' * 40)
        release.mkdir(parents=True)
        (release / 'hqip').write_bytes(b'test executable')
        (self.base / 'current').symlink_to(release, target_is_directory=True)
        backup.initialize_paths(self.root, self.data)
        run = {'runId': '20261115T040000Z-' + 'b' * 16, 'startedAt': backup.stamp(self.now)}
        env = self.base / 'storage.env'
        env.write_text('HQIP_DATABASE_URL="postgres://hqip_app:fake-secret@127.0.0.1/hqip"\nHQIP_REDIS_URL="redis://127.0.0.1"\n')
        env.chmod(0o600)
        def execute(arguments, **options):
            self.assertNotIn('fake-secret', repr(arguments))
            self.assertEqual(arguments[1], 'backup')
            self.assertNotIn('stop', arguments)
            self.assertEqual(options['env']['PATH'], '/usr/bin:/bin')
            destination = Path(arguments[-1])
            self.assertFalse((self.root / ('hqip-' + run['runId'])).exists())
            payload = {'hqip.pg.dump': b'PGDMPtestdump', 'master.key': b'x' * 32,
                       'manifest.json': json.dumps({'version': 1, 'backend': 'postgresql', 'containsSecrets': True}).encode()}
            with tarfile.open(destination, 'w:gz') as out:
                for name, raw in payload.items():
                    item = tarfile.TarInfo(name)
                    item.size = len(raw)
                    out.addfile(item, io.BytesIO(raw))
            return SimpleNamespace(returncode=0)
        with patch.object(backup, 'ENVIRONMENT', env):
            size, digest = backup.create_archive(self.root, self.base, self.data, run, execute=execute)
        folder = self.root / ('hqip-' + run['runId'])
        self.assertEqual(size, (folder / 'archive.tar.gz').stat().st_size)
        self.assertEqual(digest, backup.checksum(folder / 'archive.tar.gz'))
        self.assertEqual(json.loads((folder / 'metadata.json').read_text())['sourceSHA'], 'a' * 40)
        self.assertFalse(list(self.root.glob('.partial-*')))
        self.assertEqual(backup.create_archive(self.root, self.base, self.data, run,
            execute=lambda *_args, **_kw: self.fail('published retry must not dump again')), (size, digest))
        run['runId'] = '20261115T040000Z-' + 'c' * 16
        with patch.object(backup, 'ENVIRONMENT', env), self.assertRaises(RuntimeError):
            backup.create_archive(self.root, self.base, self.data, run,
                                  execute=lambda *_args, **_kw: SimpleNamespace(returncode=1))
        self.assertFalse(list(self.root.glob('.partial-*')))
        self.assertFalse((self.root / ('hqip-' + run['runId'])).exists())

    def test_low_disk_does_not_run_dump_but_reports_failure(self):
        with patch.object(backup.shutil, 'disk_usage', return_value=SimpleNamespace(free=100)):
            result, state = self.run_backup(lambda *_: self.fail('low disk must not dump'))
        self.assertEqual(result, 1)
        self.assertEqual(state['lastRun']['status'], 'failed')

    def test_systemd_interrupted_job_is_failed_and_retried_once_per_attempt(self):
        _, state = self.run_backup(lambda *_: (123, 'a' * 64))
        state['lastRun']['status'] = 'running'
        backup.publish_json(self.data / 'backup-status.json', state, shared=True)
        timers = []
        backup.record_abnormal_exit(data=self.data, now=self.now, timer=timers.append)
        after = json.loads((self.data / 'backup-status.json').read_text())
        self.assertEqual(after['lastRun']['errorCode'], 'backup_interrupted')
        self.assertEqual(timers, [True])
        backup.record_abnormal_exit(data=self.data, now=self.now, timer=timers.append)
        self.assertEqual(timers, [True])

    def test_old_partial_archives_are_cleaned_without_following_links(self):
        backup.initialize_paths(self.root, self.data)
        stale = self.root / ('.partial-' + (self.now - timedelta(days=15)).strftime('%Y%m%dT%H%M%SZ') + '-' + 'd' * 16)
        stale.mkdir()
        (stale / 'archive.tar.gz').write_bytes(b'incomplete')
        recent = self.root / ('.partial-' + self.now.strftime('%Y%m%dT%H%M%SZ') + '-' + 'e' * 16)
        recent.mkdir()
        backup.prune_incomplete(self.root, self.now)
        self.assertFalse(stale.exists())
        self.assertTrue(recent.exists())
        with self.assertRaises(ValueError):
            backup.discard_partial(self.root, self.base)


if __name__ == '__main__':
    unittest.main()
