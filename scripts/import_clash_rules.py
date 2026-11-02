"""Import only ordered rules and policy defaults, never proxy credentials.

Writes private runtime data to .local/rules by default. The mainland IP lists are
pinned to an immutable upstream commit; updating that pin is an explicit change.
This deliberately accepts only the ordinary scalar/list YAML emitted by Mihomo.
It rejects unfamiliar syntax instead of evaluating scripts or YAML tags.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.request
from datetime import datetime, timezone


GEO_REPO = "https://github.com/gaoyifan/china-operator-ip"
GEO_COMMIT = "ffabdf398bf6c9b3ffa2d66e536155b87188d0d9"
LICENSE_COMMIT = "0ca552913948e444853d3bce99f2f3f2477f17c7"
EXPECTED_ACTIVE_RULES = 1709
DIRECT_GROUPS = {"Bilibili", "Linkedin", "Steam", "Epic", "Apple"}
SERVICE_GROUPS = DIRECT_GROUPS | {"Proxy", "OpenAI", "Netflix", "DisneyPlus", "TWMedia", "Youtube", "GlobalTV", "Telegram", "Others"}
SUPPORTED = {"DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "PROCESS-NAME", "GEOIP", "MATCH"}


def scalar(value: str) -> str:
    value = value.strip()
    if not value or value[0] in "&*!{|>[":
        raise ValueError("Unsupported YAML scalar syntax in routing sections")
    if value.startswith('"'):
        parsed = json.loads(value)
        if not isinstance(parsed, str):
            raise ValueError("Routing values must be strings")
        return parsed
    if value.startswith("'"):
        if not value.endswith("'"):
            raise ValueError("Unclosed quoted routing value")
        return value[1:-1].replace("''", "'")
    if " #" in value:
        value = value.split(" #", 1)[0].rstrip()
    return value


def extract_routing(path: Path) -> tuple[list[str], dict[str, dict]]:
    """Stream selected sections; values under top-level proxies are discarded."""
    section = None
    rules: list[str] = []
    groups: dict[str, dict] = {}
    current = None
    in_candidates = False
    section_indent = 0
    with path.open(encoding="utf-8-sig") as source:
        for line in source:
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            if re.match(r"^[A-Za-z][\w-]*:", line):
                key = line.split(":", 1)[0]
                section = key if key in {"rules", "proxy-groups"} else None
                current = None
                in_candidates = False
                continue
            if section == "rules":
                match = re.match(r"^\s*-\s+(.+?)\s*$", line)
                if not match:
                    raise ValueError("Unsupported rule-list YAML syntax")
                rules.append(scalar(match.group(1)))
            elif section == "proxy-groups":
                match = re.match(r"^(\s*)-\s+name:\s*(.+?)\s*$", line)
                if match:
                    name = scalar(match.group(2))
                    current = {"type": None, "first": None}
                    if name in groups:
                        raise ValueError("Duplicate routing group name")
                    groups[name] = current
                    in_candidates = False
                    section_indent = len(match.group(1))
                    continue
                if current is None:
                    raise ValueError("Routing groups must use ordinary name-first block syntax")
                content = line.strip()
                if content.startswith("type:"):
                    current["type"] = scalar(content.split(":", 1)[1])
                    in_candidates = False
                elif content == "proxies:":
                    in_candidates = True
                elif in_candidates and re.match(r"^\s*-\s+", line):
                    if len(line) - len(line.lstrip()) <= section_indent:
                        raise ValueError("Invalid routing group candidate indentation")
                    if current["first"] is None:
                        candidate = scalar(content[1:])
                        # Retain only DIRECT/REJECT/group references. Never save a node name.
                        current["first"] = candidate if candidate in {"DIRECT", "REJECT", "Proxy"} else "__NODE__"
                else:
                    in_candidates = False
    if not rules:
        raise ValueError("No routing rules found")
    return rules, groups


def convert_rules(raw: list[str], groups: dict[str, dict]) -> list[dict]:
    known = set(groups) | {"DIRECT", "REJECT"}
    if set(groups) != SERVICE_GROUPS:
        raise ValueError("Inspected service routing groups changed; inspect before importing")
    for name, group in groups.items():
        if group["type"] != "select":
            raise ValueError("Only the inspected select routing groups are supported")
        if (name in DIRECT_GROUPS) != (group["first"] == "DIRECT"):
            raise ValueError("Service group defaults changed; inspect before importing")
    converted = []
    for index, text in enumerate(raw):
        parts = text.split(",")
        typ = parts[0].upper()
        if typ not in SUPPORTED:
            raise ValueError(f"Unsupported rule type at index {index + 1}: {typ}")
        base_count = 2 if typ == "MATCH" else 3
        if len(parts) not in {base_count, base_count + 1}:
            raise ValueError(f"Malformed rule at index {index + 1}")
        has_no_resolve = len(parts) == base_count + 1
        if has_no_resolve and (parts[-1] != "no-resolve" or typ not in {"IP-CIDR", "IP-CIDR6", "GEOIP"}):
            raise ValueError(f"Unsupported rule option at index {index + 1}")
        policy = parts[base_count - 1]
        if policy not in known:
            raise ValueError(f"Unknown routing policy at index {index + 1}")
        outbound = "direct" if policy == "DIRECT" or policy in DIRECT_GROUPS else "reject" if policy == "REJECT" else "proxy"
        row = {"type": typ, "outbound": outbound, "sourcePolicy": policy}
        if typ != "MATCH":
            value = parts[1]
            if typ in {"IP-CIDR", "IP-CIDR6"}:
                network = ipaddress.ip_network(value, strict=False)
                if network.version != (4 if typ == "IP-CIDR" else 6):
                    raise ValueError("Wrong IP address family")
                value = str(network)
            elif typ == "GEOIP":
                if value.upper() != "CN":
                    raise ValueError("Only the inspected CN GeoIP rule is supported")
                value = "CN"
            elif typ.startswith("DOMAIN"):
                value = value.lower()
            row["value"] = value
        elif index != len(raw) - 1:
            raise ValueError("MATCH must be the last rule")
        if has_no_resolve:
            row["noResolve"] = True
        converted.append(row)
    if converted[-1]["type"] != "MATCH":
        raise ValueError("Final MATCH is required")
    return converted


def upstream_file(name: str, commit: str) -> bytes:
    if not re.fullmatch(r"[a-fA-F0-9]{40}", commit):
        raise ValueError("An immutable 40-character source commit is required")
    url = f"https://api.github.com/repos/gaoyifan/china-operator-ip/contents/{name}?ref={commit}"
    request = urllib.request.Request(url, headers={"User-Agent": "HQIP-rule-import", "Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(request, timeout=45) as response:
        data = json.load(response)
    if data.get("encoding") != "base64" or int(data.get("size", 0)) > 2_000_000:
        raise ValueError("Unexpected upstream data encoding or size")
    return base64.b64decode(data["content"], validate=False)


def parse_cidrs(data: bytes, version: int) -> list[str]:
    values = []
    for line in data.decode("utf-8").splitlines():
        value = line.strip()
        if not value or value.startswith("#"):
            continue
        network = ipaddress.ip_network(value, strict=True)
        if network.version != version:
            raise ValueError("Wrong IP family in upstream source")
        values.append(str(network))
    if not values:
        raise ValueError("Empty upstream IP list")
    return values


def atomic_write(path: Path, data: bytes) -> None:
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_bytes(data)
    os.replace(temp, path)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, default=Path(".local/rules/bundle.json"))
    parser.add_argument("--geo-commit", default=GEO_COMMIT)
    parser.add_argument("--expected-count", type=int, default=EXPECTED_ACTIVE_RULES)
    parser.add_argument("--sing-box", type=Path, help="Optional pinned sing-box executable to compile the CN SRS")
    args = parser.parse_args()
    raw, groups = extract_routing(args.input)
    if len(raw) != args.expected_count:
        raise ValueError(f"Expected {args.expected_count} rules, found {len(raw)}; inspect source changes")
    converted = convert_rules(raw, groups)
    ipv4 = upstream_file("china.txt", args.geo_commit)
    ipv6 = upstream_file("china6.txt", args.geo_commit)
    license_text = upstream_file("LICENSE", LICENSE_COMMIT)
    if not license_text.startswith(b"MIT License"):
        raise ValueError("Unexpected upstream license")
    cidrs = sorted(set(parse_cidrs(ipv4, 4) + parse_cidrs(ipv6, 6)))
    bundle = {
        "schemaVersion": 1,
        "source": {"kind": "clash-import", "name": "Personal Clash routing snapshot", "sha256": hashlib.sha256("\n".join(raw).encode()).hexdigest(), "importedAt": datetime.now(timezone.utc).isoformat()},
        "rules": converted,
        "geoipCN": {"repository": GEO_REPO, "commit": args.geo_commit, "license": "MIT", "ipv4SHA256": hashlib.sha256(ipv4).hexdigest(), "ipv6SHA256": hashlib.sha256(ipv6).hexdigest(), "cidrs": cidrs},
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    atomic_write(args.output, json.dumps(bundle, ensure_ascii=False, separators=(",", ":")).encode())
    source = args.output.parent / "geoip-cn.json"
    atomic_write(source, json.dumps({"version": 3, "rules": [{"ip_cidr": cidrs}]}, separators=(",", ":")).encode())
    atomic_write(args.output.parent / "LICENSE.geoip-cn.txt", license_text)
    attribution = {"source": GEO_REPO, "dataCommit": args.geo_commit, "licenseCommit": LICENSE_COMMIT, "files": ["china.txt", "china6.txt"], "license": "MIT", "notice": "Copyright (c) 2017 Yifan Gao. CN rule set compiled from the pinned upstream IPv4/IPv6 lists."}
    atomic_write(args.output.parent / "geoip-cn-attribution.json", json.dumps(attribution, indent=2).encode())
    if args.sing_box:
        subprocess.run([str(args.sing_box.resolve()), "rule-set", "compile", str(source.resolve()), "--output", str((args.output.parent / "geoip-cn.srs").resolve())], check=True)
    print(json.dumps({"rules": len(converted), "cnPrefixes": len(cidrs), "sourceSHA256": bundle["source"]["sha256"], "geoCommit": args.geo_commit, "output": str(args.output.resolve())}))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"Rule import failed: {exc}", file=sys.stderr)
        raise SystemExit(1) from None
