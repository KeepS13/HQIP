"""Build the embedded mainland snapshot from pinned, offline upstream inputs.

Generate cn.txt with V2Fly's own compiler so include filters and affiliations
retain upstream semantics. This converter rejects new rule syntax for review;
it never downloads data during subscription requests. See docs/mainland-routing.md.
"""

import argparse
import hashlib
import ipaddress
import json
from pathlib import Path

DOMAIN_COMMIT = "dff1013c77ee24daaffb2168b5b425894a9f87bc"
IP_COMMIT = "a1214434b6211eca49508ef42989daaa83fa5436"
INPUT_SHA256 = (
    "4f895181d042ed46b7fd3e34b7da2bbfa6a41910b9621bf82c69d8d0bfdd04c2",
    "c6c8512853a6400a535a66c814c91a5819ad6389377f54a7688ffd2557ea2e2e",
    "149c1227b848764228dfeaa1ab7b31e9d582fde39f70a55ce9dff5aa61a5b3ac",
)
AWS_REGEX = r".+\.awsdns-cn-[0-9][0-9]\.(biz|com|net|top)$"
CN_REGEX = r".+\.awsdns-cn-[0-9][a-e0-9]\.cn$"
MIHAYO_REGEX = r"^.+-mihayo\.akamaized\.net$"


def domain_rules(data):
    """Flatten finite patterns without broadening shared overseas CDN domains."""
    values = set()
    for line in data.decode("utf-8").splitlines():
        kind, value, *_ = line.split(":")
        if kind in {"domain", "full"}:
            values.add(("DOMAIN-SUFFIX" if kind == "domain" else "DOMAIN", value))
        elif kind == "regexp" and value == AWS_REGEX:
            for number in range(100):
                for tld in ("biz", "com", "net", "top"):
                    values.add(("DOMAIN-SUFFIX", f"awsdns-cn-{number:02d}.{tld}"))
        elif kind == "regexp" and value in {CN_REGEX, MIHAYO_REGEX}:
            # CN_REGEX is already covered by .cn. The unbounded mihayo hostname
            # pattern cannot be expressed safely in legacy Clash scalar rules;
            # leave it to CN IP matching, never bypass all of akamaized.net.
            continue
        else:
            raise ValueError(f"Unsupported mainland domain entry: {line}")
    return [{"type": kind, "value": value, "outbound": "direct"}
            for kind, value in sorted(values)]


def ip_prefixes(data, version):
    result = []
    for line in data.decode("utf-8").splitlines():
        if not line or line.startswith("#"):
            continue
        prefix = ipaddress.ip_network(line, strict=True)
        if prefix.version != version or prefix.prefixlen == 0:
            raise ValueError("Unexpected address family or default route")
        result.append(str(prefix))
    if not result:
        raise ValueError("Empty mainland IP input")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--domains", type=Path, required=True)
    parser.add_argument("--ipv4", type=Path, required=True)
    parser.add_argument("--ipv6", type=Path, required=True)
    parser.add_argument("--output", type=Path, default=Path("internal/rules/data/mainland.json"))
    args = parser.parse_args()
    domains, ipv4, ipv6 = [path.read_bytes() for path in (args.domains, args.ipv4, args.ipv6)]
    digest = lambda data: hashlib.sha256(data).hexdigest()
    if tuple(digest(data) for data in (domains, ipv4, ipv6)) != INPUT_SHA256:
        raise ValueError("Inputs differ from the reviewed pins; update commits and hashes together")
    rows = domain_rules(domains)
    snapshot = {
        "rules": rows,
        "geoipCN": {
            "repository": "https://github.com/gaoyifan/china-operator-ip",
            "commit": IP_COMMIT, "license": "MIT",
            "ipv4SHA256": digest(ipv4), "ipv6SHA256": digest(ipv6),
            "cidrs": sorted(set(ip_prefixes(ipv4, 4) + ip_prefixes(ipv6, 6))),
        },
    }
    encoded = (json.dumps(snapshot, separators=(",", ":")) + "\n").encode()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(encoded)
    print(json.dumps({"domainCommit": DOMAIN_COMMIT, "ipCommit": IP_COMMIT,
                      "flattenedDomainSHA256": digest(domains), "snapshotSHA256": digest(encoded),
                      "domains": len(rows), "prefixes": len(snapshot["geoipCN"]["cidrs"])}))


if __name__ == "__main__":
    main()
