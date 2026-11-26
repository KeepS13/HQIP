# 大陆直连规则与客户端兼容

模块：`routing`（`internal/rules`）。消费者：第三方 Clash/Mihomo、sing-box 配置导出，HQIP 客户端与 Windows TUN 辅助程序。业务边界见 [订阅导入契约](contracts/subscription-import-links.md)。

## 原因与生效范围

原 `rules.Default()` 只有局域网域名、私网 IP 和最后的代理兜底，没有大陆域名或大陆 IP；保存过这个默认包的服务端不会因为修改默认函数而自动更新。Clash 门户导出还缺少 DNS 分流；原 GEOIP 翻译使用用户各自的数据库，与服务端固定的 CN 前缀不一致。

现在默认包包含大陆规则，配置生成时还统一应用 `WithMainlandBypass`，覆盖已有 LAN-only 包和把哔哩哔哩交给代理的旧服务分组。导出不修改已保存规则版本、哈希、节点凭据、订阅授权或计费状态。HQIP 原生客户端在规则模式应用同一策略，显式全局模式保持全局代理。

匹配顺序：已配置的本地域名直连；大陆域名直连；大陆 IPv4/IPv6 直连；其余管理规则按原顺序执行，保留原终止动作。大陆保护规则优先于历史代理／拒绝规则；未知海外流量沿用默认 `MATCH → 节点选择`。服务端、节点和本机控制通道的必要绕过规则仍由各配置生成器放在前面。

Clash 输出 `DOMAIN`、`DOMAIN-SUFFIX`、`IP-CIDR`、`IP-CIDR6` 等普通规则，CN 数据直接展开，无需启动时下载 MMDB、GEOSITE 或 rule-provider。大陆 IP 规则允许解析，避免 `no-resolve` 让未收录域名跳过 IP 判断。IPv6 与 IPv4 都覆盖。

Clash 使用 redir-host DNS；大陆及明确直连域名通过 `nameserver-policy` 使用 `223.5.5.5`、`119.29.29.29`，其他域名使用海外 DoH fallback；`.local`、`.lan`、`localhost` 保留系统解析，避免把局域网名字交给公共 DNS。fallback 禁用 GeoIP 判定，避免再次依赖用户的数据库。HTTP Host／TLS SNI 嗅探可识别应用直接连接 IP 的场景，`override-destination=false` 保留原目标地址。为了兼容 Meta 1.16.0，没有输出它不支持的 QUIC 嗅探项；QUIC 仍依据已知域名或目标 IP 分流。

sing-box 导出为大陆域名设置本地 DNS。HQIP 原生客户端从 `5.0.0-preview.9` 起，用于 IP 分类的 `resolve` 遵循 DNS 分流，不再强制本地解析：已知大陆域名本地解析，未匹配域名通过节点使用远程 DoH，避免污染答案导致海外域名误走直连或直接解析失败。未收录域名仍按可信解析结果进行 CN IP 判断，CDN 选择可能与本地 DNS 不同。第三方导出配置的历史行为不由本次原生修复变更。HQIP 只有在旧缓存 CIDR 与有效策略一致时才使用编译缓存，否则内嵌当前 CN 前缀。Windows 特权辅助程序允许该未指定 server 的声明式解析动作；不会接收任意路径、脚本、额外 DNS 服务器或监听器。

## 节点解析与官网恢复修复（2026-11-26）

现场 Clash Verge Rev 2.5.10 / Mihomo 1.19.33 的 vus1 HY2 与 VLESS Reality 均因节点域名进入全地址 fallback，而直连海外 DoH 超时。修复在两条 Clash 导出路径统一设置节点专用直连 DNS，并让海外 DoH 使用现有 `节点选择`，避免节点引导失败及每个未缓存海外域名的约 5 秒直连 DNS 等待。

`internal/rules/service_bypass.go` 统一精确网站主机规则：部署配置中的用户站、API、管理站，以及 Turnstile 的 `challenges.cloudflare.com`。Clash 将直连规则放在大陆 IP 分类和管理员规则之前，并提供对应本地 DNS；sing-box 补齐管理员站点和验证码。待开通配置也保留恢复入口，其他流量继续拒绝。HQIP 客户端同步官方站点的路由/DNS 与 Windows 代理例外。此名单不是认证来源或任意域名信任名单。

`scripts/verify_clash_recovery.py` 使用 HTTP 订阅测试导出，节点地址保留为测试域名、备用 DNS 故意不响应，验证节点仍可建立连接；随后关闭测试代理并验证三个网站仍命中精确 `DOMAIN → DIRECT`，排除回环 IP 规则掩盖错误。测试不接管系统代理。兼容版本及现场完整 HTTPS 测试见 [本次验收](reviews/2026-11-26-clash-dns-recovery.md)。生效及客户端无法覆盖的边界见 [导入契约](contracts/subscription-import-links.md)。

## 数据与重建

固定快照日期：2026-11-23。域名由 V2Fly 官方编译器展开 `cn`（即 `geosite:cn` 的兼容集合），保留 include 属性过滤和 full/domain 区别。不是从个人订阅复制节点或凭据。

| 来源 | 固定提交 | 内容 |
| --- | --- | --- |
| `v2fly/domain-list-community` | `dff1013c77ee24daaffb2168b5b425894a9f87bc` | 官方 `cn.txt`：6,769 项 |
| `gaoyifan/china-operator-ip` | `a1214434b6211eca49508ef42989daaa83fa5436` | `china.txt` 6,257 条、`china6.txt` 3,446 条 |
| IP 数据许可证 | `0ca552913948e444853d3bce99f2f3f2477f17c7` | MIT，原项目导入工具沿用的许可提交 |

域名快照中 6,766 条 full/domain 直接转换；AWS 有限正则展开为 400 个后缀，另一个 AWS `.cn` 正则由 `.cn` 覆盖，共 7,166 条。`^.+-mihayo\.akamaized\.net$` 是唯一未展开的无限正则：旧 Clash 的普通后缀无法精确表达，保留 IP 兜底，不把整个共享 `akamaized.net` CDN 设为直连。这个边界意味着不能声称数据覆盖所有未来新增域名、加密隐藏域名或海外 CDN 的纯 IP 流量。

输入／输出 SHA-256：

| 文件 | SHA-256 |
| --- | --- |
| 官方展开 `cn.txt` | `4f895181d042ed46b7fd3e34b7da2bbfa6a41910b9621bf82c69d8d0bfdd04c2` |
| `china.txt` | `c6c8512853a6400a535a66c814c91a5819ad6389377f54a7688ffd2557ea2e2e` |
| `china6.txt` | `149c1227b848764228dfeaa1ab7b31e9d582fde39f70a55ce9dff5aa61a5b3ac` |
| `internal/rules/data/mainland.json` | `623a18bfc54ca18f5729b96759a0ddeb108833da7b9de6ae3792a5ce3cd9cd2e` |

两个上游 MIT 许可证随 `internal/rules/data/` 保留并嵌入程序。来源说明和验收证据存放本文及库外 `.local/mainland-routing/`；未增加业务数据库表或导入记录。

重建：下载上述固定提交的域名源码，在其目录运行 `go run . -exportlists=cn -outputdir=<绝对输出目录>`，取得 `cn.txt`；从固定 IP 提交取两个地址文件，再运行：

```powershell
python scripts/build_mainland_rules.py --domains <cn.txt> --ipv4 <china.txt> --ipv6 <china6.txt>
```

构建器校验三个固定输入的 SHA-256，未知域名语法会报错。更新数据必须同时审查提交、输入摘要、正则转换和测试，再重建控制端／客户端；订阅请求不在线下载规则。现有 JSON 规则格式保持 schema 1，有效规则最多 40,000 条，Clash 展开最多 100,000 条，超过即明确失败。

## 兼容边界

| 客户端家族 | 本次处理 |
| --- | --- |
| Clash Verge Rev、Clash Meta Android、Mihomo 及其他 Meta 外壳 | 更新并启用完整订阅配置，使用规则模式；大陆域名、IP、DNS 与嗅探随配置导入 |
| 经典 Clash／旧 CFW 自带不支持 HY2、VLESS Reality 的核心 | 普通规则格式不能补齐传输协议；需要换用支持这些协议的核心，不能承诺仅修改规则就可运行 |
| 官方 sing-box 图形客户端 | 完整 JSON 内置路由和 DNS，按 1.14.4 实测 |
| HQIP Windows | 规则模式与 TUN 校验器同步，缓存不会覆盖新策略 |
| v2rayN、v2rayNG | 当前 Base64 URI 订阅只传节点；需客户端启用绕过局域网和大陆，并更新 Geo 资源 |
| Shadowrocket | 当前 Subscribe 链接只传节点；需另选包含大陆域名和 CN IP 直连的配置，使用配置路由 |

全局模式、本地规则覆写、订阅转换器只抽取节点、操作系统分应用代理和应用自带 DNS 可能改变最终效果。门户已按客户端说明上述区别。服务端不能通过节点 URI 强制修改第三方软件的路由。

## 验证方法与结果

新增回归先在旧代码复现：28 个大陆平台／CDN 域名与大陆 IPv4/IPv6 地址走代理；新策略通过相同测试。包含 `bilibili.com.example.org`、`evilbilibili.com` 等负例，防止过宽的关键词直连。

真实解析器：Clash Meta 1.16.0、Mihomo 1.18.10、Mihomo 1.19.34 从空目录加载 HTTP 订阅接口生成的 HY2 + VLESS Reality 完整配置；sing-box 1.14.4 加载默认及自定义规则配置。没有预置用户 Geo 数据库。

真实分流使用 `scripts/verify_clash_routing.py`：仅将 DNS 端点、节点传输和入站监听替换为本机测试服务，保留导出规则、DNS 策略键、模式和选择器。分别验证 24 个大陆域名直连、5 个海外／相似域名代理，关闭测试代理后再验证 3 个大陆域名成功且海外请求失败。检查核心日志命中域名规则，避免回环 IP 规则掩盖错误；检查大陆 DNS 没有发往备用解析器。另验证 IP-only CONNECT 后的 HTTP Host 嗅探。

传输层另用 `TestPortalSingBoxExportRealHY2Routing`：真实 HTTP 订阅、真实 HY2 HTTPS、直连、拒绝与停止节点后的行为；修复了该历史测试仍假设节点导出为 IP 的过时断言，使用隔离 hosts DNS 提供节点域名的本机解析。

运行示例（所有内核进程／端口属于测试，不修改系统代理、DNS 或生产账户）：

```powershell
$env:HQIP_CLASH_CORES = '<Meta 1.16.0.exe>;<Mihomo 1.18.10.exe>;<Mihomo 1.19.34.exe>'
$env:HQIP_SINGBOX = '<sing-box 1.14.4.exe>'
$env:HQIP_RULES_ARTIFACTS = '<测试输出目录>'
go test -tags=with_utls ./internal/control ./internal/rules ./internal/client ./internal/winclient -count=1
python scripts/verify_clash_routing.py --config <exported-clash.yaml> --core <核心路径> --output <日志目录>
python scripts/verify_clash_routing.py --family singbox --config <exported-singbox.json> --core <sing-box路径> --output <日志目录>
```

Python 实测工具需要 PyYAML。未设置内核环境变量时 Go 的可选真实内核测试会明确跳过，不能视为实测通过。Android/iOS/macOS 的 GUI 导入、TUN 接管，以及真实用户网络中的 CDN 选择不由 Windows 本机隔离测试替代。

性能：单份双协议测试导出约 1.20 MB，16,872 条规则、7,169 个 DNS 策略；三个 Clash 内核的内部配置初始化约 20 ms。本机 Windows/AMD 9950X 的规则翻译基准约 3.58 ms、3.80 MB 分配／次，包含 Normalize 和 IP 展开，不含数据库读取、YAML 序列化或网络。数据只读嵌入，导出不发外部网络请求；负载与合并订阅节点数及配置长度有界。此数据不是生产并发压测结论。

首次全仓回归暴露 Windows TUN 白名单未允许新字段，修复后该包通过。工作区原有 `TestNodeProbeDomainReachesTransport/reality` 曾因 `localhost` 选中 `::1` 而连接拒绝，单独及随后客户端完整包复验通过；保留其首次失败记录，没有修改原有节点探测代码或测试。

最终 `go test -tags=with_utls ./...`、`go vet -tags=with_utls ./...` 通过，其中显式设置了三套 Clash 内核及 sing-box 环境变量。用户站 12 项测试及生产构建通过；规则导入／快照构建共 7 项 Python 单测通过，固定输入重建与提交快照逐字节摘要一致。未设置真实 PostgreSQL／Redis 的专项环境变量，未执行移动端真机验收。

GitNexus：编辑前 `Default` 为 HIGH，`Normalize` 的精确消歧结果为 CRITICAL，影响存储规则、启动和客户端配置等共享路径；`ClashRules`／门户处理器曾为 UNKNOWN，补查实际调用点与 HTTP 导出回归。最终索引重建成功，`detect-changes --scope all --limit 1000 --repo .` 报告整个工作区 33 个文件、33 个符号、471 个受影响流程，风险 CRITICAL，包含用户原有节点探测／文档改动。首次增量索引因 302 MiB 缓冲池耗尽失败，提升单次分析缓冲池至 2 GiB 后强制重建并再次检查，未将失败索引上的低风险结果视为有效。图索引仍有 Go 接收者解析及流程探索上限，统计不代表完整运行时覆盖，结论由上述实际测试补充。

2026-11-23 13:12:20 UTC 已部署至正式服务器，发布提交 `cf9f6cc04f9be7ba482b946a8380fc3220c74578`，HQIP Windows 为 4.8.1。正常 CI 门禁和独立线上验收均已完成；首次计费端到端测试失败、复测与重跑结果如实保留，详见 [生产发布记录](reviews/2026-11-23-mainland-routing-deploy.md)。用户须更新并重新启用完整订阅，使用规则模式；HQIP 原生客户端需升级至 4.8.1，本地下载文件需重新下载，节点 URI 用户仍需设置自己的路由。
