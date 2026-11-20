# 新服务器部署与运行

HQIP迁移后的唯一正式目标为 `64.83.17.143:22/root`，来源为 `107.151.244.106`；当前迁移是否完成以执行验收记录为准。用户网页 https://hqip.sharevps.online，管理网页 https://cms-hqip.sharevps.online，API及客户端下载 https://hqip.sharevps.online。两个前端由Nginx独立托管，控制服务仅监听回环API端口。

此次按[保留数据迁移](server-migration-sharevps.md)执行，保留活动 release、用户、订阅、订单、服务器、节点、主密钥及商业配置。[历史全新空站流程](fresh-deployment.md)不适用于此次迁移。日常发布按[自动部署](automatic-deployment.md)执行，绝不能重新初始化或清空业务数据。

| 对象 | 正式配置 |
| --- | --- |
| 控制服务 | `hqip-control.service`，用户 `hqip-control` |
| 发布目录 | `/opt/hqip-control/releases/<完整SHA>`，`current`指向活动版本 |
| 配套密钥与运行数据 | `/var/lib/hqip-control`，权限0700 |
| 业务存储 | PostgreSQL 18，回环15432；应用角色`hqip_app` |
| 认证限流 | Redis 8，回环16379；受限`hqip`角色 |
| 存储服务 | `hqip-storage.service`，配置`/etc/hqip-storage` |
| HQIP Python | `/opt/hqip-runtime/bin/python3`，独立 Python 3.11；保留系统 Python 3.10 |
| PG18 客户端 | `/usr/lib/postgresql/18/bin`，HQIP 子进程优先使用；保留现有 PG14 |
| 控制运行配置 | `/etc/hqip-control/storage.env`，必需、0600 |
| 管理/公共API | `127.0.0.1:18090`、`127.0.0.1:18091` |
| 网站配置 | `/etc/nginx/sites-available/hqip.conf`，仅两个正式域名 |

PostgreSQL是唯一业务事实来源。Redis不保存唯一余额或最终扣量；故障时新的受限认证请求返回503，不降级为进程内计数。启动和发布检查使用`/readyz`，同时验证PG及Redis；`/healthz`只说明进程存活。

迁移保留管理员身份、支付、注册、人机验证和业务DNS配置，不重跑管理员初始化或生成新主密钥；网站HTTPS证书与Certbot续期一并核验。独立 Python 及库必须可被备份 service 沙箱读取，定时备份、重试和异常记录均使用该解释器。日志不得记录密码、URI令牌、Cookie或Authorization。

备份必须使用配置过应用角色环境的`hqip backup`，同时包含PostgreSQL数据库与配套master.key；安装匹配主版本的`pg_dump`和`pg_restore`。归档文件另行加密并保存到独立存储，使用完整发布包的`restore_postgres.py`在空库恢复、重建应用权限并验证应用账号备份，详见[PostgreSQL灾备恢复](postgresql-recovery.md)。代码回退保留当前PG数据；不得启动旧SQLite副本或恢复已退役商户配置。

源主机在迁移验收及回退窗口结束前保留停止状态的HQIP对象；后续清理仅限HQIP专属资源，保留其他项目及共享基础设施。双域名部署仅保留 hqip 与 cms-hqip；API 使用 hqip。
