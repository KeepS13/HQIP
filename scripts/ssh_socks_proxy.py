"""OpenSSH ProxyCommand helper; streams only raw bytes to stdout."""
import argparse
import os
import socket
import struct
import sys
import threading


def exact(sock, count):
    data = b""
    while len(data) < count:
        block = sock.recv(count - len(data))
        if not block:
            raise OSError("SOCKS peer closed the connection")
        data += block
    return data


def main():
    if os.name == "nt":
        import msvcrt
        msvcrt.setmode(sys.stdin.fileno(), os.O_BINARY)
        msvcrt.setmode(sys.stdout.fileno(), os.O_BINARY)
    parser = argparse.ArgumentParser()
    parser.add_argument("--socks", required=True)
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args()
    proxy_host, proxy_port = args.socks.rsplit(":", 1)
    with socket.create_connection((proxy_host, int(proxy_port)), timeout=15) as sock:
        sock.sendall(b"\x05\x01\x00")
        if exact(sock, 2) != b"\x05\x00":
            raise OSError("SOCKS proxy does not allow unauthenticated connections")
        try:
            address = b"\x01" + socket.inet_aton(args.host)
        except OSError:
            host = args.host.encode("idna")
            address = b"\x03" + bytes([len(host)]) + host
        sock.sendall(b"\x05\x01\x00" + address + struct.pack("!H", args.port))
        reply = exact(sock, 4)
        if reply[1] != 0:
            raise OSError("SOCKS connection rejected")
        size = {1: 4, 4: 16}.get(reply[3])
        exact(sock, size if size else exact(sock, 1)[0])
        exact(sock, 2)
        sock.settimeout(None)

        def upstream():
            try:
                while data := os.read(sys.stdin.fileno(), 65536):
                    sock.sendall(data)
            finally:
                try:
                    sock.shutdown(socket.SHUT_WR)
                except OSError:
                    pass

        threading.Thread(target=upstream, daemon=True).start()
        while data := sock.recv(65536):
            sys.stdout.buffer.write(data)
            sys.stdout.buffer.flush()


if __name__ == "__main__":
    main()
