# subscription-renewals 合并检查

日期：2026-11-21。目标基线：`main` 的 `9c9b6ca`；远端分支：`origin/codex/subscription-renewals` 的 `0871d94`，包含 5 个提交。本次仅在本地检查和合并，没有发布或执行生产迁移。

## 合并处理

- 在独立工作树试合并，解决 10 个文件的冲突。保留主分支的在线实例/IP 限制、节点共享额度、连接名额释放、导入链接及已拆分模块；将分支上的周期计量逻辑移到当前的 `lease_acquire.go`、`lease_renew.go`、`traffic_settlement.go` 和共享订阅卡片。
- 补齐节点额度的周期隔离：`periodId` 限定计费余数、节点确认水位及 Agent 未结算预算；初始周期继续兼容旧快照和旧水位。旧期补账不得污染新期额度，旧父授权截止不得因预付下一期而延长。
- 旧离线父授权归还未委托额度时，同步更新其原周期及累计账本。
- 补充生产节点额度入口的跨期回归、Agent 重启后的周期隔离及旧快照兼容测试。更新旧测试的到期调整调用和 PostgreSQL 显式迁移步骤，保留未迁移数据库拒绝启动的断言。
- 更新节点额度契约、续费部署说明和公共迁移接口注释。上线续费须同时升级控制端和 Agent，并验证真实客户端跨期重连。

## 检查记录

- GitNexus 在修改前执行影响分析，合并后重建索引并执行 `detect-changes --scope all`。订阅卡片和 SQLite 迁移入口为 HIGH 风险，整体变更为 CRITICAL，主要覆盖支付、启动、迁移、租约和用户页面。对于 UNKNOWN 的 Go 接口调用补充源码确认；图索引有全局执行流预算限制，不能据此宣称穷尽所有调用。
- `go test -tags=with_utls ./...` 和 `go vet -tags=with_utls ./...` 通过；`go test -tags=with_utls -race ./internal/agent` 通过。
- 隔离 PostgreSQL 18 / Redis 8 的竞态套件中，control、pgstore、ratelimit、cmd/hqip 通过。store 全套首次仅失败于历史支付迁移测试遗漏续费迁移；补全后，该测试及全部 `TestRenewal` 竞态测试重跑通过。SQLite 快照回归由 SQLite 全包测试覆盖，PostgreSQL 环境中该项按测试设计跳过。
- 前台 12 项和管理端 20 项 Node 测试通过；两个前端生产构建通过。管理端原有大包体积警告仍存在。
- Python 部署测试在 Windows 下受到符号链接权限/路径差异影响，改在 WSL Ubuntu 运行，256 项全部通过。测试内的部署输出来自模拟对象，没有连接生产服务器。
- 浏览器对已构建资源全量拦截网络：现有订阅导入覆盖 7 种客户端，轮换、撤销、慢响应及移动布局通过；续费列表/详情入口、确认内容、连续点击、修改价格后沿用原请求重试、订单跳转和 390px 手机布局通过，无页面运行错误。

本次新增生产文件均少于 300 行。原有 `internal/store/offline.go` 随周期结算增至 325 行，`scripts/upgrade_domains.py` 增至 324 行，沿用项目既有事务/升级流程的预算例外；历史大文件保持原职责边界。

本地诊断日志和截图保留于独立工作树的 `.gitnexus/` 及 `.local/client-imports-qa/`，业务数据库不保存检查记录。
