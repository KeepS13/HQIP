# 2026-11-21 同时在线计数部署

正式代码版本为 `78af0ed2a0a9fe108bd1bcb91f25d03115337682`。控制站、用户站、管理站、Windows 下载包，以及 15 个活动节点的 Agent/HY2 核心均已更新。

规则沿用 [在线计数契约](../contracts/online-ips.md)：HY2 每个在线客户端实例占一个名额；VLESS 在同一订阅内按入口来源 IP 跨节点去重，两者共同受 `maxConnections` 限制。套餐分配节点数独立。该规则支持订阅链接及第三方客户端，不表示硬件设备识别。

## 构建与发布

- 功能提交 `66c0ea1` 的首次 PostgreSQL 回归发现新增索引包含 `expires`，导致续约无法使用 HOT 更新。正式版本改用 `(subscription_id, client_ip) WHERE closed=0`，保留未关闭连接的有界查询和续约优化。首次失败版本未部署。
- CI run `44931402562` 的 Linux、Windows、PostgreSQL 三项全部成功，包含真实双协议热更新、网站失联恢复、同 IP 多个 HY2 实例及跨节点计数回归。
- GitHub 在部署作业开始前报告账户付款或消费额度限制，作业没有获得 runner。Actions 产物存储也达到配额，两平台已通过项目既有私有草稿 Release 通道上传同一提交的产物。
- 使用精确提交的 `git archive` 和同一 CI 产物执行现有 `package_domains.py`、`install_ci_release.py`、`upgrade_domains.py`，通过已固定 SSH 指纹的管理通道手动完成部署。未跳过产物验证、备份、迁移和健康检查，未改变 GitHub 账单设置。
- 完整发布包 SHA256：`76313424a7a0d6ec67c255016d3728744600af5bde0565d1f84d3a4bb43a3276`。
- 一致性备份目录：`/root/backups/hqip-ci-78af0ed2a0a9fe108bd1bcb91f25d03115337682-44931402562-1`，`backupMode=online-consistent-snapshot`，最终安装阶段为 `active`。备份保留 PostgreSQL 和配套密钥，未用旧快照覆盖实时数据。

## 节点更新

15 个活动节点逐个更新；退休节点未动。核心先验证新版能力和实际进程哈希，再调用现有 Agent 升级器更新 `80-hqip-telemetry.conf`，避免旧覆盖配置优先于新二进制。配置和实时账本保留，核心更新会使连接短暂重连。

全部 15 份升级回执均为 `succeeded`，实际进程校验结果一致：

- Agent SHA256：`37bee34bca143a71b7aac23dd4ed46b46de8013e1e2edb9b76bd1724459bdb4c`。
- HY2 SHA256：`6a32b0204c90907ae24c5889dfa8b3ce1a5bfea2d7ebd3383b87d932461d89c0`。
- 核心报告 `connectionSlotsVersion=3`，Agent 上报 `connection-slots-v3`，Reality 入口健康。

节点产物从本机及控制站暂存分发，所有通道均核对已有 SSH 指纹。`itp0` 的一次 SSH 回执读取连接被重置，远端持久任务仍已成功，重连只读取回执，没有重复重启。控制站暂时使用独立目录的分发依赖，完成后已删除；节点重复产物暂存也已清理，保留升级回执和私有恢复备份。

## 线上验收

控制站本地两路 `/readyz`、公网用户站和管理站、`/healthz` 与 `/readyz` 均通过。公网使用项目既有部署探针的 `Go-http-client/1.1` User-Agent；默认 Python User-Agent 被现有边缘规则返回 403，不能据此认定源站故障。

正式 metadata 为 `online_ips_version=1`、`connection_slots_version=3`，其余计费及节点额度版本保持 1。402 个用户、146 份订阅、24 个节点记录保留；15 个活动节点全部 ready、核心健康、desired/applied revision 一致，新版心跳持续更新。

使用第三方 sing-box 内核及已有订阅凭据验证 `itp0`、`zus2` 的 HY2/VLESS 四条真实路径，出口均匹配对应节点。每次独立测试占用从原有 1 增至 2，结束后回到 1，测试名额由节点确认释放，未执行管理员撤销或清空账本。

持续连接的线上上限验收结果：

| 步骤 | 结果 | 订阅占用 |
| --- | --- | --- |
| 保留原有连接作为基线 | 原连接继续工作 | 1 |
| 新建 itp0 VLESS | 成功 | 2 |
| 同 IP 新建 zus2 VLESS | 成功，跨节点共享该 IP 名额 | 2 |
| 新建 itp0 HY2 | 成功，实例独占一份 | 3 |
| 再新建 zus2 HY2 | 被拒绝，已有三条测试代理仍可转发 | 3 |
| 关闭本次测试进程 | 测试占用自然释放，原连接保留 | 1 |

最初用短 HTTP 请求做此项检查时，VLESS 请求结束就释放连接，无法构成稳定的同时在线场景；保留该次失败记录，改用持续下载维持真实连接后通过。没有因此修改业务实现或延长生产占用期限。

库外证据在 `.diagnostics/online-instances-*`，包含 CI 日志、15 份节点回执、四条代理结果、持续连接上限结果、最终生产快照。此次成功运行的临时 GitHub 草稿产物已清理。GitHub 账户计费限制仍需账户所有者处理，后续自动部署可能继续受阻；本次手动部署已完成。

用户原有节点测速代码、对应测试及 `docs/client.md` 的域名探测说明保持在工作区，未纳入本次发布。
