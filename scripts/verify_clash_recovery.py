"""Verify exported node bootstrap and website recovery on real Clash cores.

All listeners and DNS fixtures are local. Exported routes/policy keys remain
intact; only listener/DNS endpoints and node transports are replaced. No system
proxy, real account or production node is changed. A silent fallback resolver
reproduces the original bootstrap failure, while a stopped proxy proves website
access does not merely work because a node happens to be online.
"""

import argparse
import contextlib
import json
import pathlib
import socket
import struct
import subprocess
import tempfile
import threading
import time
import urllib.parse
import urllib.request

import yaml

from verify_clash_routing import fetch, free_port, http_fixture, stop_process


SERVICES = ("hqip.sharevps.online", "cms-hqip.sharevps.online", "challenges.cloudflare.com")
NODE = "bootstrap.hqip.invalid"


class Resolver:
    """Answer test names locally, or deliberately drop all fallback requests."""

    def __init__(self, silent=False):
        self.silent, self.questions = silent, []
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.bind(("127.0.0.1", 0))
        self.socket.settimeout(.1)
        self.address = "127.0.0.1:" + str(self.socket.getsockname()[1])
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def serve(self):
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
                labels.append(query[pos + 1:pos + size + 1].decode())
                pos += size + 1
            self.questions.append(".".join(labels).lower())
            if self.silent:
                continue
            qtype, qclass = struct.unpack("!HH", query[pos + 1:pos + 5])
            count = int(qtype == 1 and qclass == 1)
            result = query[:2] + struct.pack("!HHHHH", 0x8180, 1, count, 0, 0) + query[12:pos + 5]
            if count:
                result += b"\xc0\x0c" + struct.pack("!HHIH", 1, 1, 0, 4) + socket.inet_aton("127.0.0.1")
            self.socket.sendto(result, peer)

    def close(self):
        self.stopped.set()
        self.thread.join(timeout=1)
        self.socket.close()


def api(port, path):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(f"http://127.0.0.1:{port}" + path, timeout=8) as response:
        return json.load(response)


def verify(core, original, output):
    with contextlib.ExitStack() as cleanup:
        local, fallback = Resolver(), Resolver(silent=True)
        cleanup.callback(local.close)
        cleanup.callback(fallback.close)
        direct, proxy = http_fixture("DIRECT"), http_fixture("PROXY")
        for server in (direct, proxy):
            cleanup.callback(server.server_close)
            cleanup.callback(server.shutdown)
        folder = cleanup.enter_context(tempfile.TemporaryDirectory(prefix="hqip-recovery-"))
        config = yaml.safe_load(original)
        mixed, controller = free_port(), free_port()
        config.update({"mixed-port": mixed, "external-controller": f"127.0.0.1:{controller}",
                       "secret": "", "log-level": "debug"})
        config["proxies"] = [{"name": p["name"], "type": "http", "server": NODE,
                              "port": proxy.server_port} for p in config["proxies"]]
        dns = config["dns"]
        assert dns.get("proxy-server-nameserver"), "export lacks independent node DNS"
        dns.update({"default-nameserver": ["127.0.0.1"], "nameserver": [local.address],
                    "proxy-server-nameserver": [local.address], "fallback": [fallback.address]})
        dns["nameserver-policy"] = {key: [local.address] for key in dns["nameserver-policy"]}
        path = pathlib.Path(folder, "config.yaml")
        path.write_text(yaml.safe_dump(config, allow_unicode=True), encoding="utf-8")
        log = cleanup.enter_context(output.open("wb"))
        process = subprocess.Popen([str(core), "-d", folder, "-f", str(path)], stdout=log, stderr=log,
                                   creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        cleanup.callback(stop_process, process)
        deadline = time.monotonic() + 10
        while True:
            try:
                api(controller, "/version")
                break
            except OSError:
                if process.poll() is not None or time.monotonic() > deadline:
                    raise AssertionError("Core did not start: " + str(core))
                time.sleep(.05)
        name = config["proxies"][0]["name"]
        target = "http://overseas.example/generate_204"
        delay = api(controller, "/proxies/" + urllib.parse.quote(name, safe="") + "/delay?" +
                    urllib.parse.urlencode({"url": target, "timeout": 5000}))
        assert delay.get("delay", 0) > 0, "node with a DNS hostname did not connect"
        assert NODE in local.questions and NODE not in fallback.questions, "node bootstrap used fallback"
        proxy.shutdown()
        proxy.server_close()
        for host in SERVICES:
            assert fetch(mixed, host, direct.server_port) == "DIRECT", host
            assert host in local.questions and host not in fallback.questions, "website used fallback DNS"
        try:
            api(controller, "/proxies/" + urllib.parse.quote(name, safe="") + "/delay?" +
                urllib.parse.urlencode({"url": target, "timeout": 2000}))
        except OSError:
            pass
        else:
            raise AssertionError("Stopped node unexpectedly remained usable")
        stop_process(process)
        log.flush()
        logs = output.read_text(encoding="utf-8", errors="replace")
        for host in SERVICES:
            assert any(f"Domain({host})" in line and "DIRECT" in line for line in logs.splitlines()), (
                "A loopback/IP rule masked the missing website domain exception: " + host)
        return {"core": str(core), "nodeBootstrapWithoutFallback": True,
                "websitesWithStoppedNode": list(SERVICES), "exactDomainRules": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=pathlib.Path, required=True)
    parser.add_argument("--core", type=pathlib.Path, action="append", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    original = args.config.read_text(encoding="utf-8")
    results = [verify(core.resolve(), original, args.output / f"{i}-{core.parent.name}.log")
               for i, core in enumerate(args.core)]
    (args.output / "results.json").write_text(json.dumps(results, indent=2), encoding="utf-8")
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
