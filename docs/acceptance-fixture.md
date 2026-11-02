# Windows 客户端双节点验收夹具

此工具仅供维护者进行短期真实验收。B 与 A 位于同一 VPS，但使用 UDP 8443、独立 Agent 18181、统计 18182、独立配置目录和 systemd unit。A 的 UDP 443、证书申请、配置和现有设备凭据不被修改。B 复用 A 域名，复制已有证书进行短期测试，不负责证书续期。

## 准备

本地控制服务已迁移并处于 `active`，A 必须完成外部验证并显示就绪。执行以下命令会在本地数据库登记 B 与一个明确标记的验收设备；不会 SSH，也不会创建安装任务。

```powershell
go run ./cmd/hqip-fixture prepare --controller-config .local/controller/controller.json --node-a <A的节点ID>
```

私密配置保存于控制服务受 ACL 保护的 `stateDir/fixture-b/node-b.json`，包含 B 的 Agent token、统计认证和探针密码，不要上传或打印。此操作给验收设备分配 A/B，默认 A；每个节点连接密码分别生成。新 B 显示待外部验证，不能直接当作可用节点。

通过管理员 API 为输出的验收设备 ID 创建客户端账号。不要重置日常设备账号。

维护者先只读检查远端实际证书、私钥与二进制路径，再生成远端脚本：

```powershell
python scripts/fixture_node_b_remote.py prepare --config .local/controller/fixture-b/node-b.json --cert <已核对的远端证书路径> --key <已核对的远端私钥路径> --output .local/controller/fixture-b/prepare.sh
```

默认复制 `/opt/hqip-node/agent` 与 `/opt/hqip-node/hysteria`；如果实际路径不同，使用 `--agent-bin` 和 `--hysteria-bin` 明确指定。生成器不连接、不执行 SSH。脚本含秘密，使用已固定主机指纹的 SSH 会话通过标准输入发送，禁止 shell tracing 或打印脚本内容。

脚本拒绝已存在的 B 目录、unit 和占用端口。故障后的再次运行需人工检查已完成阶段，不应删除目录强行重试。B 的 unit 为 `hqip-agent-b.service`、`hqip-hysteria-b.service`，使用 `/opt/hqip-node-b`、`/etc/hqip-node-b`、`/var/lib/hqip-agent-b`、`/var/lib/hqip-hysteria-b`。不会覆盖 A。启用 UFW 时会开放 UDP 8443。

## 验收顺序

1. 使用独立代理测试配置访问 B 的 UDP 8443，校验 TLS、HTTPS 和出口 IP；用 `node-b.json` 的 `probePassword` 作为测试凭据。只有确认成功后，才通过管理员现有外部验证接口标记 B 就绪。
2. 在 Windows 客户端登录验收账号，先连默认 A。将分配集合保持为 A/B，只改默认 B，确认客户端切 B，A 凭据仍授权。
3. 暂停 B 的 Hysteria unit。改默认 A 再改回 B，确认 B 失败后客户端保留连接意图、实际连接为空、系统代理恢复、没有回退 A。恢复 B 后，相同配置版本的健康状态改变不应自动重连；手动连接可恢复。
4. A/B 均授权时连接 A，再移除 A、默认 B。确认 Agent 首次接到命令才开始最长 60 秒撤销期，客户端成功或失败处理新版本均能提前确认；Agent 离线时应显示待同步。
5. 在应用切换途中断开连接，确认 UI 和实际内核最终均断开。制造新规则下载失败或校验失败，确认旧路由不继续运行。

B 与 A 公网 IP 相同，验证“确实用了 B”必须结合独立端口停启、B 的统计和会话观测；仅对比出口 IP 不足以证明选择正确。

## 清理与保留历史

先让验收客户端断开或退出。若有人另给日常设备分配过 B，先在管理页面移除这些分配；本地清理命令会拒绝自动改动这些设备。

```powershell
python scripts/fixture_node_b_remote.py cleanup --output .local/controller/fixture-b/cleanup.sh
```

通过已固定指纹的 SSH 执行生成的清理脚本，停止并禁用 B 两个 unit，保留配置、证书副本、账本与历史。然后执行本地清理：

```powershell
go run ./cmd/hqip-fixture cleanup --controller-config .local/controller/controller.json
```

本地会禁用验收设备、撤销客户端会话、移除验收分配、使 B 的 Agent token 失效，并把 B 标记为 `fixture_disabled`。节点、设备、凭据历史和用量不删除，A 保持原服务状态。私密配置文件仍留在受保护目录供审计；旧 token 已失效。UFW 的 8443 规则不自动删除，避免误删维护者已有规则，B 无监听时不会提供代理服务。
