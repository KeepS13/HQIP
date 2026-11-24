# preview.4 客户端一致性与稳定性验证

日期：2026-11-23。版本：`5.0.0-preview.4`，序号 `5000004`。本地构建，不上传、不部署、不替换线上客户端。preview.3 的历史验收保留在 `godot-client-preview-validation.md`，不能用前版结果替代本版实测。

## 隔离约束

用户要求不能影响当前代理。宿主机不启动新客户端 VPN、不修改系统代理、TUN、路由或 DNS。真实 VPN 测试限定 Android 模拟器 `emulator-5554`；Windows 真内核使用 fakePlatform 和回环端口，UI 夹具不创建 NativeBridge。

10:16（America/Phoenix）只读核对：代理仍为 `127.0.0.1:10808`，默认路由以太网 ifIndex 4 经 `192.168.10.1`，DNS `192.168.10.1`，既有 sing-box PID 37500 保留，没有新客户端测试进程。证据 `.diagnostics/preview4-host-baseline.json`。原中断启动未发现遗留进程；不能把只读检查称为宿主机 TUN 验收。

## 本轮修复

- 共用桌面/手机左侧菜单与页面树；低于 740 逻辑像素时使用抽屉，保留原 Windows 深色、品牌、连接状态图标、页面和控件顺序。
- 发现固定 Godot 画布会缩小整页、阻止真实窗口触发布局断点。已先复现失败，再改为 DPI 逻辑像素布局；测试仅改变实际窗口尺寸，不同步修改设计画布来掩盖问题。
- 显示运行时版本、核心版本、实际节点、真实统计；未知数据用 `—`。测试夹具不进入导出包。构建将同一版本/序号注入 APK 与 Go，Gradle 不再静默使用旧版本默认值。
- Connect、相同 mode/TUN 命令幂等；真实模式改变保存失败时保留现有连接。加载、重复点击和过期错误状态统一处理。
- Windows TUN 管理进程规则及暂停清理补上新 companion；规则/全局模式共用。
- Android 仅实际物理网络指纹改变时重置连接，重复/无关网络通知不再反复重置；返回键在根页面退到后台。
- 真实 Android 界面暴露出 RFC3339 时区被忽略、连接时长固定为零的问题：统一处理 `Z`、正负 UTC 偏移和纳秒后缀，订阅到期判断复用该解析。累计统计在首次有效样本之前显示 `—`，过期样本保留最后有效值。窄屏收起顶部过长账户名。

## 自动化验证

| 范围 | 结果 |
| --- | --- |
| 共享控制器 | client/clientbridge/winclient/rules 回归通过；client/clientbridge/winclient/clientrelease race 通过；旧 desktop 测试通过 |
| 连接稳定性 | 已连接、恢复中、建立中重复命令不重启；真实模式切换仍应用；设置保存失败不改变模式/代次/恢复权限 |
| Android 网络事件 | 重复、无关接口、地址顺序、无效快照、离线/恢复、新 network handle 单元测试通过 |
| 规则配置 | 规则/全局 × 系统代理/TUN 四组由打包的 sing-box `check` 验证；管理绕过顺序与暂停边界测试通过 |
| 真内核回环 | 真实本地 HY2、统计、2 秒/70 秒丢包后同 PID 恢复、授权暂停和证书失败处理通过；日志 `.diagnostics/preview4-isolated-core.log` |
| 离线 UI | 320/360/400/600/740/1040 宽度、150% DPI、抽屉交互、搜索保留、nil 状态、版本数据与错误清除通过；`.diagnostics/preview4-resize-before.log` 记录修复前的失败，`preview4-resize-after.log` 记录通过 |

上述四组配置检查证明生成配置可被内核接受，不等同于宿主机真实系统代理/TUN 流量验证。真实 Windows 系统网络测试必须在独立 Windows 环境补验。

## 本版 Android 实测

测试设备为 emulator-5554（Android 14/API 34、x86_64）。正式签名包覆盖安装保留原测试账户、订阅及 VLESS 节点；运行时和抽屉页脚均为 `5.0.0-preview.4`，核心 `1.14.4`。`preview4-android-menu.png`、`preview4-android-back.png` 为真实界面，非假数据夹具。

| 项目 | 观察结果 |
| --- | --- |
| 规则 + TUN | 独立 UID 的 curl 请求：境内探测站出口 `27.18.11.28`，境外探测站出口 `38.65.93.92`，与直连/选定节点相符 |
| 全局 + TUN | 同两站均为节点出口 `38.65.93.92`，确认流量路径随模式改变 |
| 重复操作 | 20 次相同网络通知、重复 Connect 和相同模式设置后，连接起始时间不变，状态仍 connected |
| 前后台 | instrumentation 与正式界面连接均观察到后台仍有跨应用流量；根页面返回键不退出 VPN 进程 |
| 抽屉 | 正式界面左上角菜单拉开同一侧栏，页脚显示实际版本，返回键关闭抽屉 |
| 统计/时长 | 正式包真实展示累计上传 4.87 KB、下载 17.83 KB、内存 62.02 MB；连接时长由 19 秒继续递增至 8 分钟以上。尚未获得有效计费样本时显示未知，不用零填充 |
| 独立断网复测 | 正式包经模拟器内 Wi-Fi 关闭/恢复后，原 PID 8198 未重启，curl 再次返回原节点出口；证据 `preview4-android-recovered.png` 及执行记录。未精确测量恢复耗时，不能外推为固定秒数恢复 |

失败记录必须保留：最初 instrumentation Wi-Fi 恢复后 90 秒请求未成功，日志 `preview4-emulator-recovery.log` / `preview4-emulator-events.log`；该过程也中断 LDPlayer 的 ADB 通道。随后绕开 instrumentation 的正式界面复测恢复成功，因此前次失败原因未完全区分，不能称作无条件稳定。临时诊断 Go overlay 只在 `.local/preview4-emulator` 构建，未安装到模拟器、未替换交付包；正式包已观察到统计恢复，不据此改动计费口径。

## 尚需实测

ARM64 真机、Windows 托盘交互/真实系统代理/TUN、安卓系统撤销/息屏/Activity 重建及大陆运营商网络仍未完整验收。此前线上 HY2 的 QUIC 超时未确定根因，不能宣称本轮解决。线上更新渠道尚未部署。

## 构建与证据

完整包为 `dist/HQIP-Godot-Windows-x64-Portable.zip`、`dist/HQIP-Android-arm64.apk`、`dist/HQIP-Android-x64.apk`。版本、尺寸和 SHA-256 以本轮 `dist/godot-build.json` 为准，避免引用前版摘要。构建日志 `preview4-build.log`、`preview4-x64-build.log`。专用测试账号、签名私钥和 instrumentation 夹具均仅在 `.local`，不进入客户端或版本库。

最终三个包 SHA-256、ZIP CRC、测试/私密文件排除检查通过；APK 均为非 debuggable release，v2 签名有效、ABI 与文件名一致，版本号/序号一致。证据 `.diagnostics/preview4-artifact-validation.json`。Windows 压缩包包含完整稳定启动器与版本目录；不要只复制 UI 可执行文件。

共享 Connect/SetMode、GenerateConfig 和管理连接判断修改前 impact 为 HIGH，已告知影响旧桌面/托盘、新 bridge 与 Android。Godot/JNI 的 UNKNOWN 用主场景、信号及跨语言调用的文本引用补查，不能当成零风险。保留用户此前工作区修改，不提交或推送。

图检查用临时 Git 索引纳入新增源码，真实暂存区 SHA-256 保持不变。工作区全部变更包含前序修改，检测为 CRITICAL；流程图仍受入口/分支预算和跨语言解析限制。完整列表使用 `--limit 2000` 保存；不将图结果描述为无遗漏的“干净检查”。

## 最终清理和网络对照

11:10（America/Phoenix）最终只读检查与本轮开始一致：系统代理、默认路由、DNS 均未改变，既有 sing-box PID 37500 和启动时间一致，无本轮 Windows 客户端进程。记录 `.diagnostics/preview4-host-final.json`。模拟器通过正式界面断开后确认 tun0 消失，再停止本测试客户端；独立 instrumentation 包已卸载，无 adb forward。账号与订阅保留。

从模拟器拉取已安装的 base.apk，SHA-256 为 `57ca3d7c2fd2a2abf7c612ff60c58daa402ac80ff5845bb2208a2a1f5aafadea`，与最终 x64 交付包完全一致。正式包全局模式再次验证境内、境外请求均走节点，根页面返回后台后仍能请求成功、PID 8198 未变。证据 `preview4-android-final-connected.png`、`preview4-manual-acceptance.json`。宿主机系统代理/TUN 和 ARM64 真机仍不在已通过范围。
