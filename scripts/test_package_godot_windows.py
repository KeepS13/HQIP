"""Exercise installer build failures without launching the installer or VPN."""
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import package_godot_windows as packaging


class InstallerBuildTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.install = self.root / 'source with spaces'
        self.destination = self.root / 'deliveries'
        self.destination.mkdir()
        self.compiler = self.root / 'compiler with spaces.exe'
        self.compiler.write_bytes(b'fixture compiler')
        for name in ('HQIP.exe', *(f'versions/bundled/{n}' for n in packaging.PAYLOAD)):
            path = self.install / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b'fixture payload')

    def build(self, version='5.0.0-preview.16'):
        return packaging.build_installer(self.install, self.destination, version, self.compiler)

    def test_missing_runtime_cannot_produce_incomplete_installer(self):
        (self.install / 'versions/bundled/hqip-runtime.exe').unlink()
        with patch.object(packaging.subprocess, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'hqip-runtime.exe'):
                self.build()
            run.assert_not_called()

    def test_compile_failure_preserves_last_successful_artifact(self):
        target = self.destination / packaging.INSTALLER_NAME
        target.write_bytes(b'last successful installer')
        with patch.object(packaging.subprocess, 'run', side_effect=subprocess.CalledProcessError(2, 'iscc')):
            with self.assertRaises(subprocess.CalledProcessError):
                self.build()
        self.assertEqual(target.read_bytes(), b'last successful installer')
        self.assertEqual(list(self.destination.iterdir()), [target])

    def test_success_publishes_one_exe_and_handles_paths_with_spaces(self):
        def compile(command, **kwargs):
            self.assertEqual(command[0], str(self.compiler))
            self.assertIn('/DSourceRoot=' + str(self.install), command)
            output = Path(next(arg[2:] for arg in command if arg.startswith('/O')))
            (output / packaging.INSTALLER_NAME).write_bytes(b'new compiled installer')
        with patch.object(packaging.subprocess, 'run', side_effect=compile):
            target = self.build()
        self.assertEqual(target.read_bytes(), b'new compiled installer')
        self.assertEqual(list(self.destination.iterdir()), [target])

    def test_version_cannot_inject_preprocessor_values(self):
        for value in ('5.0.0\n[Files]', '5.0.0"', 'bad-version'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.build(value)

    def test_explicit_missing_compiler_does_not_silently_fall_back(self):
        with self.assertRaises(FileNotFoundError):
            packaging.find_compiler(self.root / 'missing.exe')


if __name__ == '__main__':
    unittest.main()
