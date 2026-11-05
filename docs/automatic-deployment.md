# GitHub 自动部署

推送或合并到 `main` 后，`CI` 的 Linux `check` 和 `windows` 作业分别测试、构建。同一次运行中的两个作业都成功后，`deploy` 才更新生产服务器。PR 只测试；也可以在 Actions → CI → Run workflow 选择 `main` 手动重跑。过时的提交跳过部署，正在执行的部署不会因新推送而被取消。

服务继续由 systemd 的 `hqip-control.service` 管理，两个网页由 Nginx 提供，不需要安装 PM2。本地保存文件不会上线，必须提交并推送。

## 一次性配置

仓库 Settings → Secrets and variables → Actions 中配置：

| Secret | 内容 |
| --- | --- |
| `DEPLOY_HOST` | 生产服务器 IP 或主机名 |
| `DEPLOY_PORT` | SSH 端口 |
| `DEPLOY_USER` | SSH 用户，当前为 root；其他用户必须支持 `sudo -n` |
| `DEPLOY_SSH_KEY` | 无口令部署专用 OpenSSH 私钥 |
| `DEPLOY_KNOWN_HOSTS` | 从可信 SSH 会话核验的主机公钥记录 |

部署公钥须已加入该用户的 `authorized_keys`。默认 22 端口的主机记录为 `主机名 ssh-ed25519 公钥`，其他端口为 `[主机名]:端口 ssh-ed25519 公钥`。服务器需要 Python 3.11+、现有三域名服务、证书和完整 `release.json` 清单。

## 发布与回退

发布包含已测试的 Go 控制程序、管理网页、用户网页和同提交的 Windows 下载包。Agent 和 HY2 节点产物从当前发布目录按 SHA-256 校验复用；节点升级仍使用已有专门流程。部署不会自动在节点上运行升级或重新初始化账号、数据库和主密钥。

SSH 严格校验主机身份。服务器再次核验上传包和所有文件摘要，拒绝错基线、路径越界和重复发布目录。升级复用 `upgrade_domains.py`：先冷备份数据库与主密钥及 Nginx/systemd 配置，然后切换版本、检查本机两路健康接口，再检查公网三个域名。失败回退代码与路由，保留实时用量数据库。数据库 schema 不兼容的升级需要专门设计迁移方案。

Actions 的部署摘要记录提交 SHA 和 `/root/backups/hqip-ci-<SHA>-<运行ID>-<重试次数>`。备份包含私密数据，不能上传到 Actions 或仓库。保留发布目录和备份；清理策略需按实际磁盘空间另行制定。

手动回退（使用失败运行摘要中的精确路径；不能覆盖后来成功部署的版本）：

```bash
python3 -B /opt/hqip-control/releases/<SHA>/scripts/upgrade_domains.py rollback --state /root/backups/hqip-ci-<SHA>-<运行ID>-<重试次数>
```

上传或校验失败发生在停止服务之前。若已有同 SHA 的失败发布目录，先核对当前指向和失败原因再处理该目录，脚本不会覆盖它。服务器恢复后可重跑 CI；已是同提交的活动版本仅进行公网健康检查。
