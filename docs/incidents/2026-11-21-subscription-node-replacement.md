# 2026-11-21 订阅节点更换部署

正式服务器 `64.83.17.143` 已部署 `24ee07b001ff7e44ed05a942c81fdb0c54bd22a2`，2026-11-21 21:31 UTC 完成线上验收。管理员从用户详情的“修改节点”更换单份订阅的节点，选择数量必须等于该实例原分配目标；流量、计费周期及到期时间保持原语义。规范见[节点分配](../subscription-node-allocation.md)和[接口边界](../interfaces.md)。

## 发布方式

- GitHub run `44950731355` 因账户付款或消费额度限制未启动三个测试作业，部署作业跳过；此次不能记为 GitHub CI 通过。
- 在干净工作树 `.local/subscription-nodes-release` 构建同一提交的控制端、两套网站、两架构 Agent/HY2 及 Windows 下载包，服务端和桌面清单均为 `dirty=false`。原工作区节点探测及用户导入页样式修改保留，未进入发布。
- 完整包 `hqip-full-24ee07b001ff.tar.gz`，87,443,583 字节；SHA256 `12e520ccdcde09b15252ed61d5a8fbae416161bc02b5e64a500fdb67e1152c6e`，上传后已在服务器复核。
- 使用既有固定主机指纹的 SSH 通道和 `package_domains.py`、`install_ci_release.py`、`upgrade_domains.py` 安装，从 `b8c968f8188b62575d3e8c9364977466fa3ad515` 切换。部署进程退出码 0，最终 `phase=active`。
- 一致性数据库及配套密钥备份：`/root/backups/hqip-ci-24ee07b001ff7e44ed05a942c81fdb0c54bd22a2-44950731355-1`，模式 `online-consistent-snapshot`。本次未触发数据库迁移，节点 Agent 和转发核心未重启。
- 如需回退，使用新 release 内 `scripts/upgrade_domains.py rollback --state <上述备份目录>`，先确认没有后续成功发布；保留实时 PostgreSQL 数据，不用旧备份覆盖新增订单和流量。
- 验收后已恢复 `HQIP_DEPLOY_PAUSED=false`。GitHub 账户额度问题仍待账户所有者处理。

## 验证

- Windows 干净源码 `go vet -tags=with_utls ./...`、`go test -tags=with_utls ./...`，desktop 模块 vet/test 均通过。
- Linux 隔离 PostgreSQL 18、Redis 8 环境 `go test -tags=with_utls -race ./...` 全包通过；Linux vet 通过。包含原分配数校验、模板默认数量变化、缺额修复、并发冲突及新旧节点计费回归。
- 新计费回归使用已购买且已续费的订阅、真实 store 节点额度入口及 1.5 倍流量计费，验证旧节点迟到报告、新节点累计扣量、重复报告幂等与未来周期保留；这是隔离测试，不是对生产用户执行节点更换。
- 两套网站及 Windows 下载包构建通过；用户端 12 项、管理端 20 项测试通过；Python 3.12 部署测试 256 项通过。
- 实际发布网页产物通过模拟 API 浏览器验收：1/2 节点固定数量、少选阻止提交、版本冲突重载、搜索和不可选候选、过期只读、桌面与窄屏布局。
- 活动控制进程哈希匹配发布清单，本机两路 `/readyz`、公网 `/healthz` 与 `/readyz` 均 `ok=true`；两站 HTML 和主 JS 摘要匹配发布清单，管理 JS 含新的数量限制及继续计费说明。节点 GET/PATCH 接口拒绝匿名请求，返回 401。
- 发布前后均为 402 个用户、147 份订阅、11 笔订单、24 个节点记录和 15 个活动节点。订阅身份与期限、节点分配、周期边界、支付配置和主密钥摘要一致；逐份订阅累计上传、下载均未减少。
- 15 个活动节点均 ready、核心健康、配置版本已应用；控制服务无自动重启，验收窗口日志未发现 panic/fatal 或数据库错误。未修改生产用户节点或凭据。

库外证据在 `.diagnostics/subscription-nodes-24ee07b/`，构建和本地验证日志在干净工作树 `.gitnexus/`。隔离验证容器及其数据卷已清理，业务库未保存测试证据。
