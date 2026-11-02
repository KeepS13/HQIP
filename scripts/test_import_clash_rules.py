"""Regression checks for the credential-free local rule importer."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("import_clash_rules", Path(__file__).with_name("import_clash_rules.py"))
importer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(importer)


class ImportRulesTests(unittest.TestCase):
    def groups(self):
        return {name: {"type": "select", "first": "DIRECT" if name in importer.DIRECT_GROUPS else "Proxy"} for name in importer.SERVICE_GROUPS}

    def test_extract_does_not_retain_credentials_or_node_names(self):
        text = "proxies:\n  - name: TOP_SECRET_NODE\n    password: TOP_SECRET_PASSWORD\nproxy-providers:\n  private:\n    url: https://invalid.example/SECRET_TOKEN\nproxy-groups:\n"
        for name in sorted(importer.SERVICE_GROUPS):
            first = "DIRECT" if name in importer.DIRECT_GROUPS else "TOP_SECRET_NODE"
            text += f"  - name: {name}\n    type: select\n    proxies:\n      - {first}\n      - TOP_SECRET_NODE\n"
        text += "rules:\n  - DOMAIN,example.com,Apple\n  - MATCH,Others\n"
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "source.yaml"
            path.write_text(text, encoding="utf-8")
            raw, groups = importer.extract_routing(path)
        result = json.dumps({"rules": importer.convert_rules(raw, groups), "groups": groups})
        for marker in ["TOP_SECRET_PASSWORD", "SECRET_TOKEN", "TOP_SECRET_NODE"]:
            self.assertNotIn(marker, result)

    def test_preserves_order_and_default_service_decisions(self):
        raw = ["DOMAIN,apple.example,Apple", "IP-CIDR,203.0.113.7/24,Proxy,no-resolve", "PROCESS-NAME,app.exe,GlobalTV", "GEOIP,CN,DIRECT", "MATCH,Others"]
        result = importer.convert_rules(raw, self.groups())
        self.assertEqual([r["type"] for r in result], ["DOMAIN", "IP-CIDR", "PROCESS-NAME", "GEOIP", "MATCH"])
        self.assertEqual([r["outbound"] for r in result], ["direct", "proxy", "proxy", "direct", "proxy"])
        self.assertEqual(result[1]["value"], "203.0.113.0/24")
        self.assertTrue(result[1]["noResolve"])

    def test_unknown_rules_options_groups_fail_closed(self):
        for raw in [["SCRIPT,anything,Proxy", "MATCH,Others"], ["GEOIP,US,DIRECT", "MATCH,Others"], ["DOMAIN,example.com,Proxy,no-resolve", "MATCH,Others"], ["DOMAIN,example.com,UNKNOWN", "MATCH,Others"], ["MATCH,Others", "DOMAIN,example.com,DIRECT"]]:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                importer.convert_rules(raw, self.groups())
        groups = self.groups()
        groups["Apple"]["first"] = "Proxy"
        with self.assertRaises(ValueError):
            importer.convert_rules(["MATCH,Others"], groups)

    def test_geo_family_validation(self):
        self.assertEqual(importer.parse_cidrs(b"# comment\n1.0.1.0/24\n", 4), ["1.0.1.0/24"])
        with self.assertRaises(ValueError):
            importer.parse_cidrs(b"2001:250::/32\n", 4)
        with self.assertRaises(ValueError):
            importer.parse_cidrs(b"\n# empty\n", 4)


if __name__ == "__main__":
    unittest.main()
