"""Exercise upgrade recovery against a disposable filesystem and fake systemd."""
import hashlib
import io
import json
import os
import pathlib
import tempfile
import unittest
from contextlib import ExitStack, redirect_stdout
from unittest.mock import patch

import upgrade_control as upgrade


class UpgradeSafetyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.base = self.root / 'opt/hqip-control'
        self.data = self.root / 'var/lib/hqip-control'
        self.backups = self.root / 'root/backups'
        self.data.mkdir(parents=True)
        self.backups.mkdir(parents=True)
        (self.data / 'hqip.db').write_bytes(b'old-database')
        (self.data / 'master.key').write_bytes(b'old-key-paired-with-old-database')
        (self.data / 'hqip.db-wal').write_bytes(b'old-wal')
        self.previous = self.release('a')
        self.new = self.release('b')
        try:
            (self.base / 'current').symlink_to(self.previous, target_is_directory=True)
        except OSError as error:
            self.skipTest('Directory symlinks unavailable: ' + str(error))
        self.state = self.backups / 'upgrade-test'
        self.commands = []
        self.running = True
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.object(upgrade, 'BASE', self.base))
        self.stack.enter_context(patch.object(upgrade, 'DATA', self.data))
        self.stack.enter_context(patch.object(upgrade, 'Path', self.safe_path))
        self.stack.enter_context(patch.object(upgrade, 'run', side_effect=self.command))
        self.stack.enter_context(patch.object(upgrade, 'healthy'))
        if os.name == 'nt':
            # Windows cannot atomically rename over a directory symlink as
            # Linux can. Only emulate that OS primitive; release verification,
            # snapshots, failed-data preservation and rollback remain real.
            self.stack.enter_context(patch.object(upgrade, 'point_current', side_effect=self.point_current_windows))
        self.stack.enter_context(redirect_stdout(io.StringIO()))

    def point_current_windows(self, release):
        current = self.base / 'current'
        current.unlink(missing_ok=True)
        current.symlink_to(release, target_is_directory=True)

    def safe_path(self, value):
        # Redirect every production absolute path touched by the module. Paths
        # already under our temporary tree keep normal pathlib semantics.
        if str(value).startswith(('/var/', '/etc/', '/root/', '/opt/')):
            return self.root / str(value).lstrip('/')
        return pathlib.Path(value)

    def command(self, *args):
        self.commands.append(args)
        if args[0] == 'systemctl':
            self.assertEqual(args[-1], upgrade.SERVICE, 'Touched another service')
            if args[1] == 'stop':
                self.running = False
            elif args[1] == 'start':
                self.running = True
            elif args[1] == 'is-active':
                return 'active' if self.running else 'inactive'
        elif args[0] not in ('chown', 'chmod'):
            self.fail('Unexpected host command: ' + repr(args))
        return ''

    def release(self, letter):
        release = self.base / 'releases' / (letter * 40)
        (release / 'artifacts').mkdir(parents=True)
        files = {'hqip': b'executable-' + letter.encode(),
                 'artifacts/manifest.json': b'{"version":"fixed"}',
                 'artifacts/agent-linux-amd64': b'agent-' + letter.encode()}
        for name, content in files.items():
            (release / name).write_bytes(content)
        manifest = {'sourceSHA': letter * 40,
                    'sha256': {name: hashlib.sha256(content).hexdigest()
                               for name, content in files.items()}}
        (release / 'release.json').write_text(json.dumps(manifest))
        return release

    def test_success_uses_cold_snapshot_and_touches_only_controller(self):
        real_copy = upgrade.shutil.copytree

        def snapshot(source, destination, *args, **kwargs):
            if pathlib.Path(source) == self.data:
                self.assertFalse(self.running, 'Snapshot taken with service running')
            return real_copy(source, destination, *args, **kwargs)

        with patch.object(upgrade.shutil, 'copytree', side_effect=snapshot):
            upgrade.upgrade(self.new, self.state)
        self.assertEqual((self.base / 'current').resolve(), self.new)
        self.assertEqual((self.state / 'data.before/hqip.db').read_bytes(), b'old-database')
        self.assertEqual((self.state / 'data.before/hqip.db-wal').read_bytes(), b'old-wal')
        self.assertEqual(json.loads((self.state / 'state.json').read_text())['phase'], 'active')
        self.assertTrue(self.running)
        self.assertEqual([cmd for cmd in self.commands if cmd[0] == 'systemctl'],
                         [('systemctl', 'stop', upgrade.SERVICE),
                          ('systemctl', 'start', upgrade.SERVICE)])

    def test_health_failure_restores_paired_data_and_keeps_failed_database(self):
        checks = 0

        def health():
            nonlocal checks
            checks += 1
            if checks == 1:
                (self.data / 'hqip.db').write_bytes(b'new-migrated-database')
                (self.data / 'master.key').write_bytes(b'new-key')
                (self.data / 'new-data').write_bytes(b'only-in-failed-release')
                raise RuntimeError('new release unhealthy')
            self.assertEqual((self.base / 'current').resolve(), self.previous)
            self.assertEqual((self.data / 'hqip.db').read_bytes(), b'old-database')
            self.assertEqual((self.data / 'master.key').read_bytes(), b'old-key-paired-with-old-database')

        with patch.object(upgrade, 'healthy', side_effect=health):
            with self.assertRaisesRegex(RuntimeError, 'new release unhealthy'):
                upgrade.upgrade(self.new, self.state)
        self.assertEqual(checks, 2)
        self.assertFalse((self.data / 'new-data').exists())
        failed = list(self.state.glob('failed-data-*'))
        self.assertEqual(len(failed), 1)
        self.assertEqual((failed[0] / 'hqip.db').read_bytes(), b'new-migrated-database')
        self.assertEqual((failed[0] / 'master.key').read_bytes(), b'new-key')
        self.assertEqual(json.loads((self.state / 'state.json').read_text())['phase'], 'rolled-back')
        self.assertTrue(self.running)

    def test_snapshot_failure_restores_old_service_without_destroying_data(self):
        with patch.object(upgrade.shutil, 'copytree', side_effect=OSError('disk full')):
            with self.assertRaisesRegex(OSError, 'disk full'):
                upgrade.upgrade(self.new, self.state)
        self.assertTrue(self.running)
        self.assertEqual((self.base / 'current').resolve(), self.previous)
        self.assertEqual((self.data / 'hqip.db').read_bytes(), b'old-database')

    def test_invalid_target_manifest_never_stops_service(self):
        (self.new / 'hqip').write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            upgrade.upgrade(self.new, self.state)
        self.assertEqual(self.commands, [])
        self.assertFalse(self.state.exists())

    def test_unverifiable_previous_release_blocks_before_stop(self):
        (self.previous / 'hqip').write_bytes(b'previous-tampered')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            upgrade.upgrade(self.new, self.state)
        self.assertEqual(self.commands, [])
        self.assertTrue(self.running)

    def test_manifest_rejects_nested_exemptions_and_symlinks(self):
        nested = self.new / 'artifacts/release.json'
        nested.write_text('{"unmanifested":true}')
        with self.assertRaisesRegex(ValueError, 'Unmanifested'):
            upgrade.verify_release(self.new)
        nested.unlink()
        outside = self.root / 'outside'
        outside.write_bytes(b'outside')
        (self.new / 'link').symlink_to(outside)
        with self.assertRaisesRegex(ValueError, 'Symlink'):
            upgrade.verify_release(self.new)

    def test_manifest_rejects_path_escape_and_invalid_source_commit(self):
        manifest = json.loads((self.new / 'release.json').read_text())
        outside = self.base / 'releases/outside'
        outside.write_bytes(b'outside')
        manifest['sha256']['../outside'] = hashlib.sha256(b'outside').hexdigest()
        (self.new / 'release.json').write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'Unsafe'):
            upgrade.verify_release(self.new)
        manifest['sha256'].pop('../outside')
        manifest['sourceSHA'] = 'main'
        (self.new / 'release.json').write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'commit'):
            upgrade.verify_release(self.new)
        with self.assertRaisesRegex(ValueError, 'direct child'):
            upgrade.verify_release(self.root)

    def test_rollback_incomplete_snapshot_never_stops_current_service(self):
        self.state.mkdir()
        (self.state / 'data.before').mkdir()
        (self.state / 'data.before/hqip.db').write_bytes(b'unpaired-database')
        (self.state / 'state.json').write_text(json.dumps({'previous': str(self.previous)}))
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            upgrade.rollback(self.state)
        self.assertEqual(self.commands, [])
        self.assertEqual((self.data / 'master.key').read_bytes(), b'old-key-paired-with-old-database')

    def test_rollback_rejects_symlink_snapshot_before_service_stop(self):
        self.state.mkdir()
        (self.state / 'state.json').write_text(json.dumps({'previous': str(self.previous)}))
        (self.state / 'data.before').symlink_to(self.data, target_is_directory=True)
        with self.assertRaises((ValueError, RuntimeError)):
            upgrade.rollback(self.state)
        self.assertEqual(self.commands, [])


if __name__ == '__main__':
    unittest.main()
