# 三域名部署与恢复

| 入口 | 地址 |
| --- | --- |
| 用户网页 | https://hqip.5202345.xyz |
| 管理网页 | https://cms-hqip.5202345.xyz |
| API、订阅、客户端下载 | https://api-hqip.5202345.xyz |

管理端页面没有 `/admin` 前缀。旧用户域名上的 API、订阅和下载文件接口返回 410，旧 `/admin` 返回 404。客户端使用 API 域名、用户浏览器授权使用用户域名；自定义客户端地址保持原值。订阅升级清理旧开发账户，需使用新注册账户与独立订阅实例。

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

固定提交后构建控制二进制 `dist/hqip-domains-linux-amd64`、两个架构的 Agent、两个网页和 Windows 包。执行 `python scripts/package_domains.py --base-manifest <当前生产release.json> --base-node-manifest <当前生产artifacts/manifest.json>` 生成叠加发布包；未包含文件只允许从原发布目录按清单校验后复用，不能从运行数据目录复制。节点清单仅更新 Agent 校验和及来源提交，HY2 等其他固定产物保持原值。

两个新域名必须在 Cloudflare 指向目标服务器。Nginx 仅增加这三个准确名称的虚拟主机，不修改其他站点。新域名通过 Certbot webroot 签发证书，保留 `/.well-known/acme-challenge/`，续期部署钩子 reload Nginx。API 信任 Cloudflare 官方地址段提供的客户端 IP；非可信来源不能伪造限流身份。Cloudflare 不应对 API、SSE、订阅或授权页面启用缓存、交互式挑战或 URL 重写。

先使新 API HTTPS 入口可达，再逐节点运行 `migrate_agent_api.py migrate --node-id <ID>`。脚本验证已有安装归属，备份节点配置和一致性账本，只更新该 Agent 的控制地址并重启 Agent。HY2 核心和连接凭据不变。

目标机安装目录保留 `/opt/hqip-control`，运行数据库和主密钥保留 `/var/lib/hqip-control`。验证发布清单后执行：

```bash
python3 /opt/hqip-control/releases/<SHA>/scripts/upgrade_domains.py upgrade --release /opt/hqip-control/releases/<SHA> --state /root/backups/hqip-domains-<时间>
```

升级在受限目录保存数据库、配套主密钥、Nginx 和 systemd 配置。停用并清空飞书凭据，切换程序和两个静态目录。API 按路径分发至原有 18090／18091 回环监听；两个网页域名不代理业务 API。

## 回退

数据库 schema 5 的多订阅发布不能回退至只支持 schema 4 的旧单套餐程序。脚本会在停止服务前检查目标版本；不兼容时保留新版本和当前数据，改用兼容版本修复。数据库快照不用于抹去升级后产生的流量。

先恢复站点代码／路由，再按节点备份恢复 Agent 地址：

```bash
python3 /opt/hqip-control/releases/<SHA>/scripts/upgrade_domains.py rollback --state /root/backups/hqip-domains-<时间>
python3 /path/to/migrate_agent_api.py rollback --backup /root/backups/hqip-agent-domain-<节点>-<时间>
```

控制服务回退保留当前数据库与全部新增用量，不以旧快照覆盖。飞书保持关闭，机器人凭据不会恢复。数据库快照仅用于经过核对的灾难恢复。退回旧版本后客户端服务地址需重新设回旧入口。

## 多订阅开发数据清理门禁

仅首次从旧 schema 4 开发环境升级时执行。日常 schema 5 发布不再重置用户。先完成构建、隔离联调和 CI，再准备清理；所有目录必须是 `/root/backups/` 下新的私有目录。

1. 在控制机执行 `python3 subscription_migration.py hold --state /root/backups/hqip-subscriptions-<时间>`。脚本备份一致性数据库与主密钥，保存原注册开关，关闭旧用户新准入、撤销旧会话和连接。节点继续报告最终用量。
2. 等待所有节点最终结算，在每个活动受管节点执行 `python3 subscription_migration.py node-receipt --node-id <ID> --state /root/backups/hqip-subscriptions-node-<时间>`。若有未关闭或未确认的最终账本，脚本拒绝继续。成功时备份节点配置和 SQLite 账本，生成 receipt；不清空采样基线。
3. 将各节点 receipt 放在控制机受限 JSON 数组文件中，执行 `python3 subscription_migration.py confirm --state <hold目录> --receipts <文件>`。节点集合、配套主密钥、时间及最终用量必须匹配，才写入升级就绪标记。receipt 需在 hold 后取得且不超过 15 分钟。
4. 发布包含 schema 5 的版本。升级脚本再次检查门禁；数据库事务归档监控身份和已结算租约，删除旧开发用户、套餐、会话与导入凭据，创建新的多订阅表。服务器、节点、地址、证书、规则、管理员和基础设施监控不重置。

如果尚未迁移且决定取消清理，执行 `abort-hold --state <hold目录>` 可恢复原用户启用状态与注册开关；旧登录会话仍需重新登录。schema 已升级后该操作会拒绝执行。不要手工删除待结算队列或把就绪标记当作跳过节点核对的开关。
