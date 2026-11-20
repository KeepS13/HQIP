# 协议流量计费

后台「系统设置 → 流量计费」管理全局 Hysteria 2、VLESS + Reality 倍率。整数百分位保存，150 = 1.50，范围 1～1000；PUT 必须带 expectedRevision，冲突返回 409。操作人、时间、前后配置保存在独立审计表中。

新安装和迁移后的配置均预填 1.50，但保持未启用；首次保存启用前，控制端检查所有 ready 节点都已报告 traffic-billing-v1。须先升级控制端、Agent、转发内核、HQIP 客户端，再启用。旧授权继续按 1.00 结算，不修改已有用量。

启用后，不支持协议计费的新授权请求会被拒绝；已有旧授权可继续续租、补账，不会被套用新倍率。

## 账本与生效

- 原始上下行保留在租约和管理员遥测中；订阅 portal_usage 存计费字节。
- 每段（实际上传 + 实际下载）× 协议倍率，不再乘双向转发系数。内部单位为百分之一字节，以整数检查溢出；上传、下载共享订阅余数，分包和重连不丢弃余数。
- 预留、普通离线预算采用计费单位。16 KiB 准入按所选协议换算；应急池上限仍为实际字节，其用量按段倍率计入订阅。
- 新版本随续租下发。内核接受响应后，在计数锁内创建新段，并记录时间、原始字节边界与授权有效期限。响应丢失时继续旧段；中央只按已签发且已上报的版本结算。
- VLESS 网关使用 Agent 密钥签名的短期内部标记，绑定物理节点和实际凭据；客户端自报协议不能选择倍率。离线许可、缓存、请求幂等键隔离入口协议。
- 重放使用累计分段记录核验；已确认旧段可从上报包移除，永久账本仍保留。租约过期不会释放尚未确认的预算，最终补报仍按原段结算。

## 客户端显示

仅 HQIP 客户端从经身份验证的 GET /api/v3/client/billing 获取当前用户、原生会话、物理节点与入口协议的已确认计费段。接口不包含全局设置或审计记录。

sing-box 仅在 127.0.0.1 开启 V2Ray stats，并只统计 proxy 出口；TCP、UDP、已关闭短连接均计入，direct 不计入。仅启用一个出口计数，避免 selector 和底层出口叠加。发布包通过 scripts/build_billing_core.py 从固定 1.14.4 模块、Go 1.26.10、构建标签生成，校验源码及可执行文件哈希，包含 with_v2ray_api；不修改第三方源码。

每秒读取累计代理字节；保留最近约五分钟带时间的原始样本，延迟到达的确认边界按采样区间比例拆分。总量按段累加，速度由区间计费增量除以实际时间计算，最近 60 秒曲线同源。旧总量与曲线不整体乘最新倍率。

显示注明“按计费口径显示，结算以服务端为准”。无有效倍率时保留最近估算并标记不可用；失联仍在有效期内时继续最后已确认倍率并标记同步过期。未知区间不猜测倍率，重新确认后重建采样基线。分段时间和采样时间不同，本机估算不保证与服务端逐字节一致。订阅余额始终来自服务端。

普通订阅导出无倍率字段；第三方客户端自行显示的流量不在此实现范围。带宽限制和实际转发速度均保持原始字节口径。

## 升级与回退

SQLite 和 Agent 状态库执行增量建表；PostgreSQL 由迁移角色执行：

```sh
hqip migrate-traffic-billing
```

完整发布包含 trafficBillingVersion: 1，升级脚本在切换二进制前调用迁移。控制端、Agent 回退检查会拒绝不理解新账本的版本，保留现有余额、未确认预算与补报。不能以恢复旧数据库快照的方式回退计费。

Windows 构建：python scripts/build_desktop.py；生成 HQIP 4.8.0 与经过校验的统计内核。启动器仍校验内核哈希。

## 验证

```sh
go test -tags=with_utls ./internal/store ./internal/control ./internal/agent ./internal/client
go test ./server ./billing ./internal/integration_tests # runtime/hysteria/core
python -m unittest discover -s scripts -p 'test_*.py'
```

设置 HQIP_TEST_POSTGRES_URL 为临时本地 PostgreSQL URL 后，同一存储测试集覆盖 PostgreSQL、迁移与并发事务。设置 HQIP_SINGBOX_EXE 为本次打包的 sing-box 路径可运行 TestPinnedCoreProxyStatistics；另设 HQIP_E2E_CORE 为本次 Hysteria 内核路径，可运行 TestTrafficBillingRealDualProtocolHotUpdate，测试实际 HY2 + Reality、真实 Agent 与控制端、持续传输中两次倍率调整、租约不中断和限速不变。所有端到端测试只使用本机回环、临时数据和测试证书。
