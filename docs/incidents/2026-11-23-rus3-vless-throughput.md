# rus3 候选 VLESS 吞吐验证

日期：2026-11-23，时间为 UTC。用户指定使用 rus3 测试独立出站的新 VLESS，并确认更新中断影响。前一轮 dxzus1 的下载人为限速为 8192 字节/秒，目的为稳定性验收，不能据此推断实际吞吐。

## 结论与适用范围

当前 Windows 测试端到 rus3 的真实公网链路，两轮完整下载得到：

| 路径 | 第一轮 | 第二轮 | 按总字节 / 总时间计算 |
| --- | --- | --- | --- |
| 直接 HTTPS，单连接 | 102.601 Mbps | 97.121 Mbps | 99.785 Mbps |
| 新 VLESS，单连接 | 101.230 Mbps | 88.325 Mbps | 94.338 Mbps |
| 新 VLESS，四连接合计 | 170.115 Mbps | 147.898 Mbps | 158.231 Mbps |

新 VLESS 单连接约 **11.0–12.7 MB/s**，四连接合计约 **18.5–21.3 MB/s**；本次单连接结果接近同线路直接 HTTPS。Mbps / MB/s 均使用十进制单位，8 Mbps = 1 MB/s。两轮样本不能证明固定性能差值，也未测旧 VLESS 与新 VLESS 的同条件 A/B 对比。

每组总量 64 MiB；单连接一次下载 64 MiB，四连接分别下载 16 MiB。结果使用整组墙钟时间，包含握手和客户端启动请求的开销，不用瞬时峰值。六组共 384 MiB，12 个请求均 HTTP 200、完整收到预期字节，curl 退出码为 0，证书校验结果为 0；未超时、未重试。

这组下载源为 rus3 上的临时 TLS 数据源，经过真实公网测试端、真实 sing-box、候选 Reality/Vision 入口及独立出站。它测量客户端到节点的线路及协议处理，**不包含节点到任意外部网站的路径**。客户端仅通过单次 curl 的 `--cacert` 信任临时证书；没有关闭 TLS 校验或改动系统证书库。源站无压缩、无缓存，使用固定随机数据块，单请求最多 128 MiB，总分配预算 2 GiB，20 分钟自动退出。

## 节点出口与外部源对照

在 rus3 本机启动真实 sing-box，经候选 VLESS 访问 Cloudflare 下载源，下载 64 MiB 两轮结果分别为 **692.199、700.939 Mbps**；直接 HTTPS 对照为 **821.367、848.346 Mbps**。候选网关 CPU 用量分别约 29.65%、27.42% 的一个核心。每组只持续约 0.6–0.8 秒，是短样本节点侧转发结果，不能当作用户本地下载速度或长期带宽保证。

随后对该外部源的 16 MiB 并发请求全部收到 HTTP 403。绕过 VLESS 直接请求也收到 403，因此未将其记为 VLESS 连接错误或有效测速值，也未更换身份规避限制。

站内现有下载文件对照：rus3 直接下载约 **17.907 Mbps**，新 VLESS 下载约 **18.090 Mbps**，两次均完整收到 29,391,930 字节。该文件的四连接测试在 25 秒取样上限内总计约 17.959 Mbps，其中两条流仅收到约 493 KiB，未达到脚本的 1 MiB 单流采样门槛；该并发样本没有作为通过结果。由于直连和转发均受该下载路径限制，最终用受控 TLS 源测量客户端到节点的路线。

另一个公共下载源的预检分别返回空响应 / TLS 关闭错误，未作为有效吞吐样本。所有异常样本保留，没有只保留成功结果。

## 实现与隔离

活动节点标识 `LIj_YMUbZm2bf8Rug4`，节点地址 `38.182.100.28`。数据库中同名的已删除旧节点被明确排除。预检时间 `19:24:35Z`：生产 Agent 与 HY2 均 active，`coreHealthy=true`、`realityReady=true`；1 vCPU、约 1 GiB 内存、负载为 0，HY2 在线连接数为 0。

候选进程直接复用当前源码的 `internal/reality`、`internal/outbound` 和 `runtime/nodepolicy`，使用 `with_utls` 构建。真实的数据封装、套接字、预算守卫、续约和固定点计量均参与测试；准入 HTTP 为独立内存 fixture，使用随机测试凭据、2 GiB 租约预算、倍率 100 和 **无套餐限速**，不读取生产凭据，不接入生产数据库。因此本结果是协议转发吞吐验收，不能替代完整 Agent / 控制端事务压力测试，用户实际速度还受套餐及线路限制。

临时 Reality 入口使用 TCP 18443，临时 TLS 源使用 TCP 18543，均绑定该节点 IPv4。生产 TCP / UDP 443 保持运行。未修改防火墙、系统代理、路由、DNS 或生产配置，也没有部署候选 Agent。

| 产物 | SHA-256 |
| --- | --- |
| 候选 gateway | `37bdbcbdb2db1dd480c9f4c05dcd0a9e8b57cf943d76d1cb6ffac0b74abec33f` |
| Linux sing-box | `ace52a1d248f20ba08472dbff4ef919620ffeff9901924d1025dec2c19265ee7` |
| 临时 TLS 源 | `d400954d9d4e1480b06e2a807693e8aa00bb7ca66f7f68d9e11169335ac93942` |

候选网关记录到的 RSS 高水位为 13,116 KiB；Linux sing-box 为 38,528 KiB；TLS 源为 8,624 KiB。临时源码位于忽略目录 `.local/vless-rus3/`，操作脚本和原始证据位于 `.diagnostics/vless-independent/`。本轮未修改生产源文件。

## 清理与线上状态

于 `19:39:35Z` 完成清理：先核验每个测试进程的可执行路径和启动 tick，再正常停止自有 sing-box、候选网关、TLS 源；五个 fixture 租约均已关闭。下载日志后核验精确临时目录和产物摘要，确认无引用该目录的进程，再删除测试目录。TCP 18443 / 18543 已释放，Windows 上本轮客户端也已退出。

生产 Agent PID `1844640`、HY2 PID `1691300`、二进制摘要及 InvocationID 与预检一致；重启次数均为 0，`coreHealthy=true`、`realityReady=true`。原线上服务和用户订阅没有被切换。

原始证据：`rus3-preflight.json`、`rus3-speed-builds.json`、`rus3-node-speed.json`、`rus3-node-speed-hqip.json`、`rus3-route-speed.json`、`rus3-usage.json`、`rus3-origin-usage.json`、`rus3-postflight.json`。

## 更新中断说明

更新并重启相关宿主进程时，现有连接会短暂中断，需要客户端重连。重启 Agent 影响其 VLESS 流；若同时重启 HY2 核心，HY2 连接也需要重连。中断时长取决于服务恢复和客户端重试，不能承诺固定秒数。

保留节点地址、端口、原凭据及 Reality 密钥后，原订阅继续有效，无需重新订阅或导入。独立出站版本上线后，单独 HY2 核心崩溃或重启不再结束 VLESS 的授权和目标套接字；Agent 自身重启仍影响 VLESS。完整功能验收见 [dxzus1 双协议验收](2026-11-23-dxzus1-protocol-stability.md)。
