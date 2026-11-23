# HQIP Hysteria 2 extension

Based on the unmodified protocol and configuration of upstream `app/v2.13.0`.
`UPSTREAM.json` records the source archive checksum and every original file's
SHA-256. The upstream MIT license is retained in `LICENSE.md`. Local adapters
are `core/server/hqip_*`, integration tests, and the small hooks in the server
configuration and HTTP/3 connection handler. No client protocol was changed.

The protocol-neutral guard and fixed-point billing implementation live in
`../nodepolicy`, an independent Go module shared with the direct VLESS outbound.
Server adapters retain their public configuration/API. VLESS neither creates a
HY2 client nor requires this process to run. Run `go test ../nodepolicy/...` from
this directory for the extracted guard regressions; the build source digest
includes the shared module.

Build with `python scripts/build_hy2.py --test`. Release binaries and the exact
source digest are written to `dist/hqip-hysteria-manifest.json`.

Add to the existing server YAML:

```yaml
hqipAdmission:
  url: http://127.0.0.1:20000
  secret: <same private local Agent statistics secret>
```

With this option configured, every real QUIC connection receives a fresh
cryptographically random 256-bit identifier before authentication succeeds.
The local Agent authorizes legacy/probe credentials or requests central
admission. A denial or network error never falls back to another authenticator.
The original HTTP authentication option remains required for configuration
compatibility, but is not a fallback for the new admission path.

Portal leases renew every five seconds and expire after at most 120 seconds,
including renewal network time. Expiry uses Go monotonic deadlines. The exact
QUIC connection is closed on denial, quota exhaustion or expiry, including idle
connections. No `/kick` approximation is used for portal leases.

Upload and download each receive the granted bytes-per-second rate, with
independent pacing and a shared cumulative reserved budget. Target socket wrappers gate TCP and UDP payload
before forwarding; UDP payload is charged once regardless of HY2 fragmentation.
Bytes reserved for a write that subsequently fails are conservatively charged.
Core restart never reuses connection IDs or leases. The Agent durably stores
cumulative usage before forwarding renewals. Final reports received during an
outage survive Agent restart and retry until confirmed; only a matching queued
version is acknowledged, so a delayed response cannot discard newer counters.
Nonfinal reports are never background-renewed for a dead core. A core crash with
no final report keeps its unused central reservation until explicitly reconciled.
The local queue holds at most 16,384 records. Confirmed final tombstones are kept
for up to 24 hours and may be pruned early at capacity; unconfirmed records are
never pruned automatically. A full queue pauses new admission and raises an Agent
warning. Failed finals retry with backoff up to five minutes.

The local authenticated stats endpoint `/hqip/capabilities` exposes admission
version 1 only when configured. The Agent reports `admission-v1` after checking
this endpoint; old upstream binaries cannot falsely advertise enforcement.

The central allocator supplies a per-direction share for each active connection.
Rate changes carry a monotonic `rateVersion`; `ratePending` pauses payload while
continuing renewals and must never be interpreted as unlimited bandwidth.
The core installs a rate before sending `appliedRateVersion` in the next renewal.
Waiting transfers cancel their old schedule whenever that version changes.
When an entitlement changes, renewal updates its budget/rate or ends its connection.
Legacy connections are also rechecked with the local Agent every five seconds;
activation disables ordinary legacy credentials and closes those connections.
Probe credentials remain available. These local legacy checks cannot request a
central lease and never return central quota reservations.
Existing TLS files and hot reload, native ACME, fixed IPv4 source binding, ACLs,
obfuscation and standard Hysteria 2 clients continue through upstream code.
