# 客户端自动构建与部署

本次发布接入 `client-runtime.updates v1`，版本唯一来源为 `packaging/client-version.json`。Windows 交付单个 EXE 安装包，Android 交付 ARM64 / x64 APK。Windows ZIP 只作为已安装客户端的内部更新载体。每次改变已发布的客户端都必须提高 `sequence`，不能用相同序列覆盖不同内容。

`GET /api/v1/user/installers` 返回三个安装包；下载使用 `/downloads/client/{sha256}/{filename}`。原 `/api/v1/user/releases` 和 `/download` 保持 Wails 兼容。新版更新入口为 `/api/v3/client/releases`，由固定 Ed25519 公钥验证。Windows 下载后下次启动切换版本，Android 仍需要用户确认系统安装提示。

三平台构建并行，每个任务在干净提交上产生独立回执。部署合并时验证提交号、版本、序列、平台全集、文件大小、SHA-256 和 Ed25519 签名；不从旧生产目录补文件。本轮主分支发布包含三个当前版本的更新包；旧 URL 不承诺永久保留，客户端应重新获取渠道目录。完整发布清单记录 `clientUpdatesVersion=1`，历史版本回滚继续允许缺少该能力。

正式签名只注入可信主分支任务。GitHub Secrets 使用 `HQIP_CLIENT_UPDATE_SEED`、`HQIP_ANDROID_KEYSTORE`、`HQIP_ANDROID_SIGNING_JSON`；保留原 `.local/godot-signing` 身份与库外备份。PR 使用一次性测试身份，不上传正式交付回执。工具缓存只列举公开压缩包；签名目录不进入缓存或构建产物，任务结束清理。

CI 将 Linux 竞态回归、内核集成回归、网站与 Linux 打包、Windows 平台回归、旧客户端打包、真实代理三个测试组、PostgreSQL/Redis 和新客户端三平台构建并行。保留支付并发重复验证、受限数据库角色/Redis ACL、双协议计费、节点额度、连接名额、到期撤权等门禁。Windows 新客户端执行全部 Godot 离线交互测试，以及隔离状态目录中的真实运行时管道验证。Android 两个 ABI 均编译并验证 APK 签名。

生产部署需要全部任务通过。继续使用严格 SSH 主机校验、完整包校验、磁盘预算、迁移前备份、就绪检查和失败回滚；新增公开签名渠道一致性、三个安装包分段与完整下载、Windows 更新 ZIP 完整下载验收。安装包下载服务对同一文件句柄校验 SHA-256 后发送，防止发布切换期间错误复用哈希 URL。列表请求只读取小型元数据，不重复扫描大包；下载哈希成本随包大小线性增长，成功文件采用内容寻址缓存。

preview.17 首次上线后的完整外部下载发现原 HTTP 服务器绝对写入超时会在 30 秒截断大文件。preview.18 将三个已校验下载入口改为逐块续期的 30 秒写入期限，保留停读连接的上限；普通 API 的 30 秒总期限不变。小块分段探针不足以发现该故障，因此部署验收增加四个完整文件的并行下载和摘要检查。新增真实 HTTP 慢流回归，验证完整与 Range 下载跨越普通写入期限后仍完整；Android 网络切换单测也纳入 Linux CI。

运行证据由 GitHub CI 日志、部署摘要和库外 `.local/deployment-*` 保存。数据库不存签名、构建来源或部署操作证据。

提交前验证：用户站 24 项测试和生产构建通过；新增客户端清单/签名/传输 4 项、完整发布包 16 项、部署入口 6 项、原传输 7 项、安装器 5 项通过，控制端新安装包与更新路由专项通过。Windows 本地全 Python 测试剩余 16 个符号链接权限错误，另 16 项原有平台跳过；不改变这些测试，交由 Linux CI 完整执行。工作流通过 actionlint 1.7.7。调用图阶段识别 308 个变更文件、897 个符号、85 条流程，整体 CRITICAL；CLI 在提高 limit 后仍截断展示符号/流程，因此只作为风险定位依据，不能当作无影响证明。
