# 用户套餐、中央准入与运维

门户在公网根路径，管理员页面位于 `/admin`。用户 API 和 Agent API 只注册于 PublicHandler；旧管理 API 与新管理 API 仍只注册于 AdminHandler。部署反向代理须按路径把管理员请求转到管理员监听器，不能把 PublicHandler 的未知路径改成管理 API 回退。

## 升级激活

数据库 v4 迁移只建表，默认 `portal_enabled=false`，不会在启动时撤销生产设备。

1. 部署支持中央准入的新 Hysteria 核心和 Agent。每个活动节点须通过真实连接验收，并上报 `admission-v1`。
2. 管理员登录后配置 Turnstile，允许域名必须精确匹配、不能使用通配符。确认用户注册、PKCE、订阅和节点准入均通过隔离验收。
3. 显式调用 `POST /api/v1/settings/portal/activate`，JSON 为 `{"confirm":true}`，附管理会话及 `X-CSRF-Token`。任何活动节点未上报能力、30 秒内没有健康心跳或未确认当前配置都会返回 409。
4. 激活事务禁用旧 devices、撤销旧客户端会话、提高节点配置版本。`AgentDesired.portalEnabled=true`，旧非探针设备无论管理员随后是否误启用都不能恢复节点鉴权。旧账本不删除。新用户需管理员另行分配套餐，用户注册本身不提供代理权益。

激活前保留旧客户端接口兼容；激活后所有公网 `/api/v1/client/*` 请求返回 HTTP 426，提示升级 HQIP 3.0 并使用邮箱账户在浏览器登录。新客户端使用 `/api/v2/client/*`。

### 脚本回退边界

`upgrade_portal.py` 与 `upgrade_control.py` 的自动失败回退、显式 rollback 只适用于激活之前。回退先停止本控制服务，再以只读方式检查当前 SQLite（包括已提交 WAL）的 `portal_enabled`、`portal_recovery_hold`，并检查可选的 `portal-stage.json` 激活标记。任一已激活/恢复等待标记、无法读取的数据库或不确定状态都会拒绝恢复旧快照，保留当前数据库和新发布目录，控制服务保持停止。即使目标备份也来自 v4，脚本也不允许激活后倒退用量、会话和权限。

节点 rollback 仅停止指定节点的 Agent/core，保留当前 ledger；等待 31 秒，使该节点的健康心跳超过激活接口的 30 秒门限，再读取 `agent.json` 指定的 SQLite `meta.credentials.portalEnabled`，并向当前控制端确认配置。缓存或中央任一处已激活、控制端无法访问、账本缺失/损坏均阻止旧 core 启动，两服务保持停止。脚本不会从节点快照恢复旧账本。升级、回退和激活应串行操作；旧脚本和手工替换二进制不受新脚本保护，不可用于绕过此限制。

遇到拒绝回退时，停止全部用户节点连接，保存当前控制数据库、主密钥及每个节点的最新 ledger，人工协调修复支持中央准入的新版本。不要删除标记、改写激活字段、恢复激活前数据库或启动旧 core。需要从备份恢复时走下述恢复 hold 与最终账本核对流程，不能把快照回退当作暂停或取消激活。

停售 `saleEnabled=false` 禁止新开通、重开、换入该套餐；已经持有此套餐的用户继续使用并可延长到期时间。停用 `enabled=false` 使现有权益失效。套餐更新立即改变用户引用的权益，不复制套餐快照。普通续期和换套餐保留本周期已用量；明确 `resetUsage=true` 才创建新计量代次，旧租约的最终上报仍记入旧代次。

## 管理接口

所有管理写操作沿用管理员会话、Origin 和 CSRF 校验。

| 接口 | 内容 |
| --- | --- |
| `GET/POST /api/v1/plans` | 列出/创建套餐 |
| `PUT /api/v1/plans/{id}` | 完整套餐加 `expectedRevision`，冲突 409 |
| `POST /api/v1/plans/{id}/preview` | 拟定套餐；返回受影响用户、活跃连接数、逐个 `connectionsToClose` 及原因 |
| `GET /api/v1/users` | 用户数组，含 revision 和权益 |
| `GET /api/v1/users/{id}` | 用户完整管理视图 |
| `PATCH /api/v1/users/{id}` | `{enabled,expectedRevision}`；revision 取 user.revision，缺失或冲突返回 409 |
| `POST /api/v1/users/{id}/assignment` | `{planId,expiresAt,resetUsage,expectedRevision}`；revision 取 user.revision |
| `POST /api/v1/users/{id}/password` | `{password}`；撤销全部用户会话、代理凭据和连接 |
| `POST /api/v1/users/{id}/sessions/{sessionId}/revoke` | 撤销会话及关联连接 |
| `POST /api/v1/users/{id}/connections/{leaseId}/revoke` | 请求断开指定真实连接 |
| `GET/PUT /api/v1/settings/turnstile` | `{siteKey,allowedHostnames,secretKey?,expectedRevision}`；GET 不返回 Secret |
| `GET/PUT /api/v1/settings/portal` | 注册开关 `{registrationEnabled}` |

套餐字段：`name,enabled,saleEnabled,nodeIds,defaultNodeId,bandwidthMbps,quotaBytes,maxConnections`。带宽与额度使用 `null` 表示无限。额度为一次性上传加下载总量；上下行分别可达套餐 Mbps，并按实际连接动态分配每个方向的带宽。

套餐节点必须处于 active 生命周期且已上报 `admission-v1`；未升级或已退役节点不能保存到套餐。已升级节点暂时离线不妨碍编辑套餐。默认节点必须属于该套餐。

## 用户会话与订阅

`GET /api/v1/user/config` 返回注册开关、激活状态、恢复 hold 和 Turnstile 公钥。注册、登录分别 `POST /api/v1/user/register`、`/login`，JSON `{email,password,turnstileToken}`，Turnstile action 分别为 `register`、`login`。邮箱不要求邮件验证；密码至少 12 个字符、至多 1024 字节。任何验证码未配置、失败、网络错误、域名/action/时间不符或本地令牌重放均拒绝。

浏览器 cookie 名为 `hqip_user`，HttpOnly、Secure（HTTPS）、SameSite Strict，有效 24 小时；与管理员 cookie 和桌面令牌互不接受。登录响应为 `{authenticated,user,csrfToken}`，不返回 cookie token 或 refresh token。后续写操作必须带精确 Origin 和 `X-CSRF-Token`。登录同时按 IP 与规范化邮箱限流。

`GET /api/v1/user/session` 检查登录；`GET /profile` 返回 `{user,entitlement,subscriptionUrl,nodes,sessions,connections}`。无套餐时 entitlement 为 null，仍可登录。网页节点 DTO 不返回代理密码或 SSH 信息。`POST /logout`、`POST /password`、`POST /sessions/{id}/revoke`、`POST /connections/{id}/revoke` 都从当前会话取用户身份，不接受客户端指定其他用户。

`POST /api/v1/user/subscription` 创建或获取入口，`POST /subscription/rotate` 轮换，`POST /subscription/revoke` 撤销。匿名订阅是 `/usub/{token}`，只提供当前有效套餐中的就绪节点，保持 TLS 校验开启。轮换或撤销只清理 subscription scope 凭据和租约，不影响桌面会话独立凭据。订阅 URL 不进入访问日志；响应 `Cache-Control: no-store`。

第三方订阅把就绪的套餐默认节点放在选择列表首位，不添加自动切换分组。默认节点不可用时，首项为“默认节点不可用，请手动选择”，其目标为 REJECT；用户仍可手动选择其他就绪节点。全部节点不可用时订阅返回 503。轮换过程中已发起的旧 URL 请求会在输出前重新校验，不会获得替换后的连接凭据。

连接列表保留最近 200 条历史，不能用数组长度当作在线数。`entitlement.occupied` 是尚未关闭且尚未到期的真实 QUIC 租约占位，包含等待节点确认的关闭请求。连接 state 为 active、closing、closed 或 expired；`ratePending=true` 表示等待安全带宽分配，期间不转发字节。

## 桌面 PKCE

1. `POST /api/v2/client/authorize`：`{codeChallenge,redirectUri,state,clientName,installationId}`。只允许 S256 的 43 位 challenge、`http://127.0.0.1:端口/callback`，授权请求有效 5 分钟。
2. 返回 `{requestId,authorizationUrl,expiresAt}`，浏览器打开 `/authorize?request=...`。用户必须网页登录并明确确认。页面 GET `/api/v1/user/authorize/{id}` 显示客户端名称；POST 同路径返回 `{redirectUrl}`，含一次性 code 和原 state。
3. `POST /api/v2/client/token`：`{code,codeVerifier,redirectUri,installationId}`，匹配 verifier、原 callback 与 installation 才消费 code。返回 access 15 分钟、refresh 自登录起绝对 30 天。
4. `POST /refresh {refreshToken}` 原子轮换两个 token；重放旧 refresh 会撤销整个 session 及关联租约。`POST /logout` 使用 Bearer。
5. `GET /profile` 保留原 ClientProfile 字段并增加 user、entitlement；每个 native session 的代理密码单独生成。规则地址为 `/api/v2/client/rules/{sha256}`，原始规范 JSON 字节与 hash 一致。
6. `POST /connections/drain` 请求断开当前 native session 所有连接；不提前释放占位或预算。`GET /connections` 返回当前 session 连接、全用户 occupied/maxConnections；名额已满时返回 `admissionErrorCode=CONNECTION_LIMIT` 及清晰提示。客户端等待真实关闭确认或截止时间后才尝试新连接。

自助改密、管理员重置密码或停用用户会在同一事务内撤销已经批准但尚未交换的一次性授权码；重新启用用户不会恢复旧码。批准授权时再次在事务内检查当前网页会话的归属、类型、有效期与撤销状态，入口鉴权后发生的退出或改密不能被迟到的批准请求绕过。

## 中央租约、额度和带宽

Agent 使用自己的节点 Bearer token。`POST /api/v1/agents/admission` 收 `{connectionId,password}`，每个真实 QUIC 连接生成全新随机 connectionId。重试同一连接 ID 幂等；不能跨连接复用。鉴权从该节点的用户凭据查出 user/session，再检查用户启用、套餐启用、到期、节点权限、连接数和余额，数据库事务原子占槽并预留预算。

返回 `{leaseId,userId,accountingId,nodeId,sessionId,expiresAt,bandwidthBytesPerSecond,budgetBytes,consumedBytes,revision,rateVersion,ratePending}`。`accountingId=user_<id>` 用于节点历史统计。`budgetBytes` 是该租约累计准许的上传加下载上限，单次发放为有限块，最高 16 MiB。即使无限套餐也不发无限离线预算。

`POST /api/v1/agents/leases/{leaseId}/renew` 收 `{uploadBytes,downloadBytes,closed,appliedRateVersion}`，两个字节数都是连接累计值，不是增量。上报不能回退或超过已授权预算。重复上报不重复扣款；未关闭时按需要补充剩余预算。最终 closed 可以在过期后补报，释放明确未消费的预算。过期租约不可复活；失联租约的未结算预算保留，不能再发给别的连接。超过 120 秒未获有效续租，核心必须断开。

`bandwidthBytesPerSecond` 为**每个方向**的连接份额。单连接获得套餐全部 Mbps，多连接按实际数量分配。新增连接先暂停；旧连接降速后以 `appliedRateVersion` 确认，中央才释放可再分配份额。上调立刻占用中央安全上限，下降在确认前仍保留旧上限。`ratePending=true` 时 bandwidth 为零代表暂停；仅 pending=false 且 bandwidth=0 才代表无限。相同 rateVersion 的速率/暂停状态不变。节点核心必须先实际应用速率，再发送 ACK。

通用 Agent daily/minute 报告接受该节点历史租约生成的 accountingId，供节点、服务器、历史曲线统计；套餐余额只由中央租约累计结算，不把 daily 流量再次扣到用户额度。

## 恢复保护

`hqip restore` 撤销网页/桌面会话、一次性授权码、订阅入口与代理凭据，给现有租约标记撤销，并置 `portal_recovery_hold=true`。已用量和预留预算保留。激活接口不会清除此 hold。

`GET /api/v1/settings/portal/recovery` 返回恢复代次、等待状态和每用户最低已知量。只有等待至少 120 秒、所有活动节点提交恢复后的配置版本、近 30 秒新心跳且核心健康后，才进入可人工核对状态。管理员仍须确认所有节点最终账本（包括备份后增加或丢失的节点）；无法核对时继续 hold。

`POST /api/v1/settings/portal/recovery/resume` 需要 `{recoveryEpoch,ledgerConfirmed:true,users:[{userId,usedBytes}]}`，必须涵盖全部用户且数值不低于已知最终账本。单独核对操作会记录审计，不自动退还不确定的预留预算。节点丢失或旧租约最终量无法取得时，不应猜测填写；保持 hold 或在人工确认损失后执行单独管理决策。

## 官方客户端包

发布目录固定 `HQIP-Windows-x64.zip` 与 `desktop-manifest.json`。manifest 为 `{version,sourceSHA,filename,sha256,size,dirty:false}`；必须完整 40 位源码 SHA、64 位 SHA256、正确大小，dirty 包不公开。GET `/api/v1/user/releases` 返回 releases 数组；`/api/v1/user/download` 返回单个版本元数据；实际下载固定 `/downloads/HQIP-Windows-x64.zip`，提供 Content-Length、ETag、Range。不接受任意文件名，不输出服务器本地路径。

## 隔离真实联调夹具

`internal/control/portal_integration_test.go` 只在显式设置 `HQIP_PORTAL_FIXTURE_OUTPUT` 时写入新测试目录，正常测试跳过。目录名必须包含 fixture、不能已存在。可设置 `HQIP_PORTAL_FIXTURE_PUBLIC_URL`、`HQIP_PORTAL_FIXTURE_NODE_IP`、`HQIP_PORTAL_FIXTURE_DOMAIN`，再运行：

```text
go test ./internal/control -run TestWritePortalIntegrationFixture -count=1
```

输出独立 SQLite、主密钥、私密测试用户/管理员/两个 native session 和两个节点 token。测试套餐 8 Mbps、128 MiB、最多 2 个连接；节点使用 9443/9444。夹具不会 SSH、启动服务或修改网络，不提供生产注册绕过接口。真实注册仍必须通过 Turnstile。使用隔离端口和目录运行控制服务/Agent/core，验收完成后停止测试进程并妥善清理夹具秘密。
