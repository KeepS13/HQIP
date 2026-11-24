# 双域名部署与恢复

| 入口 | 地址 |
| --- | --- |
| 用户网页 | https://hqip.sharevps.online |
| 管理网页 | https://cms-hqip.sharevps.online |
| API、订阅、客户端下载 | https://hqip.sharevps.online |

正式服务器为`64.83.17.143`。双域名切换保留全部业务数据，用户站同时承载 API，管理站保持独立。服务器迁移历史见[保留数据迁移](server-migration-sharevps.md)。管理页面没有`/admin`前缀；两个网页由Nginx托管，API按路径分发至18090/18091回环端口。后续上线见[自动部署](automatic-deployment.md)，历史空站流程不适用于本次迁移。

两个 Vite 前端支持 `VITE_API_URL`；用户端另支持 `VITE_ADMIN_URL`，管理端另支持 `VITE_USER_URL`，构建变量优先于上述默认地址。开发端口为 5173/5174，开发 API 为 8080/8081；不要在开发期间放宽生产 Origin、TLS 或人机验证约束。

发布必须使用已提交源码和完整产物：

```sh
npm --prefix admin-web ci
npm --prefix user-web ci
npm --prefix admin-web run build
npm --prefix user-web run build
go test ./...
python -B -m unittest discover -s scripts -p "test_*.py"
python scripts/build.py --skip-web --server-only
# 同一提交在Windows构建客户端后，打包所有产物；首次新站不传基线SHA。
python scripts/package_domains.py --previous-source-sha <当前正式SHA>
```

完整包内包含本次控制端、两网页、客户端、Agent/HY2/Caddy和部署配置，文件逐一校验，不继承旧发布目录。生产只配置准确的两个虚拟主机，不接管其他站点。Certbot webroot保留`/.well-known/acme-challenge/`及Nginx续期reload。API只信任官方Cloudflare网段提供的来源IP，覆盖其他转发头；API、SSE、订阅和授权页面不启用缓存、交互式挑战或URL重写。

正式控制服务必须配置PostgreSQL＋Redis，存储只在回环可访问。网站TLS与业务Cloudflare DNS Token、Turnstile配置独立；本次保留既有业务配置，用户站域名不变，无需重新录入。

日常升级与回退保留当前PG账本和所有新增订单、订阅、用量。目标代码须支持schema5、postgresSchemaVersion1、serverBillingVersion2和支付结构；不兼容则拒绝回退。旧SQLite、旧用户和旧域名不作为自动恢复目标，灾难恢复必须使用新站配套备份并先在空数据库验证。
