# IPQuality for HQIP

This directory contains a standalone adaptation of
[xykt/IPQuality](https://github.com/xykt/IPQuality), licensed under
GNU Affero General Public License version 3. Copyright remains with its
original contributors. See `LICENSE`. There is no warranty.

Upstream revision: `b4f5c99e37e20411ef6311e90cf1ffb6dbb1c049`.
HQIP adaptation date: 2026-11-11. The original preferred source is in
`upstream/ip.sh`; `hqip.patch` records the modifications. `UPSTREAM.json`
records upstream provenance and the SHA-256 of every shipped asset.
The complete corresponding source, including this adaptation, is published
in the HQIP repository under `internal/provision/ipquality/`.

## Changes

- Freeze reference files with the reviewed script. No mutable reference,
  advertisement, counter, report upload or interactive shell is downloaded.
- Remove dependency installers, remote menu execution and terminal clearing.
- Require an explicitly selected public IPv4. HTTP requests always bind to
  that address, use HTTPS, validate TLS and have bounded time/body sizes.
  The observed public IP must match the selected address.
- Use pinned DNSBL domains as positional shell arguments, with validation,
  limited parallelism, explicit source binding and DNS deadlines. Failed DNS
  queries are reported as `Unknown`, never as `Clean`.
- Omit SMTP tests: their privileged source-port check is not reliable in an
  unprivileged service. `Mail.Port25` is null, and `Mail.Status` identifies this
  limitation. No email is sent. DNSBL checks remain available.
- Save the original colored terminal report to the private runner-supplied
  `HQIP_REPORT_ANSI` path. HQIP renders it as an escaped SVG image and stores it
  with the JSON report. No report is uploaded to a public sharing service.

The detection still contacts third-party IP databases, media services, AI
services and DNS providers. Those providers can observe the tested public IP.
Privacy mode prevents publishing a report; it does not anonymize probes.

## Invocation contract

Run as a dedicated unprivileged user with no secrets in its environment or
readable working directory, a process-group timeout, and resource limits:

```sh
bash /path/to/ip.sh -i PUBLIC_IPV4 -4 -n -p -f -j -l cn -o /private/report.json
```

The output file must not already exist. The parent process validates JSON,
enforces an output size limit, and checks `Head.IP` against the assigned IP.
The runner can set `HQIP_REPORT_ANSI` to a separate output path in the same
private directory. This optional file is limited to 64 KiB and removed with
the temporary directory after HQIP receives it.
Do not treat exit status zero or a missing result as a successful measurement.
Kill the complete service cgroup after cancellation/timeout, because the script
spawns background progress and DNS processes.

Dependencies are installed by the controlled provisioning workflow, never by
this script: Bash 4+, curl with HTTPS support, jq, bc, dig (dnsutils), awk, sed,
grep, head, cut, tr, sort, xargs, date, sleep, dirname, cat, gzip/gunzip, and core
shell utilities. SMTP tools such as nc/ss are not used by this adaptation.

The report describes third-party IP reputation and service reachability at the
measurement time. It does not measure throughput, route quality, packet loss,
or the user's latency. Different providers use different score scales; no
combined score is defined. JSON may contain literal `"null"` strings, null
values, and localized labels. A missing source must remain unknown.
