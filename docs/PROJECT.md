# HQIP 项目地图

项目 ID：`hqip`。Go 控制端、节点 Agent 与 HY2/Reality 内核提供订阅授权、流量计费和代理服务；React 管理站、用户站与 Windows 桌面端消费独立 API。

本次精简：每份订阅的同时在线上限改为 HY2 在线客户端实例数加 VLESS 来源 IP 数，节点分配数独立保留。同 IP 的两个 HY2 实例加一个 VLESS 来源占三个名额。直接复用 portal_leases、订阅事务和 Agent 账本及已有计费协议字段，不新增设备表、缓存服务或授权额度。来源 IP 存在现有连接表，HY2 实例与 VLESS IP 合计不再增加数据库列。仅 VLESS 在同一订阅内按来源 IP 跨节点去重；HY2 每条独立客户端会话单独计数。旧记录缺少可信协议或 IP 时按许可逐条计数。发布先升级控制端，再统一升级节点与内核。完整语义见 [在线计数契约](contracts/online-ips.md)。

| 模块 | 所有者与位置 | 契约 |
| --- | --- | --- |
| subscription-admission | control / internal/store、internal/control | offline-admission v1；node-quota v1；[connection-slots v3](contracts/connection-slots.md) |
| node-metering | Agent / internal/agent，runtime/hysteria/core/server | 本地持久化子租约、协议计费段 |
| traffic-billing | store、model、runtime billing | 固定点倍率与幂等累计报告 |
| client-presentation | internal/client、desktop、user-web | 用户余额与原始链路统计 |
| subscription-admission / 第三方导入 | internal/store/portal_import_links.go、internal/control/portal_import_export.go、user-web | [subscription-admission.import-links v1](contracts/subscription-import-links.md)：默认合并复制、单份复制与凭据轮换 |
| subscription-admission / 管理员赠送 | internal/store/subscription_grants.go、internal/control/subscriptions.go、admin-web/src/pages/GrantSubscription.tsx | [subscription-admission.manual-grant v1](interfaces.md)：提交后读取结果、原请求恢复、明确拒绝后重新确认 |
| subscription-renewals | internal/store/subscription_renewals.go、subscription_periods.go；internal/control；user-web、admin-web | [按周期续费](subscription-renewals.md)：原订阅延期、周期账本、幂等付款；[node-quota v1](contracts/node-quota.md) 的 periodId 扩展隔离跨期节点预算 |

本次边界变更的规范见 [节点额度契约](contracts/node-quota.md)，已有行为见 [离线授权](offline-admission.md) 和 [部署](automatic-deployment.md)。数据库仅存业务状态，操作证据保存在 docs/incidents 或库外诊断记录。

验证：`go test -tags=with_utls ./internal/store ./internal/control ./internal/agent`；CI 执行 race、真实 PostgreSQL/Redis、真实双协议代理与 Windows 打包验证。部署只使用同一提交的完整发布包。

新模块文件默认不超过 300 行；已有大文件按本次修改边界逐步拆出完整职责。节点普通续约复用原 5 秒补报；新连接强制核验网站，断开立即补报并释放名额。每次释放或重新申请至多增加一次名额报告，受既有请求超时限制，待确认报告原样重放。节点 SQLite 扫描受既有账本容量上限约束。中央快照与名额读写在同一订阅事务内完成，不扫描其他订阅历史租约。所有测试前后保留既有节点探测修改。旧设备账号的登录/刷新/状态写接口和未挂载的设备页面已删除，历史构造器仅用于结算与迁移回归测试；正式路径不再发放旧授权。客户端恢复不再重复按占用数预判准入，由代理入口统一裁决。

首次准入丢回包后的恢复使用现有 `offline_requests`，新增只读回执查询接口，不新增业务表或存储明文密码。节点最多每 30 秒轮转查询 16 条待确认请求，最多使用同步剩余预算的一半；普通父账本按最早重试时间处理，避免清理饥饿。持久化、身份核验及未知用量保护见 [名额契约](contracts/connection-slots.md)。

文件预算例外：`internal/store/offline.go` 保持完整结算事务，329 行，避免跨函数隐藏结算与回执顺序；`scripts/upgrade_domains.py` 324 行、`scripts/upgrade_node_agent.py` 392 行，保留原升级/回滚职责与停止服务前后的能力检查。这三处均少于 400 行。`internal/agent/offline.go` 的授权回执校验、周期调度分别拆至 `offline_receipt.go`、`offline_sync.go`，首次准入回执恢复位于 `offline_recovery.go`，均少于 300 行。历史大文件仍存在，本次按用户精简要求只调整相关入口、字段与文案，不附带大范围结构拆分；新增生产源文件均少于 300 行。
