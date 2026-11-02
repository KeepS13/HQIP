import unittest
import fixture_node_b_remote as fixture

class NodeBFixtureTests(unittest.TestCase):
    def config(self):
        return dict(nodeId="node-b", token="t"*40, statsSecret="s"*40, controlUrl="https://example.com", publicIp="203.0.113.1", domain="example.com", port=8443, probePassword="not-copied-to-node")
    def test_isolation_and_secrets(self):
        script=fixture.prepare(self.config(),"/cert/observed.crt","/cert/observed.key")
        self.assertIn("test ! -e /var/lib/hqip-agent-b",script)
        self.assertIn("18181 18182",script)
        self.assertIn("sport = :8443",script)
        self.assertNotIn("t"*40,script)
        self.assertNotIn("s"*40,script)
        self.assertNotIn("not-copied-to-node",script)
        self.assertNotIn("set -x",script)
        self.assertNotIn("systemctl restart hqip-hysteria.service",script)
    def test_cleanup_preserves_history(self):
        script=fixture.cleanup()
        self.assertIn("systemctl stop hqip-hysteria-b.service hqip-agent-b.service",script)
        self.assertNotIn("rm ",script)
    def test_reject_traversal_and_wrong_port(self):
        with self.assertRaises(ValueError): fixture.prepare(self.config(),"/a/../secret","/key")
        config=self.config();config["port"]=443
        with self.assertRaises(ValueError): fixture.prepare(config,"/cert","/key")

if __name__ == "__main__": unittest.main()
