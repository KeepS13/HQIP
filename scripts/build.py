"""Build reproducible HQIP release assets; third-party binaries are hash pinned."""
from __future__ import annotations
import argparse, hashlib, json, os, pathlib, shutil, subprocess, sys, tarfile, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
OUT = ROOT / "dist"
PINNED = {
    "hysteria-linux-amd64": ("https://github.com/HyNetworks/hysteria/releases/download/app/v2.13.0/hysteria-linux-amd64", "907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691"),
    "hysteria-linux-arm64": ("https://github.com/HyNetworks/hysteria/releases/download/app/v2.13.0/hysteria-linux-arm64", "a68a61a84452ca250ce0368202521965ca9cc9d801a404f1dc9008ac6cf677a7"),
    "hysteria-windows-amd64.exe": ("https://github.com/HyNetworks/hysteria/releases/download/app/v2.13.0/hysteria-windows-amd64.exe", "162ef8fe55dc7ec810dda662908f8e65ec36bd6da39cb87c3bd66184ab68f067"),
    "hysteria-windows-arm64.exe": ("https://github.com/HyNetworks/hysteria/releases/download/app/v2.13.0/hysteria-windows-arm64.exe", "b22d223025299801cb018cab35120cb8f4f9fe2e73e44b9e60a48963f7dc91e7"),
    "caddy-linux-amd64.tar.gz": ("https://github.com/caddyserver/caddy/releases/download/v2.11.7/caddy_2.11.7_linux_amd64.tar.gz", "727b91701a392de6ebc5027509f548bf39979e5216340d0faed8fa5e69c84f8b"),
}

def run(args, **kwargs):
    subprocess.run(args, check=True, cwd=ROOT, **kwargs)

def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()

def download(name, url, expected):
    target = OUT / name
    if target.exists() and digest(target) == expected:
        return
    print(f"Downloading and verifying {name}", flush=True)
    request = urllib.request.Request(url, headers={"User-Agent": "HQIP-release-builder/1.0"})
    partial = target.with_suffix(target.suffix + ".part")
    with urllib.request.urlopen(request, timeout=120) as response, partial.open("wb") as output:
        while block := response.read(1024 * 1024):
            output.write(block)
    if digest(partial) != expected:
        partial.unlink()
        raise RuntimeError(f"Checksum mismatch: {name}")
    partial.replace(target)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--skip-web", action="store_true")
    build_scope = parser.add_mutually_exclusive_group()
    build_scope.add_argument("--skip-go", action="store_true")
    build_scope.add_argument("--server-only", action="store_true",
                             help="Build production Linux assets without legacy Windows CLI downloads; requires fresh Go builds.")
    parser.add_argument("--skip-download", action="store_true")
    args = parser.parse_args()
    OUT.mkdir(exist_ok=True)
    if not args.skip_web:
        npm = "npm.cmd" if os.name == "nt" else "npm"
        for frontend in ["admin-web", "user-web"]:
            run([npm, "--prefix", frontend, "ci"])
            run([npm, "--prefix", frontend, "run", "build"])
    if not args.skip_go:
        for arch in ["amd64", "arm64"]:
            env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
            for command, name in [("hqip", "hqip"), ("hqip-agent", "agent"), ("hqip-migrate-postgres", "hqip-migrate-postgres")]:
                # sing-vmess's Vision adapter for the Reality uTLS connection
                # is build-tagged; every released Agent must include it.
                run(["go", "build", "-tags=with_utls", "-trimpath", "-ldflags=-s -w", "-o", str(OUT / f"{name}-linux-{arch}"), f"./cmd/{command}"], env=env)
            if not args.server_only:
                env = dict(os.environ, GOOS="windows", GOARCH=arch, CGO_ENABLED="0")
                run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(OUT / f"hqip-windows-{arch}.exe"), "./cmd/hqip"], env=env)
        run([sys.executable, str(ROOT / "scripts/build_hy2.py")])
    if not args.skip_download:
        for name, (url, expected) in PINNED.items():
            if name.startswith("hysteria-linux-"):
                continue  # Linux nodes use the reviewed, pinned-source extension.
            if args.server_only and name.startswith("hysteria-windows-"):
                continue  # The desktop job builds the actual sing-box client.
            download(name, url, expected)
    if args.server_only and args.skip_download:
        cached = OUT / "caddy-linux-amd64.tar.gz"
        if not cached.is_file() or digest(cached) != PINNED[cached.name][1]:
            raise ValueError("Missing or mismatched pinned Caddy cache")
    if not args.skip_download or args.server_only:
        with tarfile.open(OUT / "caddy-linux-amd64.tar.gz", "r:gz") as archive:
            # Never extract arbitrary archive members onto the filesystem.
            with archive.extractfile("caddy") as source, (OUT / "caddy-linux-amd64").open("wb") as destination:
                destination.write(source.read())
    for arch in ["amd64", "arm64"]:
        extended = OUT / f"hqip-hysteria-linux-{arch}"
        provenance = json.loads((OUT / "hqip-hysteria-manifest.json").read_text(encoding="utf-8"))
        if not extended.is_file() or provenance["sha256"].get(extended.name) != digest(extended):
            raise ValueError("Missing or mismatched HQIP admission core: " + extended.name)
        shutil.copyfile(extended, OUT / f"hysteria-linux-{arch}")
    names = [f"{name}-linux-{arch}" for arch in ["amd64", "arm64"] for name in ["hqip", "agent", "hqip-migrate-postgres", "hysteria"]]
    names += ["caddy-linux-amd64"]
    if not args.server_only:
        names += [f"{name}-windows-{arch}.exe" for arch in ["amd64", "arm64"] for name in ["hqip", "hysteria"]]
    extension = OUT / "hqip-hysteria-manifest.json"
    source_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=normal"], cwd=ROOT, text=True).strip())
    # Hash exactly this build's selected targets. Missing output is an error;
    # stale Windows files or an older manifest never expand production scope.
    manifest = {"sourceSHA": source_sha, "dirty": dirty, "hysteriaVersion": "2.13.0-hqip.1", "hqipAdmissionVersion": 1, "offlineAdmissionVersion": 1, "vlessRealityVersion": 1, "caddyVersion": "2.11.7", "sha256": {name: digest(OUT / name) for name in names}}
    manifest['trafficBillingVersion'] = 1
    manifest['nodeQuotaVersion'] = 1
    if extension.exists():
        manifest["hysteriaSource"] = json.loads(extension.read_text(encoding="utf-8"))
    (OUT / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print("Release manifest generated.")

if __name__ == "__main__":
    main()
