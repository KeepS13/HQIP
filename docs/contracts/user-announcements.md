# user-announcements v1

所有者：internal/store/announcements*、internal/control/announcements.go；消费者：admin-web、user-web。

## 行为与接口

- 管理员 `POST /api/v1/announcements`：`id`（客户端 UUID，发布重试使用同一值）、`title`（1–120 个 Unicode 字符）、`body`（1–10000 字符，纯文本）、`mode`（`always` / `until_acknowledged`）、`userIds`（1–5000 个明确用户 ID，去重）。原子发布；任一用户不存在则全部拒绝。发布内容不可修改，重复相同请求返回原公告，不同内容复用 ID 返回 409。
- 管理员 `GET /api/v1/announcements?before=ID`：每页 20 条，最新优先，返回 `{items,nextCursor}`，含目标人数、已确认人数和启用状态。`GET /api/v1/announcements/{id}` 返回详情与目标用户 ID。
- 管理员 `DELETE /api/v1/announcements/{id}`：幂等停用，保留公告及确认状态；再次发布须使用新 ID。
- 用户 `GET /api/v1/user/announcements?before=ID`：每页 20 条，只返回发给当前用户且启用的公告；`always` 始终可见，`until_acknowledged` 仅未确认时可见。按创建时间、ID 倒序；游标定位保留的公告，不受确认、停用影响。
- 用户 `POST /api/v1/user/announcements/{id}/acknowledge`：不接受代确认用户 ID，身份来自会话。重复确认成功，不改变首次确认时间。非目标用户或停用公告返回 404。

管理员接口使用既有管理员鉴权；用户接口使用浏览器会话；所有写入使用既有来源和 CSRF 检查。400 输入无效、401 未登录、403 来源/CSRF 不合法、404 不存在或非目标用户、409 重试内容冲突、503 存储暂不可用。用户接口不含目标列表或确认统计。

## 展示与性能

只在用户网站登录完成或刷新页面后读取一次，不轮询，不因路由切换再次弹出。每次显示一条；下一条只前进，不确认；X/Escape/遮罩关闭整个队列，不确认。`until_acknowledged` 显示“我已知晓”，服务端成功才更新状态并前进；失败保留当前公告供重试。`always` 仅提供下一条和关闭，不会误导为永久隐藏。末条保留关闭按钮；确认末条可结束本次展示。

使用纯文本渲染，保留换行与长内容滚动。弹窗复用现有焦点限制、Escape、焦点归还。单页最多20条，按需加载下一页；请求超时15秒，卸载取消。发布和确认均幂等，重复投递不增加记录。

## 数据与迁移

公告及目标/确认状态归本模块所有；只保存业务数据。公告先于目标行写入，发布事务失败全部回滚。确认是针对目标行的条件更新，与停用并发时数据库事务决定顺序。

SQLite `EnsureAnnouncementsSchema` 启动时增量迁移；PostgreSQL 使用迁移账号运行 `hqip migrate-announcements`，新库初始化包含表。发布前执行迁移并对运行账号授权，新表为空时不影响现有用户。旧版本可忽略新增表，回滚无需删表。
