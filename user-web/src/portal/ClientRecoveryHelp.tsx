import { useEffect, useState } from "react";
import { CopyButton, ErrorNotice, Loading } from "../components";
import { userAPI } from "./api";
import type { ImportClient } from "./client-imports";

// Fetch public guidance only when opened. The server owns the recovery hosts;
// never infer parent-domain/CDN exceptions or maintain a second host list here.
export function ClientRecoveryHelp({ client }: { client: ImportClient }) {
  const [open, setOpen] = useState(false);
  const [hosts, setHosts] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!open || hosts) return;
    const controller = new AbortController();
    let disposed = false;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setError("");
    void userAPI<{ directAccessHosts?: string[] }>("/config", "GET", undefined, controller.signal)
      .then((config) => {
        if (!config.directAccessHosts?.length) throw new Error("直连地址暂不可用，请稍后重试");
        setHosts(config.directAccessHosts);
      })
      .catch((failure: Error) => {
        if (!disposed) setError(controller.signal.aborted ? "读取直连地址超时，请重试" : failure.message);
      })
      .finally(() => clearTimeout(timeout));
    return () => { disposed = true; clearTimeout(timeout); controller.abort(); };
  }, [open, hosts, attempt]);
  const nodesOnly = client.format === "v2ray";
  return <details onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>官网直连与连接故障恢复</summary>
    <p className="client-import-help">{nodesOnly
      ? "此链接只提供节点，更新订阅不会自动添加官网直连规则。请在客户端为下列地址添加优先直连规则，使用本机网络访问。仅开启「绕过大陆」不一定包含本站。"
      : "完整配置已包含下列网站及登录验证码的优先直连规则。请更新并启用本订阅；Clash 使用「规则」模式，保留订阅的 DNS 和规则。"}</p>
    {open && <>
      <ErrorNotice message={error} retry={() => setAttempt((value) => value + 1)} />
      {!hosts && !error && <Loading />}
      {hosts && <>
        <ul>{hosts.map((host) => <li key={host}><code>{host}</code></li>)}</ul>
        <CopyButton value={hosts.join("\n")} label="复制直连地址" />
      </>}
    </>}
    {nodesOnly && <p className="client-import-help">{client.id === "shadowrocket"
      ? "在当前配置的规则顶部为每个域名添加 DOMAIN → DIRECT，再选择「配置」路由。IP 地址使用对应 IP-CIDR 规则。复制的是地址列表，需要逐项添加。"
      : "在当前路由的顶部新增规则：域名使用 full:主机名，出站选择 direct（直连）；IP 地址填入 IP 规则。保存并启用该路由。复制的是地址列表，需要逐项添加。"}</p>}
    <p className="client-import-help">如果开启代理后官网进不去，先断开代理或 VPN，再打开本站、更新订阅并按上述步骤设置。Hysteria 2 无法连接时可切换 VLESS + Reality；网络限制 UDP 时尤其如此。一键导入未唤起软件时，请复制链接手动添加。</p>
  </details>;
}
