# 5202345.xyz 入口切换

## 完成状态（2026-11-20 22:02，北京时间）

用户将网站入口指向 `64.83.17.143` 后，已完成 Nginx HTTP 和 HTTPS 重定向。`5202345.xyz` 与 `www.5202345.xyz` 的请求最终到达 `https://hqip.sharevps.online`；实测经过 Cloudflare 代理。

## 配置与证书

- 独立站点 `/etc/nginx/sites-available/hqip-5202345.conf`，通过 `/etc/nginx/sites-enabled/zz-hqip-5202345.conf` 启用。文件名顺序保留原有默认站点；仓库模板为 `deploy/nginx-5202345-redirect.conf`。
- HTTP/HTTPS 均使用 `return 302 https://hqip.sharevps.online$request_uri`，保留路径及查询参数，设置 `Cache-Control: no-store`。入口不记录请求日志，HTTP 保留 ACME challenge 路由。
- Let's Encrypt 证书覆盖两个域名，路径为 `/etc/letsencrypt/live/5202345.xyz/`，到期时间为 **2027-02-18 21:00:37（北京时间）**。
- Certbot 使用 `/var/www/letsencrypt` 的 HTTP-01 验证；`certbot.timer` 已启用，续期配置保存了 `nginx -t` 后热重载 Nginx 的部署钩子。

## 验证结果

- Nginx 配置检查、热重载和证书域名校验通过。源站两个域名、两种协议及首页/登录页/带查询参数路径共 12 项检查通过。
- 从服务器经公网 Cloudflare 检查同样的 12 个入口，全部最终返回新站 HTTP 200。`https://www.5202345.xyz/` 仍先经过已有的 Cloudflare 301 到主域名，再由 Nginx 跳转新站；其他所测路径直接跳转。
- 公网首页内容与活动发布目录一致，JS/CSS 静态资源、管理站及旧 API 健康检查正常。
- 当前新站前端使用主站下的 API 路由；`https://hqip.sharevps.online/readyz` 验证为 `ok: true`，两路回环后端也正常。已不存在的 `api-hqip.sharevps.online` 未被当前前端引用，不影响本次重定向。
- 原有 Nginx 站点的 SHA-256 与本次 HTTPS 启用前一致。本次未修改 DNS、业务数据、控制服务、旧 API 或订阅域名。
- 本机 Windows 一次公网直连探测在 20 秒后超时；服务器经公网 Cloudflare 的上述检查通过，因此未将本次结果表述为所有客户端网络均已验证。

## 回执和回退

服务器证据位于 `/root/backups/hqip-5202345-20261120/`，包括 `https-activation.json`、`completion.json`、启用前站点摘要及 HTTP/HTTPS 配置副本。本地对应记录位于 `.local/domain-5202345-cutover-20261120/`。

若需要回退至本次 HTTPS 启用前的状态，先恢复备份的 `hqip-5202345-http.conf` 到独立站点，执行 `nginx -t` 后热重载；这会撤销 HTTPS 跳转，但保留 HTTP 跳转。删除整个启用链接不会自动恢复旧网站，不应视为完整业务回退。
