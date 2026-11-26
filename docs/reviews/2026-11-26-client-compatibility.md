# 网站客户端兼容审查

日期：2026-11-26，Asia/Taipei。归属 `subscription-admission.import-links v1`、`routing`、`client-presentation`。本轮在已有 [Clash DNS 修复](2026-11-26-clash-dns-recovery.md) 上继续检查网站全部客户端。当前仅修改本地工作区，未提交、推送或部署；不能将本记录理解为所有线上用户已经更新。

## 发现与修复

1. Clash Verge Rev 2.5.10 的官方 `extract_subscription_url` 消费 `url=` 后的全部字符。网站原来输出 `url=编码地址&name=编码名称`，无 query 时名称进入令牌路径，有 query 时污染订阅参数。新增消费者行为回归先失败，再将 `name` 移至 `url` 之前；标准参数解析和 Verge 尾部解析均保留完整原始 URL。Verge 改用其官方注册的 `clash-verge:` 专用 scheme，防止其他 Clash 应用接管；官方 tauri 配置与本机 HKCR 注册均已确认，新增断言先失败后通过。CMFA 同步传递订阅名称，避免多个配置都显示默认名称。通用入口明确改名为 Clash Meta / Mihomo。
2. 节点 URI 不携带路由，单纯选择绕过大陆也不能保证官网直连。新增可展开的恢复帮助，显示并复制后端 `directAccessHosts`，明确 v2rayN/NG 精确域名与 Shadowrocket DOMAIN 直连的操作。地址来自订阅导出的同一 helper，前端不另存名单，不给共享 CDN 或父域名加例外。
3. 更新 v2rayN 平台说明、启动步骤与配套内核要求，sing-box 标明经过验证的 1.14.4 版本和旧版未知字段时的升级操作。所有客户端均说明手动导入、坏节点时先断开代理恢复官网、UDP 受限时尝试 VLESS。

本轮未发现其余 URI 的服务器/SNI、认证、Reality 公钥、short ID、Vision flow 或 TLS 校验字段错误；不据此修改协议或放宽证书验证。

## 覆盖矩阵

| 网站入口 | 已检查内容 | 本轮实测与边界 |
| --- | --- | --- |
| Clash Meta / Mihomo | 完整 YAML、DNS、精确 DIRECT、双协议 | Meta 1.16.0、Mihomo 1.18.10/1.19.33/1.19.34；DNS 与失效节点隔离测试通过，真实连接结果及两次初始 HY2 EOF 保留在上轮记录 |
| Clash Verge Rev | 同上；2.5.10 深链接消费者语义 | 原顺序回归失败、修复后通过；安装内核 1.19.33 已实测，未修改现有软件配置 |
| Clash Meta Android | 官方 manifest、ExternalControlActivity 接受的 scheme/名称/URL | v2.11.37 源码核对、网页导入回归；Mihomo 核心验证不替代 Android 应用/TUN 验收 |
| v2rayN | Base64 URI、HY2/VLESS 参数、路由操作 | 官方 7.25.6 包的 sing-box 1.14.3 双协议各 HTTPS 204/200；Xray 26.10.10 VLESS 204/200；未启动 GUI 或改系统代理 |
| v2rayNG | 2.3.15 UrlSchemeActivity、HY2/VLESS 解析源码与 URL-safe 订阅令牌 | 网页导入/编码回归；未在 Android 应用实际导入或接管 VPN |
| Shadowrocket | 节点订阅入口、复制/下载、独立配置路由操作 | App Store 官方说明与页面回归；闭源 URI 解析、iOS/Apple Silicon 应用和连接未实测 |
| Sing Box | 官方 remote-profile URI、完整 JSON | sing-box 1.14.4 原生配置检查与真实 HY2 路由回归；vus1 HY2/VLESS 各 204/200；Apple/Android GUI 未实测 |
| HQIP Windows / Android | 官网下载入口、共享官方直连名单；Windows 系统代理例外 | `internal/client` / `internal/winclient` 原有回归与新增名单回归已通过；新序号 5000023 尚未打包发布，不声称安装包已修复上线 |

v2rayN 包 SHA-256 为 `9b94718c2a214a246b04dec84b9a27f1f0ced8489c2dd25eb80a0fbb806bd13f`，与官方 release asset digest 相同。真实核心测试保留节点域名和 SNI，仅使用随机回环 HTTP 端口；结束后停止测试实例并删除含凭据的临时配置。没有修改系统代理/TUN。现有 Android 模拟器正在运行其他项目，因此未打断其活动。

## 本地验证

- `npm --prefix user-web test`：25 项通过，包含新加入的 Verge 尾部解析回归；修复前该项明确失败。
- `npm --prefix user-web run build`：TypeScript 和 Vite 通过。
- `node scripts/test_user_imports.cjs`：7 客户端的复制、下载、scheme、令牌轮换/撤销竞态、错误重试、恢复名单复制、不同客户端说明及 1440/390/320 像素页面通过。恢复帮助的 503→手动重试也通过；检查了展开帮助的窄屏截图。
- `TestPortalConfigPublishesExactRecoveryHosts`：旧实现缺少字段时失败，修复后通过；验证部署自定义域名与真实导出名单一致。
- `go test -tags=with_utls ./internal/control -count=1`：通过，71.006 秒；显式设置 `HQIP_SINGBOX` 为 1.14.4、`HQIP_CLASH_CORES` 为四个真实核心，因此可选配置/连接检查实际执行。
- `go vet -tags=with_utls ./internal/control`、`git diff --check` 通过。此前四包全量 Go 测试与 vet 结果见 Clash 记录，本轮没有修改 rules/client/winclient 的实现。
- v2rayN sing-box 1.14.3 与 HQIP sing-box 1.14.4 均通过双协议完整导出配置检查。5 组真实核心/协议组合，共 10 次外部 HTTPS 请求全部返回预期 204/200。

库外证据位于 `.local/client-compatibility-20261126/`，包含上游源码/版本元数据、官方包摘要、`live-core-results.json`、测试日志和调用图结果；浏览器截图位于 `.local/client-imports-qa/`。这些真实连接是短时观测，不代表所有地区、网络和版本的长期可靠性。CI 默认 Go 测试仍可跳过未提供核心路径的可选项，本轮是显式开启后在本机执行；不能写成已经在远程 CI 验收。

## 上游依据

- Verge 2.5.10：[scheme.rs](https://github.com/clash-verge-rev/clash-verge-rev/blob/v2.5.10/src-tauri/src/utils/resolve/scheme.rs)、[注册的 URL schemes](https://github.com/clash-verge-rev/clash-verge-rev/blob/v2.5.10/src-tauri/tauri.conf.json)。
- CMFA v2.11.37：[ExternalControlActivity](https://github.com/MetaCubeX/ClashMetaForAndroid/blob/v2.11.37/app/src/main/java/com/github/kr328/clash/ExternalControlActivity.kt)。
- v2rayNG 2.3.15：[UrlSchemeActivity](https://github.com/2dust/v2rayNG/blob/2.3.15/V2rayNG/app/src/main/java/com/v2ray/ang/ui/UrlSchemeActivity.kt)、[Hysteria2Fmt](https://github.com/2dust/v2rayNG/blob/2.3.15/V2rayNG/app/src/main/java/com/v2ray/ang/fmt/Hysteria2Fmt.kt)。
- v2rayN 7.25.6：[发布包](https://github.com/2dust/v2rayN/releases/tag/7.25.6)、[Hysteria2Fmt](https://github.com/2dust/v2rayN/blob/7.25.6/v2rayN/ServiceLib/Handler/Fmt/Hysteria2Fmt.cs)。
- sing-box v1.14.4：[remote_profile.go](https://github.com/SagerNet/sing-box/blob/v1.14.4/experimental/libbox/remote_profile.go)。
- Shadowrocket：[开发者提供的 App Store 资料](https://apps.apple.com/us/app/shadowrocket/id932747118)，查询时版本为 2.2.93；无可用 Apple 测试设备。

## 影响与发布

编辑前 GitNexus：`clientImportScheme`、`ImportSubscription` LOW，涉及 ClientImports → PortalHome → Portal；`portalConfig` LOW，来自公共路由及管理员设置响应。`importClients`、浏览器测试 `main` UNKNOWN，已通过实际引用/脚本入口确认，不当作无人使用。

最终增量索引曾产生错误的跨模块关联；已再做全量重建（38.1 秒）并重跑 `detect-changes --scope all --limit 10000 --repo .`。结果为整个工作区 32 个已跟踪文件、57 个符号、17 个流程、CRITICAL；包含用户原有流量计量修改。CLI 仍只打印前 15 个符号/10 个流程，且索引报告未探索全部入口/分支，新增未跟踪文件不计入差异数量。因此这是有覆盖边界的风险审查，不能称为完整无影响或干净门禁，也没有据此提交。与本次直接相关的导入、配置、路由路径通过上述结构回归、真实核心和浏览器验证补充检查。

公共接口只新增无凭据主机数组，无数据库写入或服务端网络请求。恢复帮助按需发一个请求，15 秒有界、卸载取消、错误可重试，不增加定时轮询或影响复制/导入。修改及新增生产源文件均不超过 300 行。原有未提交流量计量工作保留，未混入本轮功能结论。

上线须按 `docs/automatic-deployment.md` 生成同一提交的完整服务端、网站与客户端产物。已有第三方用户要刷新并启用完整订阅；节点 URI 用户仍需配置精确直连规则。不能由网页覆盖任意用户的第三方代理全局模式，也不能使境外本地出口自动变成大陆 IP。
