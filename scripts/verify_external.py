"""Verify a candidate with a separate Mihomo process before publishing its node.

uv run --with pyyaml python scripts/verify_external.py --credentials /secure/admin.json \
  --node NODE_ID --device DEVICE_ID --mihomo /path/to/mihomo
Credentials JSON: {url,username,password}. No credentials are printed.
"""
from __future__ import annotations
import argparse, http.cookiejar, json, os, pathlib, secrets, socket, subprocess, tempfile, time, urllib.error, urllib.parse, urllib.request
import yaml

class API:
    def __init__(self, credentials, proxy=None):
        self.url = credentials["url"].rstrip("/")
        self.credentials = credentials
        self.csrf = ""
        handlers = [urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())]
        handlers.append(urllib.request.ProxyHandler({"http": proxy, "https": proxy} if proxy else {}))
        self.opener = urllib.request.build_opener(*handlers)

    def request(self, path, body=None, method=None, raw=False):
        headers = {"Content-Type": "application/json", "X-CSRF-Token": self.csrf, "Origin": self.url}
        data = json.dumps(body).encode() if body is not None else None
        url = path if path.startswith("https://") else self.url + path
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with self.opener.open(req, timeout=25) as response:
                data = response.read()
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"API HTTP {error.code}: {error.read().decode(errors='replace')[:300]}") from None
        return data if raw else json.loads(data)

    def login(self):
        session = self.request("/api/v1/login", {"username": self.credentials["username"], "password": self.credentials["password"]})
        self.csrf = session["csrfToken"]
        if session.get("mustChangePassword"):
            raise RuntimeError("Change the bootstrap administrator password before verification")

def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]

class Mihomo:
    def __init__(self, binary, config_text):
        self.directory = tempfile.TemporaryDirectory(prefix="hqip-mihomo-")
        folder = pathlib.Path(self.directory.name)
        self.port = free_port()
        self.controller = free_port()
        self.secret = secrets.token_urlsafe(24)
        config = yaml.safe_load(config_text)
        config.update({"mixed-port": self.port, "port": 0, "socks-port": 0, "redir-port": 0, "tproxy-port": 0, "allow-lan": False, "bind-address": "127.0.0.1", "external-controller": f"127.0.0.1:{self.controller}", "secret": self.secret, "tun": {"enable": False}, "log-level": "warning"})
        config.setdefault("dns", {})["listen"] = f"127.0.0.1:{free_port()}"
        self.config = config
        path = folder / "config.yaml"
        path.write_text(yaml.safe_dump(config, allow_unicode=True, sort_keys=False), encoding="utf-8")
        if os.name != "nt":
            path.chmod(0o600)
        flags = subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0
        validation = subprocess.run([binary, "-t", "-d", str(folder), "-f", str(path)], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, creationflags=flags, timeout=30)
        if validation.returncode:
            raise RuntimeError("Mihomo configuration validation failed: " + validation.stdout.decode(errors="replace")[-1000:])
        self.log = (folder / "mihomo.log").open("wb")
        self.process = subprocess.Popen([binary, "-d", str(folder), "-f", str(path)], stdout=self.log, stderr=subprocess.STDOUT, creationflags=flags)
        proxy = f"http://127.0.0.1:{self.port}"
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({"http": proxy, "https": proxy}))
        for _ in range(60):
            if self.process.poll() is not None:
                raise RuntimeError("Standalone Mihomo exited before listening")
            try:
                with socket.create_connection(("127.0.0.1", self.port), timeout=.3):
                    return
            except OSError:
                time.sleep(.25)
        raise RuntimeError("Standalone Mihomo did not start")

    def select_node(self, node):
        matches = [p["name"] for p in self.config["proxies"] if p.get("server") in (node["publicIp"], node["domain"]) and p.get("port") == node["port"]]
        if not matches:
            raise RuntimeError("Target node not present in configuration")
        url = f"http://127.0.0.1:{self.controller}/proxies/" + urllib.parse.quote("节点选择")
        request = urllib.request.Request(url, json.dumps({"name": matches[0]}).encode(), headers={"Content-Type": "application/json", "Authorization": "Bearer " + self.secret}, method="PUT")
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=5) as response:
            response.read()

    def fetch(self, url, data=None, timeout=35):
        request = urllib.request.Request(url, data, headers={"User-Agent": "HQIP-connectivity-verifier/1.0"})
        with self.http.open(request, timeout=timeout) as response:
            return response.read()

    def exit_ip(self):
        last = None
        for url in ["https://api.ipify.org", "https://ipv4.icanhazip.com"]:
            for _ in range(2):
                try:
                    return self.fetch(url).decode().strip()
                except Exception as error:
                    last = error
                    time.sleep(1)
        raise RuntimeError(f"Proxy HTTPS/UDP verification failed: {type(last).__name__}")

    def close(self):
        if hasattr(self, "process"):
            self.process.terminate()
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        if hasattr(self, "log"):
            self.log.close()
        self.directory.cleanup()

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--credentials", required=True)
    parser.add_argument("--node", required=True)
    parser.add_argument("--device", required=True)
    parser.add_argument("--mihomo", required=True)
    parser.add_argument("--management-proxy", help="Optional existing HTTP proxy for management API only")
    args = parser.parse_args()
    api = API(json.loads(pathlib.Path(args.credentials).read_text(encoding="utf-8-sig")), args.management_proxy)
    api.login()
    node = api.request(f"/api/v1/nodes/{args.node}")["node"]
    before = api.request(f"/api/v1/devices/{args.device}")["device"]
    candidate = api.request(f"/api/v1/nodes/{args.node}/verification-config", {"deviceId": args.device})["config"]
    client = Mihomo(args.mihomo, candidate)
    try:
        client.select_node(node)
        actual = client.exit_ip()
        if actual != node["publicIp"]:
            raise RuntimeError(f"Exit IP mismatch: got {actual}")
        data = client.fetch("https://speed.cloudflare.com/__down?bytes=2097152")
        if len(data) != 2097152:
            raise RuntimeError("Download payload size mismatch")
        client.fetch("https://speed.cloudflare.com/__up", data=b"HQIP-test" * 16384)
        print(f"Candidate passed: exit IP {actual}, 2 MiB download and 144 KiB upload")
        for _ in range(12):
            time.sleep(2)
            after = api.request(f"/api/v1/devices/{args.device}")["device"]
            if after["totalDownload"] > before["totalDownload"] and after["totalUpload"] > before["totalUpload"]:
                break
        else:
            raise RuntimeError("Agent did not report independent device usage")
        api.request(f"/api/v1/nodes/{args.node}/external-verification", {"exitIp": actual})
        print("Agent traffic increased and external node verification recorded")
    finally:
        client.close()
    device = api.request(f"/api/v1/devices/{args.device}")["device"]
    subscription = api.request(device["subscriptionUrl"], raw=True).decode()
    client = Mihomo(args.mihomo, subscription)
    try:
        client.select_node(node)
        if client.exit_ip() != node["publicIp"]:
            raise RuntimeError("Published subscription exit IP mismatch")
        print("Published Clash subscription parsed and successfully connected")
    finally:
        client.close()

if __name__ == "__main__":
    main()
