"""CI host binding, real release retention, and the post-backup disk gate."""
from contextlib import ExitStack
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import deploy_ci as ci
import upgrade_domains as domains
from package_domains import package_release
from release_bundle import verify_full_release, verify_retained_release
import test_full_release as release_fixtures


class RetentionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve() / 'control'
        (self.base / 'releases').mkdir(parents=True)
        self.events = []
        self.clock = 1700000000

    def release(self, sha, previous=None):
        fixture = release_fixtures.FullReleaseTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        fixture.sha = sha
        fixture.nodes['sourceSHA'] = sha
        fixture.inner['sourceSHA'] = sha
        (fixture.root / 'dist/manifest.json').write_text(json.dumps(fixture.nodes))
        fixture.write_desktop()
        package_release(fixture.root, sha, previous)
        target = self.base / 'releases' / sha
        shutil.copytree(fixture.root / 'dist' / ('full-' + sha), target)
        self.clock += 10
        os.utime(target, (self.clock, self.clock))
        return target

    def current(self, release):
        (self.base / 'current').symlink_to(release, target_is_directory=True)

    def test_only_verified_older_full_release_is_removed_and_backup_data_are_untouched(self):
        old = self.release('1' * 40)
        previous = self.release('2' * 40, old.name)
        current = self.release('3' * 40, previous.name)
        self.current(current)
        for name in ('backups', 'data', '.ci-unfinished'):
            folder = self.base / ('releases' if name.startswith('.') else '') / name
            folder.mkdir()
            (folder / 'preserve').write_text('private data')
        report = ci.retain_releases(self.base, current.name, previous.name, verify_full_release, self.events.append)
        self.assertEqual(report['deleted'], [old.name])
        self.assertFalse(old.exists())
        self.assertTrue(previous.is_dir())
        self.assertTrue(current.is_dir())
        self.assertEqual((self.base / 'current').resolve(), current)
        self.assertEqual((self.base / 'backups/preserve').read_text(), 'private data')
        self.assertEqual((self.base / 'data/preserve').read_text(), 'private data')
        self.assertTrue((self.base / 'releases/.ci-unfinished/preserve').is_file())
        self.assertEqual([e['action'] for e in self.events if e['release'] == old.name], ['delete-start', 'deleted'])
        self.assertTrue(any(e['action'] == 'skip' and e['release'] == '.ci-unfinished' for e in self.events))

    def test_fresh_station_with_no_previous_keeps_only_current_full_release(self):
        old = self.release('1' * 40)
        current = self.release('2' * 40)
        self.current(current)
        report = ci.retain_releases(self.base, current.name, None, verify_full_release, self.events.append)
        self.assertEqual(report['deleted'], [old.name])
        self.assertEqual(list((self.base / 'releases').iterdir()), [current])

    def test_previous_full_release_without_recovery_operator_remains_valid_for_rollback(self):
        previous = self.release('2' * 40)
        manifest = json.loads((previous / 'release.json').read_text())
        del manifest['postgresRecoveryVersion']
        del manifest['sha256']['scripts/restore_postgres.py']
        (previous / 'scripts/restore_postgres.py').unlink()
        (previous / 'release.json').write_text(json.dumps(manifest))
        current = self.release('3' * 40, previous.name)
        self.current(current)
        report = ci.retain_releases(self.base, current.name, previous.name,
            lambda path: verify_retained_release(path, current.name), self.events.append)
        self.assertEqual(report['previous'], previous.name)
        self.assertEqual(report['deleted'], [])
        self.assertTrue(previous.is_dir())
        with self.assertRaisesRegex(ValueError, 'postgresRecoveryVersion'):
            verify_retained_release(previous, previous.name)

    def test_corrupt_nonfull_links_and_newer_directories_are_audited_not_deleted(self):
        corrupt = self.release('1' * 40)
        (corrupt / 'hqip').write_text('tampered executable')
        nonfull = self.base / 'releases' / ('2' * 40)
        nonfull.mkdir()
        (nonfull / 'release.json').write_text(json.dumps({'sourceSHA': nonfull.name, 'releaseKind': 'delta'}))
        current = self.release('3' * 40)
        os.utime(nonfull, (self.clock - 5, self.clock - 5))
        newer = self.release('4' * 40)
        outside = self.base / 'private'
        outside.mkdir()
        (outside / 'data').write_text('preserve')
        linked = self.base / 'releases' / ('5' * 40)
        linked.symlink_to(outside, target_is_directory=True)
        self.current(current)
        report = ci.retain_releases(self.base, current.name, None, verify_full_release, self.events.append)
        self.assertEqual(report['deleted'], [])
        self.assertEqual({entry['release'] for entry in report['skipped']}, {p.name for p in (corrupt, nonfull, newer, linked)})
        self.assertTrue(all(path.exists() for path in (corrupt, nonfull, newer, linked)))
        self.assertEqual((outside / 'data').read_text(), 'preserve')

    def test_current_or_previous_validation_failure_prevents_any_deletion(self):
        old = self.release('1' * 40)
        previous = self.release('2' * 40)
        current = self.release('3' * 40, previous.name)
        self.current(current)
        for target in (previous, current):
            original = (target / 'hqip').read_bytes()
            (target / 'hqip').write_text('changed')
            with self.assertRaisesRegex(ValueError, 'checksum'):
                ci.retain_releases(self.base, current.name, previous.name, verify_full_release, self.events.append)
            self.assertTrue(old.exists())
            (target / 'hqip').write_bytes(original)
        with self.assertRaisesRegex(ValueError, 'retained pair'):
            ci.retain_releases(self.base, current.name, None, verify_full_release, self.events.append)
        self.assertEqual(self.events, [])

    def test_active_pointer_change_during_verification_prevents_deletion(self):
        old = self.release('1' * 40)
        previous = self.release('2' * 40)
        current = self.release('3' * 40, previous.name)
        self.current(current)

        def verify(path):
            value = verify_full_release(path)
            if path == old:
                (self.base / 'current').unlink()
                (self.base / 'current').symlink_to(previous, target_is_directory=True)
            return value

        with self.assertRaisesRegex(ValueError, 'Active release changed'):
            ci.retain_releases(self.base, current.name, previous.name, verify, self.events.append)
        self.assertTrue(old.exists())


class CIGuardTests(unittest.TestCase):
    def test_only_exact_production_ssh_target_is_accepted(self):
        ci.validate_target('107.151.244.106', '22', 'root')
        for target in [('example.test', '22', 'root'), ('127.0.0.1', '22', 'root'),
                       ('107.151.244.106', '2222', 'root'), ('107.151.244.106', '022', 'root'),
                       ('107.151.244.106', '22', 'admin'), ('107.151.244.106 ', '22', 'root')]:
            with self.subTest(target=target), self.assertRaisesRegex(ValueError, 'target must be'):
                ci.validate_target(*target)

    def test_preflight_counts_both_upload_and_extraction_on_shared_filesystem(self):
        with tempfile.TemporaryDirectory() as temp:
            requirements = [(temp, 400), (str(Path(temp) / 'future-staging'), 600)]
            with patch.object(ci.shutil, 'disk_usage', return_value=SimpleNamespace(free=ci.MIN_FREE_BYTES + 999)):
                with self.assertRaisesRegex(ValueError, '3 GiB'):
                    ci.check_ci_space(requirements=requirements)
            with patch.object(ci.shutil, 'disk_usage', return_value=SimpleNamespace(free=ci.MIN_FREE_BYTES + 1000)):
                value = ci.check_ci_space(requirements=requirements)
                self.assertEqual(len(value), 1)
                self.assertEqual(value[0]['reservedBytes'], 1000)

    def test_remote_scripts_compile_without_importing_remote_release_code(self):
        compile(ci.remote_space_script(), '<space-check>', 'exec')
        compile(ci.remote_retention_script(), '<release-retention>', 'exec')

    def run_ci(self, public_error=None, preflight_error=None, target=None):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        (root / 'dist').mkdir()
        (root / 'scripts').mkdir()
        (root / 'scripts/install_ci_release.py').write_text('# test installer')
        archive = root / 'dist/release.tar.gz'
        with tarfile.open(archive, 'w:gz') as package:
            entry = tarfile.TarInfo('release.json')
            entry.size = 2
            package.addfile(entry, io.BytesIO(b'{}'))
        (root / 'dist/domains-release.json').write_text(json.dumps({'filename': archive.name, 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest()}))
        sha, previous = '3' * 40, '2' * 40
        events, ssh_calls = [], []

        def command(args, **kwargs):
            if args[0] == 'gh':
                return SimpleNamespace(stdout=sha)
            if args[0] == 'ssh':
                ssh_calls.append(args)
                if args[-1].startswith('cat '):
                    return SimpleNamespace(stdout=json.dumps({'sourceSHA': previous}))
                if args[-1].startswith('mktemp '):
                    events.append('temporary')
                    return SimpleNamespace(stdout='/tmp/hqip-ci.1234567890')
                script = kwargs.get('input', '')
                if script == 'SPACE':
                    events.append('space')
                    if preflight_error:
                        raise preflight_error
                elif script == 'RETENTION':
                    events.append('retention')
                elif script == '# test installer':
                    events.append('install')
                elif 'rollback' in args[-1]:
                    events.append('rollback')
                return SimpleNamespace(stdout='')
            if args[0] == 'scp':
                events.append('upload')
            return SimpleNamespace(stdout='')

        def public():
            events.append('public')
            if public_error:
                raise public_error

        environment = {'GITHUB_SHA': sha, 'GITHUB_REPOSITORY': 'test/repo', 'GITHUB_RUN_ID': '7',
                       'GITHUB_RUN_ATTEMPT': '1', 'DEPLOY_HOST': '107.151.244.106', 'DEPLOY_PORT': '22',
                       'DEPLOY_USER': 'root', 'DEPLOY_SSH_KEY': 'test-key', 'DEPLOY_KNOWN_HOSTS': 'test-pin'}
        environment.update(target or {})
        with patch.dict(os.environ, environment, clear=True), patch.object(ci, 'ROOT', root), \
                patch.object(ci, 'command', side_effect=command), patch.object(ci, 'check_public', side_effect=public), \
                patch.object(ci, 'remote_space_script', return_value='SPACE'), \
                patch.object(ci, 'remote_retention_script', return_value='RETENTION'):
            error = None
            try:
                ci.main()
            except Exception as caught:
                error = caught
        return events, ssh_calls, error

    def test_retention_only_runs_after_successful_install_and_public_checks(self):
        events, commands, error = self.run_ci()
        self.assertIsNone(error)
        self.assertEqual(events, ['space', 'temporary', 'upload', 'install', 'public', 'retention'])
        self.assertTrue(commands)
        self.assertTrue(all('StrictHostKeyChecking=yes' in args and 'root@107.151.244.106' in args for args in commands))

    def test_public_failure_rolls_back_without_retention(self):
        events, _, error = self.run_ci(public_error=ValueError('unready'))
        self.assertIsInstance(error, ValueError)
        self.assertNotIn('retention', events)
        self.assertEqual(events[-2:], ['public', 'rollback'])

    def test_low_space_prevents_upload_and_switch(self):
        events, _, error = self.run_ci(preflight_error=ValueError('3 GiB required'))
        self.assertIsInstance(error, ValueError)
        self.assertEqual(events, ['space'])

    def test_wrong_target_fails_before_any_ssh(self):
        _, commands, error = self.run_ci(target={'DEPLOY_HOST': '127.0.0.1'})
        self.assertIsInstance(error, ValueError)
        self.assertEqual(commands, [])


class UpgradeDiskGateTests(unittest.TestCase):
    def test_backup_growth_blocks_new_pointer_and_restarts_old_service_without_touching_pg(self):
        with tempfile.TemporaryDirectory() as temp, ExitStack() as stack:
            root = Path(temp).resolve()
            base, data, backups = root / 'control', root / 'data', root / 'backups'
            previous, release = base / 'releases' / ('2' * 40), base / 'releases' / ('3' * 40)
            for path in (previous, release, data, backups):
                path.mkdir(parents=True, exist_ok=True)
            (base / 'current').symlink_to(previous, target_is_directory=True)
            (data / 'postgresql.enabled').write_text('paired-live-pg')
            (data / 'master.key').write_text('paired-live-key')
            site, unit = root / 'site.conf', root / 'control.service'
            site.write_text('old nginx')
            unit.write_text('old service')
            state = backups / 'ci-run'
            manifest = {'sourceSHA': release.name, 'schemaVersion': 5, 'postgresSchemaVersion': 1,
                        'serverBillingVersion': 2, 'paymentsVersion': 1}
            stack.enter_context(patch.object(domains.control, 'BASE', base))
            stack.enter_context(patch.object(domains.control, 'DATA', data))
            for name, value in [('BACKUPS', backups), ('SITE', site), ('UNIT', unit), ('PREP', root / 'unused-prep')]:
                stack.enter_context(patch.object(domains, name, value))
            stack.enter_context(patch.object(domains, 'verify', return_value=(release, manifest, previous)))
            stack.enter_context(patch.object(domains.control, 'verify_release', return_value=(previous, manifest)))
            stack.enter_context(patch.object(domains, 'data_identity', return_value='paired-identity'))
            stack.enter_context(patch.object(domains, 'schema_version', return_value=5))
            stack.enter_context(patch.object(domains, 'postgres_info', return_value={'subscriptionAllocationsVersion': 0}))
            stack.enter_context(patch.object(domains, 'subscription_migration_ready'))
            stack.enter_context(patch.object(domains, 'storage_environment', return_value={}))
            run = stack.enter_context(patch.object(domains.control, 'run'))
            stack.enter_context(patch.object(domains.control, 'healthy'))
            # Atomic symlink replacement is Linux-specific; observe requested
            # targets while exercising the rest of the real rollback path.
            pointer = stack.enter_context(patch.object(domains.control, 'point_current'))
            sqlite = stack.enter_context(patch.object(domains.sqlite3, 'connect'))

            def backup(args, **kwargs):
                self.assertEqual(args[1], 'backup')
                Path(args[-1]).write_text('postgres backup preserved')
                return SimpleNamespace(returncode=0)

            def disk(*paths):
                self.assertTrue((state / 'postgres.before.tar.gz').is_file())
                self.assertEqual((base / 'current').resolve(), previous)
                raise ValueError('Deployment requires at least 3 GiB of actual free space')

            stack.enter_context(patch.object(domains.subprocess, 'run', side_effect=backup))
            gate = stack.enter_context(patch.object(domains, 'require_free_space', side_effect=disk))
            with self.assertRaisesRegex(ValueError, '3 GiB'):
                domains.upgrade(release, state)
            gate.assert_called_once()
            self.assertNotIn(unittest.mock.call(release), pointer.call_args_list)
            self.assertEqual((base / 'current').resolve(), previous)
            run.assert_not_called()  # Online preparation failed before stopping the API.
            self.assertEqual((data / 'postgresql.enabled').read_text(), 'paired-live-pg')
            self.assertEqual((data / 'master.key').read_text(), 'paired-live-key')
            self.assertEqual((state / 'postgres.before.tar.gz').read_text(), 'postgres backup preserved')
            self.assertEqual(json.loads((state / 'domains-state.json').read_text())['phase'], 'preparation-failed-service-preserved')
            sqlite.assert_not_called()
            # With sufficient space, limits are installed before selecting the
            # new release, while the same PostgreSQL data is kept in place.
            (release / 'deploy').mkdir()
            (release / 'deploy/hqip-control.service').write_text('new service')
            (release / 'deploy/nginx-hqip.conf').write_text('new nginx')
            order = []
            gate.side_effect = lambda *paths: order.append('space')
            pointer.side_effect = lambda target: order.append(('current', target))
            with patch('fresh_deploy.install_log_limits', side_effect=lambda target: order.append(('limits', target))):
                domains.upgrade(release, backups / 'ci-success')
            self.assertEqual(order, ['space', ('limits', release), 'space', ('current', release)])
            self.assertEqual((data / 'postgresql.enabled').read_text(), 'paired-live-pg')
            self.assertEqual((data / 'master.key').read_text(), 'paired-live-key')
            sqlite.assert_not_called()


if __name__ == '__main__':
    unittest.main()
