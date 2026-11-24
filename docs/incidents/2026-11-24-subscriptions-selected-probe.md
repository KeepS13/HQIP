# 订阅信息页与当前节点测速

日期：2026-11-24。交付版本：`5.0.0-preview.10` / `5000010`，本地 Windows x64 与 Android arm64/x64。

## 行为与原因

- 原订阅组件承担展示、折叠和本机选择，仪表盘还嵌入了同一选择入口。改为 `subscriptions.gd` 管理自适应列表，`subscription_card.gd` 管理纯信息卡片：单份占整行，多份按内容宽度显示最多三列。网站参考为 `user-web/src/portal/SubscriptionCard.tsx` 与 `subscriptions.css`；保留客户端颜色和排版，不引入网站未通过运行时返回的计费周期推断。
- 去掉“在本机使用此订阅”“本机当前选择”、选择边框及折叠操作；余额、已用、待结算、到期、共享带宽与在线名额直接展示。底部营销卡片通过原生命令打开网站 `/catalog`，商城实际路由来自 `Portal.tsx`。
- 仪表盘原先调用 `testNodes`，因此一个按钮触发整个有效目录。新增 `testNode` 和匹配取消命令，通过本机草稿身份校验当前节点，复用原探测执行器，只提交一个工作项。节点弹窗保留并明确标为“全部测速”。
- 单节点结果更新保留其他目录结果；本机选择、连接状态、测速任务各自独立。旧任务取消与结果代次检查共用现有生命周期；没有新增轮询、账户 API 或测速服务。
- 卡片只在 ID 列表变化时重建；其他数据原位更新。商城导航不多发状态读取。执行器工作线程数限制为 `min(3, 实际任务数)`，单节点只用一个探测工作线程。

## 验证

- 原生 race 回归覆盖 `internal/client`、`internal/clientbridge`、`internal/rules`。新增测试证明：单次探测、缓存结果保留、0 账户 HTTP、0 内核调用、重复点击合并、选择改变不重定向、取消范围隔离、撤销/过期/端点改变/退出取消，以及旧目录和已连接状态兼容。
- 原 Wails Go 测试与前端生产构建、Windows 适配器测试通过。
- Godot 离线测试：原版仪表盘几何、按钮状态尺寸、隐藏页不重绘、选择确认无假连接、单节点命令归属，以及 320/400/600/740/850/1040/1440 像素无横向溢出。单份、多份、空订阅、商城入口均有截图。
- 原生用例只使用回环服务和注入探测器；UI `preview=true` 不创建 bridge。未部署、未替换正在运行的客户端，也未进行新的大陆网络或 ARM64 真机 VPN 验收。

## 影响分析与范围

修改前运行 GitNexus impact。共享命令分发、测速范围/结果类型的图风险为 CRITICAL，已报告；部分图边包含泛化动态分派关联，不能当作精准的运行时调用链。GDScript 文件返回 UNKNOWN，另行核对主场景 preload、仪表盘复用和 UI 测试引用，并执行实际离线场景回归。

`main.gd` 为 320 行，沿用 UI shell 的 301–400 行例外；其余本次应用代码均低于 300 行。旧 preview.9 的 DNS/连接修复与验收记录保持历史含义。

改后索引已更新（16,075 节点、65,447 边）。`detect-changes --scope all --limit 1000` 报告 44 个已跟踪变更文件、48 个符号、574 个流程，整体风险 CRITICAL；包含既有并行工作，不能归因于本次变更。索引仍存在流程预算截断及全文检索不可用提示，未跟踪文件也不能仅凭差异映射获得完整保证；不宣称全仓图检查干净。本次未提交。

## 本地产物核验

三个包版本均为 preview.10、序号 5000010。Windows 从导出的 PCK 执行订阅/测速、交互与资源检查通过；全新空状态目录运行时返回 preview.10、未登录，前后代理设置摘要一致。Android APK 元数据的 ABI 分别为 arm64-v8a、x86_64，v2 签名验证通过；更新清单 Ed25519 签名、大小与 SHA-256 匹配。测试资源未进入包，字体/图标许可存在。构建包装器因 stderr 警告返回非零，但两次 Gradle 均 BUILD SUCCESSFUL、构建脚本完成且上述独立核验通过。

| 文件 | 字节 | SHA-256 |
| --- | ---: | --- |
| HQIP-Godot-Windows-x64-Portable.zip | 81389451 | `ea957c8f9c7af97ad1e8092637cc8d14bb8e3dfa71dd620b335e6281e916b5f6` |
| HQIP-Android-arm64.apk | 148703975 | `c8de7b5d29c20b133614330000efb79f62bf50e79d4cbeb5c72e29838a802e0f` |
| HQIP-Android-x64.apk | 155565918 | `40c57691aad9692218ea6b80e2da61305ae5d42c3db3729a47e95eaeef7dbcb2` |

证据保存在库外 `.diagnostics/shop-artifact-validation.json`、`shop-package-resources.json`、`shop-runtime-validation.json` 及 `shop-*.log`；截图为 `subscriptions-single.png`、`subscriptions-400.png`、`subscriptions-1040.png`、`subscriptions-1440.png`、`subscriptions-empty.png`。
