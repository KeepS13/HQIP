# HQIP

Go 控制服务、独立 React 用户端和管理端、基于 sing-box 的 Windows 客户端，以及受管 HY2 节点。

- 用户入口：https://hqip.5202345.xyz
- 管理入口：https://cms-hqip.5202345.xyz
- API：https://api-hqip.5202345.xyz

用户自行注册并购买订阅，支付 FM 收款核验后自动开通，管理员也可直接分配。一个用户可同时持有多份独立流量包；每份分别计量、到期、限速和占用 VPN 连接名额。支持多 IP 节点、Cloudflare DNS／证书管理、Windows 流量仪表盘和基础设施预警。

`admin-web/` 与 `user-web/` 各自安装、构建、发布，Go 不再内嵌网页。`desktop/` 为 Windows 客户端。运行数据库、密码、主密钥、订阅令牌和索引缓存不提交仓库。

## 开发与部署

参阅 [订阅与准入](docs/portal.md)、[支付配置与订单](docs/payments.md)、[三域名部署与恢复](docs/domains.md)、[客户端使用说明](docs/client.md)、[站内预警中心](docs/notifications.md) 和 [接口说明](docs/interfaces.md)。

```powershell
npm --prefix admin-web ci
npm --prefix user-web ci
npm --prefix admin-web run build
npm --prefix user-web run build
go vet ./...
go test ./...
python -m unittest discover -s scripts -p "test_*.py"
```

生产使用 Nginx／Certbot 与 systemd，不运行旧 Caddy 引导脚本接管其他站点。发布前备份一致性数据库和配套主密钥；回退只切换代码与路由，不覆盖实时用量账本。

推送到 `main` 后的 GitHub Actions 自动上线与 Secret 配置见 [自动部署](docs/automatic-deployment.md)。
