"""Execute the embedded node scripts with a fake host and mocked system tools."""
import base64
import contextlib
import io
import json
import pathlib
import runpy
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[1]
REAL_PATH = pathlib.Path


class HostScriptsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = REAL_PATH(self.temp.name)
        self.addCleanup(self.temp.cleanup)
        self.commands = []

    def path(self, *args):
        p = REAL_PATH(*args)
        raw = str(args[0]) if args else '.'
        if raw.startswith('/'):
            return self.root / raw.lstrip('/')
        return p

    def write(self, path, text):
        p = self.path(path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def execute(self, script, payload, command):
        encoded = base64.b64encode(json.dumps(payload).encode()).decode()
        output = io.StringIO()
        with patch('sys.argv', ['script', encoded]), patch('pathlib.Path', side_effect=self.path), patch('subprocess.run', side_effect=command), patch('shutil.which', return_value='/usr/bin/tool'), patch('os.geteuid', return_value=0, create=True), patch('os.uname', return_value=types.SimpleNamespace(machine='x86_64'), create=True), patch('shutil.disk_usage', return_value=types.SimpleNamespace(free=1 << 30)), contextlib.redirect_stdout(output):
            try:
                exec(compile((ROOT / script).read_text(encoding='utf-8'), str(script), 'exec'), {})
            except SystemExit as exc:
                if exc.code not in (None, 0):
                    raise
        return json.loads(output.getvalue())

    def test_inspection_finds_stopped_legacy_without_leaking_secrets(self):
        self.write('/etc/os-release', 'ID=ubuntu\nVERSION_ID="24.04"\n')
        self.write('/etc/machine-id', 'host-identity')
        self.write('/etc/hqip-node/owner', 'old-node')
        self.write('/etc/hqip-node/agent.json', '{"token":"must-not-leak"}')
        self.write('/etc/hqip-node/hysteria.yaml', 'listen: :443\nauth: secret-must-not-leak\n')
        self.write('/opt/hqip-node/hysteria', 'fake-fixed-binary')
        self.write('/etc/systemd/system/hqip-hysteria.service', 'unit-body')

        def command(argv, **kwargs):
            if argv[:4] == ('ip', '-j', '-4', 'address') or list(argv[:4]) == ['ip', '-j', '-4', 'address']:
                output = '[{"addr_info":[{"family":"inet","local":"203.0.113.2"}]}]'
            elif argv[0] == 'ss':
                output = ''
            elif argv[:3] == ('systemctl', 'cat', 'hqip-hysteria.service') or list(argv[:3]) == ['systemctl', 'cat', 'hqip-hysteria.service']:
                output = 'ExecStart=/opt/hqip-node/hysteria server --config /etc/hqip-node/hysteria.yaml'
            elif argv[0:3] == ('systemctl', 'show', 'hqip-hysteria.service') or list(argv[:3]) == ['systemctl', 'show', 'hqip-hysteria.service']:
                output = 'LoadState=loaded\nActiveState=inactive\nUnitFileState=disabled\nFragmentPath=/etc/systemd/system/hqip-hysteria.service\nExecStart={ path=/opt/hqip-node/hysteria ; argv[]=/opt/hqip-node/hysteria server --config /etc/hqip-node/hysteria.yaml ; }'
            else:
                output = 'LoadState=not-found'
            return types.SimpleNamespace(stdout=output.encode(), returncode=0)

        result = self.execute('internal/provision/inspect.py', {'nodeId': 'new-node', 'publicIp': '203.0.113.2', 'port': 443}, command)
        self.assertEqual(result['units'][0]['kind'], 'hqip-legacy')
        self.assertFalse(result['units'][0]['active'])
        self.assertFalse(result['units'][0]['enabled'])
        self.assertNotIn('must-not-leak', json.dumps(result))
        self.assertEqual(result['boundIps'], ['203.0.113.2'])

    def test_firewall_cleanup_does_not_remove_foreign_or_edited_rules(self):
        self.write('/etc/hqip-nodes/node-a/firewall.json', json.dumps([{'ip': '203.0.113.2', 'port': 443, 'proto': 'udp'}, {'ip': '203.0.113.2', 'port': 80, 'proto': 'tcp'}]))
        listing = "ufw allow to 203.0.113.2 port 443 proto udp comment 'hqip:node-a'\nufw allow to 203.0.113.2 port 80 proto tcp comment 'operator-edited'\nufw allow to 203.0.113.3 port 443 proto udp comment 'hqip:node-b'"

        def command(argv, **kwargs):
            self.commands.append(list(argv))
            output = listing if list(argv) == ['ufw', 'show', 'added'] else ''
            return types.SimpleNamespace(stdout=output, stderr='', returncode=0)

        self.execute('internal/agent/firewall.py', {'nodeId': 'node-a', 'action': 'cleanup'}, command)
        deleted = [c for c in self.commands if 'delete' in c]
        self.assertEqual(len(deleted), 1)
        self.assertIn('203.0.113.2', deleted[0])
        self.assertIn('443', deleted[0])
        self.assertNotIn('80', deleted[0])

    def test_firewall_preserves_existing_unowned_allowance(self):
        self.write('/etc/hqip-nodes/node-a/firewall.json', '[]')
        def command(argv, **kwargs):
            self.commands.append(list(argv))
            output = "ufw allow proto udp from any to 203.0.113.2 port 443" if list(argv) == ['ufw', 'show', 'added'] else 'Status: active'
            return types.SimpleNamespace(stdout=output, stderr='', returncode=0)
        result = self.execute('internal/agent/firewall.py', {'nodeId':'node-a','action':'apply','rules':[{'ip':'203.0.113.2','port':443,'proto':'udp'}]}, command)
        self.assertFalse(result['error'])
        self.assertEqual(result['added'], [])
        self.assertEqual(json.loads(self.path('/etc/hqip-nodes/node-a/firewall.json').read_text()), [])


if __name__ == '__main__':
    unittest.main()
