# 独立服务器部署与历史替换记录

当前部署目标为 `107.151.244.106`：用户入口 https://hqip.sharevps.online，管理入口 https://cms-hqip.sharevps.online，API 入口 https://api-hqip.sharevps.online。两个独立前端由 Nginx 托管，Go 仅提供 API，不依赖 Windows 本机或反向隧道。日常发布参阅[三域名部署与恢复](domains.md)，本次迁移参阅[服务器与域名迁移](server-migration-sharevps.md)。

**以下“首次替换部署”和“旧环境退役”是历史空站部署记录，不是本次迁移步骤。** 当前迁移必须保留已有数据库、主密钥、用户、订阅、订单及账本，不得重复初始化空站或按历史步骤停用仍在使用的节点。

## 服务与数据

- 服务：`hqip-control.service`；运行用户 `hqip-control`。
- 程序：`/opt/hqip-control/releases/<提交 SHA>`，`current` 指向发布版本。
- 运行目录：`/var/lib/hqip-control`，保存主密钥、规则及运行文件，目录权限 700；业务数据库按当前配置使用 PostgreSQL 或 SQLite。备份须覆盖实际业务数据库，不得只复制目录中的旧 SQLite 文件。
- 管理/公共监听：`127.0.0.1:18090`、`127.0.0.1:18091`，仅由 Nginx 访问。
- Nginx：`/etc/nginx/sites-available/hqip.conf`；三个正式域名各自使用有效证书，保留 Certbot 自动续期及 Nginx reload 钩子。
- 日志：`journalctl --namespace=hqip-control -u hqip-control`，限制 100 MiB/7 天；Nginx 访问日志只保留方法、状态和耗时，不记录 URI、查询参数、Cookie 或 Authorization，错误请求详情不落盘。

管理员根地址可配置为 HTTPS；HTTP 只允许 `127.0.0.1` 本地开发。两个 Go 监听器仍强制绑定回环 IP。Nginx 覆盖来源 IP 转发头，避免客户端伪造头绕过登录限流；公共接口不会注册管理员路由。

## 历史：首次空站替换部署

这是“新建空站”，不恢复旧业务库。只导入经过审查、不含节点或订阅凭据的 1,709 条规则和大陆 IP 数据。部署前备份旧 PostgreSQL、活动发布目录、共享配置、PM2 清单及 Nginx；本地 SQLite/主密钥及旧节点账本另行备份。

本地构建 `scripts/build.py` 与 `scripts/build_desktop.py`，使用已提交源码生成发布目录，包含 `hqip`、`artifacts`、`deploy` 和 `release.json`。后者记录完整 `sourceSHA` 与每个发布文件的 `sha256`。发布目录权限 755，文件不得携带运行凭据。上传规则包到 root 专属目录。

在目标服务器执行（将占位值替换为真实发布值）：

```sh
python3 /root/deploy_nginx.py prepare --state /root/backups/hqip-control-RELEASE --release /opt/hqip-control/releases/SHA --rules /root/hqip-rules.json
# 检查两套回环接口、规则哈希和空数据库后切换
python3 /root/deploy_nginx.py activate --state /root/backups/hqip-control-RELEASE
# 公网验收通过后才停止旧站自启动
python3 /root/deploy_nginx.py finalize --state /root/backups/hqip-control-RELEASE
```

`prepare` 不接管旧站；重复准备拒绝覆盖新数据库。`activate` 停止旧站写入并保存最终 PostgreSQL 备份，然后测试和重载 Nginx；失败会恢复旧站。`finalize` 只删除旧 HQIP 两个 PM2 进程并禁用其专属守护单元，不删除数据库、不停止 PostgreSQL，也不操作其他用户的 PM2 服务。

初始管理员为 `admin`，随机密码写在 `/var/lib/hqip-control/bootstrap-admin.json`，首次登录必须修改。部署验收可能已完成首次改密，交付的受限凭据文件是最新密码。旧管理密码、设备账号与订阅均不适用于新站。备份或部署日志不得打印密码。

## 历史：旧环境退役与设备版客户端

新站验收后，停止 Windows `Stop-HQIP.ps1`，运行 `Register-HQIPStartup.ps1 -Remove`，备份最终数据并把旧控制配置标记为退役。23.175.24.91 的 `hqip`、`hqip-agent`、`hqip-hysteria`、`hqip-caddy` 保持停止及禁用，删除已备份的隧道公钥授权；保留数据和证书档案。

新版 Windows 包默认使用新域名。已有用户须先断开代理并退出旧设备登录，然后更换服务地址，以新站的设备账号登录。空站没有节点和设备，须在管理网页添加服务器、验证连接并创建设备后才能代理。保留原 Clash 配置；旧订阅将不可用。

## 升级、备份与回退

后续升级保留 `/var/lib/hqip-control` 和实际业务数据库，先备份，再停止服务、切换 `current` 至验证后的新发布目录、重新启动和检查。不要重复运行首次替换脚本，不要重新初始化主密钥。下列命令是 SQLite 环境示例；PostgreSQL 环境按照[数据库运行说明](postgresql-redis-upgrade.md)备份，不用 SQLite 快照替代在线 PostgreSQL 数据。

```sh
runuser -u hqip-control -- /opt/hqip-control/current/hqip backup --data-dir /var/lib/hqip-control --output /var/lib/hqip-control/backup-YYYYMMDD.tar.gz
systemctl status hqip-control
```

备份包含主密钥，文件权限 600，并复制到独立受限存储。用 `hqip restore --input ... --data-dir <空目录>` 验证恢复；切换数据目录前先停止服务。恢复会撤销登录会话并提升配置版本，需重新登录。

首次替换回退：`python3 /root/deploy_nginx.py rollback --state /root/backups/hqip-control-RELEASE`。它恢复 Nginx 和旧站 PM2，停用新控制服务；两套数据库不合并。旧测试节点退役后不会因网站回退自动恢复，需按其受限备份单独恢复服务及授权。
