# 客户端订阅、交互与规则模式修复

日期：2026-11-24。模块：client-presentation、routing。契约：[client-runtime.commands v1](../contracts/client-runtime.md)。本地交付：`5.0.0-preview.9` / `5000009`；未部署线上服务或替换用户正在运行的客户端。

## 根因与行为

| 问题 | 根因 | 修复后 |
| --- | --- | --- |
| 仪表盘有套餐却显示空提示 | 只读取当前授权 `subscription`，忽略账户的 `subscriptions` | 无选择时展示套餐卡片、额度、状态与展开后的选择操作；选中后立即展示对应摘要 |
| 路由模式悬停撑高 | 自定义 normal/disabled 内边距 4–5，继承 hover/pressed 内边距 10；其他按钮也存在状态不全 | 共用完整状态样式，颜色/边框变化不改变几何；同时修复开关 hover_pressed/focus 与滚动条按下样式 |
| 规则模式海外访问失败 | 大陆 IP 判断前强制本地 DNS；污染为大陆 IP 会误直连，无答案会在代理兜底前失败 | `resolve` 使用 DNS 策略，未知域名走经节点的远程 DoH；大陆域名与 CN IP 保留直连 |
| 未连接时选节点也等待 | 统一执行可达性预检、内核停止、Profile/Drain/WaitDrained/Select/Rules；全局 selectionPending 又控制连接卡片 | 加密保存账户隔离的本地草稿，0 HTTP、0 内核操作；点击连接才消费草稿与获取必要授权 |
| 断开后其他操作仍排队 | 本机已断开，但持有 routeMu 同步等待账户上报，而账户请求可能被订阅修改占用 | 本机停止后返回，已有工作任务负责上报，Close 仍取消并等待任务 |

本地草稿与服务端授权 Profile 分开，View 投影所选套餐/节点，实际连接仍使用 Actual ID。刷新、重启、旧响应晚到不得覆盖草稿。持久化失败回滚偏好，不改变现有连接代次；规则完整性等非选择错误不会因本地选节点被清除。原 Wails 托盘的 Nodes/DefaultNodeId 也按本地草稿投影。

已连接时保留订阅释放与授权约束。同套餐换节点使用已有节点切换入口，不修改套餐授权；重复选同节点幂等；跨套餐的规则摘要不变时复用已验证缓存。没有移除服务端准入、计费或离线许可校验。

## 开销与界面状态

- node/subscription 确认直接返回安全 View，界面不再多等一次轮询。桥接层过滤确认前发出的旧状态，避免选择闪回。
- 后台账户刷新不锁住节点弹窗。测速与连接具有各自等待状态；本地选节点不会显示“正在连接/切换订阅”。保存确认前仅短暂禁用连接按钮，防止连续点击连到旧目标，确认后立即恢复。
- 隐藏页面不渲染，打开时更新。用量变更原位更新套餐卡片，节点列表只因名称/资格/节点/测速/选择/搜索变更重建。
- 不变的按钮表面样式复用；连接卡片复用自身 StyleBox，减少轮询分配与布局失效。
- 在无授权套餐、只有可选目录时，例行刷新也不重复停止内核。

## 验证证据

修复前，新增交互回归报告 8 项失败；本地节点选择测试读到 SelectionPending=true。修复后的 Go 测试覆盖无网络选择、无内核操作、首次连接、持久化/账户隔离/写失败、迟到 Profile、断开期间迟到选择、同套餐切换、重复选择与工作路由保护。断开阻塞夹具修复前耗时约 10 秒，修复后该整例约 0.03 秒，并增加断开 1 秒上限断言；这不是公网性能承诺。

使用随包、哈希校验通过的 sing-box 1.14.4，保留实际路由和 DNS 策略，仅将 DNS 端点替换为确定性 hosts、传输替换为两个回环出口标记。旧代码中：海外域名污染为 CN IP 误走 direct，本地无答案的海外/未收录域名失败；新代码中，两类海外请求走 proxy，大陆域名和可信解析到 CN IP 的未收录域名走 direct。另通过 rule/global × proxy/TUN 配置解析与 Windows 特权辅助程序校验，管理绕过在暂停规则前。

相关自动化：

```powershell
go test -race ./internal/client ./internal/clientbridge ./internal/rules -count=1 -timeout 120s
go test ./internal/winclient -count=1
$env:HQIP_SINGBOX_EXE=(Resolve-Path dist/godot-windows/versions/bundled/sing-box.exe).Path
go test ./internal/client -run 'TestRuleResolutionUsesDNSPolicy|TestPinnedCoreRuleDNSPollution|TestPinnedCoreRoutingMatrix|TestRuntimeManagementBypass' -count=1 -v
godot --path godot-client --script res://tests/interaction_regression.gd --quit-after 300
godot --headless --path godot-client --script res://tests/ui_smoke.gd --quit-after 240
godot --path godot-client --script res://tests/visual_parity.gd --quit-after 900
npm --prefix desktop/frontend run build
# 在 desktop 目录运行 go test ./...
```

Godot 检查 normal/hover/pressed/hover_pressed/disabled/focus 的尺寸，以及真实输入事件下连接卡片高度；还验证隐藏页面、节点/套餐复用、焦点保持、确认后即时展示、过时回复过滤。既有视觉回归覆盖七种窗口宽度、四种连接状态，smoke 覆盖 150% DPI。日志必须有 PASS 且无脚本错误，不能只依赖 Godot 返回码。截图 `.diagnostics/ux-subscriptions.png`。

最终包与摘要见 `dist/godot-build.json`。构建沿用原签名身份，安装包排除测试夹具、保留资源许可证。详细日志位于 `.diagnostics/ux-*.log`，最终核验记录 `.diagnostics/ux-artifact-validation.json`。

## 范围与限制

未接管本机代理/TUN/DNS，不使用生产凭据进行路由测试。本轮未提供真实大陆运营商网络、ARM64 真机或新的完整模拟器 VPN 验收；不能将受控 DNS 复现等同于所有海外网站已经实测可达。服务端和第三方订阅导出不在本次修改范围。

GitNexus 对共享 View/账户路径/偏好报告 CRITICAL，对连接/订阅/配置路径报告 HIGH，编辑前已披露。GDScript 为 UNKNOWN，已用实际 preload/main.tscn 引用和动态界面回归补充；不能解释成无影响。工作区包含大量此前的未提交修改，本次保留，不提交或部署。

将连接命令与订阅授权任务拆至 `connection_commands.go` / `subscription_change.go`；本地草稿在 `local_selection.go`，新增生产文件均低于 300 行。`ui/main.gd` 318 行沿用 shell 初始化/布局/桥接集中例外。历史大文件只移动本次完整职责，不附带无关重构。

最终 Go race、原 Windows 客户端测试、原前端构建、Godot source/exported PCK 交互验证均通过。三平台包签名、实际大小/SHA-256、Android versionCode/ABI、测试排除与许可打包均已核验。Windows 运行时另使用全新空状态目录调用 state，确认运行时版本与未登录状态；未发送连接命令，前后系统代理设置摘要一致。

索引已更新；全工作区 detect-changes 与扩大 limit 后的复查均为 CRITICAL（43 个已有 tracked 修改文件），其中包含本轮之前的其他模块变更。索引流程预算仍有截断，FTS 构建失败降级；未将此结果作为全仓库无风险证明，新增/未跟踪文件由定向测试、构建和实际包核验覆盖。
