# API 边界与主要接口

正式环境由 Nginx 分别托管用户端 `hqip.sharevps.online`、管理端 `cms-hqip.sharevps.online` 和 API `api-hqip.sharevps.online`。控制服务仍使用管理员与公共两个回环监听器：管理员浏览器 API 只到管理监听，用户、客户端、Agent、订阅及健康检查只到公共监听。JSON 使用 camelCase；错误响应为 `{ "error": "..." }`。浏览器 fetch 使用 `credentials: include`，写请求携带登录时取得的 `X-CSRF-Token`。

管理会话和用户会话分别使用 host-only Secure、HttpOnly Cookie；各自 Origin 精确限制在对应网页来源。用户登录不能访问管理员 API，客户端 Bearer token 和节点 Agent token 也不能代替浏览器会话。订阅 URL、密码、SSH 信息和授权令牌不得进入访问日志。

## 用户订阅与购买

详见 [用户账户、多份订阅与节点准入](portal.md)。主要接口如下：

| 方法与路径 | 边界 |
| --- | --- |
| `GET/POST /api/v1/subscription-templates` | 管理员列出或创建模板 |
| `PUT /api/v1/subscription-templates/{id}` | 管理员更新模板并提交 `expectedRevision` |
| `POST /api/v1/subscription-templates/{id}/preview` | 预览现有实例和连接影响 |
| `GET /api/v1/subscription-requests` | 管理员只读查看历史申请；旧审批写接口返回 410 |
| `GET/POST /api/v1/users/{id}/subscriptions` | 管理员查看或分配实例 |
| `DELETE /api/v1/users/{id}/subscriptions/{subscriptionId}` | 移除单个实例 |
| `GET /api/v1/user/catalog` | 用户查看可购买商品，`priceCents` 为人民币整数分，空值不可购买 |
| `GET /api/v1/user/subscription-requests` | 用户只读查看本人历史申请；提交和取消返回 410 |
| `GET/PUT /api/v1/settings/payment` | 管理员读取脱敏支付配置或带版本保存 |
| `GET /api/v1/user/payment-info` | 用户读取结账启用状态，不返回商户或密钥 |
| `GET/POST /api/v1/user/orders` | 用户列出本人订单，或以 `requestId,templateId,templateRevision` 创建 |
| `GET /api/v1/user/orders/{id}`、`POST …/{id}/close` | 本人订单详情／关闭本地付款入口 |
| `GET /api/v1/orders[/{id}]` | 管理员查询订单及活动记录 |
| `POST /api/v1/orders/{id}/retry-fulfillment`、`POST …/{id}/query` | 带 `expectedRevision` 重试已核验收款的开通或按权限查单 |
| `GET/POST /api/v1/payments/fm/notify` | 公共监听，独立验签，不使用登录 Cookie；持久化后纯文本 `success` |
| `GET /api/v1/user/subscriptions` | 用户查看本人多份实例 |
| `GET /api/v1/user/subscriptions/{id}` | 查看单份详情与允许节点 |
| `POST /api/v1/user/subscriptions/{id}/import-link[/rotate\|/revoke]` | 管理该实例的第三方导入入口 |
| `/api/v3/client/*` | Windows 客户端授权、同步、订阅选择、状态和连接收尾 |
| `GET /usub/{token}` | 无浏览器会话的 Mihomo/Clash 配置 |

管理和用户写请求均执行各自会话授权、CSRF、修订版本冲突检查和审计。用户实例查询始终从认证会话解析 userId；不得使用请求体中的用户 ID 决定访问权限。同一个下单请求 ID 的重试返回原订单，不重复向支付平台下单；用户明确再次购买使用新 ID。收款状态与开通状态独立，按唯一 `payment:{orderId}` 原子发放，浏览器回跳参数不构成付款证明。完整配置、异常处理与迁移说明见 [支付 FM](payments.md)。

## 认证与限制

客户端下载元数据由 `GET /api/v1/user/releases` 和 `GET /api/v1/user/download` 返回，均禁止缓存。`url` 指向 `/downloads/{sha256}/HQIP-Windows-x64.zip`，每次包内容变化都会生成新的下载路径；成功响应使用 `public, max-age=31536000, immutable`。服务端仅提供与当前已校验发布包摘要一致的路径，错误摘要和已替换的版本返回禁止缓存的 404，不会把新版字节放到旧摘要地址。旧 `/downloads/HQIP-Windows-x64.zip` 入口保留兼容，响应 `no-store`；下载页始终使用元数据中的 `url`。

- 管理员 API：`/api/v1/session`、`/login`、`/logout`、`/password`。
- 用户 API：`/api/v1/user/config`、`/register`、`/login`、`/session`、`/profile`、`/logout`、`/password`、`/sessions/{id}/revoke`。
- 注册和用户密码重置／修改按 Unicode 字符数至少 6 个，最大 1024 字节；管理员自身密码策略独立。
- 注册与网页登录需要 Turnstile。客户端 OAuth 使用一次性授权码、PKCE、state 和本机回环回调；密码不交给 Windows 客户端。
- 旧 `/api/v2/client/*` 返回明确升级错误，不再提供旧单套餐客户端配置。

## Agent 租约与计量

节点 Agent 使用自己的 Bearer token。准入、租约续约、配置和报告均绑定由服务端解析的节点身份。代理凭据、租约、速率分配、流量预算及计量归属均按 `subscriptionId` 隔离；同一实例的多个客户端共享其限制，不同实例互不混算。连接关闭待节点确认时，名额及未结算预算仍保留。套餐流量按有效期内上传和下载总计，预算预留不计作已确认消耗。节点服务离线期间已有租约最多继续 120 秒，新连接不获准。

服务端保留旧监控 accounting identity 的结算映射，以接受合法迟到报告；旧开发用户、单套餐实例及导入 token 不恢复为可准入凭据。数据库 schema 升级前必须经 `scripts/subscription_migration.py` hold、节点最终账本 receipt 和显式确认门禁。详见 [三域名部署与恢复](domains.md) 与 [portal 运维说明](portal.md)。

## 站内运维预警

预警仅为管理端基础设施事件，接口为管理员专用的 `GET /api/v1/alerts`、`GET /api/v1/alerts/summary`、`GET /api/v1/alerts/{id}` 和带 `expectedRevision` 的 `PATCH /api/v1/alerts/{id}`。用户订阅流量、期限、停用及开通申请不生成运维预警。详情和留存规则见 [预警中心](notifications.md)。

## 节点、规则与服务器

节点、服务器、IP 清单、Cloudflare DNS/证书和规则接口仍为管理员 API，写入要求管理员认证及 CSRF。详细数据模型、预检／接管／删除流程和绑定公网 IP 规则见 [多 IP 运维](multi-ip.md)；服务器计量口径见 [服务器监控](monitoring.md)。控制服务不会将管理员 API 回退到公共监听，也不会根据用户提交的内容拼接 SSH 命令。

服务器创建要求 `expiresAt` 为未来北京时间整点；`PATCH /api/v1/servers/{id}/settings` 可为未配置的服务器首次设置到期时间，普通额度修改不刷新流量。已配置服务器可通过 `PATCH /api/v1/servers/{id}/expiry` 提交 `expectedRevision,requestId,expiresAt` 单独修改日期，保留当前周期标识、开始时间及已有用量，并记入 `kind=correction` 的历史。`POST /api/v1/servers/{id}/renewals` 要求 `expectedRevision` 和 `requestId`，未逾期传正整数 `months`，逾期传手动选择的 `expiresAt`，两者互斥；`GET` 同路径读取有效期与续期历史。重复请求返回原变更结果，不重复延期。首次设置、日期修改及续期均从最新到期时间向前每 30 天推算未来重置点，只安排严格晚于保存时间且早于到期的边界；保存不立即清零，只有明确确认的逾期续期会立即开启新周期。旧数据升级后保留原周期和排期，下次修改或续期时应用新规则。服务器／节点响应的 `billing` 包含 `status,expiresAt,cycleId,cycleStartedAt,nextResetAt,cycleUpload,cycleDownload,cycleComplete`，原自然月及累计字段继续保留。

Agent `hourly-usage-v1` 能力增加累计 `hours`、覆盖起点 `hourlySince`、已完整传送至的采样水位 `hourlyThrough`。小时桶与旧分钟派生小时分开保存，重传采用最大累计值，不能再次增加日累计或用户订阅消耗。不确定采样时段显式标记，周期统计不会将其视为可确定的当前周期用量。

`GET /api/v1/nodes/{id}/delete-preview` 的 `subscriptionTemplates` 数组列出引用该节点的模板，字段为 `id,name,defaultNode,enabled,listed`。存在任意引用时，`DELETE /api/v1/nodes/{id}` 返回 HTTP 409、`code: NODE_REFERENCED_BY_SUBSCRIPTIONS` 和相同数组；不会取消任务、改变授权或停止节点。须先修改模板解除全部引用，再提交节点删除。
