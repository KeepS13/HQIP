# 控制服务失联时的节点账本

当前新准入使用节点共享额度与来源 IP，规范见 [节点额度](contracts/node-quota.md)、[在线计数](contracts/online-ips.md) 和 [名额释放](contracts/connection-slots.md)。这些契约共同使用现有订阅事务、`portal_leases` 与 Agent SQLite，没有另一套设备登记系统。

## 准入与断线

启用节点账本的节点通过 `/api/v1/agents/offline-grants` 请求准入，必须提交计费协议、节点额度能力和代理入口实际观察到的 `clientIP`。同一订阅合计 HY2 在线客户端实例数和 VLESS 来源 IP 数，跨节点共享上限；仅 VLESS 同 IP 跨节点、登录会话共享名额。未启用节点账本的节点仍使用短租约接口，同样必须提供 IP。

父账本绑定订阅、节点、凭据和来源 IP，保存累计计费及已核验有效期；内核每条连接使用至多 120 秒的子租约。连接与计费记录保持独立，VLESS 同 IP 去重不会复制流量或带宽，每个 HY2 父账本同一时刻至多使用一个活跃子连接。

控制服务失联时，节点只允许尚持有中央名额的已批准账本在原节点续用或重连，不能批准新增名额，也不能恢复已经释放的名额。正常断开立即记录并补报；异常结束依据内核实例及完整子租约期限加保护期确认。HY2 实例确认结束后释放自己的名额；VLESS 一个 IP 的最后一条占用结束后才释放该 IP 名额。失联期间无法确认全局变化，不因心跳缺失直接腾出名额。

Agent 先持久化幂等请求、子预算与累计用量再放行。重试使用原请求 ID 或原版本报告；明确拒绝、无效响应和 TLS 校验失败不能作为断网放行理由。未知最终用量继续保留，释放在线名额不等于删除计费账本。

已经确认释放名额或撤销、且所有子连接已确认结束的账本，在报告内容没有变化时停止周期性授权上报。Agent 将成功回执的内容摘要与账本一起持久化，重启后仍可休眠；升级前没有摘要的记录先完成一次确认。待确认报告继续原样重试，新增流量、迟到 final、额度保留变化和名额操作仍会补报；重新连接强制核验控制端并申请名额。活跃连接、尚持有名额的授权以及节点心跳维持现有同步周期。休眠不会删除账本、归还未知预算或伪造最终结算。

## 额度与旧记录

同节点同订阅的全部协议共享最后确认的剩余额度，直到余额耗尽、订阅到期或已知登录会话到期。新授权不分割中央流量预留、不新增应急预算。跨节点断网期间按各节点最后余额续用的既有约定不变，恢复后接受全部真实用量并核验总额。

旧格式的预留、应急预算、无 IP 授权仍可结算，不能继续用于新准入。历史无 IP 行分别占用名额，直到确认旧连接结束，不能把空地址合并或从登录/下载记录猜测 IP。旧构造器仅保留在迁移回归测试中。

## 发布

1. 备份数据库与配套密钥，先升级控制端。PostgreSQL 用迁移角色执行 `hqip migrate-offline-admission`，原命令按需增加 IP 字段并推进计数版本；从 v2 升级 v3 不新增数据库列。即使已存在旧离线表，发布脚本仍检查 `connectionSlotsVersion=3` 并执行缺失迁移。SQLite 启动时增量迁移。
2. 配套升级节点 Agent 和 HY2 内核。内核提供 socket 来源 IP；Reality 入口将原始 IP 放入节点签名凭据。旧内核缺少字段会拒绝新准入，单独升级 Agent 不足以完成升级。
3. 核验节点报告 `connection-slots-v3`、真实客户端连接、断线回收与最终结算。现有 `hqip offline-admission --node NODE_ID --enabled=true/false` 只控制该节点是否新发节点账本授权；停止发放后继续结算已有账本。

发布包及节点包声明 `connectionSlotsVersion=3`。采用实例与 IP 合计规则后，控制端与节点回退检查拒绝旧版；停止服务前后都检查账本，禁止用旧快照覆盖新用量。没有自动重置、重新登记设备或清空预留步骤。

## 验证入口

- `go test -tags=with_utls ./...`：事务、HY2 实例计数、VLESS IP 去重、并发、释放、重启、计费、迁移和客户端恢复。
- 配置本地 `HQIP_TEST_POSTGRES_URL`：测试分别使用独立 PostgreSQL schema，验证迁移与事务。
- 配置 `HQIP_E2E_CORE`、`HQIP_SINGBOX_EXE` 后，运行 Agent 的 `TestOnlineIPRealTwoNodesAndDifferentSource`、`TestConnectionSlotsRealTwoNodeSwitchAndReconnect`、`TestTrafficBillingRealDualProtocolHotUpdate`：真实入口 IP、第三方客户端、双节点、双协议和最终用量。
- `python -m unittest discover -s scripts -p 'test_*.py'`：迁移、发布与回退约束。
