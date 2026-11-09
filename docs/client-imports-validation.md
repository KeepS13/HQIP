# 第三方客户端导入导出验证

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
