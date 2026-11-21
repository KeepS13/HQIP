# 第三方客户端导入导出验证

## 2026-11-21：自动准备、合并链接与轮换

本次 Windows amd64 本地验证覆盖默认复制一个合并链接、选择单份订阅、无需手动创建、轮换／撤销和账户概览 QQ 群复制。合并链接是当前有效订阅集合的快照，新购订阅须重新复制；页面已说明此边界。

- `npm --prefix user-web run build` 通过；`npm --prefix user-web test` 共 8 项通过。
- `go test -tags=with_utls ./internal/store -count=1`、`go test -tags=with_utls ./internal/control -count=1`、`go test -tags=with_utls ./internal/agent -count=1` 均通过。最后增加空单份 ID 拒绝保护后，另行通过 `go test -tags=with_utls ./internal/store -run 'TestPortal(AllSubscriptions|ImportEnvelope)' -count=1`。
- 新增回归覆盖并发幂等准备、合并链接三种导出格式、同节点不同订阅凭据隔离、过期成员过滤、跨账户／重复／超长令牌拒绝、浏览器会话与 CSRF、旧链接失效、旧凭据拒绝新连接、现有租约续租拒绝，以及原生客户端凭据保留。既有在途轮换保护和 Reality 撤权回归随完整套件通过。
- `node scripts/test_user_imports.cjs` 通过：7 个客户端、默认合并／单份选择、自动准备、复制与下载、轮换／撤销竞态、迟到响应、失败重试、无效订阅及 QQ 群号 `383590149` 复制。1440、390、320 像素导入页面和 375 像素账户概览已检查截图，无横向溢出；截图保存在 `.local/client-imports-qa/`。
- GitNexus 最终强制重建索引成功；`detect-changes --scope all --limit 1000 --repo .` 报告 13 个已跟踪变更文件、22 个符号、6 个执行流程，风险为 HIGH（包含工作区原有节点探测修改及共享控制端响应流程）。新增符号已进入索引，但部分 Go 接收者调用关系未解析，`PortalAllSubscriptions` 的 impact 为 UNKNOWN；已用实际调用点和回归测试补充检查，不将该结果视为图分析全通过。未跟踪新增文件不包含在该差异统计中。

本次未设置 `HQIP_TEST_POSTGRES_URL` 或 `HQIP_SINGBOX`，因此未执行真实 PostgreSQL、sing-box 原生配置和内核连接验证；未重复独立的 `go vet` 命令。浏览器测试使用隔离 API 模拟，未操作生产账户。真实移动客户端和生产节点撤权时延仍需环境验收。本次没有提交、推送或部署。

以下保留上次验收记录，不代表本次重复执行了其中所有验证。

## 2026-11-09：历史验收

2026-11-09（Asia/Shanghai），Windows amd64 本地验收。生产服务、用户订阅和系统代理设置未参与测试。

## 已验证

- `npm --prefix user-web run build`：TypeScript 与 Vite 构建通过。
- `npm --prefix user-web test`：5 项客户端映射、URL/下载参数、中文及特殊字符编码、订阅选择测试通过；Linux 和 Windows CI 均执行这些测试。
- `go vet ./internal/control ./internal/rules` 通过。
- 设置 `HQIP_SINGBOX` 指向配套 sing-box 1.14.4 后，`go test ./internal/control ./internal/rules -count=1` 通过，包括三种导出格式、下载响应、凭据一致性、不可用状态及在途轮换保护。
- `TestPortalSingBoxPinnedCoreCheck`：默认规则及包含 GEOIP CN、进程规则的完整配置均通过 1.14.4 原生配置检查。
- `TestPortalSingBoxExportRealHY2Routing`：从真正的 HTTP 订阅接口获取配置，用导出的凭据完成隔离 HY2 HTTPS 请求；验证代理、直连和拒绝规则。关闭测试 HY2 服务后，直连继续成功、代理请求失败。
- `scripts/test_user_imports.cjs`：7 个客户端的链接与导入协议、配置文件下载、侧栏及详情跳转、登录返回、刷新与历史导航、多份同名订阅、迟到响应、轮换与撤销竞态、超过轮询周期的慢请求、失败重试、失效与空订阅均通过。1440、390 和 320 像素宽度截图无横向溢出。

## 复现

在仓库根目录执行：

```powershell
npm --prefix user-web test
npm --prefix user-web run build
$env:HQIP_SINGBOX = (Resolve-Path dist/desktop/sing-box.exe).Path
go test ./internal/control ./internal/rules -count=1
go vet ./internal/control ./internal/rules
node scripts/test_user_imports.cjs
```

浏览器测试需要外部安装的 Playwright 及其 Chromium；可用 `HQIP_PLAYWRIGHT` 指定 Playwright 模块绝对路径，`HQIP_CHROMIUM` 指定 Chromium 可执行文件。测试会拦截所有页面和 API 请求，遇到未声明的外部请求直接失败。截图保存在 `.local/client-imports-qa/`。

`HQIP_SINGBOX` 未设置时，Go 测试会明确跳过原生配置检查和真实内核连接测试。设置后要求 1.14.4 配套内核；隔离连接测试仅替换导出的入站为随机回环代理端口，并加入临时 CA，保留节点、选择器、DNS 和路由配置。

## 验证边界

已完成浏览器界面、导入链接协议、文件内容、sing-box 配置检查和本地真实 HY2 连接验证。Android、iOS/iPadOS、macOS 客户端应用的唤起、远程配置更新和系统 TUN 接管仍需对应设备验收，不能用本地内核测试代替真机结果。
