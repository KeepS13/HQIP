# VLESS + Reality 双协议节点

能力版本 `vless-reality-v1`，HQIP Windows 客户端 4.6.0。用户的每个获授权物理节点，在升级完成后显示两个选项：`节点名 · Hysteria 2`、`节点名 · VLESS + Reality`。两种协议的连接地址均使用分配给节点的域名；HY2 的 SNI 使用该域名，Reality 的默认 SNI 使用 `www.microsoft.com`。套餐分配仍只计一台物理节点；流量、到期、带宽和连接限制共用原订阅。

## 上线与使用

1. 先发布完整控制端、网页、Windows 客户端和 Agent 产物；发布清单声明 `vlessRealityVersion: 1`。
2. 在管理后台“服务器 → 升级 Agent”选择节点。沿用已有 SSH 凭据、主机指纹、主机运维锁和持久化任务；同一主机串行执行。
3. 首次升级优先绑定该节点 IPv4 的 TCP 443，已有 HTTPS 占用时选择 8443；两者均占用则失败。已有配置保留端口、自定义伪装域名及密钥，旧的自动默认值 `www.cloudflare.com` 会迁移到 `www.microsoft.com`，失败则随配置回滚。HY2 的 UDP 端口不变。新装节点同样启用双协议。
4. 检查升级任务成功、Agent 心跳与节点状态；刷新用户订阅，分别测试两种协议。云厂商安全组若限制入站，需要允许实际选定的 TCP 端口。
5. 第三方仍使用原订阅链接。Clash/Mihomo、V2Ray URI 列表和 sing-box JSON 同时导出两种协议。HQIP 旧客户端保持只接收 HY2，更新至 4.6.0 后显示两个选项。

普通 VLESS/TCP + Reality，`flow` 为空，UDP 使用 XUDP；不是 `xtls-rprx-vision` 配置。sing-box 1.14.4 与 Xray 26.10.31 已通过真实 TCP、UDP 互通回归。

## 准入与计量

Agent 内置 Reality TCP 入口，VLESS 数据全部通过本机已有的 HQIP HY2 核心转发。入口没有直连目的地址的实现，因此保留原有 ACL、出口 IP、带宽、预算、租约撤销和有效载荷计量。TLS 与封装开销不重复计入用户流量；服务器网卡统计仍按原口径。

VLESS UUID 是原随机凭据 SHA-256 前 128 位的别名。中央数据库仍保存原有按用户、订阅、节点、作用域隔离的凭据，鉴权 Agent 只能解析自己的节点。轮换第三方订阅使旧 HY2 密码与旧 UUID 同时失效；原生客户端的独立会话不受该轮换影响。解析成功仍须经过真正的 HY2 准入，不能绕过名额、流量或撤权。

VLESS 每个请求建立流，同 UUID、同来源 IP 的流共用一个本机 HY2 连接；最后一条流结束 5 秒后释放。不同来源 IP 或不同凭据分别占连接名额。无法据此识别同 NAT 下共享凭据的物理设备，复制凭据也不会增加套餐额度。客户端不会把 VLESS 的正常空闲释放误判为故障。

新 VLESS 连接需要控制服务在线解析别名。已有本机后端继续遵守原 HY2 租约策略；不承诺 VLESS 离线冷重连，也不在节点持久化用户明文密码。HY2 的既有离线能力保持原样。

## 配置、归属与回滚

`agent.json` 的 `reality` 仅含 `port`、`backendPort`、`serverName`。默认握手目标为 `www.microsoft.com:443`；修改目标前需确认支持 TLS 1.3，并重新验证完整 Reality 握手。SNI 是握手目标，与用户拨号的节点域名分开；目标不能指回当前 Reality 入口形成自回环。私钥与 short ID 在节点状态目录的 `reality-identity.json` 一次生成，0600 保存，重启和升级不轮换。控制数据库仅保存公开业务端点；不保存私钥、素材路径或升级操作记录。

启动时验证本机真实 Reality 握手，成功才报告端点。目标或端口故障时，Agent 保留 HY2 准入并定期重试；不会把单纯监听成功视为 Reality 可用。节点周期报告缺失能力或端点时，服务端取消其 Reality 展示。

升级备份原 Agent 配置与服务路径，只重启 Agent，不重启 HY2，核对核心 InvocationID 未变。防火墙复用精确 IP/端口/协议及 `hqip:<nodeId>` 所有权规则，不改变全局策略。写入规则前保存回滚意图，保留原有和他人规则。失败恢复旧配置、服务路径及本次新增规则，绝不恢复旧用量账本。升级 Agent 会断开已有 VLESS 流，客户端随后重新鉴权。

首个已升级 Agent 报告后，业务元数据记住 Reality API 兼容性要求。控制端不能回退到不认识该报告或凭据接口的版本；使用兼容修复版本向前恢复。尚未升级节点时，控制端仍可按正常发布流程回滚。

## 验证

- Go 回归覆盖同一物理节点两种展示、凭据作用域、共享名额与用量、轮换、缺失能力、三种订阅格式、原生客户端协议选择及测速。
- `HQIP_SINGBOX_EXE=<已验证 sing-box 路径> go test ./internal/reality -run TestSingBoxRealityGuardedWire -count=1 -v`：真实客户端通过 Reality 进入受准入保护的 HY2，验证 TCP、XUDP、共享租约、累计计量和拒绝授权。测试的服务、目标及凭据均在本机临时创建。
- `HQIP_REALITY_TARGET_SMOKE=1 go test ./internal/reality -run TestDefaultRealityTargetHandshake -count=1 -v`：使用真实微软默认目标完成本机 Reality 身份握手；此项需要联网，普通 CI 不依赖外部目标可用性。
- Python 回归覆盖配置恢复、保留实时账本、既有端口不重选、原有防火墙规则保留、启动及防火墙失败回滚。

依赖锁定见 `go.mod` / `go.sum`；Reality TLS 使用 [MetaCubeX uTLS](https://github.com/MetaCubeX/utls)，VLESS/XUDP 使用 [sing-vmess](https://github.com/SagerNet/sing-vmess)，握手参数对齐 [sing-box 1.14.4](https://github.com/SagerNet/sing-box/tree/v1.14.4)。
