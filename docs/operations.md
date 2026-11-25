# 运维

HQIP控制站当前运行于 `64.83.17.143:22/root`；旧站 `107.151.244.106` 的迁移记录见[保留数据迁移](server-migration-sharevps.md)。用户网页、API与客户端下载共用 https://hqip.sharevps.online，管理网页为 https://cms-hqip.sharevps.online。以已部署的 Nginx 配置及服务 `--public-url` 为准，不使用历史文档中的独立 API 子域名。控制服务使用PostgreSQL＋Redis；Windows只运行客户端，不承担控制服务或SSH反向隧道。

此次使用[保留数据迁移](server-migration-sharevps.md)，不能运行历史空站初始化、退役现有节点或重置商业配置；后续更新使用[自动部署](automatic-deployment.md)。服务路径与运行身份见[服务器部署与运行](server-deployment.md)。保留现有PG业务数据、配套主密钥和节点账本，不得用旧SQLite覆盖。

## 日常检查

Nginx托管两个前端，将API分发到 `127.0.0.1:18090/18091`。`hqip-control.service`以 `hqip-control` 用户运行；`hqip-storage.service`管理PostgreSQL18与Redis8，分别只监听回环15432和16379。应用数据库角色不能执行结构迁移；Redis使用受限的限流角色。

```sh
systemctl status hqip-control.service hqip-storage.service nginx.service
journalctl --namespace=hqip-control -u hqip-control.service -n 100 --no-pager
curl --fail --silent --show-error http://127.0.0.1:18090/readyz
curl --fail --silent --show-error http://127.0.0.1:18091/readyz
curl --fail --silent --show-error https://hqip.sharevps.online/healthz
curl --fail --silent --show-error https://hqip.sharevps.online/readyz
```

`/healthz`只证明进程存活；`/readyz`还检查PG和Redis，不能用前者代替后者。Redis故障时新的受限认证请求返回503，不退回进程内限流。检查存储服务、连接池、网络和磁盘后恢复依赖，不要通过删除环境文件或清空数据绕过检查。

管理和公共API只绑定回环地址，数据库端口不开放公网。网站证书续期与节点证书管理独立：Nginx保留Certbot验证路径；节点DNS、Cloudflare Token及证书状态在后台管理。客户端和节点均使用正式API地址，旧地址不自动重定向。

## 发布和回退

日常CI只接受固定目标 `64.83.17.143:22`、用户 `root`，明确拒绝旧IP，SSH主机密钥严格固定。迁移验收前保持部署暂停；迁移执行者须同步新目标的secrets。Linux、Windows和真实PG/Redis测试完成后，使用同一提交的完整发布包；网页、客户端、节点产物与清单均校验，不从旧release补文件。

升级先在线一致性备份当前PG数据库与配套主密钥，再短暂停止控制服务切换代码。备份完成、切换 `current` 前必须仍有至少3GiB可用空间；不足即拒绝切换。切换后验证本机两路就绪、两个网页和公网API。通过全部验收才保留本次current与previous并清理更老的、清单完整且校验通过的发布目录；异常目录跳过并留下审计记录，备份和业务数据不自动删除。

代码回退使用发布状态目录和 `upgrade_domains.py`，保留当前PG订单、订阅、用量及租约。目标代码须与当前schema兼容，不兼容时向前修复。不要运行旧初装脚本、重新 `init-admin` 或用旧站SQLite恢复业务。完整步骤见[自动部署](automatic-deployment.md)。

## 备份与恢复

备份应包含PostgreSQL和同一份 `master.key`。控制站Python脚本统一使用 `/opt/hqip-runtime/bin/python3`；备份与重试service的启动、异常记录也必须使用此独立运行时。使用正式应用角色环境及 `/usr/lib/postgresql/18/bin` 中的PG18工具，保留系统Python 3.10和现有PG14；不要通过全局PATH或系统链接调整实现兼容。归档另行加密、复制到独立受限存储；不能把密码、环境文件、私钥、令牌或数据库提交到Git或公开日志。

恢复只能先在独立空数据库演练。使用完整包中的 `restore_postgres.py`恢复数据库与配套密钥、重建应用授权，并以应用身份验证访问和再次备份。恢复后的权限检查不可省略：重新创建schema可能改变授权。详细命令与验收见[PostgreSQL灾备恢复](postgresql-recovery.md)。

节点本地账本与控制数据库应分别备份；节点中断期间可能积累未结算用量，不能为了“重新同步”删除账本。正式新站启用后，旧站加密归档只用于受控查验，不是自动回退目标。

## 节点、流量与预警

节点实例使用独立的 `hqip-agent-<节点ID>` 和 `hqip-hysteria-<节点ID>` 服务；配置、证书和账本按节点隔离。节点Caddy或HTTP验证仍可能用于手工域名证书，不能因为控制站改用Nginx而删除节点证书组件。具体路径、Cloudflare配置与安全退役见[多IP节点运维](multi-ip.md)。

节点异常先检查部署日志、证书应用、Agent心跳及真实代理验证；进程运行或控制端能连通不代表用户代理可用。节点离线期间显示零速率不等于没有使用，恢复后还需检查账本补报。

服务器、节点周期流量用于运维提醒，周期数据不完整时仍按已知用量统计并告警，同时标明覆盖不完整。用户订阅的额度、有效期与并发连接限制属于实际准入约束，不能与服务器容量提醒混同。连接授权失效时应排查具体订阅和准入错误，不要关闭鉴权或删除计量数据。

部署失败通知无需手工确认；其他需要确认的预警应记录处理备注，并由系统核验恢复状态。处理后检查预警详情和相关对象，确认原因已消除。

## 日志与磁盘

控制服务日志使用独立 `hqip-control` journal namespace：持久日志64MiB、运行日志16MiB、保留最长7天；只调整该namespace，不清理共享journal。HQIP的Nginx访问日志位于 `/var/log/nginx/hqip-control/access.log`，由专用logrotate规则按日或大小轮转，并让Nginx重新打开日志。

定期查看发布目录、备份、PG数据、Docker存储和HQIP日志增长。磁盘不足时先判断归属；不要删除数据库、Docker卷、节点账本、共享日志或未核验备份。发布保留策略不代替备份保留策略，备份清理需单独确认有效副本和恢复结果。
