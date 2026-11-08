# 新服务器部署与运行

HQIP的唯一正式目标为 `107.151.244.106`。用户网页 https://hqip.sharevps.online，管理网页 https://cms-hqip.sharevps.online，API及客户端下载 https://api-hqip.sharevps.online。两个前端由Nginx独立托管，控制服务仅监听回环API端口。

首次完整重建按[全新空站部署](fresh-deployment.md)执行；用户、订阅、订单、服务器、节点及商业配置重新建立，旧数据仅留受限备份。日常发布按[自动部署](automatic-deployment.md)执行，绝不能重新初始化或清空业务数据。

| 对象 | 正式配置 |
| --- | --- |
| 控制服务 | `hqip-control.service`，用户 `hqip-control` |
| 发布目录 | `/opt/hqip-control/releases/<完整SHA>`，`current`指向活动版本 |
| 配套密钥与运行数据 | `/var/lib/hqip-control`，权限0700 |
| 业务存储 | PostgreSQL 18，回环15432；应用角色`hqip_app` |
| 认证限流 | Redis 8，回环16379；受限`hqip`角色 |
| 存储服务 | `hqip-storage.service`，配置`/etc/hqip-storage` |
| 控制运行配置 | `/etc/hqip-control/storage.env`，必需、0600 |
| 管理/公共API | `127.0.0.1:18090`、`127.0.0.1:18091` |
| 网站配置 | `/etc/nginx/sites-available/hqip.conf`，仅三个正式域名 |

PostgreSQL是唯一业务事实来源。Redis不保存唯一余额或最终扣量；故障时新的受限认证请求返回503，不降级为进程内计数。启动和发布检查使用`/readyz`，同时验证PG及Redis；`/healthz`只说明进程存活。

初始管理员随机密码写入`bootstrap-admin.json`，0600，首次登录必须修改。支付、注册、人机验证和业务DNS由管理员重配；网站HTTPS证书与Certbot续期独立保留。日志不得记录密码、URI令牌、Cookie或Authorization。

备份必须使用配置过应用角色环境的`hqip backup`，同时包含PostgreSQL数据库与配套master.key；安装匹配主版本的`pg_dump`和`pg_restore`。归档文件另行加密并保存到独立存储，使用完整发布包的`restore_postgres.py`在空库恢复、重建应用权限并验证应用账号备份，详见[PostgreSQL灾备恢复](postgresql-recovery.md)。代码回退保留当前PG数据；不得启动旧SQLite副本或恢复已退役商户配置。

旧主机仅清理HQIP专属对象，保留其他项目及共享基础设施。旧域名不提供重定向或兼容桥接。
