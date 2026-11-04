# 三域名部署与恢复

| 入口 | 地址 |
| --- | --- |
| 用户网页 | https://hqip.5202345.xyz |
| 管理网页 | https://cms-hqip.5202345.xyz |
| API、订阅、客户端下载 | https://api-hqip.5202345.xyz |

管理端页面没有 `/admin` 前缀。旧用户域名上的 API、订阅和下载文件接口返回 410，旧 `/admin` 返回 404。浏览器需重新登录，账号和密码保持原值；第三方客户端需从用户端复制新订阅地址。Windows 3.1 自动迁移旧官方服务地址，自定义地址保持原值。

## 开发与构建

`admin-web`、`user-web` 是独立 Vite 项目，各自安装依赖并构建。Go 二进制仅提供 API，不再内嵌网页。

```powershell
npm --prefix admin-web ci
npm --prefix user-web ci
npm --prefix admin-web run build
npm --prefix user-web run build
go test ./...
python -m unittest discover -s scripts -p "test_*.py"
```

本地开发分别将两端 `.env.example` 复制为 `.env`；管理 Vite 端口 5173、用户 Vite 端口 5174，Go 管理 API 8080、公共 API 8081。初始化私有测试数据后运行：

```powershell
go run ./cmd/hqip serve --data-dir .local/dev-data --listen 127.0.0.1:8080 --public-listen 127.0.0.1:8081 --admin-origin http://127.0.0.1:5173 --user-origin http://127.0.0.1:5174 --public-url http://127.0.0.1:8081 --artifact-dir dist
```

在另外两个终端分别执行 `npm --prefix admin-web run dev` 和 `npm --prefix user-web run dev`。本地注册仍需有效的 Turnstile 配置；测试可使用隔离 fixture，不能放宽生产验证。

## 发布

固定提交后构建控制二进制 `dist/hqip-domains-linux-amd64`、两个网页和 Windows 包。执行 `python scripts/package_domains.py --base-manifest <当前生产release.json>` 生成叠加发布包；未包含文件只允许从原发布目录按清单校验后复用，不能从运行数据目录复制。

两个新域名必须在 Cloudflare 指向目标服务器。Nginx 仅增加这三个准确名称的虚拟主机，不修改其他站点。新域名通过 Certbot webroot 签发证书，保留 `/.well-known/acme-challenge/`，续期部署钩子 reload Nginx。API 信任 Cloudflare 官方地址段提供的客户端 IP；非可信来源不能伪造限流身份。Cloudflare 不应对 API、SSE、订阅或授权页面启用缓存、交互式挑战或 URL 重写。

先使新 API HTTPS 入口可达，再逐节点运行 `migrate_agent_api.py migrate --node-id <ID>`。脚本验证已有安装归属，备份节点配置和一致性账本，只更新该 Agent 的控制地址并重启 Agent。HY2 核心和连接凭据不变。

目标机安装目录保留 `/opt/hqip-control`，运行数据库和主密钥保留 `/var/lib/hqip-control`。验证发布清单后执行：

```bash
python3 /opt/hqip-control/releases/<SHA>/scripts/upgrade_domains.py upgrade --release /opt/hqip-control/releases/<SHA> --state /root/backups/hqip-domains-<时间>
```

升级在受限目录保存数据库、配套主密钥、Nginx 和 systemd 配置。停用并清空飞书凭据，切换程序和两个静态目录。API 按路径分发至原有 18090／18091 回环监听；两个网页域名不代理业务 API。

## 回退

先恢复站点代码／路由，再按节点备份恢复 Agent 地址：

```bash
python3 /opt/hqip-control/releases/<SHA>/scripts/upgrade_domains.py rollback --state /root/backups/hqip-domains-<时间>
python3 /path/to/migrate_agent_api.py rollback --backup /root/backups/hqip-agent-domain-<节点>-<时间>
```

控制服务回退保留当前数据库与全部新增用量，不以旧快照覆盖。飞书保持关闭，机器人凭据不会恢复。数据库快照仅用于经过核对的灾难恢复。退回旧版本后客户端服务地址需重新设回旧入口。
