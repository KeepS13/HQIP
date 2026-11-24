# Godot 客户端重做

状态（2026-11-24）：当前构建版本 `5.0.0-preview.16`，序号 `5000016`，目标为 Windows x64、Android arm64/x64。尚未替换线上 Wails 安装包，也未部署新增服务端路由与发布清单。iOS/macOS 尚未发布。

preview.16 将 Windows 首次交付改为单个 EXE 安装包，并让 Windows/Android 在未登录时也能后台检查更新。安装、修复、卸载和验证边界见 [安装与自动更新](client-installation.md)。

preview.15 修复节点卡片右下角被透明占位控件挡住的点击死区；整张卡片统一响应选择，禁用节点仍不可选。真实鼠标事件回归及原因见 [节点卡片点击死区](../incidents/2026-11-24-node-card-hit-area.md)。

preview.14 修复编辑器误加载残留 `4.8.1-dev` 运行时，统一使用构建输出 `versions/bundled/hqip-runtime.exe`；快照按原生采集序号更新，断开后的迟到回执不再恢复旧连接或测速显示。断开撤销连接意图后立即拒绝新的延迟请求。原因、复现与验证见 [空闲测速及状态回跳](../incidents/2026-11-24-idle-probe-oscillation.md)。

preview.13 按最新要求改为：已连接时选择新节点自动断开旧连接并连接新节点，界面仍立即响应；未连接时仅保存选择。每次连接成功后自动检测一次当前连接延迟，其余测速手动触发。保留 preview.12 的完整目录导入、取消空闲轮询、业务变更通知和请求合并。验证与限制见 [自动切换与连接延迟](../incidents/2026-11-24-auto-switch-delay.md)。

原版样式测量与对照见 [原客户端样式分析与复刻](client-visual-replica.md)。本轮修复仪表盘套餐缺失、按钮交互尺寸变化、规则模式本地 DNS 污染及选择/连接耦合，验证边界见 [交互与规则模式修复](../incidents/2026-11-24-client-interaction-routing.md)。本次追加订阅纯信息卡片、商城入口与当前节点测速，见 [订阅页与测速记录](../incidents/2026-11-24-subscriptions-selected-probe.md)。本次改为同次点击同步显示完整本机选择、合并连续选择并取消自动测速，见 [即时切换记录](../incidents/2026-11-24-instant-local-selection.md)。历史 VPN 验收仍以对应版本记录为准。

## 模块映射

| 能力 | 可移植实现 | 平台适配 |
| --- | --- | --- |
| 账户、订阅、连接生命周期 | internal/client（既有） | 浏览器打开、凭据保护 |
| 客户端命令 | internal/clientbridge | Windows 私有管道；Android JNI |
| 界面 | godot-client | Godot Windows/Android 导出 |
| 代理内核 | 固定 sing-box 1.14.4 | Windows 进程；Android VpnService / 嵌入内核 |
| 内容同步 | 既有 profile/rules v3 | HTTPS 物理直连 |
| 应用更新 | 校验后暂存完整包 | Windows 退出切换；Android 系统安装确认 |

Go 控制器是业务规则唯一来源；原 Wails 客户端保留作为迁移期间的可用版本。GDScript 不读取 session.dat，不缓存账户密码、订阅凭据或内核配置。界面复用原版紫色 H SVG、连接状态 SVG、深色背景、侧栏、网络仪表盘、我的订阅和连接设置。桌面与手机共用同一个左侧导航：宽窗口显示可收起侧栏，低于 740 逻辑像素时由左侧菜单图标打开抽屉；内容顺序不变，只调整列数、间距和弹窗尺寸。布局使用 DPI 换算后的窗口尺寸，不能把固定画布整体缩小当成响应式。

节点选择、连接、路由模式、流量图表和订阅摘要保持原版顺序。版本号取运行时构建信息，实际节点取实际连接 ID；未知版本、缺失统计显示 `—`。命令等待状态禁止重复操作；后台同步恢复后清除过期错误。截图和假数据仅用于不创建原生运行时的离线布局检查，`tests/*` 不进入安装包。

## 安装与构建

首次安装使用 `dist/HQIP-Windows-x64-Setup.exe`，安装后从桌面或开始菜单启动 HQIP，无需手动解压。默认程序目录为 `%LOCALAPPDATA%/Programs/HQIP`，账户保存在 `%LOCALAPPDATA%/HQIP/Godot`，与原 Wails 目录隔离，首次需要重新登录；已有 Godot 便携版沿用账户目录。关闭窗口后留在系统托盘；托盘“退出并恢复网络”完全退出。开机自启使用独立的当前用户启动项，指向稳定启动器。完整安装行为见 [安装与自动更新](client-installation.md)。

Godot 编辑器 Play 使用 `dist/godot-windows/versions/bundled/hqip-runtime.exe`，与构建脚本输出一致；不回退到旧的平铺目录。源码修改后需要重建原生运行时并重新运行场景，界面设置页版本应与 `dist/godot-build.json` 一致。`--runtime=` 仅用于显式开发覆盖；自动检查 `tests/runtime_binding.gd` 使用相同路径解析及真实管道，以独立空会话验证版本和快照编号，不读取用户账户。

Android 手机使用 `dist/HQIP-Android-arm64.apk`；x86_64 模拟器使用 `dist/HQIP-Android-x64.apk`。面向 Android 8.0/API 26 及以上设备，compile/target SDK 36。它使用持久本地发布签名，不是 debug 签名。启动后后台下载新版 APK，不要求登录，再由系统确认安装；普通应用不能静默替换自身。后台 VPN 由前台服务维持。

```powershell
python scripts/build_godot.py --platform all --version 5.0.0-preview.16 --sequence 5000016
# x86_64 模拟器单独构建，脚本保留同版本其他已校验产物的摘要
python scripts/build_godot.py --platform android --android-arch amd64 --version 5.0.0-preview.16 --sequence 5000016
# 也可指定 windows 或 android
```

构建依赖：Go 1.26.10、Godot 4.7.2 及导出模板、Inno Setup 6、JDK 21、Android SDK/NDK、gomobile、Gradle。已缓存工具位于 `.local/godot-tools`。脚本复核固定 sing-box 哈希，注入版本/序号/更新公钥，生成 Windows EXE 安装包及内部更新 ZIP、Android 签名 APK 和 `dist/godot-build.json` 摘要；不会上传或部署。版本与序号现在必须显式传入。

版本更新时传入更高 `--version` 和 `--sequence`。`.local/godot-signing/ed25519.seed`、`android.jks` 与 `android.json` 是本地私密签名身份，必须私密备份并在后续构建沿用，不提交仓库、不放入安装包。当前公钥为 `611cdf0e186c76eb55e6fefce30c6c9af5bdfe6e34bbc034086d2c7ecad2ba85`。

## 更新上线材料

`dist/HQIP-Godot-Windows-x64.zip` 只用于更新，不能当作首次安装包。`dist/godot-channel` 包含签名清单及按摘要存放的三个平台更新包（Windows x64、Android arm64/x64）。部署时先将完整包复制到控制端现有 `--artifact-dir` 的 `godot/` 子目录，再原子替换 `godot-releases.json`；保证控制端服务账号有读取权限，禁止同时运行多个发布写入者。控制端必须先部署本次新增路由的构建：

- `GET /api/v3/client/releases`：签名发布清单。
- `GET /downloads/godot/{sha256}/{filename}`：校验后的包与 Range 下载。
- `GET /api/v3/client/changes`：认证业务变更通知，需允许持续 NDJSON 响应和刷新转发缓冲。

用户已要求沿用现有域名与服务器配置，因此继续经过当前 Cloudflare 转发，不修改源站的来源限制。账户、规则、更新 HTTP 请求在客户端禁用代理，Windows 绑定物理出口，Android 使用非 VPN Network。大陆链路是否稳定仍需当地网络验收，本轮没有此结果。

Windows 自动下载完整新版本到 `versions/<sha256>`，下次启动由稳定启动器切换；启动失败、就绪超时或异常退出回退。更新失败保留当前版本，更新接口尚未上线时不影响账户和连接。节点与规则走既有 API 动态同步。应用普通版本差异不强制登出；旧 v3 客户端可继续消费现有接口。已经下线的旧域名、早于 v3 的单套餐客户端不在自动兼容承诺内。

## 验收要求

需要覆盖首次浏览器登录、重启恢复、不同客户端版本同用 API v3、规则新增字段、断网及恢复、显式会话撤销、订阅/节点切换、取消登录、更新下载中断、错误摘要、拒绝降级，以及旧客户端回归。

Windows 仍需真实验收系统代理与 TUN、切换 Wi-Fi、恢复原网络设置。Android x64 已在 API 34 模拟器验证真实账户/VLESS/跨应用流量、前后台、Wi-Fi 中断恢复和 APK 系统确认覆盖更新；ARM64 真机、息屏、Activity 重建、系统撤销仍需补验。必须在真实大陆网络验收更新端点；开发机能访问不代表大陆可达。

用户后续授权本机/虚拟环境隔离测试，并授权在 HQIP 创建专用账号及开通订阅。已创建七日体验订阅，测试账户密码与浏览器会话仅保存于 `.local/godot-emulator-check/` 私密文件；不放入安装包或版本库。本机原代理、默认路由、DNS 与既有内核进程保持不变。真实 VPN 限定模拟器；Windows 内核通过不接管系统网络的本地回环夹具验证。

安卓验收发现并修复原生初始化阻塞首屏、桌面 mixed TCP 栈不适合 VpnService、测速未使用 Android 物理网络，以及系统字体缺字。嵌入内核改为 gvisor/MTU 1500，图标改 SVG，随包提供完整 Noto CJK 字体及 OFL 许可。x86_64 模拟器实测 VLESS 出口正常；HY2 的 QUIC 建连仍超时，尚不能作为该链路验收通过。ARM64 release 在此 x86_64 模拟器的 ARM 转译层加载受阻，不能外推为 ARM64 真机通过或失败。

`godot-client/tests/ui_smoke.gd` 不创建原生 bridge，覆盖 nil 状态、节点搜索保持、抽屉打开/选择/遮罩/Escape、320–1040 像素窗口和 150% DPI、真实版本绑定及过期错误清除。签名下载/失败/降级与 Windows 切换回退有隔离自动化回归。安卓实际覆盖更新使用独立 instrumentation 夹具将已校验最终 APK 交给正式 FileProvider 和安装器；不等于线上下载渠道验收。前版 Windows 首屏通过，浏览器登录因工具 URL 自动审批中止，未继续该交互；托盘退出仍需补验。前版结果见 [preview.3 验证记录](../incidents/godot-client-preview-validation.md)，本轮见 [preview.4 一致性与稳定性验证](../incidents/2026-11-23-client-consistency.md)。
