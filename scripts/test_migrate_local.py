import pathlib
import json
import sqlite3
import tempfile
import unittest
from contextlib import closing

from migrate_local import ROOT, allowed_caddy, repair_local_topology


class CaddyMigrationTests(unittest.TestCase):
    def test_preserves_acme_and_does_not_proxy_administrator_surface(self):
        original = (ROOT / "deploy/Caddyfile.template").read_text()
        candidate = allowed_caddy(original)
        self.assertIn("reverse_proxy 127.0.0.1:18080", candidate)
        self.assertIn("protocols h1 h2", candidate)
        self.assertIn("/api/v1/client/* /api/v1/agents/* /sub/* /healthz", candidate)
        self.assertIn("reverse_proxy 127.0.0.1:18088", candidate)
        self.assertNotIn("reverse_proxy 127.0.0.1:8080", candidate)
        self.assertNotIn("/api/v1/login", candidate)
        self.assertIn('respond "Not Found" 404', candidate)
        self.assertEqual(candidate.count("@hqipPublic path"), 1)
        self.assertEqual(candidate.count("handle @hqipPublic {"), 1)
        self.assertEqual(candidate.count("handle {"), 2)  # HTTP redirect plus HTTPS fallback

    def test_legacy_top_level_proxy_is_replaced_with_allowlist(self):
        original = '''http://example.test {
    handle /.well-known/acme-challenge/* {
        reverse_proxy 127.0.0.1:18080
    }
    redir https://example.test{uri} permanent
}
https://example.test {
    tls {
        issuer acme {
            disable_http_challenge
        }
    }
    reverse_proxy 127.0.0.1:8080
}
'''
        candidate = allowed_caddy(original)
        self.assertIn("reverse_proxy 127.0.0.1:18088", candidate)
        self.assertIn("reverse_proxy 127.0.0.1:18080", candidate)
        self.assertIn("@hqipPublic path /api/v1/client/* /api/v1/agents/* /sub/* /healthz", candidate)
        self.assertIn('respond "Not Found" 404', candidate)
        self.assertNotIn("reverse_proxy 127.0.0.1:8080", candidate)

    def test_rejects_unknown_nested_proxy_layout(self):
        with self.assertRaisesRegex(ValueError, "recognized Caddy layout"):
            allowed_caddy('''https://example.test {
    handle {
        reverse_proxy 127.0.0.1:8080
    }
}
''')

    def test_refuses_unknown_or_ambiguous_existing_layout(self):
        for source in [
            "reverse_proxy elsewhere:8080",
            "reverse_proxy 127.0.0.1:8080\nreverse_proxy 127.0.0.1:8080",
            "reverse_proxy 127.0.0.1:8081\nreverse_proxy 127.0.0.1:8081",
            "reverse_proxy 127.0.0.1:8080\nreverse_proxy 127.0.0.1:8081",
        ]:
            with self.assertRaises(ValueError):
                allowed_caddy(source)

    def test_bootstrap_template_exposes_only_the_public_api(self):
        template = (ROOT / "deploy/Caddyfile.template").read_text()
        self.assertIn("/api/v1/client/* /api/v1/agents/* /sub/* /healthz", template)
        self.assertIn("reverse_proxy 127.0.0.1:8081", template)
        self.assertNotIn("reverse_proxy 127.0.0.1:8080", template)
        self.assertIn('respond "Not Found" 404', template)
        self.assertNotIn("/api/v1/login", template)

    def test_fresh_service_starts_both_local_listeners_used_by_caddy(self):
        service = (ROOT / "deploy/hqip.service").read_text()
        self.assertIn("--listen 127.0.0.1:8080", service)
        self.assertIn("--public-listen 127.0.0.1:8081", service)
        self.assertIn("--admin-origin http://127.0.0.1:8080", service)
        self.assertIn("reverse_proxy 127.0.0.1:8081", (ROOT / "deploy/Caddyfile.template").read_text())


class TopologyMigrationTests(unittest.TestCase):
    def make_database(self, folder, version="2"):
        db = sqlite3.connect(folder / "hqip.db")
        db.executescript("CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE nodes(id TEXT PRIMARY KEY,config TEXT,agent_secret TEXT,desired_revision INTEGER);")
        db.execute("INSERT INTO metadata VALUES('schema_version',?)", (version,))
        for node_id, ssh_host, public_ip in [("target", "127.0.0.1", "203.0.113.10"), ("other", "localhost", "203.0.113.20"), ("external", "203.0.113.10", "203.0.113.10")]:
            config = {"id": node_id, "sshHost": ssh_host, "publicIp": public_ip, "hostKey": "PINNED-KEY", "controlOnNode": True, "sharedHost": True}
            db.execute("INSERT INTO nodes VALUES(?,?,?,?)", (node_id, json.dumps(config), "ENCRYPTED-SECRET", 123))
        db.commit()
        return db

    def test_repairs_only_matching_loopback_preserves_credentials_and_pin(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = pathlib.Path(temp)
            with closing(self.make_database(folder)) as db:
                before = dict(db.execute("SELECT id,config FROM nodes"))
                self.assertEqual(repair_local_topology(folder, "203.0.113.10"), 1)
                target, secret, revision = db.execute("SELECT config,agent_secret,desired_revision FROM nodes WHERE id='target'").fetchone()
                target = json.loads(target)
                self.assertEqual(target["sshHost"], "203.0.113.10")
                self.assertFalse(target["controlOnNode"])
                self.assertTrue(target["acmeViaCaddy"])
                self.assertEqual(target["hostKey"], "PINNED-KEY")
                self.assertEqual((secret, revision), ("ENCRYPTED-SECRET", 123))
                for node_id in ("other", "external"):
                    self.assertEqual(db.execute("SELECT config FROM nodes WHERE id=?", (node_id,)).fetchone()[0], before[node_id])
                self.assertEqual(repair_local_topology(folder, "203.0.113.10"), 0)

    def test_unexpected_schema_does_not_mutate(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = pathlib.Path(temp)
            with closing(self.make_database(folder, "1")) as db:
                before = list(db.execute("SELECT * FROM nodes"))
                with self.assertRaisesRegex(ValueError, "schema version"):
                    repair_local_topology(folder, "203.0.113.10")
                self.assertEqual(list(db.execute("SELECT * FROM nodes")), before)


if __name__ == "__main__":
    unittest.main()
