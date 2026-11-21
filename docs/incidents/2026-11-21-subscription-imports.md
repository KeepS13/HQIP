# 2026-11-21 订阅合并链接部署

正式服务器 `64.83.17.143` 已部署提交 `848f73c854815b09feb33e4d6f4656f9c323b602`。2026-11-21 19:47 UTC 验收通过，控制端、用户站、管理站及同提交 Windows 下载包均来自完整发布包。

## 已上线行为

- 进入复制页面自动准备链接，无需用户手动创建。默认复制全部有效订阅的一个合并链接，也可选择单份。
- 合并链接包含复制时的有效订阅集合；新购订阅后须重新复制。任一成员轮换或撤销，包含它的旧合并链接整体失效。
- 轮换同时作废对应旧链接、第三方节点凭据和活动租约；节点同步撤权后断开相关连接。页面说明重新导入要求及可能的同步延迟。
- 账户概览“从这里开始”的第三项加入 QQ 群 `383590149`，支持复制群号。

## 发布来源与恢复

- GitHub run `44940946113` 的三个构建／测试作业均因账户付款或消费额度限制未启动，部署作业被跳过，不能将此运行视为 CI 验证通过。
- 使用提交 `848f73c` 的独立、干净 Git worktree 运行既有构建器，服务端与 Windows 清单均为同一 `sourceSHA`、`dirty=false`；用户原有节点测速代码、对应测试和 `docs/client.md` 改动未进入发布。
- 完整包 `hqip-full-848f73c85481.tar.gz`，87,427,049 字节，SHA256 为 `63ab91a3beaa14a66d63691952978c8046a63beb9794872a225fbf876d7ce9ce`。通过已固定主机指纹的 SSH 通道上传并在服务器复核摘要。
- 沿用 `package_domains.py`、`install_ci_release.py` 和 `upgrade_domains.py` 执行完整发布，原活动版本为 `78af0ed2a0a9fe108bd1bcb91f25d03115337682`。未重新初始化账户、存储或主密钥。
- 一致性备份：`/root/backups/hqip-ci-848f73c854815b09feb33e4d6f4656f9c323b602-44940946113-1`；`backupMode=online-consistent-snapshot`，安装最终 `phase=active`，部署进程退出码为 0。回退沿用该目录与 `upgrade_domains.py rollback`，保留实时 PostgreSQL 数据。

## 本地发布验证

- 两套网站及 Windows 下载包构建通过，桌面核心来源和文件哈希验证通过；用户端 8 项、管理端 20 项前端测试通过。
- Windows `go vet -tags=with_utls ./...`、`go test -tags=with_utls ./...`，以及 desktop 模块的 `go vet ./...`、`go test ./...` 通过。
- Linux 各包 race 回归通过；受存储环境影响的 store、control、pgstore、ratelimit、cmd/hqip 在独立 PostgreSQL 18、Redis 8 的回环地址上重跑通过，包含本次合并链接与并发准备回归。首次容器 DNS／非回环 URL 配置失败保留在诊断日志，未作为通过依据。
- Linux Python 3.12 下全部 251 项脚本测试通过。Windows 符号链接权限及 Python 3.11 的测试替身兼容失败，经与 CI 一致的 Linux Python 3.12 环境复验解决，未改动生产脚本。
- 干净发布目录的浏览器验收通过：7 个客户端、自动准备、合并／单份选择、轮换与撤销竞态、复制下载、QQ 群号复制和响应式布局。
- 配套 sing-box 的 `TestPortalSingBoxPinnedCoreCheck` 通过。可选的 `TestPortalSingBoxExportRealHY2Routing` 在当前与发布前提交均失败：该测试仍要求导出服务器为回环 IP，而现有导出使用节点域名。本次未修订该历史测试，不宣称完整真实 HY2 导出链路验收通过。
- 暂存全部新增文件后的 GitNexus 检查报告 26 个变更文件、114 个符号、24 个流程，风险为 CRITICAL，涵盖共享路由和展示组件拆分及工作区原有改动；部分 Go 调用仍为 UNKNOWN，已辅以实际调用点和功能回归核查，未作无影响结论。

## 线上验收

- 正式进程二进制哈希与活动 release 清单一致，本机管理／公共两路 `/readyz` 均返回 `ok=true`。
- 公网用户站、管理站返回 200，HTML 和主 JS 的摘要与本次清单一致；用户 JS 包含新的合并链接提示及 QQ 群号。
- 公网 `/healthz`、`/readyz` 均为 `ok=true`；新 `/api/v1/user/import-link` 对匿名请求返回 401。
- 发布前后均为 402 个用户、147 份订阅、24 个节点记录；15 个活动节点全部核心健康。本次未轮换真实用户的订阅凭据，未重启节点代理核心。

库外构建、测试与验收记录位于 `.local/import-link-*`、`.diagnostics/import-link-*`。本次隔离测试容器及其临时数据卷已清理。GitHub 账户额度限制仍存在，需账户所有者另行处理。
