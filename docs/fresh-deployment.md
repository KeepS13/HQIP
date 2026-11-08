# 新服务器全新空站部署

正式目标只使用 `107.151.244.106` 和 `hqip.sharevps.online`、`cms-hqip.sharevps.online`、`api-hqip.sharevps.online`。控制数据库使用 PostgreSQL 18，Redis 8 提供共享认证限流。旧业务数据不导入，旧域名不转发、不兼容。用户、订阅、订单、服务器、节点、支付、人机验证及业务 DNS 配置重新建立；现有三个网站的 HTTPS 证书和续期保留。

这是一次显式重新开站流程，与日常 CI 升级分离。`prepare` 在独立目录和端口验证，不停止现有控制服务。`activate` 只有在旧节点已退役、旧控制已停止、最终备份已验证后才切换。任一步失败保留证据和数据，不自动启动旧业务库。验收之前保持 `HQIP_DEPLOY_PAUSED=true`。

## 产物和主机前置条件

- 使用已提交源码构建控制程序、两套网页、Windows 客户端、两种架构的 Agent/HY2 以及校验过的 Caddy。执行 `python scripts/build.py --skip-web --server-only`、Windows 的 `build_desktop.py`，然后 `python scripts/package_domains.py`。服务端模式不构建或下载旧版 Windows CLI，不能与 `--skip-go` 同用；桌面端由独立构建提供。首次打包不传 `--previous-source-sha`。
- 完整包为 `dist/hqip-full-<SHA前12位>.tar.gz`，摘要见 `dist/domains-release.json`。包内每个文件都有哈希，Agent、节点核心、客户端来源与本次提交对应，不从旧发布目录复用文件。
- 完整包同时携带 HQIP 日志策略，准备、激活及后续升级均显式安装：`hqip-control` journal 命名空间限制磁盘64MiB、运行时16MiB、最长7天；Nginx只轮转 `/var/log/nginx/hqip-control/access.log`，每日或检查时超过10MiB保留7份并压缩。该路径避开系统的Nginx日志通配规则，轮转仅通过USR1复开日志，不重启共享Nginx。大小阈值在系统logrotate调度时检查，不是写入时硬截断。
- 新机已安装 Nginx、Docker Engine/Compose、Python 3.11+、PostgreSQL 18 的 `pg_dump`/`pg_restore`；已有三个网站的有效 TLS 证书、受信任的 SSH 主机密钥和足够存储空间。数据库端口不得暴露公网。
- 将受校验的 `scripts/install_ci_release.py` 和完整包上传新机，通过 `python3 -B install_ci_release.py --fresh <完整SHA> <包路径> <包SHA256>` 解包。它只安装发布目录，不改变 `current` 或数据。
- 只在 `/etc/hqip-storage` 不存在时运行 `python3 <发布目录>/scripts/prepare_storage.py --output /etc/hqip-storage`。生成独立随机凭据，绝不覆盖已有配置；禁止使用演示或 CI 密码。

## 准备和切换

以下 `<发布目录>` 为 `/opt/hqip-control/releases/<完整SHA>`，`<状态目录>` 为 `/root/backups/hqip-fresh-<时间>`，必须为新目录。

```sh
python3 -B <发布目录>/scripts/fresh_deploy.py prepare --release <发布目录> --state <状态目录>
python3 -B <发布目录>/scripts/fresh_deploy.py verify --state <状态目录>
```

`prepare` 核对完整产物、存储模板、工具版本和网站证书，启动受限存储栈，拒绝非空 PostgreSQL。完整包已解出后、准备完成后和正式激活前，各目标文件系统必须实测仍有至少3GiB可用空间，为同机其他项目保留余量。用迁移角色执行一次 `init-admin --initialize-postgres`，随后用应用角色运行临时服务。新数据位于 `/var/lib/hqip-control-fresh`，临时接口只监听 `127.0.0.1:18190/18191`。临时与正式服务均通过 `/readyz` 检查 PostgreSQL 和 Redis。初始凭据在暂存目录的 `bootstrap-admin.json`，权限0600，不打印密码。默认注册、用户门户、支付关闭，不复制旧商业配置。

准备验证通过后，通过现有节点退役流程停止旧节点准入、结算并备份本地账本，再停止现有控制服务。对最终旧数据和配套密钥加密归档，复制一份到独立受限存储并完成恢复核对。不要在最终快照后重新启动旧控制服务。

```sh
python3 -B <发布目录>/scripts/fresh_deploy.py inventory
```

上述命令只输出正式旧数据目录及其文件清单摘要。由完成备份核验的操作者写入0600的最终回执，字段如下；摘要必须来自实际文件和上述命令：

```json
{
  "version": 1,
  "verified": true,
  "sourceDataDir": "/var/lib/hqip-control",
  "sourceInventorySHA256": "实际最终目录摘要",
  "archive": "/root/backups/实际目录/最终加密归档文件",
  "archiveSHA256": "实际归档摘要"
}
```

脚本不会将 `verified` 标记代替备份恢复核对。执行者必须先完成核验再生成回执。脚本会再次计算目录及归档摘要，并确认旧服务已停止；归档后发生的新写入会拒绝切换。

```sh
python3 -B <发布目录>/scripts/fresh_deploy.py activate --state <状态目录> --retirement-receipt <最终回执路径>
python3 -B <发布目录>/scripts/fresh_deploy.py verify --state <状态目录> --public
python3 -B <发布目录>/scripts/fresh_deploy.py finalize --state <状态目录>
```

激活仅将旧 `/var/lib/hqip-control` 移至 `<状态目录>/final-retired`，将新暂存目录移为正式目录，安装应用角色环境文件、准确的三个站点和控制服务；不删除旧数据、不覆盖其他站点。为确保移动原子性，三个目录必须位于同一文件系统。发布与新站命令共用部署锁。

`finalize` 生成初始 PostgreSQL＋master.key 备份，停止并删除临时服务。备份tar.gz本身没有加密，必须另行加密并复制到独立受限存储。正式交付前按[PostgreSQL灾备恢复](postgresql-recovery.md)将这份备份恢复到独立空数据库，重建恢复后schema的应用授权，验证管理员、结构、配套主密钥及应用身份再次备份。初始化完成后只允许日常代码升级，不能重跑初始化以覆盖用户配置。

## 首次开放用户入口

1. 管理员首次登录先修改初始密码。按业务需要配置“域名与证书”、服务器、节点及订阅模板；DNS/证书的Cloudflare Token不等于登录验证码凭据。
2. 在“注册与安全验证”保存Cloudflare Turnstile的Site Key、Secret Key和允许的公开域名，确认显示“已配置”。允许域名应包含正式用户网页 `hqip.sharevps.online`。
3. 在同页“用户门户”点击“启用用户门户”并确认。保存验证码或点击“开放注册”都不会自动执行此步骤。启用会停用旧设备鉴权、撤销旧客户端会话并更新节点配置；所有活动节点必须具备30秒内健康心跳、当前配置确认和中央准入能力，否则接口拒绝启用。空站没有活动节点时仍可启用登录入口。
4. 按需要单独操作“公开注册”开关。启用门户不会更改该开关；注册关闭时已有用户仍可完成安全验证后登录。门户未启用或Turnstile配置不完整时，用户入口保持关闭。
5. 再配置支付、模板售价并验收真实小额付款、订阅开通和VPN连接。完成配置后备份PG与配套主密钥；不重新初始化数据库。

## 验收和退役

- 空站检查：用户、订阅、订单、服务器、节点为0；新管理员首次改密；旧登录和节点凭据无效。
- 只监听回环的15432/16379和18090/18091；正式应用身份不能建表；Redis ACL拒绝管理命令和非限流键。未配置PG/Redis不得作为SQLite服务启动。
- 验证两套网页、API `/healthz`和`/readyz`、客户端版本和下载哈希、重启恢复。两个探针都必须成功，进程存活不能代替存储就绪。隔离环境验证Redis故障503、重复结算幂等、并发租约不超卖、支付通知幂等；记录真实负载下p95/p99、错误率及连接池等待。
- 完成新站和备份验收后，按精确HQIP清单清除旧主机程序、运行数据、专用服务、定时任务、凭据和三个旧域名虚拟主机。不能停止共享Nginx、Tailscale、其他站点，也不能删除共享证书父目录。
- `207.57.127.105` 不再承载HQIP业务；旧支付回调、旧订阅URL及旧域名客户端入口停止使用。新业务配置由管理员重新录入。
- 新站启用后回退只能换兼容代码并保留当前PostgreSQL；不能自动恢复旧用户、旧SQLite、旧商户配置或旧域名。中途失败保留新旧目录和状态，核对后向前修复。
