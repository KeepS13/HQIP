# Clash 节点 DNS 与网站直连恢复验收

日期：2026-11-26（Asia/Taipei）。归属 `routing`、`subscription-admission.import-links v1` 与原生客户端配置。当前仅完成本地代码和测试，未提交、推送或部署；现有线上订阅尚未因本次工作改变。原生客户端准备 `1.0.0 / 5000023`，发布时须使用完整同提交产物。

## 问题与修复

本机 Clash Verge Rev 2.5.10 / Mihomo 1.19.33 的 vus1 Hysteria 2 与 VLESS Reality 共享服务器域名。原配置的全地址 `fallback-filter` 要求海外 DoH 答案；本地解析器已经返回节点 IPv4，但直连 `1.1.1.1` / `8.8.8.8` DoH 超时，出现 `dns resolve failed: couldn't find ip`。直接使用节点 IPv4 时两个协议均成功，证明这次不是协议不支持或凭据格式问题。

两条 Clash 配置生成路径统一增加独立的节点 DNS；海外 fallback 使用已有 `节点选择`，保留网站地址的直连 DNS。单独添加节点 DNS 时一次完整 HTTPS 请求约 6.20 秒，加入 DoH 节点出站后同目标约 1.84 秒；此为一次本机对照，不是延迟承诺。

`ServiceBypassHosts` / `ServiceBypassRules` 提取部署的 API、用户站、管理站精确主机，附加登录组件实际使用的 `challenges.cloudflare.com`。Clash 将这些直连规则放在大陆 IP 解析与旧管理员规则之前；sing-box、待开通配置同步。原生官方名单与 Windows 代理例外共享同一规则入口，例外不是授权 Origin 白名单。待开通仅允许恢复网站直连，其他请求继续拒绝；不会创建节点凭据。

## 回归与实际内核

- 新增回归先在旧代码失败：缺少独立节点 DNS、Clash 网站直连、sing-box 管理站和验证码，以及原生/Windows 完整网站名单。修复后通过。
- `go test -tags=with_utls ./internal/control ./internal/rules ./internal/client ./internal/winclient -count=1` 通过。显式设置四个 `HQIP_CLASH_CORES` 和 sing-box 1.14.4，真实配置解析与原有 HY2 路由测试没有跳过。
- `go vet -tags=with_utls` 对上述四个包通过；`git diff --check` 通过。
- `scripts/verify_clash_recovery.py` 在 Meta 1.16.0、Mihomo 1.18.10、1.19.33、1.19.34 均通过：备用 DNS 完全不回应时，域名节点仍连接；停止测试节点后，官网、管理站、验证码三项仍走 DIRECT。检查核心日志命中精确域名，排除回环 IP 规则掩盖缺失。四次测试均未修改系统代理。
- 用导出配置的 DNS/路由搭配本机现有 vus1 凭据，四个内核的 VLESS HTTPS 请求均返回 204；Meta 1.16.0 / Mihomo 1.18.10 的 HY2 首次也返回 204。1.19.33 / 1.19.34 的 HY2 首轮各有一次 TLS EOF；分别独立重建实例后，对 Google / Cloudflare / Google 连续三次请求全部成功（204/200/204），没有错误日志。无法从首次结果确定断流原因，不能把它写成稳定性保证。

库外证据位于 `.local/clash-recovery-20261126/`：`recovery-cores/results.json`、真实协议首轮与复测 JSON、脱敏日志、测试导出及图分析。真实凭据只在进程临时配置中使用，退出后删除，不进入仓库。

## 影响、性能与边界

GitNexus 编辑前：`clashDNS`、`clashConfig`、`portalSingBoxSubscription`、`pendingPortalConfig` 为 LOW；`officialBypassDomains` 为 HIGH，覆盖配置生成、Apply 和连接恢复，已事先提示。`portalYAML` 为 UNKNOWN，文本确认实际挂载 `GET /usub/{token}`，用 HTTP 导出回归补充；测试入口的 UNKNOWN 通过 Go 测试执行确认。

最终索引重建成功。整个工作区 `detect-changes --scope all --limit 1000` 报告 24 个已跟踪文件、15 个符号、599 个流程，CRITICAL，包含原有未提交的流量计量修改；提高到 10000 后仍只展示部分流程。索引本身也报告入口/分支探索上限，因此不将图结果称为完整无影响检查。新增未跟踪文件与 Go 接收者解析限制由实际调用检查和测试补充；本次未提交任何代码。

配置生成只增加少量主机规则，无新增数据库访问、写入、服务端外部请求或客户端轮询。海外 DNS 使用现有选择器和连接复用，节点 DNS 独立直连，避免自依赖。Windows 代理值生成拆至独立文件，修改的生产源文件均不超过 300 行。

生效需要发布服务端并让现有用户更新、重新启用完整订阅；原生用户需更新客户端。Clash 全局模式、任意第三方坏代理、节点 URI 导入和本地覆盖无法被网站服务端强制改写。直连保留用户实际网络出口，不保证用户位于大陆，也不保证所有运营商到网站/验证码的线路质量。尚未做生产发布验收、移动端真机验证或长期网络稳定性测试。
