# NAT 节点部署与更新修复

2026-11-27，Asia/Taipei。实机 xus1-v 的网卡地址为 `10.0.186.2/24`，公网出口为 `45.205.28.141`；控制服务器到公网 TCP 80、TCP 443、UDP 443 的随机挑战收发均成功。旧实现直接绑定公网地址时报 `EADDRNOTAVAIL`，旧 Agent 更新脚本也把内网 HY2 监听误判为归属不匹配。

实现与支持范围见 [node-network v1](../contracts/node-network.md)。修改集中于预检、节点配置、两个协议的本机绑定、UFW 和 Agent 更新；公网身份及原有归属检查保持一致。NAT 候选的公网出口必须验证匹配，未知状态不放行；部署仍以真实公网代理验证作为成功条件。

## 本地验证

- NAT 回归先复现旧升级脚本的 `Cannot verify owned HY2 listener`；修复后自动识别、错误出口/无效本机地址拒绝、私网端口冲突、HY2/ACME/UFW 配置、旧配置兼容、Agent 升级及失败回滚通过。
- `go test -tags=with_utls ./internal/provision ./internal/agent ./internal/control ./internal/model -count=1` 全部通过；对应 provision、agent、model 的 `go vet` 通过。
- Linux Python 3.12 容器运行全部 326 项脚本测试通过，1 项跳过。Windows 相同脚本全集出现 17 个环境相关错误（符号链接权限及路径语义），NAT 专项 36 项通过；不把 Windows 全集记为通过。
- 工作区原有流量计量改动保留，不纳入本次发布。正式发布使用 CI 对仅包含本次修改的提交重新执行 Linux/Windows、PostgreSQL/Redis 和真实代理检查。
- 项目完整发布要求客户端产物来自同一提交，因此正式版本保持 `1.0.0`，构建序号递增到 `5000024`。

## 图谱与运行边界

编辑前执行 GitNexus impact；`assess` 为 HIGH，影响预检、安装和退役，已在修改前报告。Reality 回调、Agent 发布接口和 JSON 清单读取存在 UNKNOWN/动态引用边界，已结合实际引用、调用代码和测试核对，没有以空调用者集合当作无影响。提交前另执行更新索引与全变更检查，原始输出位于库外 `.local/nat-*`。

生产发布与 xus1-v 首次安装、重复部署、单独升级已完成，证据和可选质量探针的 NAT 限制见 [生产验收](../deployment/2026-11-27-nat-node-deployment.md)。本文本地验证不代替生产验收。
