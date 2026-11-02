"""Small SSH transport helper. Passwords remain in process memory, not argv/logs."""
import base64, hashlib, logging, pathlib, socket, struct, time
import paramiko
logging.getLogger("paramiko").setLevel(logging.CRITICAL)

def credentials(path):
    parts = [x.strip() for x in pathlib.Path(path).read_text(encoding="utf-8-sig").splitlines() if x.strip()]
    if len(parts) != 2:
        raise ValueError("Credentials file must contain host and password on separate lines")
    return parts

def exact(sock, count):
    data = b""
    while len(data) < count:
        block = sock.recv(count - len(data))
        if not block:
            raise OSError("SOCKS connection closed")
        data += block
    return data

def _connect_once(host, password, fingerprint, username="root", port=22, socks=None):
    sock = None
    if socks:
        proxy_host, proxy_port = socks.rsplit(":", 1)
        sock = socket.create_connection((proxy_host, int(proxy_port)), timeout=20)
        sock.settimeout(30)
        sock.sendall(b"\x05\x01\x00")
        if exact(sock, 2) != b"\x05\x00":
            raise OSError("SOCKS authentication unavailable")
        try:
            address = b"\x01" + socket.inet_aton(host)
        except OSError:
            encoded = host.encode("idna")
            address = b"\x03" + bytes([len(encoded)]) + encoded
        sock.sendall(b"\x05\x01\x00" + address + struct.pack("!H", port))
        reply = exact(sock, 4)
        if reply[1] != 0:
            raise OSError("SOCKS connection rejected")
        size = {1: 4, 4: 16}.get(reply[3])
        exact(sock, size if size else exact(sock, 1)[0])
        exact(sock, 2)
    class PinnedHost(paramiko.MissingHostKeyPolicy):
        def missing_host_key(self, client, hostname, key):
            actual = "SHA256:" + base64.b64encode(hashlib.sha256(key.asbytes()).digest()).decode().rstrip("=")
            if actual != fingerprint:
                raise paramiko.SSHException("SSH host fingerprint mismatch")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(PinnedHost())
    try:
        client.connect(host, port=port, username=username, password=password, sock=sock, timeout=20, banner_timeout=20, auth_timeout=20, look_for_keys=False, allow_agent=False)
    except Exception:
        client.close()
        if sock:
            sock.close()
        raise
    client.get_transport().set_keepalive(15)
    return client

def connect(host, password, fingerprint, username="root", port=22, socks=None):
    for attempt in range(3):
        try:
            return _connect_once(host, password, fingerprint, username, port, socks)
        except (paramiko.AuthenticationException, paramiko.BadHostKeyException):
            raise
        except (paramiko.SSHException, OSError) as error:
            if "fingerprint mismatch" in str(error) or attempt == 2:
                raise
            time.sleep(attempt + 1)

def execute(client, command, timeout=60, stdin_data=None):
    stdin, stdout, stderr = client.exec_command(command, timeout=timeout)
    if stdin_data is not None:
        stdin.write(stdin_data)
        stdin.flush()
    stdin.channel.shutdown_write()
    data = stdout.read().decode(errors="replace")
    error = stderr.read().decode(errors="replace")
    status = stdout.channel.recv_exit_status()
    if status:
        raise RuntimeError(f"Remote command failed ({status}): {error[-2000:]} {data[-2000:]}")
    return data
