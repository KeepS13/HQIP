"""Agent upgrade failure tests cover config/port ownership and ledger survival."""
from contextlib import closing
import json
from pathlib import Path
import sqlite3
from unittest.mock import patch

from test_upgrade_node_agent import AgentTelemetryUpgradeTests
import upgrade_node_agent as upgrade


class RealityUpgradeTests(AgentTelemetryUpgradeTests):
    def enable_reality(self):
        self.cfg['publicIp'] = '127.0.0.1'
        (self.config / 'agent.json').write_text(json.dumps(self.cfg))
        (self.config / 'hysteria.yaml').write_text('listen: 127.0.0.1:1443\n')
        manifest = json.loads((self.artifacts / 'manifest.json').read_text())
        manifest['vlessRealityVersion'] = 1
        (self.artifacts / 'manifest.json').write_text(json.dumps(manifest))
        self.firewall_calls = []
        self.mocks.enter_context(patch.object(upgrade, 'reality_firewall', side_effect=self.firewall))
        self.mocks.enter_context(patch.object(upgrade, 'verify_reality'))

    def firewall(self, node, action, rules):
        self.firewall_calls.append((node, action, rules))
        return {'added': rules, 'error': False} if action == 'apply' else []

    def test_reality_upgrade_persists_endpoint_and_rollback_preserves_usage(self):
        self.enable_reality()
        result = upgrade.upgrade(self.node, self.artifacts)
        active = json.loads((self.config / 'agent.json').read_text())
        self.assertEqual(active['reality']['backendPort'], 1443)
        self.assertEqual(result['vlessRealityVersion'], 1)
        with closing(sqlite3.connect(self.ledger)) as db, db:
            db.execute('UPDATE usage SET bytes=9876')
        upgrade.rollback(Path(result['backup']))
        self.assertEqual(json.loads((self.config / 'agent.json').read_text()), self.cfg)
        with closing(sqlite3.connect(self.ledger)) as db:
            self.assertEqual(db.execute('SELECT bytes FROM usage').fetchone()[0], 9876)
        self.assertEqual([item[1] for item in self.firewall_calls], ['inspect', 'apply', 'cleanup'])

    def test_reality_health_failure_restores_agent_configuration(self):
        self.enable_reality()
        original = (self.config / 'agent.json').read_bytes()
        with patch.object(upgrade, 'verify_reality', side_effect=RuntimeError('Reality ingress initialization failed')):
            with self.assertRaisesRegex(RuntimeError, 'Reality ingress'):
                upgrade.upgrade(self.node, self.artifacts)
        self.assertEqual((self.config / 'agent.json').read_bytes(), original)
        self.assertFalse(any('restart' in call for call in self.calls))

    def test_existing_reality_port_is_never_reselected(self):
        self.enable_reality()
        self.cfg['reality'] = {'port': 3443, 'backendPort': 443, 'serverName': 'www.example.com'}
        with patch.object(upgrade.socket, 'socket', side_effect=AssertionError('reselection')):
            actual = upgrade.reality_configuration(self.config, self.cfg)
        self.assertEqual(actual, {'port': 3443, 'backendPort': 1443, 'serverName': 'www.example.com'})

    def test_rollback_does_not_remove_preexisting_firewall_rule(self):
        self.enable_reality()
        def existing(node, action, rules):
            self.firewall_calls.append((node, action, rules))
            return rules if action == 'inspect' else {'added': [], 'error': False}
        with patch.object(upgrade, 'reality_firewall', side_effect=existing):
            result = upgrade.upgrade(self.node, self.artifacts)
            upgrade.rollback(Path(result['backup']))
        self.assertEqual(self.firewall_calls[-1][2], [])

    def test_firewall_failure_rolls_back_before_reporting_success(self):
        self.enable_reality()
        def failing(node, action, rules):
            return {'added': rules, 'error': True} if action == 'apply' else []
        with patch.object(upgrade, 'reality_firewall', side_effect=failing):
            with self.assertRaisesRegex(RuntimeError, 'firewall'):
                upgrade.upgrade(self.node, self.artifacts)
        self.assertNotIn('reality', json.loads((self.config / 'agent.json').read_text()))
