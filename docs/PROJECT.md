# HQIP 项目地图

项目 ID：`hqip`。Go 控制端、节点 Agent 与 HY2/Reality 内核提供订阅授权、流量计费和代理服务；React 管理站、用户站与 Windows 桌面端消费独立 API。

| 模块 | 所有者与位置 | 契约 |
| --- | --- | --- |
| subscription-admission | control / internal/store、internal/control | offline-admission v1；node-quota v1；[connection-slots v1](contracts/connection-slots.md) |
| node-metering | Agent / internal/agent，runtime/hysteria/core/server | 本地持久化子租约、协议计费段 |
| traffic-billing | store、model、runtime billing | 固定点倍率与幂等累计报告 |
| client-presentation | internal/client、desktop、user-web | 用户余额与原始链路统计 |

本次边界变更的规范见 [节点额度契约](contracts/node-quota.md)，已有行为见 [离线授权](offline-admission.md) 和 [部署](automatic-deployment.md)。数据库仅存业务状态，操作证据保存在 docs/incidents 或库外诊断记录。

验证：`go test -tags=with_utls ./internal/store ./internal/control ./internal/agent`；CI 执行 race、真实 PostgreSQL/Redis、真实双协议代理与 Windows 打包验证。部署只使用同一提交的完整发布包。

新模块文件默认不超过 300 行；已有大文件按本次修改边界逐步拆出完整职责。节点普通续约复用原 5 秒补报；新连接强制核验网站，断开立即补报并释放名额。每次释放或重新申请至多增加一次名额报告，受既有请求超时限制，待确认报告原样重放。节点 SQLite 扫描受既有账本容量上限约束。中央快照与名额读写在同一订阅事务内完成，不扫描其他订阅历史租约。所有测试前后保留既有节点探测修改。

文件预算例外：`internal/store/offline.go` 保持完整结算事务，320 行，避免跨函数隐藏结算与回执顺序；`internal/agent/offline.go` 301 行，保留父报告、子续约与异常结束核对的顺序，名额转换已独立拆分；`scripts/upgrade_domains.py` 313 行、`scripts/upgrade_node_agent.py` 387 行，保留原升级/回滚职责与停止服务前后的能力检查。这四处均少于 400 行。其余本次修改的大文件已按鉴权、采样、同步、租约发放、续约和计费结算拆分。
