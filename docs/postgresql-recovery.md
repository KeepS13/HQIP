# PostgreSQL 灾备恢复

完整发布包自带 `scripts/restore_postgres.py`。它面向明确授权的灾备恢复，日常 CI 不调用；代码回退仍保留当前数据库，不能拿历史快照覆盖新增订单或用量。

新发布清单声明 `postgresRecoveryVersion: 1` 并强制包含恢复脚本。历史完整包没有此能力字段时仍可经过完整哈希及节点、客户端来源校验保留为代码回退；历史包不能作为新的发布输入，也不提供本页的恢复工具。

备份以应用账号执行 `hqip backup`，同时包含 PostgreSQL custom dump 和配套 `master.key`。归档须加密保存到独立存储，并记录 SHA-256。恢复使用迁移账号，正常服务和恢复后的再次备份使用应用账号。

`hqip restore` 重建 `public` schema；备份有意不携带原环境 ACL。恢复之前设置的 schema 权限和默认授权会随旧 schema 删除，所以恢复后必须重新授权。独立恢复脚本会验证迁移身份、数据库和 schema 归属、应用账号没有管理权限，以及 schema 版本和恢复安全锁，然后仅向 `hqip_app` 授予 schema USAGE、表 CRUD、sequence USAGE/SELECT 和同样的默认权限。它不会创建角色、授予角色成员资格或给应用账号 DDL、TRUNCATE、sequence UPDATE 权限。

## 准备独立空目标

在隔离验收库或灾备主机上先创建空数据库，例如 `hqip_recovery_20261108`，owner 为 `hqip_migrator`；禁止覆盖正在接收业务写入的库。按当前部署策略撤销 PUBLIC 的数据库权限，授予 `hqip_app`、`hqip_migrator` CONNECT，撤销 PUBLIC 对 public schema 的权限。禁止应用账号 CREATE、TEMP 或迁移角色成员资格。

把应用与迁移环境文件复制到一个 0700 目录，保持文件 0600，仅修改两份配置中的数据库名，使其都指向这个明确的空目标。不要在命令行传连接串或用 shell 输出环境文件。恢复仍使用回环 PostgreSQL 18 的 15432 端口；不得重新运行 `prepare_storage.py` 覆盖已有存储配置。

目标主机须先提供独立 Python 3.11 `/opt/hqip-runtime/bin/python3`，及 `/usr/lib/postgresql/18/bin` 下的 `psql`、`pg_dump`、`pg_restore`。恢复工具显式检查PG18工具，并把PG18路径传给恢复及验证备份子进程；不改变Ubuntu 22.04系统Python或已有PG14服务。使用经过完整清单校验的 release，以及已核对摘要的备份：

```sh
/opt/hqip-runtime/bin/python3 -B /opt/hqip-control/releases/完整40位提交/scripts/restore_postgres.py \
  --release /opt/hqip-control/releases/完整40位提交 \
  --backup /root/backups/已解密受限备份.tar.gz \
  --backup-sha256 已核对的64位SHA256 \
  --database hqip_recovery_20261108 \
  --migration-env /root/recovery/migration.env \
  --app-env /root/recovery/storage.env \
  --data-dir /var/lib/hqip-recovered-20261108
```

目标数据目录必须不存在或为空，并且不能是现用的 `/var/lib/hqip-control`。脚本拒绝非空数据库，核对归档 SHA、主密钥、结构及实际角色；以迁移身份恢复并重建权限，再以应用身份执行 `storage-info` 和一次真实备份，验证完整性、全部已持久化加密记录及备份授权。最后把恢复目录交给 `hqip-control` 系统账号，保持目录 0700、文件 0600。成功只输出脱敏回执，绝不自动启动控制服务或关闭恢复安全锁。

若目标数据库明确名为正式 `hqip`，两个 HQIP 控制 unit 都必须已停止。先在独立库演练通过；真正替换正式环境前停止唯一业务写入者、保留当前完整备份及目录，再按已验证的步骤恢复。脚本不创建或删除数据库，也不自动清理失败目标；出错时保留备份、恢复目录和库，查明具体阶段后向前修复，不能直接重跑来覆盖已有数据。

## 恢复验收与启用

核对回执 `ok`、`applicationGrantsVerified`、`applicationBackupVerified` 为 true，主密钥及 schema 与备份匹配；对照业务用户、订阅、订单及流量账本的数量和摘要。隔离演练结束后只删除该精确测试库与目录，不更改正式库。

正式恢复完成后再切换配套数据目录和应用角色环境，启动控制服务并检查两路 `/healthz` 与 `/readyz`。恢复安全锁必须继续保持，等待节点账本补报、租约结算和人工核对；不要为了恢复可用性直接释放未知预留。确认无误后使用产品既有恢复流程解除安全锁。Redis 限流事件不作为业务余额备份恢复。
