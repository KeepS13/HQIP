# 账户概览续费入口与原节点验证

## 变更边界

- 续费按钮只由 `UserOverview` 注入共享订阅卡片，列表和详情不再创建续费入口。未到期但流量耗尽的订阅仍保留在概览，可以预付下一期。
- 付款购买、管理员分配或赠送的订阅，均按本人持有、未到期、未撤销、用户及套餐启用、在售且有定价的规则校验；CSRF、归属、版本和幂等检查保持有效。
- 续费只追加原订阅周期。保留原节点集合、实例分配数量、发放幂等标识、导入凭据和在线名额账本；不调用新购分配、不新增节点预留。
- 弹窗显示该订阅原节点及当前在线数／上限。HY2 每个实例与 VLESS 来源 IP 合计仍受同一上限约束，续费不扩容、不清零占用。

规范见 [续费契约](../subscription-renewals.md) 和 [在线计数契约](../contracts/online-ips.md)。无数据库迁移。没有新增请求或轮询；移除了每次读取订阅时仅用于判断原购买来源的一条查询。

## 验证

新增 `TestRenewalManualGrantKeepsOriginalSubscription` 在原实现上复现 `renewable=false`，修复后通过；同时验证重复付款通知及原管理员发放请求重放不会重复延期或覆盖已续费期限。

`TestRenewalKeepsAllocationAndConnectionSlots` 覆盖从多节点池获配 1／2 个节点、模板默认数量变更、新购节点不可分配、满额混合协议、重复通知、释放及重新申请、跨期和旧报告迟到。验证原节点与实例数量不变，名额按 3→2→3→0→1→0 正常变化，跨期旧报告不释放新连接。

- `go test -tags=with_utls -race ./internal/store ./internal/control ./internal/agent -count=1`：三个相关包完整竞态套件通过，分别约 187／60／29 秒。
- 隔离 PostgreSQL 18：`go test -tags=with_utls -race ./internal/store ./internal/control -run 'Test(Renewal|OnlineInstances|OnlineIP|OfflineSlot|SubscriptionAllocation)' -count=1` 通过，约 58／4 秒。使用本次单独创建的本机容器；完成后按精确容器身份清理，未连接生产数据库。
- `go vet -tags=with_utls ./internal/store ./internal/control ./internal/agent` 通过。
- 用户站 12 项 Node 测试及 TypeScript／Vite 生产构建通过。
- `node scripts/test_user_renewals.cjs` 使用本地 Playwright 和 Chrome、全量拦截网络：唯一入口、同名订阅准确绑定、原节点及在线数展示、无库存续费、双击防重、未知结果原参数重试、流量耗尽、关闭支付、375px 手机布局及键盘焦点验证通过。桌面和手机截图已检查。

脚本可用 `HQIP_PLAYWRIGHT` 指定本地 Playwright 模块、`HQIP_CHROMIUM` 指定浏览器。验证日志位于库外 `.diagnostics/renewal-entry-*.log`，截图位于 `.local/renewal-entry-qa/`。上述测试模拟支付平台，没有发起真实付款；本次未发布生产。

## 影响检查

编辑前已执行 GitNexus upstream 分析：共享卡片、续费弹窗／按钮、订阅读取函数为 HIGH，分别涉及概览／列表／详情及准入／续约／客户端资料；已在编辑前提示并覆盖对应回归。付款续费函数为 LOW，调用者包括付款记录和履约处理。Go 测试入口为 UNKNOWN，已通过源码定位与实际测试执行确认，未将零调用视为未使用。

首次编辑后索引重建因 LadybugDB 约 297 MiB 缓冲池耗尽而失败，其后变更分析遗漏了本次源代码，因此该结果不作为通过依据。仅为重试进程将缓冲池设为 1 GiB 并执行完整索引重建；不修改全局配置。索引器另有既有的执行流预算警告，不能将未列出的流程解释为无影响。

完整重建成功后执行 `detect-changes --scope all --limit 5000`，含本次新增测试及脚本，共映射 16 个文件、68 个符号、4 条已索引流程，风险 MEDIUM；命令退出码为 0，未报告 partial/truncated。统计也包含用户原有客户端探测和样式修改，本次未改动这些内容。CLI 默认只展开前 15 个符号，其余以总数列出。分析时临时对本次新增文件使用 intent-to-add，结束后已恢复为未暂存；未提交源码。
