# 接口与实现约定

正式 Nginx 部署使用 HTTPS 管理 Origin，管理与公共监听为 `127.0.0.1:18090/18091`，路径按下述认证边界转发；本地开发保留 `8080/8081`。`--admin-origin` 允许 HTTPS 根地址或 `http://127.0.0.1:端口`；禁止凭据、路径和查询参数。

## 监听地址与认证边界

`hqip serve` 使用两个独立的回环监听器：`--listen 127.0.0.1:8080` 提供管理员 API 和网页，`--public-listen 127.0.0.1:8081` 仅提供客户端、订阅、Agent 和健康检查。`--admin-origin` 用于管理员 Origin/会话验证，`--public-url` 用于生成公网客户端和订阅地址。公网 URL 必须为 HTTPS，仅本机开发允许回环 HTTP。

正式 Nginx 将 `/api/v1/client/*`、`/api/v1/agents/*`、`/sub/*`、`/healthz` 转发公共监听，其他页面和管理员 API 转发管理监听。公共 Go handler 自身没有管理员路由或网页回退。管理员 API 使用浏览器会话与 CSRF，客户端和 Agent 使用各自 Bearer 令牌，订阅使用独立路径令牌，四者不可互换。历史 Caddy/本地隧道拓扑不用于当前部署。

All JSON uses camelCase. API errors: `{ "error": "human readable Chinese message" }`.
Authenticated JSON fetches use cookies and, for mutations, `X-CSRF-Token` returned by login/session.
The first administrator login has `mustChangePassword=true`; only session, password change and logout are available until the initial password is changed. `POST /password` requires at least 12 characters, invalidates all previous sessions and sets a new session cookie. Fetch `GET /session` afterwards to obtain the replacement CSRF token. Public subscriptions and Agent authentication do not use browser sessions.

## Browser routes

- `GET /api/v1/session` → `{authenticated:boolean, username?:string, csrfToken?:string, mustChangePassword?:boolean}`
- `POST /api/v1/login` `{username,password}` → session object
- `POST /api/v1/logout` → `{ok:true}`
- `POST /api/v1/password` `{currentPassword,newPassword}` → `{ok:true}`
- `GET /api/v1/overview` → `{nodes:Node[],devices:Device[],traffic:TrafficPoint[],usage:UsageTotals,alerts:{level:string,message:string,nodeId?:string,serverId?:string}[],serverTime:string}`。`usage` 从计量账本汇总，包含已删除节点/设备的历史贡献，不通过可见节点列表求和。
- `GET /api/v1/nodes` → `Node[]`; `POST` accepts `{name,serverId,addressId,dnsMode,zoneId?,domain?,port?,bandwidthMbps,monthlyQuotaBytes,password}`; returns Node (202). `dnsMode` 为 `manual/cloudflare`，自动域名可省略 `domain`；手工域名必须有效并指向所选 IP。SSH 与公网 IP 从服务器/IP 清单解析。新界面使用服务器模型；旧直接 SSH 字段仅为兼容路径。
- `GET /api/v1/nodes/{id}` → `{node:Node,events:TaskEvent[],traffic:TrafficPoint[],devices:Device[]}`. Each returned device's monthly/lifetime counters, `online`, `instances` and `pendingSync` are scoped to this node; identity and subscription URL remain device-wide.
- `POST /api/v1/nodes/{id}/retry` `{password,takeover?,preflightFingerprint?,certificateMode?,zoneId?}` → Node (202)。显式证书切换保存为待部署任务选项；安装成功才提交节点当前模式。失败及回滚不宣称新模式已应用。
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

Node status: `pending`, `installing`, `verifying`, `awaiting_external`, `ready`, `error`, `retiring`, `deleted`。独立 `lifecycle` 为 `active/retiring/deleted`，`revision` 是管理并发版本，区别于 Agent 的 `desiredRevision/appliedRevision`。LastSeen >30s is stale independently of provision status. Form defaults: sshPort 22, sshUser root, port 443; nullable quotas mean unlimited. `Metrics`, `Node`, `Device`, `TaskEvent`, `TrafficPoint` are defined in internal/model/model.go. TrafficPoint.at is Unix seconds. Amounts bytes; rates bytes/sec. Avoid logging subscription URLs, SSH or proxy passwords, authorization headers.

## 服务器、IP 清单与生命周期

以下接口均只在管理员监听注册，写入继续使用管理员会话、来源检查和 CSRF。部署和删除进度通过现有节点事件接口查看。

| 请求 | 输入与结果 |
| --- | --- |
| `GET /api/v1/servers` | `Server[]`，包含 IP 清单、独立额度、计量汇总、有效节点速率合计及一份主机指标 |
| `POST /api/v1/servers` | `{name,sshHost,sshPort?,sshUser?,password,bandwidthMbps?,monthlyQuotaBytes?}` → 已通过 SSH 检查的 Server（201），不安装节点；缺省额度为无限 |
| `GET /api/v1/servers/{id}` | `{server,addresses,nodes,nodeUsage}`，`nodeUsage` 包含已删除节点，`nodes` 仅保留未完成删除的节点 |
| `PATCH /api/v1/servers/{id}` | 完整服务器表单加 `expectedRevision`，重新验证 SSH 指纹及 machine-id |
| `PATCH /api/v1/servers/{id}/settings` | `{name,bandwidthMbps,monthlyQuotaBytes,expectedRevision}` → Server；两项额度必须明确提交，`null` 为无限；不调用 SSH，不改节点配置。过期版本返回 409 |
| `GET /api/v1/servers/{id}/traffic?period=24h\|7d` | `TrafficPoint[]`，24h 为分钟桶，7d 为小时桶；缺省 24h，其他值返回 400 |
| `DELETE /api/v1/servers/{id}` | `{expectedRevision}`，仅允许全部节点清理完成的服务器 |
| `GET /api/v1/servers/{id}/addresses` | `ServerAddress[]` |
| `POST /api/v1/servers/{id}/addresses` | `{publicIp}` → `{id,serverId,ip,createdAt,nodeIds}`（201） |
| `DELETE /api/v1/servers/{id}/addresses/{address}` | 仅删除未被活动/待清理节点引用的清单条目，不修改网卡或厂商配置 |
| `POST /api/v1/nodes/{id}/preflight` | `{password,certificateMode?,zoneId?}` → 系统、绑定 IP、端口、归属与接管对象，以及预检指纹 |
| `POST /api/v1/nodes/{id}/takeover` | `{password,preflightFingerprint,certificateMode?,zoneId?}` → 排队备份后接管（202） |
| `GET /api/v1/nodes/{id}/delete-preview` | `{node,devices:[{id,name,assignment,candidates}]}` |
| `DELETE /api/v1/nodes/{id}` | `{expectedRevision,replacements:{deviceId:{expectedRevision,defaultNodeId}}}` → Node（202） |
| `POST /api/v1/nodes/{id}/retire` | `{password}` → 排队 SSH 停止/清理，适用于待停止节点（202） |
| `DELETE /api/v1/devices/{id}` | 无请求体 → 已撤销会话及订阅的 Device（202） |

节点删除必须为预览中的每个受影响设备提交其分配版本；设备默认节点被删除且仍有授权节点时必须显式选择替代，不能静默取第一项。版本冲突返回 409，无任何部分分配更新。删除操作幂等。进行中的安装先取消，已经写入当前控制中心 owner 的部分安装继续承担远端清理义务。

`Server` 的监控字段：`nodeCount/ipCount`、`monthUpload/monthDownload/totalUpload/totalDownload`、`expectedNodes/reportingNodes`、`ratesStatus`（`none/complete/partial/unknown`）、`metricsFresh`、`metricsSourceNodeId`、`metricsReportedAt`。`metrics` 内速率为有效节点合计，CPU/内存/磁盘只取最新有效的一份；`lastSeen` 为最近报告，不等于资源采样时间。用量为字节，速率为字节/秒；页面转换 Mbps 并分别展示双向占用率。`UsageTotals` 包含上述四项流量字段。详细口径见 [服务器监控](monitoring.md)。

`Node` 增加 `serverId,addressId,machineId,authPort,statsPort,installationVersion,certificateMode,dnsMode,zoneId,managed,lifecycle,revision,deletedAt,capabilities,retirement,certificate`。`Server` 提供 SSH 信息和主机身份；`ServerAddress.ip` 为选定公网 IPv4，`nodeIds` 是尚未清理的引用。`Device.lifecycle` 为 `active/revoking/deleted`。常规列表保留未完成的停止或撤销项，完成后隐藏；按 ID 仍可读历史。计量凭据映射不随删除丢失。缺少设备记录的凭据不会被视为探测账户，只有准确的 `__probe_<nodeId>` 可用于探测。

## Cloudflare 与节点证书

飞书通知配置使用独立凭据和管理员接口，见本文末尾“飞书机器人通知”，不复用 DNS Token。

| 请求 | 输入与结果 |
| --- | --- |
| `GET /api/v1/settings/cloudflare` | 是否配置、默认 Zone、邮箱、版本；不返回 Token |
| `PUT /api/v1/settings/cloudflare` | `{token,defaultZoneId,email,expectedVersion}`；空 Token 保留现值；版本冲突 409 |
| `POST /api/v1/settings/cloudflare/verify` | `{token}` → `{ok,zones,dnsWriteVerified,message}`；空 Token 使用已保存值 |
| `GET /api/v1/settings/cloudflare/zones` | `{zones}`，当前 Token 可访问的 Zone |
| `GET /api/v1/settings/cloudflare/certificates` | `{certificates}`，签发/应用版本、有效期、重试时间及告警 |
| `GET /api/v1/settings/cloudflare/dns` | `{records}`，HQIP 自建 DNS 资源与清理状态 |

验证接口检查 Token 与 Zone 可读性，`dnsWriteVerified:false` 明确表示尚未实际写入 DNS；首次节点记录创建验证写权限。Token 需要限定 Zone 的 `Zone:Read` 与 `DNS:Edit`，加密保存，不发给 Agent。A 记录固定 DNS only，每节点独立域名与 IP。记录清理核对 Record ID、内容和归属，手工 A 记录从不因节点删除而清理。

DNS-01 签发材料以加密版本保存，证书发布增加 Agent 期望版本。`certificateMode=http` 使用 HY2 原生 HTTP 续期；`cloudflare` 使用控制面 DNS-01 与 Agent 文件证书。已有手工域名可以只切换证书模式，仍保持 `dnsMode=manual`。部署成功前目标证书模式仅保存在任务选项，不能将尚未应用的证书当作节点当前证书。

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

`ClientProfile` 包含 `deviceId,name,enabled,revision,defaultNodeId,nodes,rules,subscriptionUrl`。`nodes` 只包含当前设备的显式分配，每项提供 `id,name,server,publicIp,port,password,sni,ready`；`server=publicIp` 直接连接指定 IP，`sni=domain` 严格校验证书，Clash 同样采用此约定。未完成授权同步时 `ready=false`。`rules` 包含 `version,hash,url`。配置含连接凭据，不得写入普通日志。

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

`AgentDesired` 另包含 `retirement:{id,finalize}` 和 `certificate:{version,domain,certificatePem,privateKeyPem,notBefore,notAfter}`，只对该节点 Bearer 返回。`AgentReport` 增加 `capabilities:string[]`、`retirement:{id,stopped,final}`、`certificate:{version,notAfter,error}`。HTTP 模式用证书版本 0 报告有效期。Agent 先持久保存停止任务并停止内核，报告 `stopped=true` 后才获 `finalize=true`；清理本实例并报告 `final=true` 后后台完成删除。退休任务在相同配置 revision 下也须处理，最终回执可幂等重试。旧安装不具备相应能力时展示需升级或 SSH 清理，不能假设停服已生效。

## Backup restoration and replay

多 IP 版本使用数据库 schema 3。迁移为旧节点建立服务器/IP 关联，不更换现有节点 ID、订阅、代理凭据及计量键。固定控制中心身份保存在备份内，用于校验安装归属。回退旧二进制必须配套恢复升级前数据库及 master.key；旧程序不能读取 schema 3。已经接管或换凭据的节点还需与对应节点备份保持一致，详见 [多 IP 运维](multi-ip.md)。

`hqip restore --input BACKUP.tar.gz --data-dir EMPTY_DIR` validates database integrity and its paired encryption key before installing restored files. It assigns each node `desiredRevision = max(previousDesiredRevision + 1, currentUnixMilliseconds)`, sets `appliedRevision=0`, clears last heartbeat/online state and invalidates all saved browser sessions. Millisecond epochs fit exactly in JavaScript JSON numbers. Restoration reapplies the backed-up device enablement and credentials, including their older revocation/rotation state; it does not preserve changes made after that snapshot.

An Agent receiving a revision jump greater than one marks retained buckets for replay; it also replays acknowledged history hourly. Replay includes up to 13 months of retained daily buckets and 7 days of retained minute buckets, using the same MAX/difference accounting. This fills snapshot gaps when the Agent's current ledger survives. It cannot reconstruct data absent from both the backup and node ledger, and older missing hourly detail cannot be rebuilt from daily totals. Keep the controller clock correct and wait for configuration acknowledgments and accounting replay before treating restored monitoring as current.

## Provision boundary

Package internal/provision exports `New(Options) *Runner`, `CheckServer`, `Inspect`, `Install`, `Verify` and `Retire`. Options 提供 `ControlURL,ArtifactDir,ControllerID`；Input 提供 Node、SSH 密码、Agent/探测凭据、显式 Takeover、PreflightFingerprint 及 Certificate。Result 中 `Managed` 表示已经写入当前安装归属，即使后续失败也不能当作从未安装；另返回 machine-id、备份位置和安装版本。Control creates an internal probe device credential (not shown among registered devices), sends it in AgentDesired, and provides its per-node password as ProbePassword.

Installer 为每个节点建立独立 `/etc/hqip-nodes/<id>`、`/var/lib/hqip-nodes/<id>`、`hqip-agent-<id>.service`、`hqip-hysteria-<id>.service`。HY2 监听选定 IPv4，direct 出站 `bindIPv4` 固定同一地址，失败不能回落默认出口。本机鉴权/统计端口由数据库事务分配，二进制按内容摘要不可变存放。同服务器任务互斥；部署前确认 IP 已绑定，拒绝共享 H-ui、容器或归属不明服务的自动接管。真实代理探测仍验证出口 IP，同控制机部署还要求外部验证。固定 amd64/arm64 产物及校验和继续由 release manifest 管理。

## 飞书机器人通知

以下接口只在管理员监听注册，要求管理员会话；写入执行来源检查、CSRF、审计。用户、客户端和 Agent 凭据无效。所有配置响应均不返回原始 Webhook 或签名密钥。

| 请求 | 输入与结果 |
| --- | --- |
| `GET /api/v1/settings/feishu` | 返回 `FeishuSettings`，包括脱敏配置和队列状态 |
| `PUT /api/v1/settings/feishu` | `{enabled,webhook?,secret?,clearCredentials?,categories,expectedRevision}` → 更新后的 `FeishuSettings`；版本冲突 409 |
| `POST /api/v1/settings/feishu/test` | 使用已保存配置排队固定测试消息 → `NotificationRecord`；允许总开关关闭时测试，每 30 秒最多一次 |
| `GET /api/v1/notifications?page=1&pageSize=20` | `{items:NotificationRecord[],total,page,pageSize}` |
| `POST /api/v1/notifications/{id}/retry` | 手工重试仍有效的失败通知；失效、已取消或不允许投递的记录不能重新发送 |

`FeishuSettings` 字段为 `enabled,webhookConfigured,webhookMasked,secretConfigured,categories,revision,lastSuccessAt,lastError,queueDepth`。`categories` 为 `{system,capacity,certificate,package,recovery}` 五个布尔值；新安装默认全选，总开关默认关闭。时间使用 RFC3339，尚无记录的时间可为 `null`。

保存时省略 `webhook/secret` 或提交空值保留已存凭据；非空值替换对应凭据。启用必须同时存在 Webhook 和签名密钥。`clearCredentials:true` 必须同时 `enabled:false` 且不得提交非空新凭据；历史投递记录保留。停用或更换 Webhook 取消旧待发任务。客户端必须保存自己的读取版本，遇到 409 重新读取并由管理员核对，不能静默覆盖。

`NotificationRecord` 包括 `id,category,level,title,message,detailPath,status,attempts,createdAt,nextAttempt,lastError,canRetry`。`status` 为 `pending/sending/retry/sent/failed/cancelled`，`sent` 仅表示飞书业务响应 `code == 0`。测试也是异步投递；返回记录并不表示成功，调用方通过列表轮询查看结果。`detailPath` 为管理员详情路径，提示、标题和错误均需作为纯文本显示。自动通知不发送原始日志或任何连接凭据。

投递每秒最多一个批次；固定事件编号可用于识别超时导致的重复消息。单次超时 10 秒，初次尝试后最多 5 次自动重试，基础间隔为 30 秒、2 分钟、10 分钟、30 分钟、2 小时并附加最多 10% 抖动。队列上限 1,000 条，记录保留 90 天。签名与配置错误不自动重试。详情见 [飞书通知配置与运维](notifications.md)。
