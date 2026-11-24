"""Real archives exercise independent packaging and current-source provenance."""
from pathlib import Path
import hashlib
import json
import shutil
import stat
import struct
import tempfile
import unittest
import warnings
import zipfile

from install_ci_release import materialize
from package_domains import package_release
from release_bundle import (DESKTOP_BINARIES, DESKTOP_FILES, NODE_ASSETS, REQUIRED,
                            digest, verify_desktop, verify_full_release)


def windows_image(icon=True):
    """Small PE fixture with a real resource tree, group and referenced image."""
    data = bytearray(1024)
    data[:2] = b'MZ'
    struct.pack_into('<I', data, 0x3c, 0x80)
    data[0x80:0x84] = b'PE\0\0'
    struct.pack_into('<HHIIIHH', data, 0x84, 0x8664, 1, 0, 0, 0, 240, 2)
    struct.pack_into('<H', data, 0x98, 0x20b)
    struct.pack_into('<IIII', data, 0x98 + 240 + 8, 512, 0x1000, 512, 512)
    if icon:
        struct.pack_into('<II', data, 0x98 + 128, 0x1000, 512)
        for offset, entries in ((0, [(3,0x80000020),(14,0x80000038)]),
                                (0x20,[(1,0x80000050)]),(0x38,[(3,0x80000068)]),
                                (0x50,[(1033,0x80)]),(0x68,[(1033,0x90)])):
            struct.pack_into('<HH', data, 512 + offset + 12, 0, len(entries))
            for index, entry in enumerate(entries):
                struct.pack_into('<II', data, 512 + offset + 16 + index * 8, *entry)
        struct.pack_into('<IIII', data, 512 + 0x80, 0x10a0, 4, 0, 0)
        struct.pack_into('<IIII', data, 512 + 0x90, 0x10b0, 20, 0, 0)
        data[512 + 0xa0:512 + 0xa4] = b'ICON'
        struct.pack_into('<HHHBBBBHHIH', data, 512 + 0xb0, 0, 1, 1, 16, 16, 0, 0, 1, 32, 4, 1)
    return bytes(data)


class FullReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sha = 'b' * 40
        self.version = '4.3.4'
        for name in REQUIRED:
            if name.startswith('artifacts/'):
                target = self.root / 'dist' / name.removeprefix('artifacts/')
            elif name == 'hqip':
                target = self.root / 'dist/hqip-linux-amd64'
            elif name.startswith(('admin-web/', 'user-web/')):
                app, rest = name.split('/', 1)
                target = self.root / app / 'dist' / rest
            else:
                target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(('payload:' + name).encode())
        (self.root / 'scripts/build_desktop.py').write_text('DESKTOP_VERSION=' + repr(self.version))
        firewall = self.root / 'internal/agent/firewall.py'
        firewall.parent.mkdir(parents=True)
        firewall.write_text('owned firewall fixture')
        icon = self.root / 'desktop/assets/hqip.ico'
        icon.parent.mkdir(parents=True)
        icon.write_bytes(b'build-source-icon')
        self.payload = {name: windows_image() if name in DESKTOP_BINARIES else b'package document'
                        for name in DESKTOP_FILES if name != 'manifest.json'}
        self.inner = {'version': self.version, 'sourceSHA': self.sha, 'dirty': False,
                      'trafficBillingVersion': 1, 'singBoxBuildTags': 'with_v2ray_api',
                      'windowsResources': {'groupId': 3, 'iconSha256': digest(icon)},
                      'sha256': {name: hashlib.sha256(self.payload[name]).hexdigest() for name in DESKTOP_BINARIES}}
        self.nodes = {'sourceSHA': self.sha, 'dirty': False, 'hqipAdmissionVersion': 1,
                      'trafficBillingVersion': 1,
                      'hysteriaSource': {'sha256': {'hqip-hysteria-linux-' + arch:
                      digest(self.root / ('dist/hysteria-linux-' + arch)) for arch in ('amd64', 'arm64')}},
                      'sha256': {name: digest(self.root / 'dist' / name) for name in NODE_ASSETS}}
        self.nodes['sha256']['hqip-linux-amd64'] = digest(self.root / 'dist/hqip-linux-amd64')
        (self.root / 'dist/manifest.json').write_text(json.dumps(self.nodes))
        self.write_desktop()
        from test_client_bundle import client_fixture
        self.client_files = client_fixture(self.root / 'dist/client-release', self.sha)

    def write_desktop(self, outer=None, extras=(), entries=None):
        archive = self.root / 'dist/HQIP-Windows-x64.zip'
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as package:
            for name, data in {**self.payload, 'manifest.json':json.dumps(self.inner).encode()}.items():
                package.writestr((entries or {}).get(name, 'HQIP/' + name), data)
            for name, data in extras:
                package.writestr(name, data)
        self.outer = {'version':self.version,'sourceSHA':self.sha,'dirty':False,
                      'filename':archive.name,'size':archive.stat().st_size,'sha256':digest(archive), **(outer or {})}
        (self.root / 'dist/desktop-manifest.json').write_text(json.dumps(self.outer))

    def check_desktop(self):
        output = self.root / 'desktop-check'
        (output / 'artifacts').mkdir(parents=True, exist_ok=True)
        for name in ('HQIP-Windows-x64.zip','desktop-manifest.json'):
            shutil.copyfile(self.root / 'dist' / name, output / 'artifacts' / name)
        verify_desktop(output, {'sourceSHA':self.sha,'desktopVersion':self.version,
                       'desktopIconSHA256':digest(self.root / 'desktop/assets/hqip.ico')},
                       {'artifacts/HQIP-Windows-x64.zip':digest(output / 'artifacts/HQIP-Windows-x64.zip')})

    def test_package_and_install_without_old_release(self):
        result = package_release(self.root, self.sha)
        output = self.root / 'installed'
        archive = self.root / 'dist' / result['filename']
        materialize(archive, result['sha256'], self.sha, None, output)
        manifest = verify_full_release(output)
        from client_bundle import verify_client_bundle
        clients = {'artifacts/' + name for name in verify_client_bundle(self.root / 'dist/client-release', self.sha)}
        self.assertEqual(set(manifest['sha256']), REQUIRED | {'scripts/hqip_firewall.py'} | clients)
        self.assertNotIn('previousSourceSHA', manifest)

    def test_stale_core_is_rejected_even_if_control_and_client_are_new(self):
        self.nodes['hysteriaSource']['sha256']['hqip-hysteria-linux-amd64'] = '0' * 64
        (self.root / 'dist/manifest.json').write_text(json.dumps(self.nodes))
        with self.assertRaisesRegex(ValueError, 'core provenance'):
            package_release(self.root, self.sha)

    def test_restore_operator_must_ship_in_a_complete_release(self):
        package_release(self.root, self.sha)
        output = self.root / 'dist' / ('full-' + self.sha)
        recovery = output / 'scripts/restore_postgres.py'
        self.assertEqual(recovery.read_bytes(), (self.root / 'scripts/restore_postgres.py').read_bytes())
        manifest = json.loads((output / 'release.json').read_text())
        del manifest['sha256']['scripts/restore_postgres.py']
        recovery.unlink()
        with self.assertRaisesRegex(ValueError, 'Incomplete full release'):
            verify_full_release(output, manifest)

    def test_legacy_recovery_capability_is_only_accepted_for_historical_verification(self):
        package_release(self.root, self.sha)
        output = self.root / 'dist' / ('full-' + self.sha)
        manifest = json.loads((output / 'release.json').read_text())
        del manifest['postgresRecoveryVersion']
        del manifest['sha256']['scripts/restore_postgres.py']
        (output / 'scripts/restore_postgres.py').unlink()
        with self.assertRaisesRegex(ValueError, 'postgresRecoveryVersion'):
            verify_full_release(output, manifest)
        self.assertEqual(verify_full_release(output, manifest, allow_legacy=True)['sourceSHA'], self.sha)
        manifest['postgresRecoveryVersion'] = 1
        with self.assertRaisesRegex(ValueError, 'Incomplete full release'):
            verify_full_release(output, manifest, allow_legacy=True)

    def test_wrong_source_and_missing_node_binary_are_rejected(self):
        self.nodes['sourceSHA'] = 'a' * 40
        (self.root / 'dist/manifest.json').write_text(json.dumps(self.nodes))
        with self.assertRaisesRegex(ValueError, 'current clean source'):
            package_release(self.root, self.sha)
        self.nodes['sourceSHA'] = self.sha
        (self.root / 'dist/manifest.json').write_text(json.dumps(self.nodes))
        (self.root / 'dist/hysteria-linux-arm64').unlink()
        with self.assertRaisesRegex(ValueError, 'unverified current node asset'):
            package_release(self.root, self.sha)

    def test_payload_injection_after_packaging_is_rejected(self):
        package_release(self.root, self.sha)
        output = self.root / 'dist' / ('full-' + self.sha)
        (output / 'old-private-backup').write_text('must not ship')
        with self.assertRaisesRegex(ValueError, 'exactly match'):
            verify_full_release(output)

    def test_plain_bytes_cannot_masquerade_as_a_client_zip(self):
        archive = self.root / 'dist/HQIP-Windows-x64.zip'
        archive.write_bytes(b'not a zip')
        self.outer.update(size=archive.stat().st_size, sha256=digest(archive))
        (self.root / 'dist/desktop-manifest.json').write_text(json.dumps(self.outer))
        with self.assertRaisesRegex(ValueError, 'valid ordinary ZIP'):
            self.check_desktop()

    def test_outer_version_filename_size_and_hash_must_match_real_package(self):
        for key, value in [('version','4.3.3'),('filename','old-client.zip'),('size',1),('sha256','0' * 64)]:
            with self.subTest(field=key):
                self.write_desktop({key:value})
                with self.assertRaisesRegex(ValueError, 'Desktop manifest'):
                    self.check_desktop()

    def test_inner_version_and_source_must_match_the_build(self):
        for key, value in [('version','4.3.3'),('sourceSHA','a' * 40),('dirty',True)]:
            with self.subTest(field=key):
                previous = self.inner[key]
                self.inner[key] = value
                self.write_desktop()
                with self.assertRaisesRegex(ValueError, 'inner manifest'):
                    self.check_desktop()
                self.inner[key] = previous

    def test_version_is_derived_from_build_parameter_and_accepts_future_versions(self):
        self.version = self.inner['version'] = '4.4.0'
        (self.root / 'scripts/build_desktop.py').write_text('DESKTOP_VERSION="4.4.0"')
        self.write_desktop()
        package_release(self.root, self.sha)
        manifest = verify_full_release(self.root / 'dist' / ('full-' + self.sha))
        self.assertEqual(manifest['desktopVersion'], '4.4.0')

    def test_matching_old_inner_and_outer_versions_do_not_override_build_version(self):
        self.inner['version'] = '4.3.3'
        self.write_desktop({'version':'4.3.3'})
        with self.assertRaisesRegex(ValueError, 'Desktop manifest'):
            package_release(self.root, self.sha)

    def test_binary_corruption_is_rejected_even_with_updated_zip_hash(self):
        self.payload['hqip-helper.exe'] += b'corruption'
        self.write_desktop()
        with self.assertRaisesRegex(ValueError, 'inner binary checksum'):
            self.check_desktop()

    def test_manifested_non_pe_binary_and_missing_icon_resources_are_rejected(self):
        for data, message in [(b'not executable','Truncated desktop Windows PE'),
                              (windows_image(icon=False),'no Windows icon resources')]:
            with self.subTest(message=message):
                self.payload['HQIP.exe'] = data
                self.inner['sha256']['HQIP.exe'] = hashlib.sha256(data).hexdigest()
                self.write_desktop()
                with self.assertRaisesRegex(ValueError, message):
                    self.check_desktop()

    def test_zip_rejects_traversal_and_duplicate_entries(self):
        for entry in [('HQIP/../../escape',b'bad'), ('HQIP/HQIP.exe',b'duplicate')]:
            with self.subTest(entry=str(entry[0])), warnings.catch_warnings():
                warnings.simplefilter('ignore', UserWarning)
                self.write_desktop(extras=[entry])
                with self.assertRaisesRegex(ValueError, 'safe paths'):
                    self.check_desktop()

    def test_zip_rejects_symlink_even_with_exact_allowed_member_names(self):
        link = zipfile.ZipInfo('HQIP/HQIP.exe')
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        self.write_desktop(entries={'HQIP.exe':link})
        with self.assertRaisesRegex(ValueError, 'unsafe or oversized'):
            self.check_desktop()

    def test_missing_required_resource_and_wrong_icon_source_are_rejected(self):
        self.payload.pop('libcronet.dll')
        self.write_desktop()
        with self.assertRaisesRegex(ValueError, 'safe paths'):
            self.check_desktop()
        self.payload['libcronet.dll'] = windows_image()
        self.inner['windowsResources']['iconSha256'] = '0' * 64
        self.write_desktop()
        with self.assertRaisesRegex(ValueError, 'resource provenance'):
            self.check_desktop()


if __name__ == '__main__':
    unittest.main()
