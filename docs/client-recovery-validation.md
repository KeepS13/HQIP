# 客户端静默恢复验收

## 行为

成功连接后，空租约、租约关闭/过期、节点链路异常及账户接口暂时不可达进入 `recovering`。桌面保持“已连接”和“断开连接”，侧栏、任务栏、托盘图标与托盘提示均保持绿色“已连接”，不显示恢复提示。管理端显示“恢复中”。首次连接和用户主动切换仍显示明确失败，不自动换节点、换订阅或抢占其他设备名额。

恢复任务绑定 API 对象、连接代次及选择版本，仅有一个调度任务；HTTPS 验证不重叠，每次最多 25 秒。立即尝试后采用 2/5/10/20/30 秒、±20% 抖动退避，之后持续重试。恢复必须同时满足当前授权有效、HTTPS 出口匹配、服务端存在匹配租约。断开、退出、切换取消旧任务；旧结果不能覆盖新连接。

账户失联 120 秒后通过本地管理 API 切换 `hqip-paused`，确认模式已应用，再关闭已有业务连接。暂停规则先于 DNS、私网和用户分流规则，管理通信和探测入口除外。保留内核、代理设置和 TUN，不把原代理流量改为直连。恢复授权和探测后恢复 `Rule` 模式；用户选择的规则/全局策略仍由配置决定。暂停控制失败立即停止并显示本地故障。

订阅到期、额度耗尽、撤权、节点移除、规则完整性错误、出口不匹配、证书失败或内核停止仍终止会话。HY2 的证书错误会被 HTTP CONNECT 包装，客户端只从内核的临时内存诊断流识别证书类别，不保存原始消息。诊断文件仅记录固定事件码，`recovery.log` 加两份轮转文件，每份最多 1 MiB。

## 自动测试

```powershell
go test ./...
go test -race ./internal/client ./internal/winclient
node scripts/test_client_recovery_ui.cjs
npm --prefix desktop/frontend run build
npm --prefix admin-web run build
```

Windows 竞态测试需要 `CGO_ENABLED=1` 和可用的 MinGW/LLVM C 编译器。桌面 Go 模块需在 `desktop` 内另行执行 `go test ./...`。

控制器测试使用可控授权时钟、HTTP 故障注入和阻塞/迟到的 Engine 响应，覆盖：

- 旧租约缺失、错误订阅租约、新租约建立，HTTPS 暂时失败和连接名额暂满。
- 断网 119 秒不暂停、121 秒暂停一次，网络回来后授权及出口验证通过才恢复。
- 到期、额度耗尽、订阅/账户/节点撤销、配置完整性失败、暂停控制失败。
- 手动断开、切换节点/订阅、退出登录/程序、迟到失败、旧账户响应、重复触发。
- 首次失败仍提示、流量累计与连接时长保留、采样失败不显示旧速率、日志轮转上限。
- Helper 版本不匹配触发升级并重新校验；匹配版本不升级；取消或 UAC 失败不继续连接。
- 实际 React 组件渲染验证桌面静默状态、首次失败横幅，以及管理端恢复中/过期心跳。

## 配套 sing-box 隔离验收

```powershell
$env:HQIP_SINGBOX_EXE='D:\workspace\HQIP-new\dist\desktop\sing-box.exe'
go test ./internal/client -run '^TestPinnedCoreRecoveryIntegration$' -v -count=1 -timeout 160s
$env:HQIP_RECOVERY_TUN='1'
go test ./internal/client -run '^TestPinnedCoreRecoveryIntegration$' -v -count=1 -timeout 160s
```

测试校验配套内核 SHA-256，启动本地 HY2 服务、独立 UDP 丢包中继与自签名 HTTPS 验证服务；只信任测试证书，不关闭 TLS 校验，不使用生产凭据。验证 2 秒及 70 秒 UDP 丢包后在同一进程恢复、暂停业务请求被拒绝且没有直连命中、探测仍通过、恢复后可再次访问，以及 HY2 错误 SNI 必须终止。`-short` 只跳过 70 秒丢包。

TUN 模式使用独立 `HQIPAcceptance` 网卡和测试地址 `198.18.0.1/32`，保持本机默认路由和 DNS。验收结束后终止所有测试进程并清理测试目录。此项验证原生 TUN 数据路径；已有生产 Helper 服务未被替换，真实 Windows UAC 安装/升级对话框没有自动点击。Helper 升级入口、版本比较和失败处理由故障注入测试覆盖。

整机断网超过 120 秒的边界通过控制器故障注入及可控时钟验证；未物理禁用正在使用的网卡。构建产物是本地验收包，保留当前版本号，未发布更新或修改生产服务器。

## 本次结果（2026-11-09）

- 全仓 `go test ./...`、桌面独立模块测试通过。
- Windows `go test -race ./internal/client ./internal/winclient` 通过。
- 桌面/管理端渲染测试及两端前端构建通过。
- 配套 sing-box 的系统代理入口和原生 TUN 完整隔离验收通过，分别覆盖 2 秒、70 秒 UDP 中断和证书终止分类。
- Windows 客户端、Helper 和 PE 图标资源校验构建通过。
- GitNexus 全文索引故障经重建及修复解决。`detect_changes(scope=all)` 原始完整结果无 `partial` / `truncated`，包含新文件；使用临时 Git index 纳入新增文件，未改动用户暂存区。全工作区报告为 CRITICAL，50 个变更文件、229 个符号、62 个受影响流程；这些计数包含本任务前已有的其他改动，不代表全由本任务产生。恢复路径、暂停路径、界面状态和取消竞争分别通过上述测试覆盖。

本地包：`dist/HQIP-Windows-x64.zip`；解压目录：`dist/desktop-recovery`。本次只交付本地构建，未提交、推送、发布或修改生产部署。
