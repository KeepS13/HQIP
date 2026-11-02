"""Prepare and explicitly cut over HQIP to a local controller through pinned SSH.

The four modes are intentionally separate: prepare does not stop services;
cutover freezes the old controller and restores its final consistent backup;
activate requires a running tunnel and only then switches Caddy; rollback returns
to the frozen server database (it does not merge later local changes).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import secrets
import shlex
import shutil
import sqlite3
import subprocess
import sys
from datetime import datetime, timezone
from contextlib import closing

from ssh_connection import connect, credentials, execute

ROOT = pathlib.Path(__file__).resolve().parents[1]
REMOTE_CANDIDATE = "/etc/hqip/Caddyfile.local-candidate"
REMOTE_ORIGINAL = "/etc/hqip/Caddyfile.before-local"
TUNNEL_USER = "hqip-tunnel"


def protect(path: pathlib.Path) -> None:
    path.mkdir(parents=True, exist_ok=True)
    if os.name == "nt":
        identity = subprocess.check_output(["whoami"], text=True).strip()
        subprocess.run(["icacls", str(path), "/inheritance:r", "/grant:r", f"{identity}:(OI)(CI)F", "SYSTEM:(OI)(CI)F"], check=True, stdout=subprocess.DEVNULL)
    else:
        path.chmod(0o700)


def save_json(path: pathlib.Path, value: dict) -> None:
    temp = path.with_suffix(path.suffix + ".new")
    temp.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding="utf-8")
    if os.name != "nt":
        temp.chmod(0o600)
    temp.replace(path)


def sha_file(path: pathlib.Path) -> str:
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def allowed_caddy(original: str) -> str:
    public_paths = "/api/v1/client/* /api/v1/agents/* /sub/* /healthz"
    upstreams = re.findall(r"(?m)^(\s*)reverse_proxy 127\.0\.0\.1:(8080|8081)\s*$", original)
    all_proxies = re.findall(r"(?m)^\s*reverse_proxy\s+([^\s]+)", original)
    if len(upstreams) != 1:
        raise ValueError("Expected exactly one known HQIP upstream; review the current Caddyfile before migration")
    expected_proxies = ["127.0.0.1:" + upstreams[0][1]]
    if "127.0.0.1:18080" in all_proxies:
        expected_proxies.append("127.0.0.1:18080")
    if sorted(all_proxies) != sorted(expected_proxies):
        raise ValueError("Unexpected reverse proxy route; review the current Caddyfile before migration")

    indent, upstream_port = upstreams[0]
    proxy_line = f"{indent}reverse_proxy 127.0.0.1:{upstream_port}"
    route_block = re.compile(
        rf"(?ms)^[ \t]*@hqipPublic path {re.escape(public_paths)}\r?\n"
        rf"[ \t]*handle @hqipPublic \{{\r?\n"
        rf"[ \t]*reverse_proxy 127\.0\.0\.1:{upstream_port}\r?\n"
        rf"[ \t]*\}}\r?\n"
        rf"[ \t]*handle \{{\r?\n"
        rf"[ \t]*respond \"Not Found\" 404\r?\n"
        rf"[ \t]*\}}"
    )
    if len(route_block.findall(original)) == 1:
        candidate = original.replace(proxy_line, indent + "reverse_proxy 127.0.0.1:18088", 1)
    else:
        # Older bootstrap files used a single top-level reverse_proxy. Replace
        # only that direct site directive. Nested or unfamiliar handlers are
        # rejected rather than rewritten loosely.
        depth = 0
        line_depth = None
        for line in original.splitlines():
            if line.rstrip() == proxy_line:
                line_depth = depth
                break
            stripped = line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            if stripped == "}":
                depth -= 1
            if stripped.endswith("{"):
                depth += 1
        if line_depth != 1:
            raise ValueError("HQIP proxy is not in a recognized Caddy layout; review it before migration")
        replacement = f"""{indent}@hqipPublic path {public_paths}
{indent}handle @hqipPublic {{
{indent}    reverse_proxy 127.0.0.1:18088
{indent}}}
{indent}handle {{
{indent}    respond \"Not Found\" 404
{indent}}}"""
        candidate = original.replace(proxy_line, replacement, 1)

    if candidate.count("reverse_proxy 127.0.0.1:18088") != 1 or "reverse_proxy 127.0.0.1:8080" in candidate or "reverse_proxy 127.0.0.1:8081" in candidate:
        raise ValueError("Could not safely isolate the public HQIP route")
    if "reverse_proxy 127.0.0.1:18080" in original and "reverse_proxy 127.0.0.1:18080" not in candidate:
        raise ValueError("The Hysteria ACME validation route was not preserved")
    return candidate


def remote_write(sftp, path: str, value: str, mode: int = 0o600) -> None:
    with sftp.file(path, "w") as handle:
        handle.write(value.encode())
    sftp.chmod(path, mode)


def known_hosts_line(host: str, key) -> str:
    return f"{host} {key.get_name()} {key.get_base64()}\n"


def repair_local_topology(data_dir: pathlib.Path, public_host: str) -> int:
    """Rewrite only the old controller's loopback SSH target after local restore.

    Credentials, host-key pins, usage and node revisions are deliberately left
    untouched. The old Caddy remains on the VPS to serve the ACME challenge.
    """
    database = data_dir / "hqip.db"
    if not database.is_file():
        raise ValueError("Restored HQIP database does not exist")
    with closing(sqlite3.connect(database.as_uri() + "?mode=rw", uri=True)) as db, db:
        db.execute("BEGIN IMMEDIATE")
        columns = {row[1] for row in db.execute("PRAGMA table_info(nodes)")}
        if not {"id", "config"}.issubset(columns):
            raise ValueError("Unexpected HQIP nodes schema; topology was not changed")
        version = db.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone()
        if version != ("2",):
            raise ValueError("Expected migrated HQIP schema version 2")
        updates = []
        for node_id, raw in db.execute("SELECT id,config FROM nodes"):
            node = json.loads(raw)
            if node.get("sshHost") not in ("127.0.0.1", "localhost", "::1") or node.get("publicIp") != public_host:
                continue
            node["sshHost"] = public_host
            node["controlOnNode"] = False
            node["acmeViaCaddy"] = True
            node["sharedHost"] = True
            updates.append((json.dumps(node, ensure_ascii=False, separators=(",", ":")), node_id))
        db.executemany("UPDATE nodes SET config=? WHERE id=?", updates)
        return len(updates)


def prepare(client, args, folder: pathlib.Path, host: str) -> dict:
    config_path = folder / "controller.json"
    if config_path.exists():
        raise ValueError("Preparation already exists; use its existing configuration or choose a new --state-dir")
    key_path = folder / "tunnel_ed25519"
    ssh_keygen = shutil.which("ssh-keygen")
    if not ssh_keygen:
        raise ValueError("OpenSSH ssh-keygen is required on the local computer")
    subprocess.run([ssh_keygen, "-t", "ed25519", "-f", str(key_path), "-N", "", "-C", "hqip-local-controller"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    public_key = key_path.with_suffix(".pub").read_text().strip()
    if not re.fullmatch(r"ssh-ed25519 [A-Za-z0-9+/=]+ hqip-local-controller", public_key):
        raise ValueError("Unexpected generated public key format")
    if os.name == "nt":
        identity = subprocess.check_output(["whoami"], text=True).strip()
        subprocess.run(["icacls", str(key_path), "/inheritance:r", "/grant:r", f"{identity}:F"], check=True, stdout=subprocess.DEVNULL)
    remote_key = client.get_transport().get_remote_server_key()
    (folder / "known_hosts").write_text(known_hosts_line(host, remote_key), encoding="ascii")
    sftp = client.open_sftp()
    try:
        with sftp.file("/etc/hqip/Caddyfile", "r") as handle:
            original = handle.read().decode()
        candidate = allowed_caddy(original)
        (folder / "Caddyfile.before-local").write_text(original, encoding="utf-8")
        (folder / "Caddyfile.local-candidate").write_text(candidate, encoding="utf-8")
        remote_write(sftp, REMOTE_CANDIDATE, candidate, 0o644)
    finally:
        sftp.close()
    execute(client, "/opt/hqip/caddy validate --config " + REMOTE_CANDIDATE + " --adapter caddyfile")
    execute(client, """set -eu
getent passwd hqip-tunnel >/dev/null || useradd --system --create-home --home-dir /var/lib/hqip-tunnel --shell /bin/sh hqip-tunnel
usermod -p '*' hqip-tunnel
install -d -m 700 -o hqip-tunnel -g hqip-tunnel /var/lib/hqip-tunnel/.ssh
test ! -e /var/lib/hqip-tunnel/.ssh/authorized_keys
test ! -e /etc/ssh/sshd_config.d/70-hqip-tunnel.conf
""")
    authorized = 'restrict,port-forwarding,permitlisten="127.0.0.1:18088",command="/bin/false" ' + public_key + "\n"
    restriction = """Match User hqip-tunnel
    AuthenticationMethods publickey
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AllowTcpForwarding remote
    PermitListen 127.0.0.1:18088
    PermitOpen none
    GatewayPorts no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
    ForceCommand /bin/false
Match all
"""
    sftp = client.open_sftp()
    try:
        remote_write(sftp, "/var/lib/hqip-tunnel/.ssh/authorized_keys", authorized)
        remote_write(sftp, "/etc/ssh/sshd_config.d/70-hqip-tunnel.conf", restriction, 0o644)
    finally:
        sftp.close()
    execute(client, "chown hqip-tunnel:hqip-tunnel /var/lib/hqip-tunnel/.ssh/authorized_keys && /usr/sbin/sshd -t && systemctl reload ssh")
    ssh_config = f"""Host hqip-controller
    HostName {host}
    User hqip-tunnel
    Port 22
    IdentityFile "{key_path.as_posix()}"
    IdentitiesOnly yes
    UserKnownHostsFile "{(folder / 'known_hosts').as_posix()}"
    StrictHostKeyChecking yes
    BatchMode yes
    ExitOnForwardFailure yes
    ServerAliveInterval 10
    ServerAliveCountMax 3
    ConnectTimeout 15
    RemoteForward 127.0.0.1:18088 127.0.0.1:8081
"""
    if args.socks:
        if not re.fullmatch(r"[A-Za-z0-9.:-]+:[0-9]+", args.socks):
            raise ValueError("Invalid SOCKS endpoint")
        ssh_config += f'    ProxyCommand "{pathlib.Path(sys.executable).as_posix()}" "{(ROOT / "scripts/ssh_socks_proxy.py").as_posix()}" --socks {args.socks} --host %h --port %p\n'
    (folder / "ssh_config").write_text(ssh_config, encoding="utf-8")
    config = {
        "version": 1, "host": host, "fingerprint": args.fingerprint,
        "publicUrl": "https://" + args.domain,
        "adminOrigin": "http://127.0.0.1:8080", "adminListen": "127.0.0.1:8080", "publicListen": "127.0.0.1:8081",
        "binary": str(pathlib.Path(args.binary).resolve()), "artifactDir": str((ROOT / "dist").resolve()),
        "dataDir": "", "sshConfig": str(folder / "ssh_config"),
        "stateDir": str(folder), "phase": "prepared", "originalCaddyHash": hashlib.sha256(original.encode()).hexdigest(),
        "candidateCaddyHash": hashlib.sha256(candidate.encode()).hexdigest(),
    }
    save_json(config_path, config)
    return config


def cutover(client, folder: pathlib.Path, config: dict) -> dict:
    if config["phase"] not in ("prepared", "frozen"):
        raise ValueError("Cutover requires a prepared or interrupted frozen configuration")
    binary = pathlib.Path(config["binary"])
    if not binary.is_file():
        raise ValueError("Build the current Windows HQIP binary before freezing the server")
    stamp = config.get("snapshotID") or datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + secrets.token_hex(3)
    remote_backup = "/var/lib/hqip/final-local-" + stamp + ".tar.gz"
    local_backup = folder / ("server-final-" + stamp + ".tar.gz")
    restore_dir = folder / ("data-" + stamp)
    # Freeze first: the snapshot must include the final successful admin writes.
    execute(client, "systemctl disable --now hqip")
    config["phase"] = "frozen"
    config["snapshotID"] = stamp
    config["remoteBackup"] = remote_backup
    save_json(folder / "controller.json", config)
    execute(client, "if test ! -f " + shlex.quote(remote_backup) + "; then runuser -u hqip -- /opt/hqip/hqip backup --data-dir /var/lib/hqip --output " + shlex.quote(remote_backup) + "; fi", timeout=180)
    sftp = client.open_sftp()
    try:
        sftp.get(remote_backup, str(local_backup))
    finally:
        sftp.close()
    remote_hash = execute(client, "sha256sum " + shlex.quote(remote_backup)).split()[0]
    if sha_file(local_backup) != remote_hash:
        raise RuntimeError("Downloaded final database backup checksum mismatch; server remains frozen for recovery")
    subprocess.run([str(binary), "restore", "--input", str(local_backup), "--data-dir", str(restore_dir)], check=True)
    repair_local_topology(restore_dir, config["host"])
    config.update({"dataDir": str(restore_dir), "localBackup": str(local_backup), "backupSHA256": remote_hash, "phase": "restored"})
    save_json(folder / "controller.json", config)
    return config


def activate(client, folder: pathlib.Path, config: dict) -> dict:
    if config["phase"] != "restored":
        raise ValueError("Activate requires the final backup restored and the local supervisor started")
    check = """import urllib.request, urllib.error
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
for path,expected in [('/healthz',200),('/api/v1/login',404),('/api/v1/nodes',404),('/',404)]:
 try:
  response=opener.open('http://127.0.0.1:18088'+path,timeout=10)
  status=response.status
 except urllib.error.HTTPError as error:
  status=error.code
 if status!=expected: raise SystemExit('Tunnel API isolation check failed')
"""
    execute(client, "python3 -", stdin_data=check)
    actual = execute(client, "sha256sum /etc/hqip/Caddyfile").split()[0]
    if actual != config["originalCaddyHash"]:
        raise RuntimeError("Active Caddy configuration changed since preparation; review it before activating")
    candidate_hash = execute(client, "sha256sum " + REMOTE_CANDIDATE).split()[0]
    if candidate_hash != config["candidateCaddyHash"]:
        raise RuntimeError("Prepared Caddy configuration checksum mismatch")
    execute(client, f"test ! -e {REMOTE_ORIGINAL} && cp -p /etc/hqip/Caddyfile {REMOTE_ORIGINAL} && /opt/hqip/caddy validate --config {REMOTE_CANDIDATE} --adapter caddyfile && cp {REMOTE_CANDIDATE} /etc/hqip/Caddyfile && systemctl reload hqip-caddy && systemctl disable hqip", timeout=45)
    config["phase"] = "active"
    save_json(folder / "controller.json", config)
    return config


def rollback(client, folder: pathlib.Path, config: dict) -> dict:
    execute(client, f"if test -f {REMOTE_ORIGINAL}; then cp {REMOTE_ORIGINAL} /etc/hqip/Caddyfile; fi; /opt/hqip/caddy validate --config /etc/hqip/Caddyfile --adapter caddyfile && systemctl enable --now hqip && systemctl reload hqip-caddy", timeout=60)
    config["phase"] = "rolled_back"
    save_json(folder / "controller.json", config)
    return config


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["prepare", "cutover", "activate", "rollback"])
    parser.add_argument("--credentials", required=True)
    parser.add_argument("--fingerprint", required=True)
    parser.add_argument("--domain", default="testvps.sharevps.online")
    parser.add_argument("--state-dir", default=str(ROOT / ".local/controller"))
    parser.add_argument("--binary", default=str(ROOT / "dist/hqip-windows-amd64.exe"))
    parser.add_argument("--socks")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]", args.domain):
        raise ValueError("Invalid domain")
    host, password = credentials(args.credentials)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.:-]*", host):
        raise ValueError("Invalid SSH host")
    folder = pathlib.Path(args.state_dir).resolve()
    protect(folder)
    with connect(host, password, args.fingerprint, socks=args.socks) as client:
        if args.mode == "prepare":
            config = prepare(client, args, folder, host)
        else:
            config = json.loads((folder / "controller.json").read_text(encoding="utf-8"))
            if config["host"] != host or config["fingerprint"] != args.fingerprint:
                raise ValueError("Migration target differs from the prepared SSH identity")
            config = {"cutover": cutover, "activate": activate, "rollback": rollback}[args.mode](client, folder, config)
    print(f"Migration phase: {config['phase']}. Local configuration: {folder / 'controller.json'}")
    if args.mode == "cutover":
        print("Start the local supervisor, verify its listeners and tunnel, then run activate.")
    if args.mode == "rollback":
        print("Rollback uses the frozen server database; changes made only to the local database are not merged.")


if __name__ == "__main__":
    main()
