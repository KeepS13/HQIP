"""Generate isolated node B fixture scripts. This tool never connects or executes SSH."""
from __future__ import annotations
import argparse
import base64
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex

AGENT_UNIT = "hqip-agent-b.service"
CORE_UNIT = "hqip-hysteria-b.service"
PATHS = ("/opt/hqip-node-b", "/etc/hqip-node-b", "/var/lib/hqip-agent-b", "/var/lib/hqip-hysteria-b")

def _path(value: str) -> str:
    if not value.startswith("/") or any(c in value for c in "\x00\r\n") or ".." in value.split("/"):
        raise ValueError("expected an absolute path without traversal")
    return value

def _write(path: str, content: str, mode: str = "600") -> str:
    payload = base64.b64encode(content.encode()).decode()
    return f"printf '%s' {shlex.quote(payload)} | base64 -d > {shlex.quote(path)}\nchmod {mode} {shlex.quote(path)}\n"

def prepare(config: dict, cert: str, key: str, agent_binary="/opt/hqip-node/agent", hysteria_binary="/opt/hqip-node/hysteria") -> str:
    for value in (cert, key, agent_binary, hysteria_binary):
        _path(value)
    if not re.fullmatch(r"[A-Za-z0-9_-]{1,80}", config["nodeId"]):
        raise ValueError("invalid node ID")
    for name in ("token", "statsSecret"):
        if len(config[name]) < 24 or "\x00" in config[name]:
            raise ValueError(f"invalid {name}")
    if not config["controlUrl"].startswith("https://") or any(c in config["controlUrl"] for c in "\r\n\x00"):
        raise ValueError("control URL must use HTTPS")
    if not re.fullmatch(r"[A-Za-z0-9.-]+", config["domain"]):
        raise ValueError("invalid domain")
    if ipaddress.ip_address(config["publicIp"]).version != 4 or config.get("port", 8443) != 8443:
        raise ValueError("fixture B must use IPv4 and UDP 8443")
    agent = dict(controlUrl=config["controlUrl"], nodeId=config["nodeId"], token=config["token"], listen="127.0.0.1:18181", statePath="/var/lib/hqip-agent-b/state.db", statsUrl="http://127.0.0.1:18182", statsSecret=config["statsSecret"], hysteriaService=CORE_UNIT)
    blocked = ["0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"]
    core = dict(listen=":8443", tls=dict(cert="/etc/hqip-node-b/cert.pem", key="/etc/hqip-node-b/key.pem"), auth=dict(type="http", http=dict(url="http://127.0.0.1:18181/auth?secret=" + config["statsSecret"])), trafficStats=dict(listen="127.0.0.1:18182", secret=config["statsSecret"]), acl=dict(inline=[f"reject({cidr})" for cidr in blocked]), resolver=dict(type="udp", udp=dict(addr="1.1.1.1:53", timeout="4s")), outbounds=[dict(name="direct", type="direct", direct=dict(mode="4"))])
    script = "#!/bin/sh\nset -eu\numask 077\ntest \"$(id -u)\" = 0\n"
    script += "command -v systemctl >/dev/null\ncommand -v ss >/dev/null\ncommand -v base64 >/dev/null\n"
    for path in PATHS + (f"/etc/systemd/system/{AGENT_UNIT}", f"/etc/systemd/system/{CORE_UNIT}"):
        script += f"test ! -e {shlex.quote(path)} && test ! -L {shlex.quote(path)} || {{ echo 'fixture B already exists; inspect it before reuse' >&2; exit 1; }}\n"
    for path in (agent_binary, hysteria_binary, cert, key):
        script += f"test -f {shlex.quote(path)} && test -r {shlex.quote(path)}\n"
    script += "test -z \"$(ss -H -lun 'sport = :8443')\"\nfor p in 18181 18182; do test -z \"$(ss -H -ltn \"sport = :$p\")\"; done\n"
    script += "install -d -m 700 " + " ".join(PATHS) + "\n"
    for source, target, mode in ((agent_binary, "/opt/hqip-node-b/agent", "755"), (hysteria_binary, "/opt/hqip-node-b/hysteria", "755"), (cert, "/etc/hqip-node-b/cert.pem", "600"), (key, "/etc/hqip-node-b/key.pem", "600")):
        script += f"install -m {mode} {shlex.quote(source)} {target}\n"
    script += _write("/etc/hqip-node-b/owner", config["nodeId"])
    script += _write("/etc/hqip-node-b/agent.json", json.dumps(agent, indent=2))
    script += _write("/etc/hqip-node-b/hysteria.yaml", json.dumps(core, indent=2))
    common = "Restart=always\nRestartSec=3\nUMask=0077\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=true\nPrivateTmp=true\nLimitNOFILE=65536\nLogNamespace=hqip\n[Install]\nWantedBy=multi-user.target\n"
    agent_unit = "[Unit]\nDescription=HQIP temporary node B agent\nAfter=network-online.target\n[Service]\nType=simple\nExecStart=/opt/hqip-node-b/agent --config /etc/hqip-node-b/agent.json\nReadWritePaths=/var/lib/hqip-agent-b\n" + common
    core_unit = "[Unit]\nDescription=HQIP temporary node B Hysteria\nAfter=network-online.target hqip-agent-b.service\nWants=hqip-agent-b.service\n[Service]\nType=simple\nWorkingDirectory=/var/lib/hqip-hysteria-b\nExecStart=/opt/hqip-node-b/hysteria server --config /etc/hqip-node-b/hysteria.yaml --log-level error\nReadWritePaths=/var/lib/hqip-hysteria-b\n" + common
    script += _write(f"/etc/systemd/system/{AGENT_UNIT}", agent_unit, "644")
    script += _write(f"/etc/systemd/system/{CORE_UNIT}", core_unit, "644")
    script += f"systemctl daemon-reload\nsystemctl enable {AGENT_UNIT} {CORE_UNIT} >/dev/null 2>&1\nsystemctl start {AGENT_UNIT} {CORE_UNIT}\n"
    script += "if command -v ufw >/dev/null 2>&1 && ufw status | head -1 | grep -q '^Status: active$'; then ufw allow 8443/udp >/dev/null; fi\n"
    script += "echo 'Fixture B started; external UDP/TLS probe is still required.'\n"
    return script

def cleanup() -> str:
    return f"#!/bin/sh\nset -eu\ntest \"$(id -u)\" = 0\nsystemctl stop {CORE_UNIT} {AGENT_UNIT}\nsystemctl disable {CORE_UNIT} {AGENT_UNIT} >/dev/null 2>&1\necho 'Fixture B stopped; configuration, ledger and certificate copy preserved.'\n"

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["prepare", "cleanup"])
    parser.add_argument("--config", type=Path)
    parser.add_argument("--cert")
    parser.add_argument("--key")
    parser.add_argument("--agent-bin", default="/opt/hqip-node/agent")
    parser.add_argument("--hysteria-bin", default="/opt/hqip-node/hysteria")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    if args.action == "prepare":
        if not all((args.config, args.cert, args.key)):
            parser.error("prepare requires --config, --cert and --key")
        script = prepare(json.loads(args.config.read_text(encoding="utf-8")), args.cert, args.key, args.agent_bin, args.hysteria_bin)
    else:
        script = cleanup()
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as output:
        output.write(script)
    print("Fixture script written; no SSH commands were executed.")

if __name__ == "__main__":
    main()
