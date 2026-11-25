# 节点用户流量排行

Contract ID：`node-user-traffic v1`。所有者：store / control；消费者：admin-web 节点详情页。只读派生查询，不新增表，不修改计费或采集链路。

`GET /api/v1/nodes/{id}/user-traffic?offset=0&limit=20`：仅管理员认证可读，公共站无此路由。offset 默认 0，范围 0–1000000；limit 默认 20，范围 1–100。非法或重复分页参数返回 400，节点不存在返回 404。查询错误返回 500，不能伪装为空列表。

响应字段：nodeId、items、totalUsers、offset、limit、generatedAt（UTC 读取时间）。items 始终为数组；每项包含 userId、email、enabled、upload、download、total、hasData。所有流量单位为字节。

- 从 `node_accounting_identities` 的 subscription 身份映射到订阅所属用户，合并该用户在指定节点的全部身份和订阅。映射在授权连接时产生，单纯分配但从未连接的用户不列入。
- 流量来自 `lifetime` 原始代理累计账本，total = upload + download，不乘 1.5 或其他计费倍率，不重复相加日桶、小时桶、名额租约或用户计费余额。续订、到期、撤销或停用不清除历史用量。
- 不包含其他节点流量、服务器自身网卡流量、探测及无法归属用户的历史身份。此排行合计不一定等于节点或网卡总量。
- hasData=false 表示没有累计账本记录；显示“暂无记录”，不能表达为已测得 0。存在零值账本时 hasData=true，显示 0 B。中断或从未上报的流量只能在采集补报后反映，generatedAt 不是采样时间。
- 按 total 降序、userId 升序固定排序。SQL 单次快照同时计算节点存在性、用户总数与当前页，空节点或超出末页仍返回正确 totalUsers。分页之间实时变化可能改变排名；每次请求独立读取。

性能：利用身份与累计账本的 `(node_id, accounting_id/device_id)` 主键限定单节点；单次聚合查询，最多返回 100 行，无逐用户查询。请求上下文 + 5 秒数据库超时。界面每页 20 行，每 30 秒刷新，复用共享轮询的隐藏暂停、取消和退避；只在节点详情页加载，不增加节点列表轮询成本。

交互：详情页下方展示邮箱、上传、下载、总量和名次，点击邮箱跳转 `/users/{userId}`。未知、空列表、首次失败和刷新失败分别处理；刷新失败保留同页数据并标明缓存，切换页或节点不显示上一页数据。窄屏使用表格内部滚动，保持整页不横向溢出。
