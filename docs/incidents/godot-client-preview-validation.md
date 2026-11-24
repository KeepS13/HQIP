# Godot 客户端预览版验证记录

日期：2026-11-23。最终本地版本 `5.0.0-preview.3`，sequence `5000003`。
范围：Windows x64、Android arm64/x64。尚未生产部署；iOS/macOS 未构建。

## 授权与隔离

用户后续明确允许本机/虚拟环境测试，但不得影响当前网络，并授权自行注册真实测试账号及开通订阅。此授权替代此前“禁止本机连接测试”的限制。

HQIP 专用账号 `hqip-godot-acceptance-20261123@example.invalid`，订阅 `原生IP周体验`，配额 50 GB、节点 vus1、两并发，到期 `2026-11-30T15:20:19Z`。通过现有 Store 注册与赠送业务方法开通，未关闭验证码/认证，未修改其他账号。测试浏览器会话通过现有授权端点确认，客户端完成真实 PKCE 交换。口令和会话不出现在本文、安装包或 Git 中。

宿主机代理基线为 `127.0.0.1:10808`，默认路由以太网 ifIndex 4 经 `192.168.10.1`，DNS 同为 `192.168.10.1`；既有 sing-box PID 37500 保留。真实系统 VPN 仅在 emulator-5554（Android 14/API 34、x86_64、ARM 转译）启动。Windows 真内核恢复夹具使用 fakePlatform 和回环端口，不修改系统代理或 TUN。

## 本轮修复

- Android 原生 Go 初始化移出 Godot 渲染线程，避免卡住首屏。
- Android TUN 改为 gvisor/MTU 1500。此前 mixed 栈核心出口探针成功，但其他应用 TCP 卡在 sniff；修复后浏览器验证实际节点出口。
- Android 节点测速补上物理网络 DNS 与受保护 TCP/UDP socket；VLESS 测速成功。
- 更新平台从固定 arm64 改为实际 GOARCH，发布工具支持 x64，拒绝交叉 ABI 更新。
- 使用 SVG 电源图标与完整 Noto CJK 字体，修复 Android 电源符号/全角标点缺字；许可证随包分发。
- 连续构建不同 ABI 时保留同版本、摘要匹配的其他产物记录。

## 验收结果与边界

| 项目 | 结果 |
| --- | --- |
| 真实账户 | 模拟器浏览器授权、PKCE 登录、订阅与两个协议节点同步成功；重启/覆盖更新保留账号与选择 |
| 真实 VLESS | diagnostic 与签名 release x64 均显示连接；跨应用浏览器 API 返回出口 `38.65.93.92`，前后台流量成功 |
| 网络恢复 | 模拟器 WLAN 中断约四分钟后恢复，无需重新登录/手动重连；新 JSON 请求返回同一节点出口。WLAN 关闭同时中断 LDPlayer ADB 通道，恢复 WLAN 后重新连接调试通道；未重启客户端 |
| HY2 线上 | 域名解析成功，但 QUIC 建连超时；未证明是运营商、模拟器或服务器原因，不算验收通过。独立宿主机回环代理对照同样未通过，未改生产节点配置 |
| 本地真内核 | `TestPinnedCoreRecoveryIntegration`、`TestPinnedCoreProxyStatistics` 通过：真实 HY2 本地转发、统计、短/70 秒丢包后同 PID 恢复、授权暂停及证书失败终止恢复；日志 `.diagnostics/godot-isolated-core.log` |
| APK 覆盖更新 | preview.2 → preview.3 经正式 NativeHost/FileProvider 调起系统“更新”并确认，安装后 versionCode 5000003、账号/订阅/选择保留；最终包重新建立 TUN。独立 instrumentation 仅注入已校验安装夹具，不进入 release APK |
| 更新下载 | TLS 隔离夹具通过签名、摘要、缓存、坏包、降级、架构区分与缺失更新渠道测试；线上清单/下载服务未部署，不能称线上自动更新已验收 |
| Windows | 最终完整目录包在独立 LOCALAPPDATA 启动，原版深色登录首屏正常；浏览器授权验收被 Computer Use URL 自动审批中止，未绕过；托盘交互与宿主机系统代理/TUN未验收 |
| ARM64 | release 编译/签名/ABI 检查通过；x86_64 模拟器 ARM 转译层原生加载受阻，无 ARM64 真机验收结论 |
| Go 回归 | client/clientrelease/clientbridge/control/winclient/launcher/release CLI 回归通过；新增嵌入栈清理、ABI 更新测试通过 |
| 静态/并发 | 相关包 vet 通过；client/clientbridge/clientrelease/winclient/launcher 最终 race 通过。控制端前序 race 通过，本轮无新增控制端行为修改 |
| Godot UI | headless 离线检查通过；最终 Android 字体与图标、Windows 首屏实际观察通过；前序离线 OpenGL 截图保留 |

普通版本差异不作为 API v3 准入条件；GET 有限重试、网络错误不等于会话失效、未知可选字段兼容、更新失败不影响既有功能已有回归。旧安装首次迁移到新客户端仍需安装一次；未升级的旧程序本身的缺陷不会被本地新包自动修复。已下线域名与早于 v3 的旧账户模型不在兼容承诺内。

## 产物

| 文件 | 字节数 | SHA-256 |
| --- | ---: | --- |
| `dist/HQIP-Godot-Windows-x64-Portable.zip` | 81329515 | `8a6c9a8124c766bc38273c6a0b34d130369b4523258f0b58fe1392c435fe1695` |
| `dist/HQIP-Android-arm64.apk` | 148933033 | `ee535092df35be07d9353d3fd29fa9d2a62d7b67e0bba9208cabd538072748f5` |
| `dist/HQIP-Android-x64.apk` | 155835520 | `fb59375d3cd08c42ee4b27978c2d830cdd5353ab669eda33d7ba1f59a57af545` |

完整摘要见 `dist/godot-build.json`。Windows 更新载荷为另一个 `dist/HQIP-Godot-Windows-x64.zip`；首次使用应下载 Portable ZIP 并完整解压运行 HQIP.exe。`dist/godot-channel` 保留各平台最近两版的签名清单与完整包，尚未上传。构建日志 `.diagnostics/godot-preview3-build.log`、`godot-preview3-x64-build.log`。最终生产构建未使用 Go 诊断 overlay。

## 未完成与上线边界

必须补验真实 ARM64 手机、安卓系统撤销/息屏、Windows 托盘和真实系统代理/TUN，以及 HY2 线上问题。现有域名及 Cloudflare 配置沿用；客户端绕过代理不代表大陆运营商链路已经通过，仍需大陆实测。控制端更新路由、签名清单和下载包尚未部署，本轮没有替换任何线上客户端。

修改前公共直连传输 impact 为 CRITICAL，发布文件名映射为 HIGH，均已说明风险；Godot/JNI UNKNOWN 用文本调用边界补查。最终图检查日志为 `.diagnostics/godot-preview3-graph-*`，detect-changes 统计 92 文件、415 符号、33 条已索引流程，风险 CRITICAL。使用临时 Git 索引纳入新增源码，真实暂存区摘要未变。工作区仍包含用户先前修改；重建后图仍有流程上限和跨语言解析缺口，不能作为无遗漏的“干净检查”。未提交或推送。

## 最终校验与清理

三个包的 SHA-256、ZIP CRC、私密文件排除检查通过；两份 APK 均为非 debuggable release，ABI 与文件名一致，未包含诊断 overlay 写日志代码。字体许可证存在于各包内，新增生产源码未超过 300 行。结果见 `.diagnostics/godot-preview3-artifact-validation.json` 和 `godot-preview3-apk-metadata.json`。

2026-11-23 08:59（America/Phoenix）重新比对网络基线：代理、默认路由、DNS 均完全一致，原有 sing-box PID 37500 仍在运行，记录为 `.diagnostics/godot-network-final.json`。本任务 Windows 测试进程已结束；模拟器 HQIP 已停止、tun0 已消失、无遗留 adb forward。独立安装测试包已卸载，测试 APK 缓存与诊断日志已从客户端私有目录清除，测试账号登录数据保留。
