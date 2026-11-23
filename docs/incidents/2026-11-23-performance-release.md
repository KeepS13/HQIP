# 性能与稳定性发布记录

发布日期：2026-11-23，以下时间均为 UTC。控制端、两个网站、Windows 稳定客户端 4.8.2 下载包及全部 16 台受管节点 Agent 已完成发布，最终生产验收时间为 `2026-11-23T21:05:02Z`。Godot/Android 预览版及其更新渠道不在此次稳定版发布中，现有本地工作保留。

生产源码为 `5f68c04b88a02fda38dbea6a5ddf0444823fa329`，完整 CI `45260498466` 的 Linux、Windows、PostgreSQL/Redis、真实客户端回归及部署任务均成功。发布产物来自同一干净提交，服务二进制、网站资源和 Windows 包的来源与摘要已核对。本记录的收尾提交只更新文档，使用 `[skip ci]`，不另行切换生产版本。

本次合入 VLESS 独立出站与公共 nodepolicy 依赖、网站单请求轮询、Agent 上报响应复用、客户端采样精简、真实域名握手测速、账户物理网络直连和连接命令幂等。行为与本地验证见 [性能评审](../reviews/2026-11-23-performance-stability.md) 和 [协议契约](../contracts/protocol-policy.md)。测速数值没有人工缩小。

发布前确认生产运行 cf9f6cc，16 个活动节点均健康且配置收敛。发布先切换控制端与网站，再以 vus1 为首台验证，随后逐台升级 Agent；每台均在成功回执后核对运行状态。Agent 重启会中断该进程持有的 VLESS 流，由客户端重连，不能称为零中断升级。

## 最终上线验收

- 两个本机 readyz、两个公网健康探针均返回正常；两个网站首页及所引用的 JS/CSS 摘要与发布清单一致，匿名访问受保护接口仍被拒绝。
- 控制端状态为 active/running，自动重启计数为 0；自本次启动以来未发现 panic/fatal 或数据库错误。没有 pending/running/uncertain 的服务器运维任务。
- 控制端记录 16 个活动节点，健康、最近 30 秒内上报、目标配置与应用配置一致的数量均为 16。
- 16 台节点均有成功升级回执和独立的升级后快照，运行中的 Agent 摘要一致。每台的 `coreHealthy`、`realityReady` 均为 true。
- HY2 核心的 PID、InvocationID、二进制摘要逐台与升级前一致；agent.json、hysteria.yaml 摘要及两个服务的自动重启计数也与基线一致。此次没有升级或重启 HY2 核心，没有轮换节点地址、端口、凭据或 Reality 密钥。
- 407 个用户、167 个订阅、17 个订单、167 个订阅周期的数量，以及订阅、分配和周期摘要与发布前一致；支付配置及主密钥摘要一致。正常实时用量不要求保持旧值，也没有回写旧账本。

升级节点：`dxzus1`、`itp0`、`rus1`、`rus2`、`rus3`、`vus1`、`vus2`、`vus3`、`vus4`、`vus5`、`vus6`、`wjp1`、`wjp2`、`ytjp1`、`zus1`、`zus2`。

Agent SHA-256：`63f5d2f31663218078faf779ab93916bbb7ddb0d7a7de7ad509541aee95cbca5`。Windows 4.8.2 ZIP 为 29,236,833 字节，SHA-256 为 `0cbda7906f198c2925f273ede12eba9aaf1da4b1976e43e64240088522e75d07`。网站下载包已更新不表示所有用户设备已自动安装新版。

## 真实代理验证与 HY2 未解决项

使用既有专用测试订阅，在本机回环 SOCKS 上验证最终 Windows 包的内核，没有接管系统代理、TUN、路由或 DNS。vus1 的 VLESS 真实出口正确；通过代理下载最终客户端包的前 256 KiB，返回 HTTP 206、262,144 字节，TLS 校验结果为 0，内容与已验证发布包逐字节一致。冷 HTTPS 请求耗时 2,885 毫秒，这包含协议与目标站开销，不能当作节点 RTT 或吞吐提升证据。测试停止后连接名额自然释放，最终占用为 0。

同一节点 HY2 的公网路径仍失败：出口请求 curl 退出码 97，有限下载退出码 28，未取得有效响应。此前的线上 HY2 测试已有 QUIC 超时记录，不能把该现象直接归因于此次升级。用户明确要求修复不了先更新，因此该线路问题没有阻塞后续 Agent 发布。

`2026-11-23T20:54:12Z` 的 vus1 服务端只读检查确认：HY2 服务 active、自动重启计数 0，UDP 443 由该核心进程监听，证书有效期为 2026-11-12 至 2027-02-10，UFW 未启用，最近两小时所读取的日志未见 panic/fatal。没有发现可直接修复的进程停止、监听缺失或证书过期问题。这不排除其他主机或上游过滤、网络丢包及本地线路问题，也不能据此确认 GFW 封锁或宣称 HY2 已恢复。

本次没有修改延迟显示数值、伪造测速成功、关闭证书校验或盲目更换 SNI/端口。第三方订阅仍使用原连接参数；测速口径由实际客户端决定。大陆三网多时段、全节点公网双协议连通性、峰值吞吐和 Android 真机不属于此次验收的覆盖范围。

## 发布过程中的问题

Windows 发布准备修复一项部署测试的临时路径规范化：Windows 8.3 路径别名与 resolve() 的长路径不相等，使测试提前触发“较新发布”保护，未达到原本要覆盖的迁移防降级检查。测试夹具改为规范路径后四项定向回归通过，生产回滚逻辑不变。

首轮 CI 45258423128 被 Windows 常规测试阻止：`TestOfflineRecoveryTimeoutStillAllowsKnownSlotRelease` 的 300 毫秒测试预算在共享运行器上超时。其余 Linux、真实 PostgreSQL/Redis、Windows 打包及三组真实客户端回归全部通过，生产未切换。旧测试本地连续 20 次通过；调整夹具使用生产的 4 秒预算，同时明确验证恢复请求保留正常报告预算、真实超时发生，以及已知空闲名额最终释放。生产超时和释放逻辑保持不变。

部分节点上传路径较慢，停止已核实身份的本机上传进程后，使用既有控制服务器 SSH 中转；控制端缺少 Paramiko 的直接中转方案在上传前退出，没有安装依赖或改动生产运行环境，随后改用工作站保持两端主机指纹校验的 SSH 隧道。一次隧道连接超时后，逐文件检查剩余节点，仅对未完成的 zus1 重新直传。所有节点均在七个暂存文件完整性核验成功后才执行升级；上传故障没有被当作运行服务升级成功，也未盲目重放已完成升级。

## 备份与回滚

控制端保留在线一致性 PostgreSQL 快照与部署回滚资料：

`/root/backups/hqip-ci-5f68c04b88a02fda38dbea6a5ddf0444823fa329-45260498466-1`

每台节点在 `/root/backups/hqip-agent-telemetry-<nodeId>-<timestamp>` 留存 Agent 升级备份，精确映射见库外最终清单。需要回退时使用仓库已有升级/回滚流程和版本保护，保留实时账务与已激活协议约束，不能用旧数据库或旧用量账本覆盖上线后新增记录。

原始证据保存在库外 `.diagnostics/performance-release/`：`ci-final.json`、`verified-artifacts.json`、`acceptance-before.json`、`acceptance-after-control.json`、`acceptance-after.json`、`nodes-before.json`、`nodes-final.json`、各节点 `device-slots-result-*.json` 与 `node-after-*.json`、`live-smoke.json`、`live-smoke-vless.json`、`hy2-service-vus1.json`。SSH 凭据和订阅密钥不写入发布文档、Git 提交或业务数据表。
