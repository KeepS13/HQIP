"""Exercise actual embedded inspection with a NAT guest and bounded failures."""
import json
import subprocess
import types

from test_node_runtime import HostScriptsTests


class NATInspectionTests(HostScriptsTests):
    def inspect(self, source='10.0.186.2', exit_ip='45.205.28.141', target='45.205.28.141', timeout=False):
        self.write('/etc/os-release', 'ID=ubuntu\nVERSION_ID="24.04"\n')
        self.write('/etc/machine-id', 'machine-one')

        def command(argv, **kwargs):
            self.commands.append(list(argv))
            if argv[0] == 'ip' and 'address' in argv:
                output = json.dumps([{'addr_info': [{'family': 'inet', 'local': '10.0.186.2'}]}])
            elif argv[0] == 'ip' and 'route' in argv:
                output = json.dumps([{'prefsrc': source}])
            elif argv[0] == 'curl':
                if timeout:
                    raise subprocess.TimeoutExpired(argv, 8)
                output = exit_ip
            elif argv[0] == 'ss':
                output = 'udp UNCONN 0 0 10.0.186.2:443 0.0.0.0:*'
            else:
                output = 'LoadState=not-found'
            return types.SimpleNamespace(stdout=output.encode(), returncode=0)

        return self.execute('internal/provision/inspect.py', {'nodeId': 'nat-node', 'publicIp': target, 'port': 443}, command)

    def test_nat_source_and_exit_are_observed_without_interface_mutation(self):
        result = self.inspect()
        self.assertEqual(result['routeIp'], '10.0.186.2')
        self.assertEqual(result['exitIp'], '45.205.28.141')
        curl = next(c for c in self.commands if c[0] == 'curl')
        self.assertEqual(curl[curl.index('--interface') + 1], '10.0.186.2')
        self.assertEqual(curl[curl.index('--max-time') + 1], '8')
        self.assertFalse(any('add' in c or 'replace' in c for c in self.commands))

    def test_direct_binding_does_not_add_an_external_preflight_query(self):
        result = self.inspect(target='10.0.186.2')
        self.assertNotIn('routeIp', result)
        self.assertFalse(any(c[0] == 'curl' for c in self.commands))

    def test_unbound_route_source_is_not_used(self):
        result = self.inspect(source='10.0.186.3')
        self.assertNotIn('routeIp', result)
        self.assertFalse(any(c[0] == 'curl' for c in self.commands))

    def test_timeout_is_unknown_and_does_not_abort_host_inventory(self):
        result = self.inspect(timeout=True)
        self.assertEqual(result['boundIps'], ['10.0.186.2'])
        self.assertNotIn('routeIp', result)

    def test_unexpected_exit_is_returned_for_go_policy_to_reject(self):
        result = self.inspect(exit_ip='45.205.28.142')
        self.assertEqual(result['exitIp'], '45.205.28.142')
