# 定制订阅与人工开通

Owner：subscription-admission / subscription-renewals。契约 ID：`subscription-admission.activation v1`。消费者：管理站、用户站、付款履约、客户端配置和连接准入。

## 购买和状态

订阅模板 `nodeIds: []` 表示人工开通的定制套餐；`nodesPerSubscription` 仍须为 1–1000，表示正式开通时需要分配的节点数。已启用、商城展示且有效定价的定制套餐可以购买。商城返回 `manualProvisioning: true`，`availableCopies: 0` 不代表无货；普通非空节点池仍按实际可分配库存限制购买。

下单保存模板快照，锁定人工/自动模式、有效天数和节点数。定制订单不预占节点。付款核验及履约成功后生成原订阅实例，订单 `fulfillmentState=fulfilled` 表示“订阅已生成”，不等于已经开始计时。用户/套餐停用时仍保持原履约拦截。原本自动分配的订单不会因库存耗尽或模板后来清空而自动变为定制订阅。

空池的管理员赠送同样生成待开通实例。实例新增 `activationPending`、`validityDays`、可选 `activatedAt`。待开通时状态为 `pending`；禁用和移除状态仍优先。`expiresAt` 是零时间 `0001-01-01T00:00:00Z`，周期字段为空/零时间、`periods=[]`、`renewable=false`，展示端必须显示“尚未开始计时”，不能展示为公元 1 年到期。有效期从管理员正式开通时开始，付款后的等待时间不扣除天数。

## 原子开通

已有管理员接口 `PATCH /api/v1/users/{user}/subscriptions/{id}/nodes` 同时承担首次开通。用户确认按钮为“保存节点并开通”。输入仍为 `nodeIds` 和 `expectedRevision`，必须选满实例保存的节点数，校验健康、容量、权限、同用户同套餐去重；允许选择模板池外的合格节点。

事务先获取用户分配锁，然后验证实例版本、状态及节点，保存分配、设置首次 `activatedAt`、按保存天数计算 `expiresAt`、创建初始周期，并写开通审计。全部成功才提交。并发旧版本返回 409；失败不留下节点或部分计费周期。已经开通的节点修改不重置期限或账本，重复相同选择保持原有无操作语义。待开通时不能调整到期时间或续费，不能发放节点凭据、选择为客户端使用订阅或占用设备名额。

开通后继续使用已有名额规则：HY2 每个客户端实例一个名额，VLESS 按来源 IP 去重，两者合计；续费保留原实例、原节点、凭据、当前周期用量和占用数。定制实例首次开通后仍由管理员维护节点，模板池变更不会重新分配它的节点。模板的带宽、流量、设备上限、启用状态仍按既有规则生效。

## 订阅链接

待开通实例允许按现有方式获取、复制、轮换和撤销链接，也纳入复制时的合并集合。单份或全部成员待开通时，导出返回 200，Clash/sing-box 提供不含代理凭据的拒绝转发配置，V2Ray 返回空 URI 列表。正式开通后刷新原链接即可获取节点，令牌不旋转。合并含已开通成员时仅导出已有可用成员的节点。失效、撤销和跨账户鉴权继续遵循 [导入链接契约](contracts/subscription-import-links.md)。

## 持久化和升级

`subscription_provisioning(subscription_id, validity_days, activated_at)` 仅存业务状态：一份定制订阅对应一行；`activated_at=0` 为待开通，正式开通后保留此行表示人工管理节点。待开通的 `portal_subscriptions.expires=0`，没有初始 `subscription_periods`。历史普通订阅不回填，不推迟已有订阅期限。

周期能力 `subscription_renewals_version` / 发布清单 `subscriptionRenewalsVersion` 升至 **2**。SQLite 启动时事务升级；PostgreSQL 停止业务写入后，以迁移角色运行既有 `hqip migrate-subscription-renewals`。1→2 只建立新表并更新能力标记，不改已有周期、续费、用量或凭据。旧分配迁移也可先建立空表供库存快照读取，但周期能力升级仍必须明确完成。应用角色只读校验版本，不能隐式执行 DDL；备份、恢复、SQLite→PostgreSQL 迁移和发布/回滚保护均识别版本 2。

并发和跨数据库测试位于 `subscription_activation*_test.go`；HTTP 导出和管理员开通测试位于 `internal/control/subscription_activation_test.go`；页面回归为 `scripts/test_custom_activation_ui.cjs`。没有常驻激活任务或额外设备记录。每次订阅读取、分配快照各增加一次主键左连接；人工开通沿用既有事务和节点候选读取。
