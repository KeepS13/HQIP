"""A NAT upgrade must preserve the public identity, local binding and ledger."""
import json
from pathlib import Path
from unittest.mock import patch

from test_reality_upgrade import RealityUpgradeTests
import upgrade_node_agent as upgrade


class NATUpgradeTests(RealityUpgradeTests):
    def enable_nat(self):
        self.enable_reality()
        self.cfg.update(publicIp='45.205.28.141', bindIp='10.0.186.2')
        self.cfg['reality'] = {'port': 443, 'backendPort': 1443, 'serverName': 'www.microsoft.com'}
        (self.config / 'agent.json').write_text(json.dumps(self.cfg))
        (self.config / 'hysteria.yaml').write_text('listen: 10.0.186.2:1443\n')
        manifest = json.loads((self.artifacts / 'manifest.json').read_text())
        manifest['nodeNetworkVersion'] = 1
        (self.artifacts / 'manifest.json').write_text(json.dumps(manifest))

    def test_nat_upgrade_and_rollback_preserve_binding(self):
        self.enable_nat()
        with patch.object(upgrade.socket, 'socket', side_effect=AssertionError('port reselection')):
            result = upgrade.upgrade(self.node, self.artifacts)
        active = json.loads((self.config / 'agent.json').read_text())
        self.assertEqual(active['publicIp'], '45.205.28.141')
        self.assertEqual(active['bindIp'], '10.0.186.2')
        self.assertEqual(self.firewall_calls[-1][2], [{'ip': '10.0.186.2', 'port': 443, 'proto': 'tcp'}])
        upgrade.rollback(Path(result['backup']))
        self.assertEqual(json.loads((self.config / 'agent.json').read_text()), self.cfg)

    def test_nat_incompatible_agent_rejected_before_stopping(self):
        self.enable_nat()
        manifest = json.loads((self.artifacts / 'manifest.json').read_text())
        manifest.pop('nodeNetworkVersion')
        (self.artifacts / 'manifest.json').write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'NAT nodes require a compatible Agent'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertFalse(any('stop' in call for call in self.calls))

    def test_nat_mismatched_core_listener_rejected(self):
        self.enable_nat()
        (self.config / 'hysteria.yaml').write_text('listen: 10.0.186.3:1443\n')
        with self.assertRaisesRegex(ValueError, 'Cannot verify owned HY2 listener'):
            upgrade.upgrade(self.node, self.artifacts)
        self.assertFalse(any('stop' in call for call in self.calls))
