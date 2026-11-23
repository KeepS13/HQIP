"""Exercise exported Clash rules/DNS on real cores without changing system proxy.

First run TestPortalClashMainlandExport with HQIP_RULES_ARTIFACTS set. This test
replaces only listener ports, DNS endpoints and node transports with loopback
fixtures; the exported rules, DNS policy keys, mode and group membership stay
intact. Authentic transport/config parsing is covered separately by Go tests.
"""

import argparse
import contextlib
import http.server
import json
import pathlib
import re
import socket
import struct
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

import yaml


class DNS:
    """Deterministic UDP DNS fixture; every question remains on this computer."""
    def __init__(self, answer):
        self.answer = socket.inet_aton(answer)
        self.questions = []
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.bind(("127.0.0.1", 0))
        self.address = "127.0.0.1:" + str(self.socket.getsockname()[1])
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def serve(self):
        self.socket.settimeout(.2)
        while not self.stopped.is_set():
            try:
                query, peer = self.socket.recvfrom(4096)
            except socket.timeout:
                continue
            except OSError:
                return
            pos, labels = 12, []
            while query[pos]:
                size = query[pos]
                labels.append(query[pos + 1:pos + 1 + size].decode())
                pos += size + 1
            qtype, qclass = struct.unpack("!HH", query[pos + 1:pos + 5])
            name = ".".join(labels).lower()
            self.questions.append(name)
            count = int(qtype == 1 and qclass == 1)
            result = query[:2] + struct.pack("!HHHHH", 0x8180, 1, count, 0, 0) + query[12:pos + 5]
            if count:
                answer = self.answer
                if name not in MAINLAND and name not in STOPPED_MAINLAND:
                    answer = socket.inet_aton("203.0.113.254")
                result += b"\xc0\x0c" + struct.pack("!HHIH", 1, 1, 0, 4) + answer
            self.socket.sendto(result, peer)

    def close(self):
        self.stopped.set()
        self.thread.join(timeout=1)
        self.socket.close()


def http_fixture(marker, sniff_port=False):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_CONNECT(self):
            self.send_response(200)
            self.end_headers()
            self.close_connection = False
            self.handle_one_request()

        def do_GET(self):
            body = marker.encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass
    for port in (range(8080, 8881) if sniff_port else [0]):
        server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler, bind_and_activate=False)
        server.allow_reuse_address = False
        if hasattr(socket, "SO_EXCLUSIVEADDRUSE"):
            server.socket.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
        try:
            server.server_bind()
            server.server_activate()
            break
        except OSError:
            server.server_close()
            continue
    else:
        raise AssertionError("No free loopback HTTP sniff-test port")
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def fetch(port, host, target_port):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({
        "http": f"http://127.0.0.1:{port}",
    }))
    request = urllib.request.Request(f"http://{host}:{target_port}/", headers={"Connection": "close"})
    with opener.open(request, timeout=5) as response:
        return response.read().decode()


def sniff_ip_request(port, target_port):
    """An IP-only CONNECT must use HTTP Host for policy, keeping the target IP."""
    with socket.create_connection(("127.0.0.1", port), timeout=5) as sock:
        sock.sendall(f"CONNECT 127.0.0.1:{target_port} HTTP/1.1\r\nHost: 127.0.0.1:{target_port}\r\n\r\n".encode())
        response = b""
        while b"\r\n\r\n" not in response:
            part = sock.recv(4096)
            assert part, "CONNECT closed before acknowledgement"
            response += part
        assert b" 200 " in response.split(b"\r\n")[0], response
        sock.sendall(f"GET /sniff HTTP/1.1\r\nHost: www.bilibili.com:{target_port}\r\nConnection: close\r\n\r\n".encode())
        response = b""
        while part := sock.recv(4096):
            response += part
        assert response.endswith(b"DIRECT"), "IP-only application traffic did not go direct"


MAINLAND = [
    "www.bilibili.com", "api.bilibili.com", "api.biliapi.net", "upos.bilivideo.com",
    "i0.hdslb.com", "v.acgvideo.com", "www.douyin.com", "v.douyinvod.com",
    "www.kuaishou.com", "v.qq.com", "wx.qq.com", "music.163.com", "www.iqiyi.com",
    "www.youku.com", "www.taobao.com", "www.jd.com", "www.pinduoduo.com",
    "www.alipay.com", "www.meituan.com", "www.baidu.com", "www.zhihu.com",
    "www.xiaohongshu.com", "www.weibo.com", "www.12306.cn",
]
OVERSEAS = ["www.google.com", "www.youtube.com", "api.openai.com", "bilibili.com.example.org", "evilbilibili.com"]
STOPPED_MAINLAND = ["after-stop.bilibili.com", "after-stop.qq.com", "after-stop.douyin.com"]


def isolate_singbox(original, port, proxy_port, local, remote):
    config = json.loads(original)
    config["log"] = {"level": "debug"}
    config["inbounds"] = [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": port}]
    config["outbounds"] = [
        {"type": "http", "tag": node["tag"], "server": "127.0.0.1", "server_port": proxy_port}
        if node["type"] in {"hysteria2", "vless"} else node for node in config["outbounds"]]
    config["dns"]["servers"] = [{"type": "udp", "tag": tag, "server": "127.0.0.1", "server_port": int(dns.address.split(":")[1])}
                                 for tag, dns in [("local", local), ("remote", remote)]]
    return config


def verify(binary, original, output, family="clash"):
    local, remote = DNS("127.0.0.1"), DNS("203.0.113.254")
    direct, proxy = http_fixture("DIRECT", sniff_port=True), http_fixture("PROXY")
    process = None
    try:
        port = free_port()
        if family == "singbox":
            config = isolate_singbox(original, port, proxy.server_port, local, remote)
        else:
            config = yaml.safe_load(original)
            config["mixed-port"] = port
            config["log-level"] = "info"
            config["proxies"] = [{"name": p["name"], "type": "http", "server": "127.0.0.1", "port": proxy.server_port}
                                 for p in config["proxies"]]
            dns = config["dns"]
            dns["default-nameserver"] = ["127.0.0.1"]
            dns["nameserver"] = [local.address]
            dns["fallback"] = [remote.address]
            dns["nameserver-policy"] = {key: [local.address] for key in dns["nameserver-policy"]}
        with tempfile.TemporaryDirectory(prefix="hqip-clash-routing-") as folder, contextlib.ExitStack() as cleanup:
            path = pathlib.Path(folder, "config.json" if family == "singbox" else "config.yaml")
            path.write_text(json.dumps(config) if family == "singbox" else yaml.safe_dump(config, allow_unicode=True, sort_keys=False), encoding="utf-8")
            command = [str(binary), "run", "-c", str(path)] if family == "singbox" else [str(binary), "-d", folder, "-f", str(path)]
            with output.open("wb") as log:
                process = subprocess.Popen(command, stdout=log, stderr=log,
                                           creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
                cleanup.callback(stop_process, process)
                deadline = time.monotonic() + 10
                while True:
                    try:
                        with socket.create_connection(("127.0.0.1", port), timeout=.1):
                            break
                    except OSError:
                        if process.poll() is not None or time.monotonic() > deadline:
                            raise AssertionError("Core failed to start: " + str(binary))
                        time.sleep(.05)
                for host in MAINLAND:
                    assert fetch(port, host, direct.server_port) == "DIRECT", host
                assert not set(MAINLAND).intersection(remote.questions), "mainland DNS went to fallback"
                for host in OVERSEAS:
                    assert fetch(port, host, direct.server_port) == "PROXY", host
                sniff_ip_request(port, direct.server_port)
                proxy.shutdown()
                proxy.server_close()
                for host in STOPPED_MAINLAND:
                    assert fetch(port, host, direct.server_port) == "DIRECT", host
                    assert host not in remote.questions, "direct DNS required proxy"
                try:
                    result = fetch(port, "after-stop.google.com", direct.server_port)
                except (OSError, urllib.error.URLError):
                    result = "FAILED"
                assert result == "FAILED", "overseas traffic bypassed the stopped proxy"
                process.terminate()
                process.wait(timeout=5)
            logs = output.read_text(encoding="utf-8", errors="replace").splitlines()
            if family == "clash":
                assert any("127.0.0.1:" + str(direct.server_port) in line and "DomainSuffix(bilibili.com)" in line
                           and "DIRECT" in line for line in logs), "IP-only flow was not classified by the sniffed domain"
            for host in MAINLAND:
                if family == "singbox":
                    matched = any("route(direct)" in line and (m := re.search(r"domain_suffix=(\S+)", line))
                                  and (host == m[1] or host.endswith("." + m[1])) for line in logs)
                else:
                    matched = any(host in line and "DIRECT" in line and re.search(r"Domain(Suffix)?\(", line) for line in logs)
                assert matched, (
                    "Request did not hit a domain rule (a private IP bypass would mask the bug): " + host)
            return {"family": family, "core": binary.parent.name, "mainland": len(MAINLAND), "overseas": len(OVERSEAS),
                    "directWithProxyStopped": 3, "mainlandDNSViaFallback": 0, "ipOnlyHTTPSniff": "passed", "result": "passed"}
    finally:
        if process:
            stop_process(process)
        for server in (direct, proxy):
            server.shutdown()
            server.server_close()
        local.close()
        remote.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=pathlib.Path, required=True)
    parser.add_argument("--core", type=pathlib.Path, action="append", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--family", choices=["clash", "singbox"], default="clash")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    original = args.config.read_text(encoding="utf-8")
    results = [verify(core.resolve(), original, args.output / (core.parent.name + ".log"), args.family) for core in args.core]
    (args.output / "results.json").write_text(json.dumps(results, indent=2), encoding="utf-8")
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
