# 接口与实现约定

## 监听地址与认证边界

`hqip serve` 使用两个独立的回环监听器：`--listen 127.0.0.1:8080` 提供管理员 API 和网页，`--public-listen 127.0.0.1:8081` 仅提供客户端、订阅、Agent 和健康检查。`--admin-origin` 用于管理员 Origin/会话验证，`--public-url` 用于生成公网客户端和订阅地址。公网 URL 必须为 HTTPS，仅本机开发允许回环 HTTP。

VPS Caddy 经受限 SSH 反向隧道将 `127.0.0.1:18088` 转发至本地 `8081`。公网只允许 `/api/v1/client/*`、`/api/v1/agents/*`、`/sub/*`、`/healthz`，管理员接口和网页返回 404；公共 Go handler 自身也没有管理员路由或网页回退。管理员 API 使用浏览器会话与 CSRF，客户端和 Agent 使用各自 Bearer 令牌，订阅使用独立路径令牌，四者不可互换。

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
- `GET /api/v1/devices/{id}` → `{device:Device,traffic:TrafficPoint[],nodes:Node[],assignment:Assignment,clientAccount:ClientAccount,sessions:ClientSession[],removals:Removal[]}`. `device` and `traffic` aggregate this device across nodes; each entry in `nodes` has `monthUpload`, `monthDownload`, `totalUpload` and `totalDownload` scoped to this device on that node. Other Node fields still describe the node itself. Historical usage remains visible after removal from the current assignment.
- `POST /api/v1/devices/{id}/client-account` `{username,password?}` → `{username,password}`. 初始化或重置设备登录账号；空密码生成随机值，仅本次响应返回明文密码。用户名 3–64 位，匹配 `[A-Za-z0-9][A-Za-z0-9_.@-]{2,63}`，密码至少 12 位。重置会撤销该设备的全部客户端会话。
- `PATCH /api/v1/devices/{id}/assignment` `{nodeIds:string[],defaultNodeId:string,expectedRevision:number}` → `Assignment`。默认节点必须属于分配集合；集合为空时默认节点必须为空。按设备配置版本乐观更新，版本过期返回 409，调用方须重新读取再提交，不能静默覆盖并发修改。
- `GET /api/v1/rules` → `{version:number,hash:string,bundle:Bundle}`；`PUT` 接收 `{bundle:Bundle}`，校验成功后返回更新后的版本；`GET /api/v1/rules/export` 下载当前规范化 JSON。规则写入的请求上限为 9 MiB。

`GET /sub/{token}` 位于公共监听器，返回完整 Clash YAML（路径令牌认证、不缓存），只包含该设备已分配、已就绪且已完成节点授权同步的节点，默认节点排序优先。设备停用或令牌轮换后旧链接返回 404；没有可发布节点时返回 503 和 `Retry-After: 15`。新建设备没有自动分配；从旧版本迁移时保留原有凭据对应的分配。旧版只使用订阅的设备可继续使用，无需初始化客户端账号。

Overview, node detail and device detail accept the optional `period` query parameter for their `traffic` array. Counters are bytes within the bucket, not bytes/second; absent buckets are omitted.

| `period` | Window | Bucket size |
| --- | --- | --- |
| absent, `24h`, or unrecognized value | Last 24 hours | Minute |
| `7d` | Last 7 days | Hour |
| `90d` | Last 90 days | Hour |
| `13m` | Last 13 calendar months | Shanghai calendar day |

Node status: `pending`, `installing`, `verifying`, `awaiting_external`, `ready`, `error`. LastSeen >30s is stale independently of provision status. Form defaults: sshPort 22, sshUser root, port 443; nullable quotas mean unlimited. `Metrics`, `Node`, `Device`, `TaskEvent`, `TrafficPoint` are defined in internal/model/model.go. TrafficPoint.at is Unix seconds. Amounts bytes; rates bytes/sec. Avoid logging subscription URLs, SSH or proxy passwords, authorization headers.

Global device lists and overview deduplicate registered device identities across nodes and sum their connection instances. Internal `__probe_<nodeId>` credentials never count as registered devices or billed proxy usage. Missing/stale heartbeats or `coreHealthy=false` mean online information is unknown; numeric counts omit such nodes and must not be presented as proof of zero active connections. `pendingSync` remains true until relevant nodes report the desired revision, including after revocation or credential rotation.

## 桌面客户端

以下路径均位于公共监听器的 `/api/v1/client` 下。登录与刷新不使用管理员 Cookie；其他请求使用 `Authorization: Bearer <accessToken>`，所有响应禁止缓存。

| 请求 | 输入与结果 |
| --- | --- |
| `POST /login` | `{username,password,installationId,clientName}` → `ClientTokens` |
| `POST /refresh` | `{refreshToken}` → 新的 `ClientTokens`，同时轮换 access/refresh token |
| `POST /logout` | 撤销当前 Bearer 对应的客户端会话，返回 `{ok:true}` |
| `GET /profile` | 返回 `ClientProfile`；支持 `ETag` / `If-None-Match`，未变化返回 304 |
| `GET /rules/{hash}` | 返回经过认证的规则包原始 JSON 字节；不存在的 hash 返回 404 |
| `POST /status` | 接收 `ClientStatus`，返回 `{ok:true}`；过期或不一致的配置状态可返回 409 |

访问令牌有效期 15 分钟，刷新令牌有效期 30 天；每设备最多保留 32 个登录会话。刷新令牌单次使用，检测到旧令牌重放时撤销对应会话。登录有速率和并发限制，账号密码以密码哈希保存；令牌只保存哈希。字段定义位于 `internal/model/client.go`。

`ClientProfile` 包含 `deviceId,name,enabled,revision,defaultNodeId,nodes,rules,subscriptionUrl`。`nodes` 只包含当前设备的显式分配，每项提供 `id,name,server,publicIp,port,password,sni,ready`；未完成授权同步时 `ready=false`。`rules` 包含 `version,hash,url`。配置含连接凭据，不得写入普通日志。

`ClientStatus` 包含 `desiredNodeId,actualNodeId,runState,mode,tunEnabled,connectionIntent,appliedConfigRevision,processedConfigRevision,coreVersion,lastError`。`ClientSession` 另包含服务器维护的 `desiredConfigRevision,lastSeen,revocationPending` 和会话身份信息。`runState` 为 `disconnected/connecting/connected/failed/cached`，`mode` 协议接受 `rule/global/direct`，当前桌面界面提供规则和全局两种模式。

`processedConfigRevision` 表示已完成处理，包括失败或保持断开；`appliedConfigRevision` 只表示已成功运行。失败状态不得因处理版本追上期望版本而被展示为连接成功。只有代理探测成功且内核存活时报告实际节点；失败清空实际节点。用户手动断开会设置 `connectionIntent=false`，收到新配置只更新缓存，不自动连接。客户端重新启动也默认为断开。

## 分配移除与确认

移除分配立即从新客户端配置和订阅中排除节点，同时创建 `Removal`。其字段为 `id,nodeId,deviceId,revision,status,deadline,createdAt,immediate`；对管理员可见状态为 `pending/scheduled/revoked`，重新分配产生的旧移除任务可标为 `cancelled` 并从当前列表排除。

Agent 收到任务后持久保存截止时间，控制面离线也继续执行；期限从节点实际接收开始，不从管理员提交时间开始。客户端已处理新版本且结束切换时可提前发起撤销。Agent 先拒绝旧凭据的新鉴权，再清除旧会话，确认完成后报告 `revoked`。界面不能由本机时钟或截止时间自行推定撤销成功。设备停用和凭据轮换跳过宽限期，但仍等待节点确认。

## 规则包

`internal/rules` 定义 schema version 1：`{schemaVersion,source,rules,geoipCN}`。规则保持原始先后顺序，输出动作仅为 `direct/proxy/reject`；支持已验证的域名、域名后缀、域名关键字、IPv4/IPv6 CIDR、`GEOIP,CN` 及末尾 `MATCH`。未知类型、未知选项和非法 CIDR 拒绝导入。保留每条规则的 `noResolve` 语义；必要的显式 DNS resolve 插入在首个需要解析的 IP/GEOIP 规则之前，后续 no-resolve 规则可能匹配此前已解析的地址。

`geoipCN` 记录仓库、固定 40 位提交、MIT 许可、IPv4/IPv6 原始文件摘要及规范化 CIDR。控制面使用 Go 规范化 JSON（固定结构字段顺序、无尾换行、不转义 HTML）计算 SHA-256 并保存不可变版本。`GET /rules/{hash}` 返回同一字节序列，客户端校验下载字节摘要后再编译本地 SRS。hash 不等于用户原 YAML 的摘要，也不能通过解析后自行重新序列化校验。

`scripts/import_clash_rules.py` 只读取路由规则和已核对服务组的默认策略，不导入原代理列表、原节点名称和订阅 URL。导入快照及编译结果保存于忽略目录 `.local/rules`。未导入自定义包时，默认规则为本地网段直连、其余代理，不包含 CN 数据集。

## Agent

Bearer token is per node, not an administrator session. `GET /api/v1/agents/config` returns AgentDesired. `POST /api/v1/agents/report` accepts AgentReport and returns AgentDesired. No nodeId is trusted from the body: resolve from token; unknown JSON fields are rejected. Bodies are capped at 4 MiB and each minute/day array at 4000 buckets; the Agent batches at most 4000 buckets total, prioritizing days.

The Agent samples every second and sends a heartbeat/report every 5 seconds. It keeps a durable sampling baseline keyed by systemd InvocationID across core restarts. Minute/day cumulative buckets use server-side MAX upserts; only positive daily differences increment separate lifetime totals. Minute detail lasts 7 days, derived hourly detail 90 days and daily detail 13 months. Lifetime totals survive retention cleanup. Day/month boundaries use Asia/Shanghai; unknown device IDs and internal probes do not contribute to accounting. Quotas never enforce traffic blocking or throttling.

Agent local HTTP POST `/auth` receives `{addr,auth,tx}` and returns `{ok,id}`; it binds only to loopback and uses the configured authentication secret. Hysteria stats APIs also require their Authorization secret. The Agent caches credential hashes, applies denied auth before repeated kick, and advertises a revision only after denied sessions are cleared. `coreHealthy=false` means stats/online data is unknown. The controller records the reported applied revision directly, so a reverted Agent cache makes synchronization pending again.

## Backup restoration and replay

`hqip restore --input BACKUP.tar.gz --data-dir EMPTY_DIR` validates database integrity and its paired encryption key before installing restored files. It assigns each node `desiredRevision = max(previousDesiredRevision + 1, currentUnixMilliseconds)`, sets `appliedRevision=0`, clears last heartbeat/online state and invalidates all saved browser sessions. Millisecond epochs fit exactly in JavaScript JSON numbers. Restoration reapplies the backed-up device enablement and credentials, including their older revocation/rotation state; it does not preserve changes made after that snapshot.

An Agent receiving a revision jump greater than one marks retained buckets for replay; it also replays acknowledged history hourly. Replay includes up to 13 months of retained daily buckets and 7 days of retained minute buckets, using the same MAX/difference accounting. This fills snapshot gaps when the Agent's current ledger survives. It cannot reconstruct data absent from both the backup and node ledger, and older missing hourly detail cannot be rebuilt from daily totals. Keep the controller clock correct and wait for configuration acknowledgments and accounting replay before treating restored monitoring as current.

## Provision boundary

Package internal/provision exports `New(Options) *Runner`, `Runner.Install(context.Context, Input, LogFunc, PinFunc) (Result,error)`, `Runner.Verify(context.Context, Input, LogFunc) (Result,error)`. Options fields ControlURL, ArtifactDir. Input fields Node model.Node, Password string, AgentToken string, ProbePassword string. LogFunc func(stage,message string). PinFunc func(fingerprint string) error. Control creates an internal probe device credential (not shown among registered devices), sends it in AgentDesired, and provides its per-node password as ProbePassword. Installer detects controller/node placement separately from ACME-via-Caddy, writes agent config, installs units and Hysteria, and probes with a standalone Hysteria client using a random loopback SOCKS port. Same-host placement requires external verification even after a successful local probe. Moving the controller to Windows preserves Caddy ACME forwarding on the old VPS. ArtifactDir contains agent-linux-amd64/arm64 and hysteria-linux-amd64/arm64 with checksums pinned by the release manifest; the release packaging includes both architectures.
