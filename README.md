# HQIP

自用的 Hysteria 2 节点管理平台。通过网站填写 SSH 地址、密码、公网 IP、域名和套餐信息，自动部署节点，为每台设备生成独立 Clash/Mihomo 订阅，并查看代理流量、在线凭据和服务器资源。

## 功能

- 中文深色管理界面，适配手机与桌面，单管理员登录。
- SSH 指纹固定、安装组件 SHA-256 校验、后台部署进度、失败重试。
- Hysteria 2 HTTP 鉴权与本机 Agent；控制网站离线时，已同步凭据继续工作。
- 每设备、每节点独立连接密码；独立订阅、停用和轮换；撤销等待节点确认。
- 每秒采样、每五秒上报；SQLite 持久计数、断网补报和幂等聚合。
- 带宽/月流量仅作参考，80%/100% 站内提醒，**不主动限速或断网**。
- 系统服务自动恢复、TLS 自动续期、一致性备份与恢复命令。

## 使用流程

1. 先为节点域名设置 DNS A 记录，指向服务器的公网 IPv4；不要启用普通 HTTP CDN 代理。
2. 在网站添加服务器。SSH 地址可以是管理地址，公网 IP 是客户端连接地址；两者可不同。默认 root、SSH 22、节点 UDP 443。
3. 等待系统检查、安装和连通性验证。远程节点由控制台实际连接验证；控制台自身兼作节点时还须执行外部验证。
4. 在“设备”中分别创建电脑、手机，复制订阅或点击“导入 Clash”。使用支持 Hysteria 2 的 Clash Verge Rev/Mihomo。
5. 在设备/节点详情查看速率与用量。当前网络必须支持 UDP；第一版没有 TCP 备用协议。

“在线登记设备”是有活动连接的独立设备凭据，并非硬件识别。复制同一订阅到多台机器会共享一个设备身份；“连接实例”可能大于设备数。节点心跳超过 30 秒时在线信息未知。流量统计是代理有效载荷，不含所有加密、重传与网络开销，不等同服务商账单；突然崩溃可能漏掉最后约一秒尚未采样的流量。

## 技术与目录

- `cmd/hqip`、`internal/control`、`internal/store`：Go 控制面、SQLite、后台任务与订阅。
- `cmd/hqip-agent`、`internal/agent`：节点鉴权、计量和补报。
- `internal/provision`：带预检、指纹固定与真实代理探测的 SSH 安装器。
- `web`：React / TypeScript / Vite，构建后嵌入 Go 二进制。
- `deploy`、`scripts`：systemd、Caddy、发布构建、部署与外部验证。
- API 约定见 [docs/interfaces.md](docs/interfaces.md)，运维见 [docs/operations.md](docs/operations.md)。

## 构建与测试

安装 Go（版本见 `go.mod`）、Node.js 22+ 和 Python 3.11+。

```sh
npm --prefix web ci
npm --prefix web run build
go test ./...
go vet ./...
python scripts/build.py --skip-web
```

发布脚本交叉构建 Linux amd64/arm64，并下载校验固定版本 Hysteria 2.13.0、Caddy 2.11.7。第三方下载摘要固定在 `scripts/build.py`，输出 `dist/manifest.json`。节点运行不需要 Node、Go 编译器、Docker 或独立数据库。

本地开发：

```sh
go run ./cmd/hqip init-admin --data-dir .local/dev --username admin --output .local/dev/admin.json
go run ./cmd/hqip serve --data-dir .local/dev --listen 127.0.0.1:8080 --public-url http://127.0.0.1:8080 --artifact-dir dist
```

若使用 Vite 热更新，将上述控制服务的 `--public-url` 改为 `http://127.0.0.1:5173`，另运行 `npm --prefix web run dev -- --port 5173 --strictPort`，然后访问 `http://127.0.0.1:5173`。浏览器 Origin 必须与配置的公网地址一致。首次登录必须修改初始密码（至少 12 字符）。凭据文件属于本机私密运行数据，不要提交。

## 部署

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
