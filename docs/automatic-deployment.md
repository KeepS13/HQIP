# GitHub 自动部署

`main`的CI先运行Linux、Windows及真实PostgreSQL/Redis回归，全部成功才允许部署。PR只测试。过时提交跳过，正在执行的部署不会被新推送取消。

Windows 普通回归与客户端打包分别运行；真实客户端验证在打包成功后分为计费/断网、跨节点名额、到期时间修改三组并行执行。三组只读取同一次运行、同一提交的客户端产物，先校验干净构建标记、归档摘要和固定内核摘要，再启动隔离的代理进程。任何一组失败均阻止部署，保留原有 race、Vision、统计、PostgreSQL/Redis 和支付幂等检查，并持续覆盖名额释放与重连。Go 缓存键覆盖实际使用的根模块、桌面端和 HY2 子模块；缓存仅用于加速，发布产物始终重新构建并校验。

仓库变量`HQIP_DEPLOY_PAUSED=true`只暂停deploy作业，测试与构建继续运行。保留数据迁移期间保持暂停；目标验收完成后将其设为`false`或删除。日常CI不会调用`init-admin`、重置数据或运行fresh activate。本文描述后续部署要求，不表示迁移已完成。

## 一次性配置

目标必须先通过[保留数据迁移](server-migration-sharevps.md)建立可验证的正式基线，初次接管保留源站的精确活动 release 与数据。后续 CI 只接受 `64.83.17.143:22/root`，旧 IP `107.151.244.106` 在任何 SSH 前被拒绝。Actions secrets 必须由迁移执行者同步设置：`DEPLOY_HOST=64.83.17.143`、`DEPLOY_PORT=22`、`DEPLOY_USER=root`、新目标的 `DEPLOY_SSH_KEY` 与 `DEPLOY_KNOWN_HOSTS`。SSH 严格验证主机身份，不能沿用源站主机密钥；验收后移除旧 HQIP 部署专用授权。

控制服务运行PostgreSQL＋Redis；存储环境文件必需。Ubuntu 22.04 目标必须提前提供独立 Python 3.11 入口 `/opt/hqip-runtime/bin/python3`，以及 `/usr/lib/postgresql/18/bin` 中的 PG18 备份/恢复工具与 psql、两个正式站点和证书。远程磁盘检查、安装、版本保留及回退均显式使用独立 Python；安装器子脚本继承同一解释器，备份 service 的 ExecStart/ExecStopPost 也使用此入口。路径缺失时部署失败，不回退到系统 Python 3.10。运行时及库由 root 管理且可被 service 沙箱读取；保留系统默认 Python 和现有 PG14。不要把密码、运行数据或旧备份放进发布包。

## 完整发布与回退

Linux作业构建控制程序、两架构Agent/HY2及校验过的Caddy，输出当前提交的manifest；Windows作业生成同一提交的客户端。部署作业将上述产物和两个网页打成完整包。安装器要求包内文件与清单完全一致，缺失任何文件即失败，不能从旧release补齐。

当前生产SHA只作为并发部署基线检查；旧目录不再提供构建输入或节点二进制。服务器重新核验每个文件摘要、节点核心来源和客户端来源，拒绝错基线、路径越界、符号链接和重复发布目录。

Actions 产物保留 3 天。主分支上传受存储配额或传输故障阻塞时，CI 自动使用同一私有仓库的临时草稿 Release 传递产物，PR 不启用此路径。草稿按运行 ID 和平台隔离，重试只接受同一提交；下载校验提交、干净构建标记、传输摘要和二进制摘要，并限制解压路径。生产验收成功后删除本次草稿；失败保留以供重试。构建与部署任务的 `contents: write` 用于草稿上传/清理，生产 SSH 凭据仍只出现在原有部署步骤中。

升级先在线一致性备份PG和配套密钥、准备兼容的增量结构，再短暂停止控制服务并切换代码及准确站点配置，检查本机两路`/readyz`、公网两个网页以及API的`/healthz`和`/readyz`。两个API探针都须通过。失败仅回退兼容的代码与路由，保留实时PG数据库。数据结构不兼容时拒绝回退并向前修复。

Actions摘要记录提交和`/root/backups/hqip-ci-<SHA>-<运行ID>-<重试次数>`。备份含密钥，必须另行加密、独立保存，不能上传Actions或源码库。手动回退使用对应失败运行的精确状态目录：

```sh
/opt/hqip-runtime/bin/python3 -B /opt/hqip-control/releases/<SHA>/scripts/upgrade_domains.py rollback --state /root/backups/hqip-ci-<SHA>-<运行ID>-<重试次数>
```

不得覆盖随后成功的发布，或用旧SQLite/旧站备份抹去新站数据。同提交已经活动时只验收就绪状态；失败发布目录不会被重复覆盖。
