# VLESS 独立出站验证记录

日期：2026-11-23。范围：本地实现、构建和测试；未提交 Git，未执行线上部署。工作区原有客户端、Godot、原生端等改动保留，不作为本次协议解耦成果。

## 实现与兼容边界

- VLESS / Reality 由 `internal/outbound` 直接建立目标 TCP / UDP 套接字，入口和出站包不再依赖 HY2 的 client、server 或 internal 包。旧 `reality.backendPort` 可继续读取，但不参与转发。
- `runtime/nodepolicy` 提取原 HY2 授权守卫、限速、预算、计费段、续约、到期与幂等 final；HY2 保留薄适配器。鉴权、套餐、节点额度、来源 IP 名额和累计结算仍由原 Agent / store 规则处理，没有第二套业务判断或新增业务表。
- VLESS 授权代次属于 Agent，HY2 授权代次属于核心进程。HY2 停机不使 VLESS 授权失效，也不隐藏仍可用的 Reality 端点；Agent 重启仍会中断 VLESS。
- 原始遥测使用独立累计基线，写入既有日、分钟、小时统计，不重复扣套餐。HY2 停机时已知 VLESS 流量继续上报；整节点小时完整性仍保守保留 HY2 的采样边界。时钟回退、首次累计样本和超过 24 小时的采样间隔有专门回归。
- 保留受管节点 IPv4 绑定、1.1.1.1 DNS 和默认私网拒绝规则；默认 ACL 列表与 HY2 配置生成共用。单独人工修改 HY2 YAML 的网络策略不再隐式影响 VLESS，应统一维护公共出口策略。

详细契约：[protocol-policy](../contracts/protocol-policy.md)；模块说明：[nodepolicy](../modules/nodepolicy.md)；运行说明：[Reality](../reality.md)。

## 已通过的验证

| 验证 | 结果与实际范围 |
| --- | --- |
| 根模块 `go test -tags=with_utls ./...`、`go vet -tags=with_utls ./...` | 通过全部根模块包；依赖环境变量的外部进程测试另行执行 |
| Agent / outbound / provision 竞态检测 | `go test -tags=with_utls -race ./internal/agent ./internal/outbound ./internal/provision` 通过 |
| 公共模块 | `runtime/nodepolicy` 的普通测试、竞态检测和 vet 通过，包含从 HY2 迁移的预算并发、速率、到期、计费段及截断回执测试 |
| HY2 兼容 | Hysteria 工作区的公共模块、`core/server`、`core/internal/integration_tests`、`app/cmd` 测试通过 |
| 无 HY2 的真实 Reality 互通 | `TestSingBoxRealityGuardedWire` 通过 legacy / Vision、TCP、TLS 1.2 / 1.3、XUDP、计量和拒绝授权；测试不启动 HY2 |
| Reality 互通竞态检测 | 上述 wire 测试通过 `-race`；沿用已记录的 sing-vmess/vless 专用 `-gcflags=github.com/sagernet/sing-vmess/vless=-d=checkptr=0`，未关闭全局检查 |
| 真实 HY2 进程故障 | `TestRealityDirectSurvivesHY2ProcessCrashAndRestart` 通过；真实控制端与 Agent 准入下，同一独立出站租约及目标 TCP 连接经历测试 HY2 进程被强制终止、重启后继续传输；停机期间新准入、续约、名额释放、结算和凭据轮换撤权通过 |
| 双协议业务一致性 | `TestTrafficBillingRealDualProtocolHotUpdate` 通过；两个 HY2 来源加一个 VLESS 来源占用三个名额，超限拒绝，两次倍率热更新保持授权和精确累计结算 |
| 统计边界 | `TestDirectTelemetry*`、`TestNativeHours*`、`TestHourly*` 通过；修复前新增四项测试均复现问题，修复后通过 |
| 构建与依赖 | Linux amd64 / arm64 Agent、Linux amd64 HY2 构建通过；desktop、native/mobile 的 `go list -mod=readonly -deps ./...` 通过 |
| 部署相关 Python 定向回归 | `test_reality_upgrade`、`test_reality_release`、`test_connection_slots_deployment`、`test_build_scope` 共 25 项通过 |

进程故障测试直接驱动 VLESS 使用的独立出站和生产业务入口；真实 Reality 封装互通由独立的无 HY2 wire 测试覆盖。测试只关闭自己启动、持有句柄的 HY2 进程。构建产物与临时图查询结果保存在本地 `.diagnostics/vless-independent/`，不写业务数据库。

## 验证限制

Python 全套 `python -m unittest discover -s scripts -p 'test_*.py'` 共 262 项，16 项跳过，16 项因 Windows 缺少符号链接权限报 `WinError 1314`；另有 1 项失败：`test_rollback_refuses_old_controller_after_attempt_even_before_switch` 期待 `prevents rollback`，实际为 `Refusing rollback over a later release`。该失败已单独复现，涉及的 `scripts/test_allocation_deployment.py` 与 `scripts/upgrade_domains.py` 均未修改。不能将该全套结果记为通过。

GitNexus 最终增量刷新曾因 315 MiB 缓冲池不足失败；本次命令设置 `GITNEXUS_LBUG_BUFFER_POOL_SIZE=1073741824` 后完整重建成功。工具日志显示 16,205 个节点、63,425 条边、538 条流程；尽管带合并输出重定向的重建执行返回了非零状态，随后单独 `status` 返回 0，确认索引与 1209 个覆盖文件匹配、状态为 up-to-date，独立变更查询也返回 0。

最终 `detect-changes --scope all --limit 10000 --repo .` 报告 49 个已跟踪变更文件、76 个符号、23 条受影响流程，整体风险为 **CRITICAL**，包含原有未提交改动。统计修复前旧图对 `recordDirect` 的上游 impact 也报 CRITICAL，已在继续修改前告知；完整重建后的同项为 LOW，直接调用方为 `sampleDirect`，经 `sample` / `trackedDirect.Close` 到采样、退役及强制排空路径，共 6 个上游符号。`NewSession` 为 LOW，直接调用方为 `connectRealityBackend`。单项 LOW 不抵消整个变更集的 CRITICAL。

变更查询文本仅预览 15 个符号及 10 条流程，增加 limit 不改变此预览；格式器未报告本次 detect 的 `partial` / `truncated` 标志，但索引阶段有动态接口解析、候选上限和流程提取截断警告。新建未跟踪文件也不能仅靠 tracked diff 的摘要覆盖。该结果不是“无影响”证明，应结合直接依赖检查、原守卫测试、业务集成和真实协议/进程测试审查。
