package provision

// Called under the existing host operation lock. The Agent's actual bind is
// still authoritative; a concurrent external process causes install rollback.
const realityPortScript = `import base64,ipaddress,json,socket,sys
ip=str(ipaddress.IPv4Address(json.loads(base64.b64decode(sys.argv[1]))['ip']))
for port in (443,8443):
    with socket.socket(socket.AF_INET,socket.SOCK_STREAM) as probe:
        try: probe.bind((ip,port))
        except OSError: continue
    print(port);break
else: raise SystemExit('no-free-reality-port')
`
