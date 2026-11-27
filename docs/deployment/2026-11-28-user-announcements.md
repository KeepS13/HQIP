# 用户公告上线验收

源提交 `a9dbbde42c827feddad2d8b56b6eb66a44097999`。管理员在 [用户公告](https://cms-hqip.sharevps.online/announcements) 批量指定用户并发布；用户网站登录或刷新后逐条弹出。两种展示方式、确认持久化、下一条和关闭的边界见 [公告契约](../contracts/user-announcements.md)，本地与浏览器验证见 [审查记录](../reviews/2026-11-28-user-announcements.md)。

## CI 与部署

[运行 46056304933](https://github.com/KeepS13/HQIP/actions/runs/46056304933) 的12项测试/构建及生产部署全部成功，无部署重试。UTC 2026-11-27 19:23:00开始，19:42:12完成，共19分12秒；下文时间均为 Asia/Taipei 2026-11-28。

- Linux完整测试、race、运行时测试、真实 PostgreSQL/Redis、Windows回归、三组真实客户端测试及完整构建门禁通过。
- 03:38:18记录活动版本切换；公告增量迁移在切换前完成，既有业务结构保留。
- 03:41:38完成签名渠道、全部交付物首尾范围及完整 Windows 安装包公网摘要验收。
- 03:42:11部署作业成功；活动版本为 `a9dbbde`，保留上一个版本 `31d0e23`。签名客户端渠道仍为 `1.0.0 / 5000027`，本次未更改原生客户端展示行为。

在线一致性备份目录：`/root/backups/hqip-ci-a9dbbde42c827feddad2d8b56b6eb66a44097999-46056304933-1`。独立确认 `postgres.before.tar.gz` 存在，大小4,440,993字节；包含配套密钥的备份保存在服务器，不纳入源码或Actions产物。

## 最终独立核验

03:43:14以后核验：

- 活动 release 与 manifest 源提交匹配，部署状态为 `active`。
- 用户410、订阅180、订单28，与发布前一致；主密钥摘要未变。
- 两路本机18090/18091的 `/readyz` 均 `ok=true`；18/18活动节点ready且核心健康。
- PostgreSQL `announcements_version=1`，公告及接收人表存在；运行账号对两表的 SELECT/INSERT/UPDATE/DELETE 权限齐全。
- 公告、接收人和确认记录均为0。本次生产验收仅做只读检查，没有给真实用户发送测试公告；发布、确认及刷新持久化由隔离数据库和浏览器测试覆盖。
- 用户站 `/login`、管理站 `/announcements` 均HTTP200；用户站 `/healthz`、`/readyz` 均HTTP200且 `ok=true`。
- 用户站承载的管理端 `/api/v1/announcements` 和用户端 `/api/v1/user/announcements` 未登录均返回401。管理站域名下的API按现有Nginx规则返回404；两站共用用户站API域名。

两站HTML引用当前资源；公网下载的JS/CSS与活动目录及本地通过浏览器回归的构建均逐字节一致：

| 资源 | SHA-256 |
| --- | --- |
| `admin-web/index-C91JAj2W.js` | `0d3c6f2fa4011cd268a345a973197e224ed52ef6e15c3eb0d54ead80377e8b05` |
| `admin-web/index-D6C0vkX-.css` | `fa33a43489a94cb074faa0dbf236216bed7f124e498108761dc02ffec167e91b` |
| `user-web/index-CXlBpUjU.js` | `b7076a261feb6290de434067cb04eff8006cc50658871bca83cea61583def05e` |
| `user-web/index-BZ1PMLGq.css` | `c6be860cee385018b978c67286bca1c2104ebf32871d49ba5d48b5f949e8afda` |

库外证据：`.local/announcements-ci-final.json`、`announcements-deploy-final.log`、`announcements-production-before.json`、`announcements-production-after.json`、`announcements-production-public.json`、`announcements-linux-python.log`。保留用户原有23份未提交文件，逐份摘要不变，未纳入本次发布。

代码提交前执行完整图谱分析：全工作区57文件、198符号、37条流程；本次暂存45文件、168符号、36条流程，均为 **CRITICAL**，没有将入口拆分或全量回归通过解释为低风险。范围、精确符号核对和图谱覆盖边界见上述审查记录。部署后的文档更新仅记录验收证据。
