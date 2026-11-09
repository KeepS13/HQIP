"""Exercise the embedded rollback against real archives in a disposable host."""
import base64
import json
import os
import pathlib
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

REAL_PATH = pathlib.Path
SOURCE = (REAL_PATH(__file__).resolve().parents[1] / 'internal/provision/backup.py').read_text(encoding='utf-8')


class NodeRollbackTests(unittest.TestCase):
    def test_rollback_restores_configuration_but_keeps_live_state(self):
        with tempfile.TemporaryDirectory(prefix='hqip-rollback-test-') as folder:
            root = REAL_PATH(folder).resolve()

            class HostPath:
                def __init__(self, *parts):
                    self.virtual = pathlib.PurePosixPath(*(str(p) for p in parts))

                def __str__(self): return str(self.virtual)
                def __truediv__(self, other): return HostPath(self.virtual / str(other))
                def __eq__(self, other): return str(self) == str(other)

                def __fspath__(self):
                    target = (root / str(self.virtual).lstrip('/')).resolve()
                    if target != root and root not in target.parents:
                        raise AssertionError('path escaped disposable host')
                    return str(target)

                @property
                def parent(self): return HostPath(self.virtual.parent)
                @property
                def name(self): return self.virtual.name
                def exists(self): return os.path.exists(os.fspath(self))
                def is_symlink(self): return os.path.islink(os.fspath(self))
                def unlink(self, missing_ok=False): return REAL_PATH(os.fspath(self)).unlink(missing_ok=missing_ok)
                def read_text(self): return REAL_PATH(os.fspath(self)).read_text(encoding='utf-8')

            config = root / 'etc/hqip-nodes/node-a'
            state = root / 'var/lib/hqip-nodes/node-a'
            backup = root / 'var/backups/hqip-nodes/node-a-fixture'
            units = root / 'etc/systemd/system'
            for directory in (config, state, backup, units):
                directory.mkdir(parents=True)
            agent_config = config / 'agent.json'
            agent_config.write_text('original configuration')
            unit = units / 'hqip-agent-node-a.service'
            unit.write_text('original service')
            db = state / 'state.db'
            connection = sqlite3.connect(db)
            connection.execute('CREATE TABLE usage(total INTEGER)')
            connection.execute('INSERT INTO usage VALUES(100)')
            connection.commit()
            connection.close()
            with tarfile.open(backup / 'own.tar.gz', 'w:gz') as archive:
                for source, name in [(config, 'etc/hqip-nodes/node-a'), (state, 'var/lib/hqip-nodes/node-a'), (unit, 'etc/systemd/system/hqip-agent-node-a.service')]:
                    archive.add(source, arcname=name)
            (backup / 'manifest.json').write_text(json.dumps({'ownExisted': True, 'ownStates': [], 'legacyStates': []}))
            agent_config.write_text('failed new configuration')
            unit.write_text('failed new service')
            (config / 'new-only.json').write_text('remove on rollback')
            connection = sqlite3.connect(db)
            connection.execute('UPDATE usage SET total=200')
            connection.commit()
            connection.close()
            # These represent files created after the snapshot. Rollback must
            # not delete them or restore their earlier archived contents.
            for name in ['state.db-wal', 'state.db-shm', 'unreported-final.json', 'certificate.pem']:
                (state / name).write_bytes(('current-' + name).encode())
            before = {p.name: p.read_bytes() for p in state.iterdir()}
            real_extract = tarfile.TarFile.extractall

            def extract(archive, path='.', **kwargs):
                self.assertEqual(path, '/')
                return real_extract(archive, root, **kwargs)

            def command(argv, **kwargs):
                self.assertEqual(argv[0], 'systemctl')
                return types.SimpleNamespace(returncode=0, stdout=b'')

            payload = base64.b64encode(json.dumps({'nodeId': 'node-a', 'backup': '/var/backups/hqip-nodes/node-a-fixture', 'action': 'rollback'}).encode()).decode()
            with patch.object(pathlib, 'Path', HostPath), patch.object(tarfile.TarFile, 'extractall', extract), patch.object(subprocess, 'run', command), patch.object(sys, 'argv', ['backup.py', payload]):
                exec(compile(SOURCE, 'internal/provision/backup.py', 'exec'), {})
            self.assertEqual(agent_config.read_text(), 'original configuration')
            self.assertEqual(unit.read_text(), 'original service')
            self.assertFalse((config / 'new-only.json').exists())
            self.assertEqual({p.name: p.read_bytes() for p in state.iterdir()}, before)


if __name__ == '__main__':
    unittest.main()
