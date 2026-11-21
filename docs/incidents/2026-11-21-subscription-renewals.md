# 2026-11-21 订阅续费部署

正式服务器 `64.83.17.143` 已部署 `b8c968f8188b62575d3e8c9364977466fa3ad515`。2026-11-21 20:44:10 UTC 自动验收通过，20:45:40 UTC 复查控制服务及全部活动节点正常。功能规则见[按周期续费](../subscription-renewals.md)，先前合并审查见[检查记录](../reviews/2026-11-21-subscription-renewals.md)。

## 发布与迁移

- GitHub run `44946831020` 因账户付款或消费额度限制未获得 runner，三个检查作业均未启动；不能将此运行视为 CI 通过。沿用项目已有人工发布路径，在干净工作树完成构建和本地验证后，通过已固定主机指纹的 SSH 通道发布。
- 控制端、两套网页、两架构节点产物及 Windows 客户端均由上述同一提交构建；服务端和桌面清单均为 `dirty=false`。用户原有节点探测代码、测试、`docs/client.md` 和导入页面样式修改未进入发布。
- 完整包 `hqip-full-b8c968f8188b.tar.gz`，87,444,291 字节，SHA256 为 `15cb5fc7be569a068cbc125656e566e139ca4f8f13d4892409f5517ded83ea0f`；上传后在服务器再次校验。
- 先升级一个节点验证，再逐台完成 15 个活动节点的 Agent 升级，随后才启用控制端续费。全部运行 Agent 的 SHA256 为 `1d737b623ffb15b582df3638e0f17c2517944c54def08d342eba362140a5c047`，与发布清单一致。每台持久化升级回执均为 `succeeded`、`coreInvocationUnchanged=true`；原节点账本保留，转发核心未重启。
- 沿用 `package_domains.py`、`install_ci_release.py`、`upgrade_domains.py`，从原活动版本 `848f73c854815b09feb33e4d6f4656f9c323b602` 切换。安装进程退出码为 0，最终 `phase=active`；`renewalMigrationAttempted=true`、`renewalMigrationCompleted=true`。
- 一致性数据库及配套密钥备份位于 `/root/backups/hqip-ci-b8c968f8188b62575d3e8c9364977466fa3ad515-44946831020-1`，`backupMode=online-consistent-snapshot`。周期迁移在控制端停止写入后执行，能力标记为 `subscription_renewals_version=1`。
- 周期 schema 已生效，不能启动不理解周期的旧控制端或旧 Agent。故障需按兼容性检查向前修复；备份不用于覆盖迁移后产生的新订单和流量。节点私有升级备份及回执保留在各节点的 `/root/backups/hqip-managed-renewals-b8c968f-<节点ID>`、`/var/lib/hqip-operations/renewals-b8c968f-<节点ID>`。
- 协调发布时临时暂停自动部署，成功验收后已恢复 `HQIP_DEPLOY_PAUSED=false`；GitHub 账户额度问题仍需账户所有者处理。

## 自动验证

- 合并后续费专项测试在 store、control、agent 三个包开启 race 并重新执行，通过。
- Linux 全量 `go test -tags=with_utls -race ./...` 使用隔离 PostgreSQL 18 和 Redis 8，通过；store 全套本次也完整通过，容器退出码为 0。SQLite 全包测试和 `go vet -tags=with_utls ./...` 已在合并检查通过。
- 两套前端生产构建通过；用户端 12 项和管理端 20 项测试通过。Windows 客户端完成实际构建、核心校验和图标验证；desktop 模块 `go vet ./...`、`go test ./...` 通过。
- Linux Python 3.12 的全部 256 项部署测试通过。
- 使用新构建 Hysteria 核心及打包 sing-box，开启 race 执行真实 HY2/VLESS 双协议热更新计费测试，通过。该测试验证在线名额、共享带宽、倍率变更和最终计量。
- 部署专用隔离测试使用临时 SQLite、真实控制接口、Agent、Hysteria 与 sing-box 进程，按实际时钟跨过周期边界。同一客户端取得新周期授权后继续传输；重复签名付款通知只追加一期，原导入链接保留，旧授权未延长至新期。测试旧期记录 580,176 字节、新期记录 8,532 字节，61.10 秒通过。
- 上述隔离跨期测试的支付平台响应和签名通知为测试数据；浏览器交互验证也使用模拟 API。不能将其描述为生产真实付款或长时间生产跨期观察。

## 线上验收

- 活动控制进程哈希匹配发布清单；本机管理和公共两路 `/readyz`、公网 `/healthz` 与 `/readyz` 全部为 `ok=true`。
- 公网用户站及管理站返回 200，HTML 和主 JS 摘要与本次清单一致，用户 JS 包含续费入口及订单界面。订单、订阅及导入接口拒绝匿名访问。
- 发布前后均为 402 个用户、147 份订阅、11 笔订单、24 个节点记录，其中 15 个活动节点。订阅身份、归属、模板和到期时间的汇总校验值一致；支付配置及其历史版本、配套主密钥的摘要一致。
- 147 份订阅均初始化为一期；缺失初始周期、周期边界不一致、缺失租约周期关联、原账本与周期账本用量或预留不一致的计数全部为 0。
- 原有 8 笔已付款订单仍全部履约，另有 3 笔未付款订单；验收时生产续费订单为 0。用户选择先部署并自动验证，本次未执行真实新购或续费付款。
- 全部 15 个活动节点 ready、核心健康、Reality 就绪；配置 revision 全部应用完成。最后复查最旧心跳距检查约 5 秒，控制服务无自动重启。复查窗口内控制日志无新增条目，不据此推断长期无故障。

库外证据位于 `.diagnostics/renewals-b8c968f/` 和独立工作树 `.local/subscription-renewals-review/.gitnexus/deploy-*`。本次隔离验证容器及其数据卷已清理；业务数据库中未存入测试证据。
