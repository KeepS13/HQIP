from pathlib import Path
from contextlib import ExitStack, contextmanager
import json
import socket
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import commercial_protection as protection
import render_domains
import upgrade_domains


class Response:
    status = 200
    headers = {'CF-Ray': 'test'}

    def __enter__(self):
        return self

    def __exit__(self, *_):
        pass


class CommercialProtectionTests(unittest.TestCase):
    def resolver(self, host, port, **options):
        return [(socket.AF_INET, socket.SOCK_STREAM, 6, '', ('104.16.1.2', 443)),
                (socket.AF_INET6, socket.SOCK_STREAM, 6, '', ('2606:4700::1', 443, 0, 0))]

    def test_all_three_names_require_proxied_addresses_and_cloudflare_response(self):
        calls = []
        def open_request(request, **_):
            self.assertEqual(request.get_header('User-agent'), 'HQIP-Deployment/1.0')
            calls.append(request.full_url)
            return Response()
        opener = SimpleNamespace(open=open_request)
        protection.verify_cloudflare(self.resolver, opener)
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls, ['https://' + render_domains.USER + '/', 'https://' + render_domains.ADMIN + '/', 'https://' + render_domains.API + '/healthz'])
        self.assertIn('https://' + render_domains.API + '/healthz', calls)
        bad_resolver = lambda *_args, **_kwargs: [(socket.AF_INET, socket.SOCK_STREAM, 6, '', ('203.0.113.2', 443))]
        with self.assertRaisesRegex(ValueError, 'configuration was preserved'):
            protection.verify_cloudflare(bad_resolver, opener)
        with patch.object(Response, 'headers', {}), self.assertRaises(ValueError):
            protection.verify_cloudflare(self.resolver, opener)

    def test_guard_uses_socket_peer_and_only_exact_acme_tokens_bypass_http(self):
        text = render_domains.render()
        self.assertIn('geo $realip_remote_addr $hqip_cloudflare_peer', text)
        self.assertNotIn('geo $http_cf_connecting_ip', text)
        self.assertNotIn('geo $remote_addr $hqip_cloudflare_peer', text)
        self.assertEqual(text.count('if ($hqip_cloudflare_peer = 0) { return 403; }'), 4)
        self.assertEqual(text.count('[A-Za-z0-9_-]{1,128}$'), 2)
        self.assertNotIn('location ^~ /.well-known/acme-challenge/', text)
        self.assertNotIn('default_server', text)

    def test_operation_log_migration_requires_correct_endpoint_and_role(self):
        app = {'HQIP_DATABASE_URL': 'postgres://hqip_app:test@127.0.0.1:15432/hqip'}
        migration = {'HQIP_DATABASE_URL': 'postgres://hqip_migrator:test@127.0.0.1:15432/hqip'}
        deploy = SimpleNamespace(postgres_enabled=lambda: True, postgres_info=lambda: {}, MIGRATION_ENV='migration',
                                 storage_environment=lambda path=None: migration if path else app)
        self.assertEqual(protection.prepare_operation_logs(deploy, {'operationLogsVersion': 1}), migration)
        with patch.object(protection.subprocess, 'run') as run:
            # Explicit executor seam prevents spawning a tool in this unit test.
            run.return_value = SimpleNamespace(returncode=0)
            protection.migrate_operation_logs(Path('/release'), migration, execute=run)
            self.assertEqual(run.call_args.args[0][-1], 'migrate-operation-logs')
            self.assertNotIn('test', repr(run.call_args.args))
        migration['HQIP_DATABASE_URL'] = 'postgres://postgres:test@127.0.0.1:15432/hqip'
        with self.assertRaises(ValueError):
            protection.prepare_operation_logs(deploy, {'operationLogsVersion': 1})

    def test_additive_logs_schema_allows_compatible_old_release(self):
        with patch.object(upgrade_domains, 'postgres_enabled', return_value=True), \
                patch.object(upgrade_domains, 'postgres_info', return_value={'operationLogsVersion': 1}):
            upgrade_domains.require_billing_compatible({'postgresSchemaVersion': 1, 'serverBillingVersion': 2})

    def test_rollback_restores_only_owned_units_and_previous_timer_state(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            release, state, units = root / 'release', root / 'state', root / 'units'
            for path in (release / 'deploy', state, units):
                path.mkdir(parents=True)
            for name in protection.UNITS:
                (release / 'deploy' / name).write_text('new')
            (units / 'hqip-backup.timer').write_text('old')
            (units / 'unrelated.service').write_text('other')
            steps = []
            with patch.object(protection, 'UNIT_ROOT', units), patch.object(protection, 'verify_cloudflare'):
                self.assertTrue(protection.prepare(release, {'commercialProtectionVersion': 1}, state,
                                                  run=lambda *_args, **_kw: SimpleNamespace(returncode=0)))
                for name in protection.UNITS:
                    (units / name).write_text('new')
                protection.rollback(state, lambda *args: steps.append(args))
            self.assertEqual((units / 'hqip-backup.timer').read_text(), 'old')
            self.assertFalse((units / 'hqip-backup.service').exists())
            self.assertEqual((units / 'unrelated.service').read_text(), 'other')
            self.assertIn(('systemctl', 'enable', '--now', 'hqip-backup.timer'), steps)

    def test_first_backup_requires_a_real_success_and_preserves_existing_success(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'release.json').write_text(json.dumps({'commercialProtectionVersion': 1}))
            calls = []
            def execute(args, **_):
                calls.append(args)
                (root / 'backup-status.json').write_text(json.dumps({'lastSuccessAt': '2026-11-15T00:00:00Z'}))
                return SimpleNamespace(returncode=0)
            protection.ensure_initial_backup(root, data=root, execute=execute)
            self.assertEqual(calls, [['systemctl', 'start', 'hqip-backup.service']])
            protection.ensure_initial_backup(root, data=root, execute=execute)
            self.assertEqual(len(calls), 1)
            (root / 'backup-status.json').unlink()
            with self.assertRaisesRegex(RuntimeError, 'preserved'):
                protection.ensure_initial_backup(root, data=root,
                    execute=lambda *_args, **_kw: SimpleNamespace(returncode=1))

    def test_failed_new_service_restores_previous_code_routes_and_retains_log_tables(self):
        import fresh_deploy
        import operations_deployment
        with tempfile.TemporaryDirectory() as folder, ExitStack() as mocks:
            root = Path(folder).resolve()
            base, backups, data = root / 'base', root / 'backups', root / 'data'
            previous, release = base / 'previous', base / 'release'
            for path in (previous, release / 'deploy', backups, data):
                path.mkdir(parents=True)
            (base / 'current').symlink_to(previous, target_is_directory=True)
            for name in ('hqip-control.service', 'nginx-hqip.conf'):
                (release / 'deploy' / name).write_text('new')
            site, unit, state = root / 'site', root / 'unit', backups / 'state'
            site.write_text('old-route'); unit.write_text('old-unit')
            ledger = data / 'business-ledger'
            ledger.write_text('paid-orders-and-live-usage')
            old_manifest = {'schemaVersion': 5, 'postgresSchemaVersion': 1, 'serverBillingVersion': 2}
            new_manifest = {**old_manifest, 'sourceSHA': 'a' * 40, 'operationLogsVersion': 1, 'commercialProtectionVersion': 1}
            for obj, key, value in ((upgrade_domains.control, 'BASE', base), (upgrade_domains.control, 'DATA', data),
                                    (upgrade_domains, 'BACKUPS', backups), (upgrade_domains, 'SITE', site),
                                    (upgrade_domains, 'UNIT', unit), (upgrade_domains, 'PREP', root / 'absent')):
                mocks.enter_context(patch.object(obj, key, value))
            for key, value in (('verify', (release, new_manifest, previous)), ('allocation_migration_environment', None),
                               ('data_identity', 'key'), ('schema_version', 5), ('postgres_enabled', True),
                               ('postgres_info', {'operationLogsVersion': 1}), ('storage_environment', {})):
                mocks.enter_context(patch.object(upgrade_domains, key, return_value=value))
            for key in ('subscription_migration_ready', 'migrate_allocations', 'require_free_space'):
                mocks.enter_context(patch.object(upgrade_domains, key))
            mocks.enter_context(patch.object(operations_deployment, 'prepare', return_value=None))
            mocks.enter_context(patch.object(operations_deployment, 'migrate'))
            mocks.enter_context(patch.object(protection, 'prepare', return_value=True))
            mocks.enter_context(patch.object(protection, 'prepare_operation_logs', return_value={}))
            mocks.enter_context(patch.object(protection, 'migrate_operation_logs',
                side_effect=lambda *_: (data / 'new-operation-log-tables').write_text('preserve')))
            mocks.enter_context(patch.object(fresh_deploy, 'install_log_limits'))
            mocks.enter_context(patch.object(upgrade_domains.subprocess, 'run', return_value=SimpleNamespace(returncode=0)))
            mocks.enter_context(patch.object(upgrade_domains.control, 'run'))
            def point(target):
                (base / 'current').unlink()
                (base / 'current').symlink_to(target, target_is_directory=True)
            mocks.enter_context(patch.object(upgrade_domains.control, 'point_current', side_effect=point))
            mocks.enter_context(patch.object(upgrade_domains.control, 'verify_release', return_value=(previous, old_manifest)))
            mocks.enter_context(patch.object(upgrade_domains.control, 'healthy', side_effect=[RuntimeError('new service failed'), None]))
            with self.assertRaisesRegex(RuntimeError, 'new service failed'):
                upgrade_domains.upgrade(release, state, defer_protection=True)
            self.assertEqual((base / 'current').resolve(), previous)
            self.assertEqual(site.read_text(), 'old-route')
            self.assertEqual(unit.read_text(), 'old-unit')
            self.assertEqual(ledger.read_text(), 'paid-orders-and-live-usage')
            self.assertEqual((data / 'new-operation-log-tables').read_text(), 'preserve')
            self.assertEqual(json.loads((state / 'domains-state.json').read_text())['phase'], 'rolled-back-data-preserved')

    def test_origin_guard_is_applied_only_after_backup_and_cf_recheck(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            base, release, state = root / 'base', root / 'release', root / 'state'
            for path in (base, release / 'deploy', state):
                path.mkdir(parents=True)
            (base / 'current').symlink_to(release, target_is_directory=True)
            site = root / 'site'
            site.write_text('old-unrestricted-route')
            (release / 'deploy/nginx-hqip.conf').write_text('new-guard')
            self.assertTrue(protection.defer_origin(release, {'commercialProtectionVersion': 1}, state, site))
            self.assertEqual(site.read_text(), 'old-unrestricted-route')
            steps = []
            @contextmanager
            def lock(_):
                steps.append('lock')
                yield
            def backup(*_args, **_kwargs):
                self.assertEqual(site.read_text(), 'old-unrestricted-route')
                steps.append('backup')
            with patch.object(protection, 'ensure_initial_backup', side_effect=backup), \
                    patch.object(protection, 'verify_cloudflare', side_effect=lambda: steps.append('cloudflare')):
                protection.complete_release(release, state, lambda *args: steps.append(args[0]),
                    site=site, base=base, data=root, lock=lock)
            self.assertEqual(steps, ['backup', 'lock', 'cloudflare', 'nginx', 'systemctl', 'cloudflare'])
            self.assertEqual(site.read_text(), 'new-guard')
            self.assertFalse(json.loads((state / 'origin-pending.json').read_text())['pending'])

    def test_failure_after_origin_reload_restores_original_route_and_allows_retry(self):
        from contextlib import nullcontext
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            base, release, state = root / 'base', root / 'release', root / 'state'
            for path in (base, release / 'deploy', state):
                path.mkdir(parents=True)
            (base / 'current').symlink_to(release, target_is_directory=True)
            site = root / 'site'; site.write_text('original-route')
            (release / 'deploy/nginx-hqip.conf').write_text('new-guard')
            protection.defer_origin(release, {'commercialProtectionVersion': 1}, state, site)
            calls = []
            with patch.object(protection, 'ensure_initial_backup'), patch.object(protection, 'verify_cloudflare',
                    side_effect=[None, ValueError('edge failed')]):
                with self.assertRaisesRegex(ValueError, 'edge failed'):
                    protection.complete_release(release, state, lambda *args: calls.append(args),
                        site=site, base=base, data=root, lock=lambda _: nullcontext())
            self.assertEqual(site.read_text(), 'original-route')
            self.assertEqual(calls.count(('systemctl', 'reload', 'nginx')), 2)
            self.assertTrue(json.loads((state / 'origin-pending.json').read_text())['pending'])
            self.assertFalse((state / 'nginx.origin-before.conf').exists())

    def test_rollback_before_first_unit_install_does_not_stop_missing_units(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); units = root / 'units'; units.mkdir()
            (root / 'protection-before.json').write_text(json.dumps({**{name: False for name in protection.UNITS}, 'timerEnabled': False}))
            calls = []
            with patch.object(protection, 'UNIT_ROOT', units):
                protection.rollback(root, lambda *args: calls.append(args))
            self.assertEqual(calls, [('systemctl', 'daemon-reload')])

    def test_historical_release_without_protection_payload_is_only_legacy_valid(self):
        from test_full_release import FullReleaseTests
        from package_domains import package_release
        from release_bundle import PROTECTION_FILES, verify_full_release
        fixture = FullReleaseTests()
        fixture.setUp()
        try:
            package_release(fixture.root, fixture.sha)
            release = fixture.root / 'dist' / ('full-' + fixture.sha)
            manifest = json.loads((release / 'release.json').read_text())
            for field in ('commercialProtectionVersion', 'operationLogsVersion'):
                manifest.pop(field)
            for name in PROTECTION_FILES:
                manifest['sha256'].pop(name)
                (release / name).unlink()
            self.assertEqual(verify_full_release(release, manifest, allow_legacy=True)['sourceSHA'], fixture.sha)
            with self.assertRaisesRegex(ValueError, 'commercialProtectionVersion'):
                verify_full_release(release, manifest)
        finally:
            fixture.doCleanups()


if __name__ == '__main__':
    unittest.main()
