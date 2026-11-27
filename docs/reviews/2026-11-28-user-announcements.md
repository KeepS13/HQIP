# 用户公告本地验收

2026-11-28。模块 `user-announcements`，契约 [v1](../contracts/user-announcements.md)，使用说明见 [用户公告](../announcements.md)。代码已作为 `a9dbbde` 提交、推送并通过完整 CI 部署；生产证据见 [上线记录](../deployment/2026-11-28-user-announcements.md)。没有向真实用户发布测试公告。

## 完成范围

管理站 `/announcements` 支持标题、纯文本正文、两种展示模式、搜索用户、跨搜索保留多选、批量全选结果、发布、查看接收名单/确认人数和停用。发布请求先在当前标签页保存固定 ID 与内容；回执不确定时重试同一请求，服务端比较原文和目标集合，避免重复发布。

用户网站在登录成功或整页刷新后加载一次，组件位于路由外并按账号隔离；无轮询。多个公告按最新优先逐条展示，下一条与关闭不确认，确认成功才前进；已确认的 `until_acknowledged` 由服务端过滤，`always` 继续展示。正文转义、保留换行并可滚动，弹窗沿用焦点限制和 Escape。Windows/Android 客户端保持原状。

新增两张业务表：公告及明确目标/首次确认时间。SQLite 启动增量迁移，PostgreSQL 独立迁移命令、全新初始化、SQLite迁移、备份恢复、完整发布包切换前迁移均已接入。每页20条，收件人最多5000，批量写入每段200个参数；没有新增外部服务、依赖或定时任务。

## 验证

- 完整 `go test ./...` 通过，`go vet ./...` 通过。
- SQLite 公告测试通过：指定用户隔离、两种模式、重复发布/确认、内容冲突、未知用户整批回滚、确认时间不变、分页不漏条、停用、并发发布、重开数据库保留确认。
- 控制端测试通过：管理员/用户鉴权、CSRF、来源、公开监听不暴露管理端、不能代其他用户确认、用户响应不泄露接收名单或统计。
- 使用本轮创建的隔离 PostgreSQL 18 实例（仅127.0.0.1:25434）执行 store/control 公告测试以及 `TestPostgresMigrationPreservesSecretsAndOpenLedger`、`TestPostgresLegacyBackupAddsOperationLogsOnRestore`，全部通过。测试完成已正常停止该实例，未改已有5432服务。
- 两站 TypeScript/Vite 构建通过；管理站仍有超过500 kB的既有分包提示。
- 用户站26项 Node 测试通过。
- `admin-web/tests/announcements.browser.cjs` 通过：批量指定用户、丢失发布回执后关闭/重开/重试保持同一 ID、停用、X、Escape、下一条跨页、失败确认重试、刷新持久化、常驻公告再次展示、路由切换不再次请求、HTML转义、焦点、375/812/1440宽度布局。全部请求拦截为测试数据，无公网业务写入。截图在 `.local/announcements-ui/`。
- 既有 `subscription-apply.browser.cjs` 浏览器回归通过，验证拆分入口后原套餐编辑/应用流程仍正常。
- 新增公告部署3项 Python 测试通过。初次 Windows 全量脚本332项测试有17项错误、20项跳过：16项为当前 Windows 缺少符号链接权限（WinError 1314），另1项为未修改的 Flutter 安装目录检查失败；该次检查未通过。部署前在 WSL Ubuntu 重跑全量332项，结果 `OK (skipped=1)`；随后同一源提交的 Linux CI 检查也通过。
- `git diff --check` 通过。按 HEAD 对比，搬迁的8个React组件函数体、serve/初始化/备份函数体逐字保持；恢复命令只增加公告增量迁移。

## 图谱与风险

编辑前路由注册、存储初始化、迁移、发布脚本及搬迁函数的影响分析为 LOW，调用者包括 AdminHandler/PublicHandler、OpenConfigured、main/run 和网站入口。App/Portal 入口为 UNKNOWN，已用实际 main.tsx JSX 引用补查，没有将其当作无人调用。

变更后重新全量索引。将本轮新文件以 intent-to-add 临时纳入完整 diff 后，`detect-changes --scope all --limit 1000` 返回56文件、198符号、37条执行流程、**CRITICAL**。该风险保留，不能记为低风险通过；统计同时包含用户已有流量改动及本轮入口拆分。已核对路由、迁移/恢复/发布调用以及保留函数体并执行上述回归。使用精确 UID `Function:cmd/hqip/main.go:run` 的独立上游分析为LOW、1个直接调用者main，不能用于抹掉全量CRITICAL结果。detect-changes此次没有partial/truncated标记；索引器仍报告动态调用/流程采样边界，未出现的流程不代表不受影响。

新生产文件均小于300行；既有306/382/327行文件例外及原因记录在 PROJECT.md。保留原有流量统计未提交修改，未混入公告实现。
