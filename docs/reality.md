# VLESS + Reality 双协议节点

基础能力版本 `vless-reality-v1`，Vision 能力 `vless-reality-vision-v1`，HQIP Windows 客户端 4.7.0。用户的每个获授权物理节点，在升级完成后显示两个选项：`节点名 · Hysteria 2`、`节点名 · VLESS + Reality`。两种协议的连接地址均使用分配给节点的域名；HY2 的 SNI 使用该域名，Reality 的默认 SNI 使用 `www.microsoft.com`。套餐分配仍只计一台物理节点；流量、到期、带宽和连接限制共用原订阅。

## 上线与使用

1. 先发布完整控制端、网页、Windows 客户端和 Agent 产物；发布清单声明 `vlessRealityVersion: 1`。
2. 在管理后台“服务器 → 升级 Agent”选择节点。沿用已有 SSH 凭据、主机指纹、主机运维锁和持久化任务；同一主机串行执行。
3. 首次升级优先绑定该节点 IPv4 的 TCP 443，已有 HTTPS 占用时选择 8443；两者均占用则失败。已有配置保留端口、自定义伪装域名及密钥，旧的自动默认值 `www.cloudflare.com` 会迁移到 `www.microsoft.com`，失败则随配置回滚。HY2 的 UDP 端口不变。新装节点同样启用双协议。
4. 检查升级任务成功、Agent 心跳与节点状态；刷新用户订阅，分别测试两种协议。云厂商安全组若限制入站，需要允许实际选定的 TCP 端口。
5. 第三方仍使用原订阅链接。Clash/Mihomo、V2Ray URI 列表和 sing-box JSON 同时导出两种协议。HQIP 4.6.0 之前的客户端保持只接收 HY2；4.6.0 支持普通 Reality，更新至 4.7.0 后按节点能力自动使用 Vision。

升级后的 Agent 默认支持 VLESS/TCP + Reality + XTLS-Vision，用户配置使用 `flow: xtls-rprx-vision`，UDP 使用 XUDP。只有已报告 Vision 能力且入口可用的节点才下发该 flow；未升级节点继续使用空 flow，避免控制端先发布导致连接中断。节点回滚后，后续报告会清除 Vision flow。sing-box JSON、Clash/Mihomo 和 VLESS URI 均使用同一能力结果；HQIP 4.7.0 检测 flow 变化后重建连接配置。

入口同时兼容旧的空 flow 连接，让缓存订阅与 HQIP 4.6.0 可以继续连接；旧客户端需要升级并刷新配置才能获得 Vision。这只是同一 VLESS 端点的兼容方式，不增加第三个节点。两条路径均经原有准入与计量后端。Vision 对符合条件的 TLS 1.3 流量切换外层 TLS 直传，并保留初始填充；它不提供“不可识别”的保证，也不改变目的端的 TLS 校验。

## 准入与计量

Agent 内置 Reality TCP 入口，VLESS 通过自身的 `internal/outbound` 直接连接目标服务，不建立本机 HY2 连接。两种协议共同使用 `runtime/nodepolicy` 的授权守卫和 Agent 的业务账本，沿用带宽、套餐预算、连接名额、撤权和有效载荷计费规则。受管节点仍绑定节点 IPv4、使用 1.1.1.1 DNS，并拒绝相同的私网网段；默认拒绝列表统一生成 HY2 ACL。TLS 与封装开销不重复计入用户流量；VLESS 原始流量独立写入既有日、分钟、小时统计，与 HY2 合并展示但不重复扣套餐。

VLESS UUID 是原随机凭据 SHA-256 前 128 位的别名。中央数据库仍保存原有按用户、订阅、节点、作用域隔离的凭据，鉴权 Agent 只能解析自己的节点。轮换第三方订阅使旧 HY2 密码与旧 UUID 同时失效；原生客户端的独立会话不受该轮换影响。解析成功仍须经过同一 Agent 准入和公共租约守卫，不能绕过名额、流量或撤权。

VLESS 每个请求建立流，同 UUID、同来源 IP 的流共用一个独立出站授权；最后一条流结束 5 秒后释放。不同凭据仍有各自账本，中央按原规则在同一订阅内对 VLESS 来源 IP 去重。无法据此识别同 NAT 下共享凭据的物理设备，复制凭据也不会增加套餐额度。客户端不会把 VLESS 的正常空闲释放误判为故障。

新 VLESS 连接解析别名后按既有准入规则申请授权。网站暂时失联时，仅已加密缓存且仍有有效本地授权的凭据可走原有离线路径；已释放名额不能离线重新占用。节点不持久化用户明文密码。两种协议的短租约仍最多 120 秒，每 5 秒续约。VLESS 的运行代次属于 Agent，HY2 崩溃、停止或重启不会中断 VLESS 数据、撤销其名额或隐藏端点；Agent 重启会断开 VLESS。

## 配置、归属与回滚

`agent.json` 的 `reality` 使用 `port`、`serverName`；`backendPort` 仅兼容读取旧配置，可省略，不参与转发。默认握手目标为 `www.microsoft.com:443`；修改目标前需确认支持 TLS 1.3，并重新验证完整 Reality 握手。SNI 是握手目标，与用户拨号的节点域名分开；目标不能指回当前 Reality 入口形成自回环。私钥与 short ID 在节点状态目录的 `reality-identity.json` 一次生成，0600 保存，重启和升级不轮换。控制数据库仅保存公开业务端点；不保存私钥、素材路径或升级操作记录。

启动时验证本机真实 Reality 握手，成功才报告端点。目标或端口故障时，Agent 保留 HY2 准入并定期重试；不会把单纯监听成功视为 Reality 可用。节点周期报告缺失能力或端点时，服务端取消其 Reality 展示。

升级备份原 Agent 配置与服务路径，只重启 Agent，不重启 HY2，核对核心 InvocationID 未变。防火墙复用精确 IP/端口/协议及 `hqip:<nodeId>` 所有权规则，不改变全局策略。写入规则前保存回滚意图，保留原有和他人规则。失败恢复旧配置、服务路径及本次新增规则，绝不恢复旧用量账本。升级 Agent 会断开已有 VLESS 流，客户端随后重新鉴权。

首个已升级 Agent 报告后，业务元数据记住 Reality API 兼容性要求。控制端不能回退到不认识该报告或凭据接口的版本；使用兼容修复版本向前恢复。尚未升级节点时，控制端仍可按正常发布流程回滚。

## 验证

- Go 回归覆盖同一物理节点两种展示、凭据作用域、共享名额与用量、轮换、缺失能力、三种订阅格式、原生客户端协议选择及测速。
- `HQIP_E2E_CORE=<本次构建的 HY2 路径> go test -tags=with_utls ./internal/agent -run TestRealityDirectSurvivesHY2ProcessCrashAndRestart -count=1 -v`：真实控制端、Agent 和本机测试核心，强制终止并重启 HY2，验证同一 VLESS 授权及目标 TCP 连接继续转发、停机期间新准入、续约、名额释放、计费和轮换撤权。下面的 Reality wire 测试不启动 HY2 服务。
- `HQIP_SINGBOX_EXE=<已验证 sing-box 路径> go test -tags=with_utls ./internal/reality -run TestSingBoxRealityGuardedWire -count=1 -v`：同时验证旧 flow 与 Vision、TLS 1.2/1.3 多记录双向传输、TCP、XUDP、共享租约、累计计量和拒绝授权，并断言 sing-box 实际进入 Vision 直传。设置 `HQIP_XRAY_EXE` 可运行 `TestXrayRealityGuardedWire`，同样覆盖两种 flow。测试的服务、目标及凭据均在本机临时创建。
- `HQIP_REALITY_TARGET_SMOKE=1 go test ./internal/reality -run TestDefaultRealityTargetHandshake -count=1 -v`：使用真实微软默认目标完成本机 Reality 身份握手；此项需要联网，普通 CI 不依赖外部目标可用性。
- Python 回归覆盖配置恢复、保留实时账本、既有端口不重选、原有防火墙规则保留、启动及防火墙失败回滚。

依赖锁定见 `go.mod` / `go.sum`；Reality TLS 使用 [MetaCubeX uTLS](https://github.com/MetaCubeX/utls)，VLESS/XUDP 使用 [sing-vmess](https://github.com/SagerNet/sing-vmess)，握手参数对齐 [sing-box 1.14.4](https://github.com/SagerNet/sing-box/tree/v1.14.4)。

## 构建与 Vision 回归

`scripts/build.py` 对发布的 Agent 显式使用 `-tags=with_utls`，启用锁定版本 sing-vmess 的 MetaCubeX uTLS 适配器。CI 使用同一标签测试，并以打包的 sing-box 运行真实互通。手工调试构建未加标签时只支持普通 Reality，明确不报告 Vision 能力。

上游锁定版本的 Vision TLS 适配器通过 `uintptr` 暂存私有 TLS 缓冲字段的地址，Go 的 `checkptr` 插桩会在初始化时中止。普通发布构建及互通测试正常；开启竞态检测的互通测试使用 `-race -gcflags=github.com/sagernet/sing-vmess/vless=-d=checkptr=0`，只对该依赖关闭指针插桩，保留竞态检测及其他包的指针检查。依赖升级时需重新审查这一限制，不能扩大为全局跳过。根模块常规 `go test -tags=with_utls -race ./...` 保持原指针检查。
