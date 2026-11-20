# 107 独立测试环境

> 2026-11-20 测试结束：控制服务及独立存储已停止并禁用开机启动，测试域名与 Nginx 入口已撤下，对应证书续期已停用。数据、配置和备份保留。下文记录此前的部署状态。

2026-11-20 按测试用途手工部署，不接入 GitHub CI。

| 入口 | 地址 |
| --- | --- |
| 用户端 | https://test-hqip.sharevps.online/ |
| 管理端 | https://test-hqip.sharevps.online/admin/ |
| API | https://test-hqip.sharevps.online/api/ |
| 就绪检查 | https://test-hqip.sharevps.online/readyz |

Cloudflare `sharevps.online` 下独立 A 记录指向 `107.151.244.106`，已开启橙云（`proxied: true`）。正式三个域名仍指向 `64.83.17.143`。测试站源站 HTTPS 只接受 Cloudflare 来源，证书由 Certbot 自动续期；本次证书有效期至 2027-02-18。

## 隔离与运行

- 控制服务：`hqip-test107.service`，独立系统用户 `hqip-test107`，管理/公共 API 仅监听 `127.0.0.1:19090/19091`。
- 程序：`/opt/hqip-test107/hqip`，从 107 保留版本 `cabbc213be124d82aef0fc004bc57769c0716122` 复制并核验发布清单哈希。该版本与当前工作区 HEAD 的后端及两套前端源码无差异，管理端新增构建路径支持。
- 网页：`/opt/hqip-test107/www`；用户端位于根目录，管理端位于 `admin/`。
- 数据与独立主密钥：`/var/lib/hqip-test107`。
- PostgreSQL：独立 Compose 项目 `hqip-test107-storage`，新库 `hqip_test107`，独立数据卷 `hqip-test107-storage_postgres_data`，仅监听 `127.0.0.1:25432`。
- Redis：同一独立项目内的新实例和数据卷，仅监听 `127.0.0.1:26379`，新生成的凭据与受限 ACL。
- 存储服务：`hqip-test107-storage.service`；凭据配置在 `/etc/hqip-test107-storage`，不提交仓库。
- Nginx：`/etc/nginx/sites-available/hqip-test107.conf`，对应仓库 `deploy/nginx-test107.conf`；独立日志及 logrotate 配置。
- 新控制与存储服务已设置开机启动；旧 `hqip-control`、`hqip-storage` 及其数据卷保持停止，旧服务启动保护仍保留。
- 安装用公共节点产物通过只读使用原保留发布目录；桌面客户端并未重构建为测试站版本。

仅重启本测试服务：

```sh
systemctl restart hqip-test107
systemctl status hqip-test107 hqip-test107-storage
curl --fail http://127.0.0.1:19091/readyz
```

## 前端构建与路由

两套前端构建时使用：

```text
VITE_API_URL=https://test-hqip.sharevps.online
VITE_ADMIN_URL=https://test-hqip.sharevps.online/admin/
VITE_USER_URL=https://test-hqip.sharevps.online
```

管理端 Vite 使用 `--base /admin/`，`BrowserRouter` 使用 `import.meta.env.BASE_URL`，支持子路径导航与直接刷新；默认正式构建仍使用 `/`。用户端使用 `--base /`。Nginx 根据路径将管理 API 转发到 19090，将用户、客户端、Agent、订阅、支付回调与健康检查接口转发到 19091。

初始管理员为 `admin`，随机初始密码保存在服务器 `/var/lib/hqip-test107/bootstrap-admin.json`（0600），本机交付副本位于 `.local/test-107-20261120/bootstrap-admin.json`，首次登录强制改密。未复制正式站账户、节点、支付、Cloudflare 业务 Token 或验证码设置。用户门户、注册、支付保持关闭，用户登录需先在管理端配置 Turnstile 并启用门户。

## 验证记录

- 两套前端 TypeScript 检查与生产构建通过。
- 橙云下用户页、管理页及管理深层路径、两端 session、用户 config、`/healthz`、`/readyz` 均返回 200，并有 Cloudflare 响应标识。
- 浏览器确认两套页面可渲染、`/admin/servers` 可刷新、用户端管理入口跳转到同域名 `/admin/`。
- 公网管理员登录、会话读取及退出通过；临时验证会话已清理。
- 新库仅有一个初始管理员；用户、订阅、订单、服务器、节点均为 0。PostgreSQL 与 Redis 的新旧卷、端口及容器状态已核对。
- 旧 Nginx 站点、旧数据库连接配置及旧主密钥的哈希未变。
- GitNexus pre-edit：管理 App 文件上游为 `main.tsx`，风险 LOW；入口文件和 Nginx 为 UNKNOWN，已用 HTML 入口、部署引用及线上路由补查。`detect-changes --scope all` 提示入口改动没有重叠的已索引符号，不能作为无影响证明；未提交或推送。

服务器交付证据位于 `/root/backups/hqip-test107-20261120`，本机产物及验证副本位于 `.local/test-107-20261120`。
