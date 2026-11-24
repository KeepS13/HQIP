# 原客户端样式分析与 Godot 复刻

日期：2026-11-24。原版依据为本工作区 `desktop/frontend/src` 的实际 React/CSS，复刻目标为 `godot-client/ui`。本次是客户端表现层调整，账户、计费、VPN、更新签名和 API 业务规则继续由既有 Go 控制器拥有。

## 证据与方法

`scripts/capture_client_reference.cjs` 打包原版组件，在 Edge 中注入内存 Wails bridge；所有请求本地拦截，不访问服务器。`godot-client/tests/visual_parity.gd` 使用同一份 `tests/visual-fixture.json`，设置 `preview=true`，不创建 NativeBridge。统一为 1040×820、100% DPI、America/Phoenix。原版代码没有为了比较而修改样式。

原版七种画面、新版七种对应画面、连接状态及手机画面保存在 `.diagnostics/style-replica/`；`comparison.html` 是可直接打开的离线叠加对照页，包含截图，不依赖外网。`reference-bounds.json` 来自浏览器实际矩形，`new/bounds.json` 来自 Godot 实际控件矩形。比较数据是虚构夹具，不代表真实订阅或网络实测。

## 原版视觉规范

| 层级 | 原版实际规则 | Godot 对应实现 |
| --- | --- | --- |
| 背景/侧栏/卡片 | `#0c111a` / `#101722` / `#131c28` | `style.gd` 集中颜色和控件原语 |
| 品牌/选中 | 紫色 `#b5a9fa`，导航底色 `#26283f` | 原 H SVG、连接状态 SVG、原 Lucide SVG |
| 窗口 | 34px 顶栏；侧栏 206px，850px 以下 175px | `window_chrome.gd`、`main.gd` |
| 主区 | 左右 35px、上 43px；标题区 63px、下间距 28px | 与浏览器内容区相同的逻辑像素布局 |
| 字体 | Segoe UI；中文 Microsoft YaHei UI；正文多为 10–12px | 明确的拉丁/中文字体回退和 400/500/600/700 字重；Android 随包 Noto CJK |
| 卡片 | 1px 边框、12px 圆角；登录/弹窗 16px | 共享 StyleBox；状态渐变用局部 shader |
| 节点选择 | 图标、名称、订阅编号、延迟、就绪状态分别排版 | `dashboard.gd`，保留订阅与节点联合选择 |
| 连接卡片 | 状态点/标题/说明，右侧 106×44 开关；下方模式组与 TUN | `connection_card.gd`、`toggle.gd` |
| 流量 | 53px 曲线，三列两行指标，竖分隔线，独立时长/延迟行 | `traffic_panel.gd`、`traffic_chart.gd` |
| 订阅 | 默认折叠；有效/当前选择徽标；详情按 1.2:1:0.7 显示流量、带宽、在线 | `subscriptions.gd`、`usage.gd`，5px 使用量进度条 |
| 设置 | 右对齐值、横分隔、紧凑模式组、自绘开关、独立更新区 | `settings.gd` |
| 弹窗 | 820px 最大宽、三列节点、搜索、状态徽标、背景模糊 | `node_picker.gd`；手机降为两列或一列 |
| 侧栏底部 | 50px 迷你曲线、上下行/内存独立行、内核版本、帮助 | `navigation.gd`，空/过期样本不绘制实时数据 |

之前的新客户端主要差异是统一使用大按钮/大字号、标题多出菜单占位、卡片字段合并成文字、图标及徽标缺失、弹窗固定留白过大。本轮按原版重新拆分这些展示元素，同时保留原有业务命令。

## 同尺寸实测

以下为 x/y/宽/高，单位为逻辑像素。原版小数来自 CSS 行高，Godot 容器落在整数像素。

| 元素 | 原版 | 新版 |
| --- | --- | --- |
| 标题区 | 241 / 43 / 756 / 63 | 241 / 43 / 756 / 63 |
| 节点卡片 | 241 / 134 / 756 / 175 | 241 / 134 / 756 / 175 |
| 连接卡片 | 241 / 329 / 756 / 174.59 | 241 / 329 / 756 / 175 |
| 流量卡片 | 241 / 523.59 / 756 / 288.88 | 241 / 524 / 756 / 289 |
| 订阅摘要 | 241 / 830.47 / 756 / 211.78 | 241 / 831 / 756 / 213 |
| 登录卡片 | 329 / 150 / 580 / 422.38 | 329 / 150 / 580 / 422 |
| 节点弹窗 | 106 / 252.20 / 820 / 331.59 | 106 / 252 / 820 / 332 |

这是主要区域的几何验收，不是整张截图的像素一致率。原版 WebView 使用的字体栅格化与 Godot 不同，部分文字基线、字形边缘、控件焦点环和模糊核仍有差异。不能据此声称全平台每个像素完全相同。

## 交互与运行约束

- 保留节点搜索、对应订阅选择、延迟测试、连接/取消/断开、路由与 TUN、刷新、浏览器登录、服务地址编辑、开机自启、更新及退出账户命令。断开操作在连接/恢复期间可用。
- 搜索和服务地址不被轮询覆盖；订阅详情展开状态独立保存。模态弹窗有 Tab 焦点循环、Escape/遮罩关闭、关闭后焦点恢复。
- Windows 桌面复用原侧栏外观；点击品牌可收起，再由标题菜单展开。低于 740 逻辑像素使用原有抽屉。手机订阅状态另起一行，保持可见。
- JSON 数字在 Godot 中可能是浮点数，人数、带宽、毫秒、版本计数显式按整数显示。无样本时保持 `—`，不把初始零计数伪装为测量结果。
- Android 的系统 VPN 和凭据保护说明、运行时真实版本、每六小时更新周期按实际实现显示，不复制原 Windows 的不适用文案。
- 模糊仅在弹窗显示时启用；曲线仅绘制运行时提供的数据。没有新增网络请求、数据库字段、后台采样或计费逻辑。资源加载使用 Godot 缓存。

## 验证与复现

```powershell
# Playwright 从本机已有工具依赖解析；不需要修改项目依赖。
$env:NODE_PATH='C:/Users/Administrator/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules'
node scripts/capture_client_reference.cjs
godot --path godot-client --script res://tests/visual_parity.gd --quit-after 900
godot --headless --path godot-client --script res://tests/ui_smoke.gd --quit-after 240
npm --prefix desktop/frontend run build
```

两个 Godot 脚本均通过；包括首次打开弹窗高度、原版坐标断言、节点命令参数、搜索/地址保持、订阅展开保持、四种连接状态、320/400/600/740/850/1040/1440 宽度、既有 150% DPI 和 nil 数据回归。Godot 断言失败有时仍返回进程码 0，验收同时要求日志出现 PASS/通过标志且没有 SCRIPT ERROR、Parse Error 或 Assertion failed。

原版前端构建通过。当时 `scripts/test_client_recovery_ui.cjs` 在完成桌面断言后引用已删除的管理端设备页面，整条命令未通过。2026-11-24 仓库清理移除了这段失效断言，保留仍在交付的桌面恢复、恢复成功和首次失败验证；详见 [仓库清理记录](../reviews/2026-11-24-repository-cleanup.md)。

GitNexus 的 GDScript 文件影响结果为 UNKNOWN，不能解释成无调用。已补充核查 main→页面/导航/弹窗→style/usage/chart 的 preload 关系及 tests/main.tscn 入口。索引最初遇到 2GiB 缓冲池不足，使用进程内 4GiB 配置后完成；后续 FTS 报 Invalid UTF-8，概念全文检索降级。全工作区变更检测为 CRITICAL，包含大量本轮之前的后端变更，且有执行流预算限制；Godot 目录目前未跟踪，不能将 git diff 的 35 文件/60 符号计数当作本轮 UI 的完整统计。不能称为全仓库无风险检查。未提交或部署。

文件预算例外：`ui/main.gd` 约 310 行，保留 shell 构建、DPI/抽屉布局、NativeBridge 生命周期和状态分发在同一入口，便于核对初始化次序；具体连接/流量/订阅展示已拆分，其他 UI 文件均低于 300 行。此例外不扩大业务所有权。

## 本地交付

最终本地预览版本为 `5.0.0-preview.6`、序号 `5000006`。Windows、Android 构建使用已有签名身份，测试与假数据目录不进入安装包。包清单与 SHA-256 见 `dist/godot-build.json`。本轮只验证样式和离线交互，不等同于新版本真实 VPN、Android ARM64 真机或大陆网络验收。

Windows 导出 PCK 已在空目录中重新加载并渲染节点弹窗，断言其布局和无 NativeBridge，通过；ZIP 内的 UI、PCK、运行时文件与已校验的产物逐字节摘要一致。Android ARM64/x64 均构建成功并通过 APK 签名校验。三个分发包的实际尺寸和 SHA-256 与清单一致；两份 APK 均包含字体/图标许可且不含 tests 夹具。记录在 `.diagnostics/style-artifact-validation.json`、`style-package-resources.json` 和 `style-export-audit.log`。

## 后续修复（2026-11-24）

本页记录 preview.6 样式基线；preview.9 的套餐展示、统一交互状态、选择与连接解耦、DNS 分流及性能回归见 [专项记录](../incidents/2026-11-24-client-interaction-routing.md)。当前 main.gd 为 318 行，沿用上述不超过 400 行的 shell 例外。

preview.10 按用户新要求将订阅页改为网站式纯信息卡片与商城入口，仪表盘测速仅处理当前节点；这是有意更新的产品行为，参见 [订阅与测速记录](../incidents/2026-11-24-subscriptions-selected-probe.md)。其余原版仪表盘几何与交互状态回归继续通过。main.gd 当前 320 行，沿用 shell 例外。

preview.11 保留上述几何；未连接选择改为点击内同步展示整组节点/套餐信息、合并连续点击，并删除自动测速，见 [即时切换记录](../incidents/2026-11-24-instant-local-selection.md)。main.gd 为 349 行，继续采用 shell 例外。
