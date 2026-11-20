# 流量计费本地验收记录

验收时间：2026-11-20 至 2026-11-21（北京时间）。工作区基准为 69ba869，包含尚未提交的本次实现及其他并行改动；未部署、未推送、未修改线上倍率。

| 验证 | 结果 |
| --- | --- |
| SQLite：store、control、agent、client 回归 | 通过 |
| PostgreSQL 17.7：store、control 完整回归 | 修正嵌套 JSON 查询兼容问题后，control 全量通过；store 仅旧迁移测试漏调用新迁移，补齐后专项复测通过 |
| PostgreSQL：倍率、余数、混合协议并发、CAS、尾额准入、增量迁移、旧租约结算 | 通过，包括重复迁移与旧用量、预留保留 |
| Go race：store、control、agent、client | 通过 |
| PostgreSQL race：计费与并发准入专项 | 通过 |
| 转发内核：server、billing、integration_tests | 通过；server 与 billing 的 race 通过 |
| 真实 HY2 + VLESS Reality + Agent + 控制端 | 持续 HTTPS 传输期间两次上调/下调，两份租约 ID 保持，实际每方向速率均保持 50,000 B/s，按确认段准确结算；race 联调通过 |
| 客户端真实 sing-box 出口统计 | TCP 短连接、UDP、直连排除、selector 单次计数、查询不清零全部通过 |
| Reality/Vision 真实协议回归 | legacy 和 xtls-rprx-vision 两种模式通过 |
| 客户端估算 | 分段累计、延迟边界、历史不整体重算、过期停显原始值、未知区间恢复、计数器重置通过 |
| 发布脚本 | 240 项测试通过，2 项既有环境条件跳过 |
| 静态与构建 | Go vet、桌面 Go 测试、管理端/用户端/桌面前端构建通过；管理端保留现有 Vite 大于 500 kB 提示 |
| Windows 客户端包 | 4.8.0 已构建，PE 图标资源、内层各文件 SHA256、计费能力标记与统计内核标签校验通过 |

测试只使用本机回环、临时 PostgreSQL、临时账户和测试证书。PostgreSQL 角色边界仍由已有发布角色及默认授权配置维持，本次未连接真实生产数据库验证。Go race 使用独立 LLVM-MinGW；Reality 测试仅对已有 sing-vmess/vless 依赖关闭 checkptr，race 保持开启。

GitNexus 已对计费链路及新增 applyBillingSample → sampleMetrics → runMetrics → Controller.Run 链路执行影响分析。整个共享工作区的图变更分析标为 critical，包含其他并行节点部署/IP 切换改动，不能把它当作仅本功能的影响清单；未执行提交。完整输出保存在 .local/traffic-billing-graph-review.txt。

客户端包：dist/HQIP-Windows-x64.zip。此包来自带未提交变更的工作区，manifest 如实标记 dirty；生产完整发布仍要求从审核后的干净提交重新构建。上线顺序和计费说明见 traffic-billing.md。
