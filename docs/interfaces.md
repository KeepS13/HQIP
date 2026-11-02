# Implementation contract

All JSON uses camelCase. API errors: `{ "error": "human readable Chinese message" }`.
Authenticated JSON fetches use cookies and, for mutations, `X-CSRF-Token` returned by login/session.
The first administrator login has `mustChangePassword=true`; only session, password change and logout are available until the initial password is changed. `POST /password` requires at least 12 characters, invalidates all previous sessions and sets a new session cookie. Fetch `GET /session` afterwards to obtain the replacement CSRF token. Public subscriptions and Agent authentication do not use browser sessions.

## Browser routes

- `GET /api/v1/session` → `{authenticated:boolean, username?:string, csrfToken?:string, mustChangePassword?:boolean}`
- `POST /api/v1/login` `{username,password}` → session object
- `POST /api/v1/logout` → `{ok:true}`
- `POST /api/v1/password` `{currentPassword,newPassword}` → `{ok:true}`
- `GET /api/v1/overview` → `{nodes:Node[],devices:Device[],traffic:TrafficPoint[],alerts:{level:string,message:string,nodeId?:string}[],serverTime:string}`
- `GET /api/v1/nodes` → `Node[]`; `POST` accepts `{name,sshHost,sshPort?,sshUser?,publicIp,domain,port?,bandwidthMbps,monthlyQuotaBytes,password}`; returns Node (202).
- `GET /api/v1/nodes/{id}` → `{node:Node,events:TaskEvent[],traffic:TrafficPoint[],devices:Device[]}`. Each returned device's monthly/lifetime counters, `online`, `instances` and `pendingSync` are scoped to this node; identity and subscription URL remain device-wide.
- `POST /api/v1/nodes/{id}/retry` `{password}` → Node (202)
- `POST /api/v1/nodes/{id}/verify` → Node (202), schedules a real control-plane proxy probe; shared-host nodes remain `awaiting_external`.
- `POST /api/v1/nodes/{id}/verification-config` `{deviceId:string}` → `{config:string}`. Administrator-only Clash YAML containing the specified candidate and its enabled device credential. The node must already be `awaiting_external` or `ready`; this endpoint does not publish or mark the candidate ready. It shares the ordinary subscription's YAML generator.
- `POST /api/v1/nodes/{id}/external-verification` `{exitIp:string}` → Node; administrator-only endpoint for the documented external verifier after actual Mihomo validation. Accepts `awaiting_external`/`ready`, requires the observed exit IP to match the node's `publicIp`, then marks it `ready` with a conditional state update.
- `PATCH /api/v1/nodes/{id}` `{name?,bandwidthMbps?,monthlyQuotaBytes?}` → Node; explicit `null` resets either quota to unlimited. Only these metadata fields are updated, preserving concurrent deployment state and SSH fingerprint changes.
- `GET /api/v1/nodes/{id}/events` → SSE `event: progress`, `data: TaskEvent`; include 15s heartbeat and replay events after Last-Event-ID.
- `GET /api/v1/devices` → `Device[]`; `POST` `{name}` → Device including subscriptionUrl (201).
- `PATCH /api/v1/devices/{id}` `{name?,enabled?}` → Device
- `POST /api/v1/devices/{id}/rotate` → Device; rotates subscription token AND proxy credentials, applies revocation to nodes.
- `GET /api/v1/devices/{id}` → `{device:Device,traffic:TrafficPoint[],nodes:Node[]}`. `device` and `traffic` aggregate this device across nodes; each entry in `nodes` has `monthUpload`, `monthDownload`, `totalUpload` and `totalDownload` scoped to this device on that node. Other Node fields still describe the node itself.
- `GET /sub/{token}` → Clash YAML (no cookies; token auth; no cache), containing only `ready` nodes. A disabled device or rotated token returns 404; no ready nodes returns 503 with `Retry-After: 15`.

Overview, node detail and device detail accept the optional `period` query parameter for their `traffic` array. Counters are bytes within the bucket, not bytes/second; absent buckets are omitted.

| `period` | Window | Bucket size |
| --- | --- | --- |
| absent, `24h`, or unrecognized value | Last 24 hours | Minute |
| `7d` | Last 7 days | Hour |
| `90d` | Last 90 days | Hour |
| `13m` | Last 13 calendar months | Shanghai calendar day |

Node status: `pending`, `installing`, `verifying`, `awaiting_external`, `ready`, `error`. LastSeen >30s is stale independently of provision status. Form defaults: sshPort 22, sshUser root, port 443; nullable quotas mean unlimited. `Metrics`, `Node`, `Device`, `TaskEvent`, `TrafficPoint` are defined in internal/model/model.go. TrafficPoint.at is Unix seconds. Amounts bytes; rates bytes/sec. Avoid logging subscription URLs, SSH or proxy passwords, authorization headers.

Global device lists and overview deduplicate registered device identities across nodes and sum their connection instances. Internal `__probe_<nodeId>` credentials never count as registered devices or billed proxy usage. Missing/stale heartbeats or `coreHealthy=false` mean online information is unknown; numeric counts omit such nodes and must not be presented as proof of zero active connections. `pendingSync` remains true until relevant nodes report the desired revision, including after revocation or credential rotation.

## Agent

Bearer token is per node, not an administrator session. `GET /api/v1/agents/config` returns AgentDesired. `POST /api/v1/agents/report` accepts AgentReport and returns AgentDesired. No nodeId is trusted from the body: resolve from token; unknown JSON fields are rejected. Bodies are capped at 4 MiB and each minute/day array at 4000 buckets; the Agent batches at most 4000 buckets total, prioritizing days.

The Agent samples every second and sends a heartbeat/report every 5 seconds. It keeps a durable sampling baseline keyed by systemd InvocationID across core restarts. Minute/day cumulative buckets use server-side MAX upserts; only positive daily differences increment separate lifetime totals. Minute detail lasts 7 days, derived hourly detail 90 days and daily detail 13 months. Lifetime totals survive retention cleanup. Day/month boundaries use Asia/Shanghai; unknown device IDs and internal probes do not contribute to accounting. Quotas never enforce traffic blocking or throttling.

Agent local HTTP POST `/auth` receives `{addr,auth,tx}` and returns `{ok,id}`; it binds only to loopback and uses the configured authentication secret. Hysteria stats APIs also require their Authorization secret. The Agent caches credential hashes, applies denied auth before repeated kick, and advertises a revision only after denied sessions are cleared. `coreHealthy=false` means stats/online data is unknown. The controller records the reported applied revision directly, so a reverted Agent cache makes synchronization pending again.

## Backup restoration and replay

`hqip restore --input BACKUP.tar.gz --data-dir EMPTY_DIR` validates database integrity and its paired encryption key before installing restored files. It assigns each node `desiredRevision = max(previousDesiredRevision + 1, currentUnixMilliseconds)`, sets `appliedRevision=0`, clears last heartbeat/online state and invalidates all saved browser sessions. Millisecond epochs fit exactly in JavaScript JSON numbers. Restoration reapplies the backed-up device enablement and credentials, including their older revocation/rotation state; it does not preserve changes made after that snapshot.

An Agent receiving a revision jump greater than one marks retained buckets for replay; it also replays acknowledged history hourly. Replay includes up to 13 months of retained daily buckets and 7 days of retained minute buckets, using the same MAX/difference accounting. This fills snapshot gaps when the Agent's current ledger survives. It cannot reconstruct data absent from both the backup and node ledger, and older missing hourly detail cannot be rebuilt from daily totals. Keep the controller clock correct and wait for configuration acknowledgments and accounting replay before treating restored monitoring as current.

## Provision boundary (owned by root)

Package internal/provision exports `New(Options) *Runner`, `Runner.Install(context.Context, Input, LogFunc, PinFunc) (Result,error)`, `Runner.Verify(context.Context, Input, LogFunc) (Result,error)`. Options fields ControlURL, ArtifactDir. Input fields Node model.Node, Password string, AgentToken string, ProbePassword string. Result fields SharedHost bool, Verified bool, ExitIP string, Message string. LogFunc func(stage,message string). PinFunc func(fingerprint string) error. Control creates an internal probe device credential (not shown among registered devices), sends it in AgentDesired, and provides its per-node password as ProbePassword. Installer obtains node machine-id vs local to identify shared host, writes agent config, installs units and Hysteria, probes with standalone Hysteria client using random loopback SOCKS port. For same host, successful local probe still requires external verification. ArtifactDir contains agent-linux-amd64/arm64 and hysteria-linux-amd64/arm64 with checksums pinned by the release manifest; the release packaging includes both architectures. Control/API agent owns cmd/hqip and internal/control, internal/store, internal/secure. Node agent owns cmd/hqip-agent and internal/agent. Frontend agent owns web/. Root owns internal/model, internal/provision, deploy/, scripts/, docs/, CI, module versions, final integration.
