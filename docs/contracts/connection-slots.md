# subscription-admission.connection-slots

版本：connection-slots-v1。所有者：store 的订阅事务、Agent 的本地连接账本。节点数与同时在线上限保持独立，连接上限跨节点和协议共享。

订阅凭据确定身份与套餐，父账本保存累计流量和已核验有效期；在线名额具有独立的申请/释放状态。网站返回 connectionSlotsVersion=1 后，新 Agent 才能通过累计报告中的 slotAction=release/acquire 启用该能力。旧 Agent 不自动释放名额，防止其未重新申请就复用已释放许可。

节点持有 offlineMu，先持久化结束状态和操作意图，再发报告。正常 final 立即触发同步和释放；异常连接由节点依据内核实例变化或最后一次子租约发放后经过完整的 120 秒单调时钟期限及 5 秒保护期判断结束。缺失 final 的获发流量继续保留，释放名额不能清空或估算这些计数。

中央在同一订阅事务中结算真实累计用量并处理名额变更。释放后 portal_leases.expires 不再占在线名额；原许可有效期独立保留在计费状态。释放同时将 bandwidth/ceiling 归零、pending 置位并推进速率版本，重新申请时必须先预留带宽再转发。重新申请先检查身份、套餐和全订阅在线上限；即使名额已满，也返回已经确认的真实用量和 slotReleased=true，不回滚补账。已撤销或用尽额度的账本也可释放已结束连接的名额，并继续接受迟到 final。所有版本重放保持原请求不变，不重复占用或释放名额。

网站失联时不把活跃连接当成离线。已持有名额的节点继续按原许可、余额和倍率服务；已释放的名额必须重新得到网站批准，不能离线复活。失联期间的正常断开先记录在本地，通信恢复后立即补报并释放。恢复后的超额或撤销结果在节点下一次续约时停止转发。

发布顺序为控制端、节点 Agent。首次使用名额操作会记录业务契约版本，发布与回退检查必须拒绝不理解该状态的旧控制端/Agent。Agent 账本中的已释放、待确认操作、版本标记均触发旧版拒绝；停止 Agent 前后各检查一次，禁止恢复旧账本。节点汇报 connection-slots-v1，发布包与节点包均声明 connectionSlotsVersion=1。操作证据保存在库外，不写入业务数据库。

验证入口：store/agent 的 TestOfflineSlot 系列覆盖单名额、并发抢占、满额补账、丢回执重启、逐连接超时、撤销与迟到 final、失联重连边界；TestConnectionSlotsRealTwoNodeSwitchAndReconnect 使用真实客户端和双节点验证退出、切换与返回原节点。部署兼容性测试见 scripts/test_connection_slots_deployment.py。
