# 定制订阅开通计时与主机 CPU 修复验证

## 交付边界

按用户最终确认，空节点池的套餐付款后生成待开通订阅及导入链接，等待期间不消耗有效天数。管理员为原实例分配足额节点并点击“保存节点并开通”时，节点分配、首次有效期和首个计费周期在同一事务提交。已开通实例后续换节点不重置期限；订阅模板修改不会自动重分配人工管理的节点。

接口、状态及持久化规范见 [定制订阅契约](../subscription-activation.md)。待开通链接可以提前获取、复制和导入；暂不提供代理节点或设备准入，正式开通后刷新原链接取得节点。禁用、撤销、版本冲突和不合格节点都不能留下部分开通结果。

续费入口仍只在账户概览“正在生效的订阅”卡片，待开通订阅没有续费入口。符合在售和定价要求的有效管理员分配或赠送订阅同样可续费；保留原实例、节点、凭据及设备名额账本。详情见 [续费验证记录](2026-11-22-renewal-entry.md)。

CPU 首次采样需要两次计数；原实现把第二次采样推迟到下一轮需求采样，页面又未跟进异步采样状态，叠加 5 秒轮询产生延迟。现在首次计数后最多补采一次，页面在采样期间短暂跟进结果；后台采样仍只有一个工作协程，页面只有一个在途请求，隐藏页面停止轮询，超时和追踪时间均有上限。真实 Windows 采样回归在本机测得首个 CPU 样本约 **1.012 秒**；这是本机测量，不是生产环境时延保证。规范见 [主机资源契约](../host-resources.md)。

## 验证结果

- SQLite：`go test -tags=with_utls -race ./internal/store ./internal/control ./internal/agent ./internal/hostmetrics ./cmd/hqip -count=1` 全部通过，分别约 215、61、29、1、4 秒。
- PostgreSQL 18：隔离本机数据库上的定制开通、导入、迁移、续费、节点库存和节点修改竞态回归通过，store/control 分别约 93/4 秒；SQLite→PostgreSQL 迁移及加密付款历史迁移回归另行通过。完成后仅清理本次精确标识的 QA 容器，没有连接生产数据库。
- 最终补充用例通过：付款后等待 60 天不扣期限、订单快照锁定模式/天数/节点数、并发开通只有一次成功、重放不重置期限、不合格状态无部分写入、原链接开通后返回节点、待开通不可续费、混合协议设备满额拒绝、续费保留原节点及占用。
- 迁移回归通过：周期能力 1→2 保留已有预付周期、用量和凭据；重启及恢复保持待开通状态。正式发布采用的 sing-box 1.14.4 实际解析器接受待开通拒绝转发配置，Clash 拒绝转发和 V2Ray 空 URI 列表亦有 HTTP 回归。
- CPU 单次采样合并、冷启动/恢复补采、有界失败、有效 0% 和真实 Windows CPU 回归通过；`go vet -tags=with_utls ./internal/store ./internal/control ./internal/hostmetrics ./cmd/hqip` 通过。
- 用户站 12 项测试、管理站和用户站 TypeScript/Vite 生产构建通过。管理站保留既有大于 500 KB 的构建分块提示。
- `scripts/test_custom_activation_ui.cjs` 和 `scripts/test_user_renewals.cjs` 通过：空池模板、定制购买、待开通链接、唯一续费入口、节点数量校验、CSRF、正式开通、CPU 首次/手动/失败/隐藏恢复和 375px 布局。页面网络全部由夹具拦截，没有真实付款。最终桌面、移动和开通确认截图已检查。
- 发布相关 Python 选定套件 **41 项通过**，订阅专项发现套件另有 **15 项通过**。较宽的 45 项运行存在一个既有 Windows 失败：`test_allocation_deployment.AllocationDeploymentTests.test_rollback_refuses_old_controller_after_attempt_even_before_switch` 预期“prevents rollback”，实际为“Refusing rollback over a later release”。已用 `HEAD` 未修改的 `upgrade_domains.py` 复现同一失败，没有将其计为本次通过或修改无关逻辑。

回归日志保留在 `.diagnostics/custom-activation-*.log`，页面截图在 `.local/custom-activation-ui/`；这些操作证据不写入业务数据库。

## 变更影响与限制

编辑前按项目规则进行了 upstream impact。共享订阅读取及页面组件涉及付款、准入、客户端资料、概览和订单，HIGH/CRITICAL 风险已提示；UNKNOWN 结果通过源码定位及实际测试补证，未视为无调用。

最终源码索引重建成功：15,319 个节点、60,430 条边、707 个聚类、525 条已索引流程。重建后重跑 `detect-changes --scope all --repo . --limit 5000`，退出码 0，映射 59 个文件、267 个符号、38 条已索引流程，整体风险 **critical**，包含工作区原有修改。为涵盖新增源码和测试，临时对本次新增文件使用 intent-to-add，分析后恢复为未暂存。

不能把以上结果视为完整执行流覆盖：索引器仍报告 1,821 个入口候选未纳入、1,725 个被调用分支被预算截断、12 个遍历被预算中止；重新索引仍出现同样限制。最终变更分析本身未报告 partial/truncated，但 CLI 展示固定只展开前 15 个符号和前 10 条流程，其余以数量省略。未列出的流程不等于没有影响；本次以相关源码审查、SQLite/PostgreSQL 竞态测试、HTTP 边界测试和浏览器回归补充，保留图谱覆盖限制。

新增生产文件均小于 300 行；已有大文件遵循项目现有约定，只修改相关行为。主要数据库成本是订阅读取和库存快照各增加一次主键关联；人工开通沿用既有用户分配锁与事务，无常驻开通任务、额外设备记录或无界 CPU 轮询。

## 发布状态

本次未提交、未部署。用户已有客户端节点探测、导入样式和相关文档修改已保留。

与前一轮仅调整续费入口不同，本轮人工开通增加业务表，需要周期能力 **2**。SQLite 启动时事务升级；PostgreSQL 发布时须停止业务写入，以迁移角色执行既有 `hqip migrate-subscription-renewals`，应用角色不会隐式执行 DDL。发布清单、备份恢复和回滚保护已同步识别版本 2，具体步骤以 [定制订阅契约](../subscription-activation.md) 为准。
