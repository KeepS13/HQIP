# PostgreSQL 与 Redis 架构升级

正式部署基线为 PostgreSQL 18＋Redis 8。此次重新开空站，不导入旧SQLite业务数据；执行与验收以[全新空站部署](fresh-deployment.md)为准。必须完成真实数据库回归、备份恢复和压力测试，不得仅凭单元测试宣称容量达标。

## 数据边界

- PostgreSQL 是用户、会话、订阅、流量预留、租约结算、审计及部署任务的唯一事实来源。Redis 不保存唯一余额，不参与最终扣量。
- Redis 负责认证入口的共享限流。每个键保存有限数量的事件并自动到期；IP/账号只以散列键保存。远程 Redis 强制 TLS；配置 Redis 后故障拒绝新的受限请求，不降级到可绕过的进程计数。
- 客户端、节点 Agent 和控制 API 保持现有协议。新站生成新的管理员和主密钥；旧节点由管理员重新添加部署。节点本地SQLite断线结算账本与控制中心业务存储是不同用途，不能随控制中心重构删除。

## 必须验证的并发约束

1. 同订阅的租约竞争不能超卖流量或连接数；预留与租约创建在同一事务提交。
2. 重复/乱序结算不会重复扣量；关闭租约回收预留恰好一次。
3. Agent 重发累计流量不能重复加到 lifetime；服务器周期归属保持原有规则。
4. 刷新令牌、授权码、人机验证令牌只能消费一次；多进程共享状态。
5. 部署/证书/通知任务只能由一个有效所有者处理；崩溃后按持久租约恢复。

## 初始化与发布约束

- 新站先以迁移角色显式初始化空schema，非空目标拒绝覆盖。PG和新配套数据目录在独立暂存环境验证，旧站暂时继续服务。
- 切换前完成旧节点退役、最终结算与归档，再停止旧控制。最终回执核对通过才替换正式目录；禁止同时运行两份业务写入中心。
- PostgreSQL 开始接收新写入后，旧 SQLite 快照不能作为无损回滚目标。代码回滚必须仍连接当前 PostgreSQL；若要退回 SQLite，必须先停止写入并反向迁移核对。
- 数据库与 Redis 仅允许内网/回环访问。生产使用独立应用角色、SCRAM/TLS、受限密钥文件；开发测试身份不得复用。
- 日常发布只读取新站PG配置，备份和回滚都保留当前PG；旧SQLite历史工具不属于新正式发布包。

## 配置文件与角色

`python3 scripts/prepare_storage.py --output /etc/hqip-storage` 只生成新的私有配置目录，不启动服务、不迁移数据，也不会覆盖已有目录。使用其中的 `compose.yaml` 启动隔离存储环境后，按以下角色执行操作：

- `migration.env` 使用 `hqip_migrator`，仅供离线导入、全新站点 `init-admin --initialize-postgres`、空数据库恢复使用。它拥有 schema 创建权限，不得作为控制服务的长期身份。
- `storage.env` 使用 `hqip_app`，仅有应用表的读写及序列使用权限。完成初始化或迁移后才将这个文件以 `0600` 安装至 `/etc/hqip-control/storage.env`；正常启动和备份都使用这个身份。不要使用应用身份执行建表或恢复。
- PostgreSQL 备份工具 `pg_dump`、`pg_restore` 必须单独安装在控制机上，并与数据库主版本兼容。环境文件不安装这些工具。
- `secrets/` 和其父目录保持 `0700`，宿主机其他用户不能访问。挂载给容器的文件为只读 `0444`，供容器内部的非 root 用户读取；连接配置文件保持 `0600`。Docker Compose 的本地文件 secret 使用 bind mount，不会代为转换文件权限，见 [Docker 官方说明](https://docs.docker.com/reference/compose-file/services/#secrets)。

正式`hqip-control.service`要求`storage.env`存在，并依赖`hqip-storage.service`。初始化与日常CI严格分开，缺少配置时拒绝启动；`postgresql.enabled`也会拒绝回退SQLite。CI会启动生成的Compose配置，验证应用身份不能建表、业务读写可用，且Redis ACL允许限流并拒绝其他键和管理命令。

## Redis 认证限流配置

控制程序读取 `HQIP_REDIS_URL` 与 `HQIP_REDIS_NAMESPACE`（默认 `hqip`）。配置文件通过 systemd EnvironmentFile 或部署密钥管理注入，不将密码写入命令行、仓库或日志。所有同站点控制实例必须使用相同 Redis 和命名空间。

每个认证维度维持现有每 15 分钟 10 次上限，采用 Redis 服务端时间的原子滑动窗口；现有密码验证并发上限保留。连接池、等待时间和请求时间都设上限。Redis 不可用返回 503 和 Retry-After；达上限返回 429。

## 验收证据

必须记录 PostgreSQL/Redis 版本、数据规模、并发数、持续时间、吞吐、p95/p99、错误率与连接池等待。测试覆盖正常流量、重复上报、超额竞争、数据库锁等待及 Redis 故障。没有实测数据时不声称支持特定用户规模。
