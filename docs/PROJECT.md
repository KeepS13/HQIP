# HQIP 项目地图

订阅入口与套餐应用（2026-11-28，本地实现）：默认账户链接固定，空账户也可复制，动态读取当前订阅；选择器仅列可用单份。套餐新增「立刻应用」，更新现存实例节点目标并协调在线限制；NAT 质量探针区分本机源地址与公网报告身份。契约见 [import-links v2](contracts/subscription-import-links.md)、[template-apply v2](contracts/subscription-template-apply.md)，验证及未发布范围见 [本轮审查](reviews/2026-11-28-subscriptions-nat-quality.md)。

本轮文件预算：原 869 行套餐页面拆分为列表、草稿编辑、选点和商城预览；`TemplateEditor.tsx` 380 行作为小于 400 行的例外，保留同一草稿的校验、影响预览与保存交互，避免跨文件分散状态所有权。其余新增生产文件均不超过 300 行。上游固定版本 `ipquality/ip.sh` 保留供应商脚本结构，以补丁与摘要追踪修改。

节点上限修正：支付创建／结果收尾从订单读取模块拆出，商城库存查询从分配模块拆出，相关 Go 文件均不超过300行。`user-web/src/portal/Commerce.tsx` 344行保留为小于400行的例外，沿用同页商城与购买对话框共享状态，只修正节点数量展示。

NAT 节点部署（2026-11-27，已发布 `216c48e`）：自动分离公网身份和验证过的本机监听地址，支持首次部署、重新部署与兼容 Agent 更新。xus1-v 三阶段验收通过，当前 17/17 节点健康；独立网络质量探针尚不支持 NAT，详见 [生产验收](deployment/2026-11-27-nat-node-deployment.md) 和 [网络契约](contracts/node-network.md)。完整发布客户端序号为 `5000024`。

Clash DNS 与网站恢复（2026-11-27，已发布 `339286d`）：`routing` 修复 HY2 / VLESS 节点引导解析，海外 DoH 经节点；Clash、sing-box、待开通配置及 HQIP 原生官方站点名单统一保留官网、后台和验证码直连。正式客户端为 `1.0.0 / 5000023`。行为边界见 [导入契约](contracts/subscription-import-links.md)，实测见 [验收记录](reviews/2026-11-26-clash-dns-recovery.md)，CI、备份及公网结果见 [发布记录](deployment/2026-11-27-client-compatibility.md)。

全部客户端导入审查（2026-11-26 完成，2026-11-27 已上线）：检查页面 7 种客户端及 HQIP 原生入口；修复 Verge 一键导入参数顺序，补充服务端统一的精确直连名单及恢复帮助。v2rayN 配套核心和 sing-box 的 vus1 双协议验证通过，Apple/Android 应用真机仍未覆盖，见 [兼容审查](reviews/2026-11-26-client-compatibility.md)。

节点用户排行（2026-11-25，已发布 `5157d54`）：仅在管理员节点详情页下方增加用户累计实际代理流量分页排行，合并同用户的多份订阅，支持点击用户详情；口径与接口见 [node-user-traffic v1](contracts/node-user-traffic.md)，本地验证见 [排行验收](reviews/2026-11-25-node-user-traffic.md)，生产结果见 [流量监控发布](deployment/2026-11-25-traffic-monitoring.md)。

网卡监控扩展（2026-11-25，已发布并验收 16/16 Agent）：新增 `host-network v1`，在管理员界面并列保留节点用户代理流量与整机网卡收发总量；同机去重、离线持久化补报和未知历史边界见 [网卡计量契约](contracts/host-network.md)。本地检查见 [流量检查记录](reviews/2026-11-25-host-network-audit.md)，实际二进制、网卡和计费核对见上述发布记录。系统消耗包含在总量中，独立分项未分类。

客户端部署（2026-11-25）：完整发布同时更新正式 1.0.0（序号 5000022），源提交 `5157d54`。运行 `45673842895` 的 12 项门禁和生产部署全部通过，完整流程 17 分 21 秒。构建身份、回滚历史和发布证据见 [客户端 CI](deployment/client-release-ci.md)。

仓库清理（2026-11-24）：移除经调用图与静态引用确认的旧入口、前端复制模型和孤立组件；本地旧产物回收约 5.44 GiB，签名发布工具增加两版本保留与安全回收。仍在 CI/生产发布链使用的 Wails 客户端、业务迁移和签名身份保留，详见 [清理记录](reviews/2026-11-24-repository-cleanup.md)。

客户端安装与更新（2026-11-24）：正式版本重新定义为 1.0.0，内部序号继续递增，唯一来源为 `packaging/client-version.json`。`client-presentation` / `client-runtime.updates v1` 提供 Windows 当前用户 EXE 安装包，固定启动器和版本目录由安装器管理，Android 交付签名 APK。启动后无需登录即可检查更新；Windows 下次启动切换，Android 仍走系统安装确认。旧 5.x 预览版需覆盖安装一次，之后按签名序号更新。构建与验证记录见 [安装与自动更新](applications/client-installation.md)。

节点卡片命中修复（2026-11-24）：preview.15 将透明占位控件从鼠标命中中排除，修复卡片右下角点击死区；42 个真实输入点及禁用节点回归通过。仅调整 Godot 展示层，未增加请求，详情见 [节点卡片点击死区](incidents/2026-11-24-node-card-hit-area.md)。

空闲测速与状态回跳修复（2026-11-24）：preview.14 修复 Godot 编辑器误用残留 4.8.1-dev 运行时，增加原生快照采集序号以拒绝迟到回执，断开撤销连接意图后立即拒绝新延迟检测；无新增网络请求。现场证据与回归见 [空闲测速及状态回跳](incidents/2026-11-24-idle-probe-oscillation.md)。

客户端选择与延迟调整（2026-11-24）：preview.15 按最新要求在已有连接意图时自动停止旧连接并连接新选择，未连接时仅保存偏好；每次连接成功后自动测一次当前连接延迟，其余测速手动触发。界面继续立即响应，连续选择以最新目标为准，手动断开优先，迟到授权/测速结果不能覆盖新节点。保留登录一次导入、业务通知、并发刷新合并和状态去重。详情见 [自动切换与连接延迟](incidents/2026-11-24-auto-switch-delay.md)，同步性能历史见 [选择与同步性能](incidents/2026-11-24-client-performance.md)。

测速空闲占位修复（2026-11-23）：归属 `subscription-admission`、`node-metering` 与 HY2 适配器，公共守卫新增只读 `Admission.Check`，控制端和 Agent 提供身份检查端点，内核按业务目标引用管理活动授权。空闲 QUIC 保活不占名额，最后一个目标结束即提交 final，再次使用重新准入。名额版本、数据库结构和累计计费不变。HY2 认证与 UDP 适配器从 `server.go` 拆出，均不超过 300 行。证据和验收见 [事故记录](incidents/2026-11-23-hy2-idle-slots.md)。

本次架构调整（2026-11-23）：VLESS + Reality 由 Agent 内的独立出站连接目标；HY2 与 VLESS 共用 `runtime/nodepolicy` 的租约守卫、速率和固定点计费，以及 Agent 既有鉴权、节点额度、名额和撤权账本。VLESS 连接代次归 Agent 所有，HY2 停止或重启不结束 VLESS 授权。契约与迁移边界见 [协议公共业务能力](contracts/protocol-policy.md)。

项目 ID：`hqip`。Go 控制端、节点 Agent 与 HY2/Reality 内核提供订阅授权、流量计费和代理服务；React 管理站、用户站与 Windows 桌面端消费独立 API。

性能与稳定性维护（2026-11-23）：`client-presentation` 的两个网站共用无框架依赖的 `web-shared/polling.ts` 调度器，API/鉴权仍由各应用独立管理。目标为每个读取任务最多一个在途请求，提交后刷新最多合并一个后续读取，15 秒取消超时读取、后台暂停、故障退避最多 60 秒（原间隔更长时保留）。客户端采样和 `protocol-outbound` 的多地址连接优化及验证边界记录在 `docs/reviews/2026-11-23-performance-stability.md`；不改变计费、准入和协议凭据契约。

客户端重做（2026-11-23）：按用户确认先实现 Windows、Android；Godot 沿用原版深色布局与品牌，复用 `internal/client` 的账户、订阅、离线许可与连接生命周期。该阶段仅生成本地预览包，尚未部署更新入口；后续正式发布见客户端 CI 记录。iOS/macOS 尚未发布。契约见 [客户端运行时与更新](contracts/client-runtime.md)，构建、签名和验收边界见 [Godot 客户端](applications/godot-client.md)。应用版本与 API v3 分离；新功能采用可选能力，更新检查失败不能清除账户或中断有效连接。按用户要求保留现有域名和 Cloudflare 配置，用户已追加授权隔离测试及 HQIP 测试账号/订阅；前版已完成真实账户、模拟器 VLESS 流量/断网恢复/系统覆盖更新验收。preview.15 阶段：保留原版样式、统一左侧导航与 DPI 布局，修复仪表盘套餐展示、交互尺寸、规则 DNS，并将未连接时的本机选择与连接授权分离；专项验证见 [交互与规则模式修复](incidents/2026-11-24-client-interaction-routing.md)。本次追加订阅纯展示、自适应卡片、商城入口与当前节点单独测速，见 [订阅页与测速记录](incidents/2026-11-24-subscriptions-selected-probe.md)。本次进一步修复 [未连接节点即时切换](incidents/2026-11-24-instant-local-selection.md)，此前删除自动列表测速；preview.13 仅恢复连接成功后的当前连接单次延迟检测。当前代理不可接管，真实 VPN 仅在独立模拟器验证；详情与未覆盖项见 [本轮验收记录](incidents/2026-11-23-client-consistency.md)。

本次精简：每份订阅的同时在线上限改为 HY2 在线客户端实例数加 VLESS 来源 IP 数，节点分配数独立保留。同 IP 的两个 HY2 实例加一个 VLESS 来源占三个名额。直接复用 portal_leases、订阅事务和 Agent 账本及已有计费协议字段，不新增设备表、缓存服务或授权额度。来源 IP 存在现有连接表，HY2 实例与 VLESS IP 合计不再增加数据库列。仅 VLESS 在同一订阅内按来源 IP 跨节点去重；HY2 每条独立客户端会话单独计数。旧记录缺少可信协议或 IP 时按许可逐条计数。发布先升级控制端，再统一升级节点与内核。完整语义见 [在线计数契约](contracts/online-ips.md)。

| 模块 | 所有者与位置 | 契约 |
| --- | --- | --- |
| node-provisioning | internal/provision、Agent、节点升级脚本 | [node-network v1](contracts/node-network.md)：直连/NAT 本机地址识别、部署、重部署与兼容 Agent 升级 |
| subscription-admission | control / internal/store、internal/control | offline-admission v1；node-quota v1；[connection-slots v3](contracts/connection-slots.md) |
| node-metering | Agent / internal/agent，runtime/nodepolicy | [公共守卫](contracts/protocol-policy.md)、本地持久化子租约、协议计费段 |
| host-network | internal/hostnetwork、Agent、store、admin-web | [host-network v1](contracts/host-network.md)：整机网卡收发、同机去重、离线补报及明确未知范围 |
| protocol-outbound | internal/reality、internal/outbound；HY2 server 适配器 | VLESS 独立 TCP/UDP 出站、共同节点出口策略 |
| traffic-billing | store、model、runtime billing | 固定点倍率与幂等累计报告 |
| client-presentation | internal/client、internal/clientbridge、desktop、godot-client、native、user-web | 用户余额与原始链路统计；[client-runtime.commands / updates v1](contracts/client-runtime.md) |
| routing | internal/rules；internal/control；internal/client；internal/winclient | [大陆直连策略与兼容验证](mainland-routing.md)：统一域名/IP/DNS 分流，旧订阅在导出时应用，节点 URI 的路由由客户端管理 |
| subscription-admission / 第三方导入 | internal/store/portal_account_import.go、portal_import_links.go、internal/control/portal_import_export.go、user-web | [subscription-admission.import-links v2](contracts/subscription-import-links.md)：固定账户入口、单份复制与凭据轮换 |
| subscription-admission / 套餐应用 | internal/store/subscription_template_apply.go、internal/control、admin-web | [template-apply v2](contracts/subscription-template-apply.md)：已有实例扩缩容、缺额回执与即时准入校验 |
| subscription-admission / 管理员赠送 | internal/store/subscription_grants.go、internal/control/subscriptions.go、admin-web/src/pages/GrantSubscription.tsx | [subscription-admission.manual-grant v1](interfaces.md)：提交后读取结果、原请求恢复、明确拒绝后重新确认 |
| subscription-renewals | internal/store/subscription_renewals.go、subscription_periods.go；internal/control；user-web、admin-web | [按周期续费](subscription-renewals.md)：原订阅延期、周期账本、幂等付款；[node-quota v1](contracts/node-quota.md) 的 periodId 扩展隔离跨期节点预算 |
| subscription-admission / 人工开通 | internal/store/subscription_activation.go；internal/control/portal_import_pending.go；admin-web、user-web | [activation v1](subscription-activation.md)：空节点购买、待开通链接、首次分配节点起算；周期 schema 2 |

本次边界变更的规范见 [节点额度契约](contracts/node-quota.md)，已有行为见 [离线授权](offline-admission.md) 和 [部署](automatic-deployment.md)。数据库仅存业务状态，操作证据保存在 docs/incidents 或库外诊断记录。

验证：`go test -tags=with_utls ./internal/store ./internal/control ./internal/agent`；CI 执行 race、真实 PostgreSQL/Redis、真实双协议代理与 Windows 打包验证。部署只使用同一提交的完整发布包。

新模块文件默认不超过 300 行；已有大文件按本次修改边界逐步拆出完整职责。节点普通续约复用原 5 秒补报；新连接强制核验网站，断开立即补报并释放名额。每次释放或重新申请至多增加一次名额报告，受既有请求超时限制，待确认报告原样重放。节点 SQLite 扫描受既有账本容量上限约束。中央快照与名额读写在同一订阅事务内完成，不扫描其他订阅历史租约。所有测试前后保留既有节点探测修改。旧设备账号的登录/刷新/状态写接口和未挂载的设备页面已删除，历史构造器仅用于结算与迁移回归测试；正式路径不再发放旧授权。客户端恢复不再重复按占用数预判准入，由代理入口统一裁决。

首次准入丢回包后的恢复使用现有 `offline_requests`，新增只读回执查询接口，不新增业务表或存储明文密码。节点最多每 30 秒轮转查询 16 条待确认请求，最多使用同步剩余预算的一半；普通父账本按最早重试时间处理，避免清理饥饿。持久化、身份核验及未知用量保护见 [名额契约](contracts/connection-slots.md)。

文件预算例外：`internal/store/offline.go` 保持完整结算事务，329 行，避免跨函数隐藏结算与回执顺序；`scripts/upgrade_domains.py` 324 行、`scripts/upgrade_node_agent.py` 392 行，保留原升级/回滚职责与停止服务前后的能力检查。这三处均少于 400 行。`internal/agent/offline.go` 的授权回执校验、周期调度分别拆至 `offline_receipt.go`、`offline_sync.go`，首次准入回执恢复位于 `offline_recovery.go`，均少于 300 行。历史大文件仍存在，本次按用户精简要求只调整相关入口、字段与文案，不附带大范围结构拆分；新增生产源文件均少于 300 行。
