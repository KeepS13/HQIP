"""Bootstrap/update the control host from locally verified artifacts over pinned SSH."""
from __future__ import annotations
import argparse, hashlib, io, json, os, pathlib, re, shlex, tarfile
from ssh_connection import connect, credentials, execute

ROOT = pathlib.Path(__file__).resolve().parents[1]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--credentials", required=True)
    parser.add_argument("--fingerprint", required=True)
    parser.add_argument("--domain", required=True)
    parser.add_argument("--socks")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9.-]+[a-zA-Z0-9]", args.domain):
        raise ValueError("Invalid domain")
    host, password = credentials(args.credentials)
    manifest = json.loads((ROOT / "dist/manifest.json").read_text())
    names = ["hqip-linux-amd64", "agent-linux-amd64", "agent-linux-arm64", "hysteria-linux-amd64", "hysteria-linux-arm64", "caddy-linux-amd64"]
    for name in names:
        if hashlib.file_digest((ROOT / "dist" / name).open("rb"), "sha256").hexdigest() != manifest["sha256"][name]:
            raise ValueError(f"Checksum mismatch: {name}")
    with connect(host, password, args.fingerprint, socks=args.socks) as client:
        execute(client, "test \"$(id -u)\" = 0 && test \"$(uname -m)\" = x86_64 && test -f /etc/os-release")
        execute(client, "if test ! -f /etc/hqip/owner; then test -z \"$(ss -H -ltn 'sport = :443')\" && test -z \"$(ss -H -ltn 'sport = :80')\"; fi")
        execute(client, "install -d -m 755 /opt/hqip /opt/hqip/artifacts /etc/hqip /etc/systemd/journald@hqip.conf.d; install -d -m 700 /var/lib/hqip /var/lib/hqip-caddy; touch /etc/hqip/owner")
        sftp = client.open_sftp()
        try:
            for name in names:
                destination = {"hqip-linux-amd64": "/opt/hqip/hqip", "caddy-linux-amd64": "/opt/hqip/caddy"}.get(name, "/opt/hqip/artifacts/" + name)
                existing = execute(client, "if test -f " + shlex.quote(destination) + "; then sha256sum " + shlex.quote(destination) + "; fi").split()
                if existing and existing[0] == manifest["sha256"][name]:
                    print(f"Verified existing {name}", flush=True)
                    continue
                print(f"Uploading {name}", flush=True)
                sftp.put(str(ROOT / "dist" / name), destination + ".new")
                checksum = execute(client, "sha256sum " + shlex.quote(destination + ".new")).split()[0]
                if checksum != manifest["sha256"][name]:
                    raise RuntimeError("Uploaded checksum mismatch")
                execute(client, "chmod 755 " + shlex.quote(destination + ".new") + " && mv -f " + shlex.quote(destination + ".new") + " " + shlex.quote(destination))
            files = {
                "/opt/hqip/artifacts/manifest.json": json.dumps(manifest, indent=2),
                "/etc/hqip/Caddyfile": (ROOT / "deploy/Caddyfile.template").read_text().replace("__DOMAIN__", args.domain),
                "/etc/hqip/control.env": "HQIP_PUBLIC_URL=https://" + args.domain + "\n",
                "/etc/systemd/system/hqip.service": (ROOT / "deploy/hqip.service").read_text(),
                "/etc/systemd/system/hqip-caddy.service": (ROOT / "deploy/hqip-caddy.service").read_text(),
                "/etc/systemd/journald@hqip.conf.d/limits.conf": "[Journal]\nSystemMaxUse=100M\nRuntimeMaxUse=16M\nMaxRetentionSec=7day\n",
            }
            for path, content in files.items():
                with sftp.file(path + ".new", "w") as handle:
                    handle.write(content)
                sftp.chmod(path + ".new", 0o644)
                execute(client, "mv -f " + shlex.quote(path + ".new") + " " + shlex.quote(path))
        finally:
            sftp.close()
        output = execute(client, r"""set -eu
getent passwd hqip >/dev/null || useradd --system --home-dir /var/lib/hqip --shell /usr/sbin/nologin hqip
getent passwd hqip-caddy >/dev/null || useradd --system --home-dir /var/lib/hqip-caddy --shell /usr/sbin/nologin hqip-caddy
chown -R hqip:hqip /var/lib/hqip
chown -R hqip-caddy:hqip-caddy /var/lib/hqip-caddy
/opt/hqip/caddy validate --config /etc/hqip/Caddyfile --adapter caddyfile
if test ! -f /etc/hqip/admin-created; then
 runuser -u hqip -- /opt/hqip/hqip init-admin --data-dir /var/lib/hqip --username admin --output /var/lib/hqip/bootstrap-admin.json
 touch /etc/hqip/admin-created
fi
systemctl daemon-reload
systemctl enable hqip.service hqip-caddy.service >/dev/null
systemctl restart hqip.service hqip-caddy.service
if command -v ufw >/dev/null 2>&1 && ufw status | head -1 | grep -q '^Status: active$'; then ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null; fi
systemctl is-active hqip.service hqip-caddy.service
""", timeout=90)
        print(output)
    print("Control services started. Initial credentials are in /var/lib/hqip/bootstrap-admin.json (owner-only).")

if __name__ == "__main__":
    main()
