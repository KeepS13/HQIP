"""Regression coverage for the pinned mainland source conversion."""
import unittest

import build_mainland_rules as rules


class MainlandBuildTest(unittest.TestCase):
    def test_domains_keep_exact_suffix_distinction(self):
        got = rules.domain_rules(b"domain:bilibili.com:@cn\nfull:cdn.example.org:@cn\n")
        self.assertIn({"type": "DOMAIN-SUFFIX", "value": "bilibili.com", "outbound": "direct"}, got)
        self.assertIn({"type": "DOMAIN", "value": "cdn.example.org", "outbound": "direct"}, got)

    def test_finite_aws_expansion_never_bypasses_shared_cdn(self):
        source = f"regexp:{rules.AWS_REGEX}:@cn\nregexp:{rules.CN_REGEX}:@cn\nregexp:{rules.MIHAYO_REGEX}:@cn\n"
        got = rules.domain_rules(source.encode())
        self.assertEqual(len(got), 400)
        self.assertTrue(all(row["value"].startswith("awsdns-cn-") for row in got))
        with self.assertRaises(ValueError):
            rules.domain_rules(b"regexp:.*\\.com$\n")

    def test_bad_ip_sources_fail_closed(self):
        for data, version in [(b"::/0", 6), (b"0.0.0.0/0", 4), (b"1.0.1.0/24", 6), (b"", 4)]:
            with self.assertRaises(ValueError):
                rules.ip_prefixes(data, version)


if __name__ == "__main__":
    unittest.main()
