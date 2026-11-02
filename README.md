# HQIP

自用的 Hysteria 2 节点管理平台，包含本地中文管理网站和 Windows 客户端。通过网站填写 SSH 地址、密码、公网 IP、域名和套餐信息，自动部署节点，为每台设备分配允许使用的节点，使用桌面账号或 Clash/Mihomo 订阅连接，并查看代理流量、在线凭据和服务器资源。

## 功能

- 中文深色管理界面，适配手机与桌面，单管理员登录。
- SSH 指纹固定、安装组件 SHA-256 校验、后台部署进度、失败重试。
- Hysteria 2 HTTP 鉴权与本机 Agent；控制网站离线时，已同步凭据继续工作。
- 每设备、每节点独立连接密码；独立订阅、停用和轮换；撤销等待节点确认。
- Windows 设备账号、节点选择、规则/全局模式、系统代理与受限 TUN 服务；关闭窗口隐藏到托盘，退出恢复网络。
- 管理员入口只在本机；公网客户端与 Agent 接口经固定指纹、受限 SSH 隧道接入。
- 每秒采样、每五秒上报；SQLite 持久计数、断网补报和幂等聚合。
- 带宽/月流量仅作参考，80%/100% 站内提醒，**不主动限速或断网**。
- 系统服务自动恢复、TLS 自动续期、一致性备份与恢复命令。

## 使用流程

1. 先为节点域名设置 DNS A 记录，指向服务器的公网 IPv4；不要启用普通 HTTP CDN 代理。
2. 在本机 `http://127.0.0.1:8080` 添加服务器。SSH 地址可以是管理地址，公网 IP 是客户端连接地址；两者可不同。默认 root、SSH 22、节点 UDP 443。
3. 等待系统检查、安装和连通性验证。远程节点由控制台实际连接验证；控制台自身兼作节点时还须执行外部验证。
4. 在“设备”中分别创建电脑、手机，显式分配节点并选择默认节点。Windows 可初始化设备账号后登录 HQIP 客户端；其他设备可复制订阅或点击“导入 Clash”，使用支持 Hysteria 2 的 Clash Verge Rev/Mihomo。
5. 在设备/节点详情查看速率与用量。当前网络必须支持 UDP；第一版没有 TCP 备用协议。

“在线登记设备”是有活动连接的独立设备凭据，并非硬件识别。复制同一订阅到多台机器会共享一个设备身份；“连接实例”可能大于设备数。节点心跳超过 30 秒时在线信息未知。流量统计是代理有效载荷，不含所有加密、重传与网络开销，不等同服务商账单；突然崩溃可能漏掉最后约一秒尚未采样的流量。

## 技术与目录

- `cmd/hqip`、`internal/control`、`internal/store`：Go 控制面、SQLite、后台任务与订阅。
- `cmd/hqip-agent`、`internal/agent`：节点鉴权、计量和补报。
- `internal/provision`：带预检、指纹固定与真实代理探测的 SSH 安装器。
- `web`：React / TypeScript / Vite，构建后嵌入 Go 二进制。
- `desktop`、`internal/client`、`internal/winclient`、`cmd/hqip-helper`：Wails Windows 客户端、配置同步、内核与系统网络生命周期。
- `internal/rules`：共享路由规则校验、规范化、版本摘要和 Clash/sing-box 输出。
- `deploy`、`scripts`：systemd、Caddy、发布构建、部署与外部验证。
- [客户端使用说明](docs/client.md) · [接口约定](docs/interfaces.md) · [运维与迁移](docs/operations.md)

## 构建与测试

安装 Go（版本见 `go.mod`）、Node.js 22+ 和 Python 3.11+。

```sh
npm --prefix web ci
npm --prefix web run build
go test ./...
go vet ./...
python scripts/build.py --skip-web
```

发布脚本交叉构建 Linux amd64/arm64 与 Windows 控制命令，并下载校验固定版本 Hysteria 2.13.0、Caddy 2.11.7。第三方下载摘要固定在 `scripts/build.py`，输出 `dist/manifest.json`。节点运行不需要 Node、Go 编译器、Docker 或独立数据库。

在 Windows 上构建桌面发布包：

```powershell
python scripts/build_desktop.py
```

输出 `dist/HQIP-Windows-x64.zip`，包含 HQIP、受限 helper、固定 sing-box 1.14.4、许可、摘要和使用说明。`desktop` 是独立 Go 模块，根目录的 `go test ./...` 不包含它；CI 会单独构建、vet 和测试该模块，并运行两个前端构建以及 Python 脚本测试。

本地开发：

```sh
go run ./cmd/hqip init-admin --data-dir .local/dev --username admin --output .local/dev/admin.json
go run ./cmd/hqip serve --data-dir .local/dev --listen 127.0.0.1:8080 --public-listen 127.0.0.1:8081 --admin-origin http://127.0.0.1:8080 --public-url http://127.0.0.1:8081 --artifact-dir dist
```

若使用 Vite 热更新，将 `--admin-origin` 改为 `http://127.0.0.1:5173`，另运行 `npm --prefix web run dev -- --port 5173 --strictPort`，然后访问 `http://127.0.0.1:5173`。`--public-url` 仍指向公共监听器。浏览器 Origin 必须与管理员入口配置一致。首次登录必须修改初始密码（至少 12 字符）。凭据文件属于本机私密运行数据，不要提交。

## 部署与运行

当前使用 Windows 本地控制面。VPS 的 Caddy 经受限 SSH 反向隧道 `127.0.0.1:18088 → 本机 127.0.0.1:8081` 提供公共 API；管理页面只在本机 `8080` 开放。完成迁移后，公网域名首页和管理接口返回 404 是预期行为。按 [运维文档](docs/operations.md) 的 `prepare → cutover → Start-HQIP → activate` 顺序迁移已有服务器控制面，然后用 `Start-HQIP.ps1` / `Stop-HQIP.ps1` 管理本地进程。

控制电脑离线时，已同步的节点继续代理，但不能登录客户端、下载新订阅或下发配置；节点账本在恢复后补报。设备账号和订阅地址始终使用公网 HTTPS 域名。

以下服务器引导脚本保留用于首次建立原有服务器拓扑；已完成本地迁移的环境不要重复执行，应按运维文档更新本机二进制：

控制面引导脚本支持干净的 Ubuntu 22.04/24.04 或 Debian 12 amd64 主机；节点支持这三种系统的 amd64/arm64。SSH 用户须为 root 或可免密 sudo。

```sh
uv run --with paramiko python scripts/deploy.py \
  --credentials /secure/server.txt \
  --fingerprint 'SHA256:已核对的服务器指纹' \
  --domain panel.example.com
```

凭据文件两行分别为主机地址和密码。可通过 `--socks 127.0.0.1:7897` 使用已有 SOCKS5 管理通道。脚本不打印密码；初始管理员凭据存放于服务器 `/var/lib/hqip/bootstrap-admin.json`，仅所有者可读。

网站使用 TCP 80/443，节点使用 UDP 443；本机鉴权和统计在 127.0.0.1:18081/18082。共享主机禁用 Caddy HTTP/3，Hysteria HTTP ACME 挑战转发到 127.0.0.1:18080。云安全组须放行这些公网端口；安装器不会清空已有防火墙策略。

## 数据与安全

SSH 密码在部署任务期间加密，成功删除，失败最多保存 24 小时。连接凭据与订阅令牌加密保存，Agent 只缓存连接密码摘要；订阅链接是持有者凭据，请按密码对待。初始管理员密码修改后应删除引导凭据文件。系统不记录用户浏览目标，也不公开本机统计端口。

分钟曲线保留 7 天、小时曲线 90 天、日/月汇总 13 个月，终身累计独立保存。分钟补报队列预算 64 MiB，超限时保留日累计并标记精细历史缺失；未确认的权威日累计不会因预算被静默丢弃。日志命名空间上限 100 MiB，磁盘低于 512 MiB 时站内告警。

代码索引通过 GitNexus 在本地建立：`gitnexus analyze`，然后 `node .gitnexus/run.cjs status`。索引缓存、数据库、密钥、构建产物和服务器凭据均在 Git 忽略范围内。
