import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Plus, Server, Trash2, Settings2, Globe2, Clock3 } from "lucide-react";
import { api, usePolling } from "../api";
import { Badge, Breadcrumb, Button, Empty, ErrorNotice, Field, Loading, Modal, Notice, PageHeader, Panel, RefreshButton, Resource, bytes, date, useToast } from "../components";
import type { ManagedServer, ServerAddress, Node } from "../types";

function ServerForm({ server, onClose, onSaved }: { server?: ManagedServer; onClose: () => void; onSaved: (server: ManagedServer) => void }) {
  const [form, setForm] = useState({ name: server?.name || "", sshHost: server?.sshHost || "", sshPort: String(server?.sshPort || 22), sshUser: server?.sshUser || "root", password: "" });
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const change = (key: keyof typeof form, value: string) => setForm(p => ({ ...p, [key]: value }));
  async function submit(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const result = await api<ManagedServer>(server ? `/servers/${server.id}` : "/servers", server ? "PATCH" : "POST", { ...form, sshPort: Number(form.sshPort), expectedRevision: server?.revision || 0 });
      setForm(p => ({ ...p, password: "" })); onSaved(result);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal title={server ? "编辑服务器" : "添加服务器"} description="检查 SSH 连接与主机身份，节点将在下一步单独部署。" onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack" onSubmit={submit}>
      <Field label="服务器名称"><input required maxLength={80} value={form.name} onChange={e => change("name", e.target.value)} placeholder="例如：香港 VPS" /></Field>
      <Field label="SSH 管理地址"><input required value={form.sshHost} onChange={e => change("sshHost", e.target.value)} placeholder="IP 或完整域名" autoCapitalize="none" /></Field>
      <div className="form-grid"><Field label="SSH 用户"><input required value={form.sshUser} onChange={e => change("sshUser", e.target.value)} /></Field><Field label="SSH 端口"><input type="number" min={1} max={65535} required value={form.sshPort} onChange={e => change("sshPort", e.target.value)} /></Field></div>
      <Field label="SSH 密码" hint="仅用于验证本次连接；部署节点时重新输入。"><input type="password" autoComplete="new-password" required value={form.password} onChange={e => change("password", e.target.value)} /></Field>
      <Notice>HQIP 会固定 SSH 主机指纹。新增公网 IP 请先在 VPS 厂商和系统中配置，再加入此服务器的 IP 清单。</Notice>
      <ErrorNotice message={error} /><div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" busy={busy}>检查并保存</Button></div>
    </form>
  </Modal>;
}

export function Servers() {
  const { data, error, loading, refresh } = usePolling<ManagedServer[]>("/servers");
  const [add, setAdd] = useState(false); const navigate = useNavigate();
  return <>
    <PageHeader title="服务器" eyebrow="SERVERS" description="先登记 VPS，再为各个公网 IP 创建独立节点。" actions={<><RefreshButton refresh={refresh} /><Button icon={Plus} onClick={() => setAdd(true)}>添加服务器</Button></>} />
    <ErrorNotice message={error} retry={() => void refresh()} />
    {loading ? <Loading /> : data?.length ? <div className="node-grid">{data.map(s => <article className="node-card" key={s.id}><div className="node-card-heading"><span className="node-card-icon"><Server size={22} /></span><Badge tone={s.machineId ? "success" : "warning"}>{s.machineId ? "身份已验证" : "待验证"}</Badge></div><Link className="node-card-title" to={`/servers/${s.id}`}><h2>{s.name}</h2></Link><p className="mono">{s.sshUser}@{s.sshHost}:{s.sshPort}</p><p className="muted">{s.platform || "系统待检测"} · {s.architecture || "—"}</p><Link to={`/servers/${s.id}`}>管理 IP 与节点 →</Link></article>)}</div> : <Panel title="添加第一台服务器"><Empty icon={Server} title="还没有服务器" description="登记 SSH 地址并完成身份检查后，可以添加一个或多个公网 IP。" action={<Button onClick={() => setAdd(true)} icon={Plus}>添加服务器</Button>} /></Panel>}
    {add && <ServerForm onClose={() => setAdd(false)} onSaved={s => navigate(`/servers/${s.id}`)} />}
  </>;
}

export function ServerPage() {
  const { id } = useParams(); const navigate = useNavigate(); const toast = useToast();
  const { data, error, loading, refresh } = usePolling<{ server: ManagedServer; addresses: ServerAddress[]; nodes: Node[] }>(`/servers/${id}`);
  const [edit, setEdit] = useState(false), [add, setAdd] = useState(false), [ip, setIP] = useState(""), [busy, setBusy] = useState(false), [actionError, setActionError] = useState("");
  const [remove, setRemove] = useState<ServerAddress | "server" | null>(null);
  if (loading) return <Loading />;
  if (!data) return <ErrorNotice message={error} retry={() => void refresh()} />;
  const { server, addresses, nodes } = data;
  const hostLive = !!server.lastSeen && Date.now() - Date.parse(server.lastSeen) < 30_000;
  const metrics = server.metrics;
  async function addAddress(e: FormEvent) {
    e.preventDefault(); setBusy(true); setActionError("");
    try { await api(`/servers/${id}/addresses`, "POST", { publicIp: ip.trim() }); setAdd(false); setIP(""); await refresh(); } catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  }
  async function deleteEntry() {
    setBusy(true); setActionError("");
    try {
      if (remove === "server") { await api(`/servers/${id}`, "DELETE", { expectedRevision: server.revision }); navigate("/servers"); }
      else if (remove) { await api(`/servers/${id}/addresses/${remove.id}`, "DELETE"); setRemove(null); await refresh(); toast("IP 已从管理清单移除"); }
    } catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  }
  return <>
    <Breadcrumb to="/servers" label="服务器" current={server.name} />
    <PageHeader title={server.name} eyebrow="SERVER DETAILS" description={`${server.sshUser}@${server.sshHost}:${server.sshPort} · ${server.platform || "待验证"}`} actions={<><RefreshButton refresh={refresh} /><Button variant="secondary" icon={Settings2} onClick={() => setEdit(true)}>编辑连接</Button><Button variant="danger" icon={Trash2} onClick={() => { setActionError(""); setRemove("server"); }}>删除服务器</Button></>} />
    <ErrorNotice message={error} />
    <Panel title="服务器资源" subtitle={server.lastSeen ? (hostLive ? "同一 VPS 的最新主机指标，各 IP 节点共享" : "心跳已过期，以下为最近一次资源快照") : "节点 Agent 首次上报后显示主机资源"}>
      {server.lastSeen ? <>
        <div className="resources">
          <Resource label="CPU" value={metrics.cpuPercent} text={`${metrics.cpuPercent.toFixed(1)}%`} />
          <Resource label="内存" value={metrics.memoryTotal ? metrics.memoryUsed / metrics.memoryTotal * 100 : 0} text={metrics.memoryTotal ? `${bytes(metrics.memoryUsed)} / ${bytes(metrics.memoryTotal)}` : "未知"} color="green" />
          <Resource label="磁盘" value={metrics.diskTotal ? metrics.diskUsed / metrics.diskTotal * 100 : 0} text={metrics.diskTotal ? `${bytes(metrics.diskUsed)} / ${bytes(metrics.diskTotal)}` : "未知"} color="blue" />
        </div>
        <div className="resource-footer"><Clock3 size={14} /><span>最近主机上报<br /><strong>{date(server.lastSeen)}</strong></span><Badge tone={hostLive ? "success" : "warning"}>{hostLive ? "实时" : "已过期"}</Badge></div>
      </> : <Empty icon={Server} title="等待主机指标" description="部署节点并收到 Agent 心跳后显示 CPU、内存和磁盘；不会累计多个 IP 节点的重复指标。" />}
    </Panel>
    <Panel title="公网 IP" subtitle="一个 IP 对应一个独立节点；入口和出口使用同一地址" action={<Button icon={Plus} onClick={() => { setActionError(""); setAdd(true); }}>添加 IP</Button>}>
      {addresses.length ? <div className="table-scroll"><table><thead><tr><th>公网 IPv4</th><th>节点</th><th>操作</th></tr></thead><tbody>{addresses.map(a => { const n = nodes.find(n => n.addressId === a.id && n.lifecycle !== "deleted"); return <tr key={a.id}><td className="mono">{a.ip}</td><td>{n ? <Link to={`/nodes/${n.id}`}>{n.name} · {n.status}</Link> : <span className="muted">未部署</span>}</td><td><div className="row-actions">{n ? <Link className="button button-secondary" to={`/nodes/${n.id}`}>管理节点</Link> : <Link className="button button-primary" to={`/nodes?add=1&server=${server.id}&address=${a.id}`}>部署节点</Link>}<Button variant="ghost" icon={Trash2} disabled={!!n} onClick={() => { setActionError(""); setRemove(a); }}>删除 IP</Button></div></td></tr>; })}</tbody></table></div> : <Empty icon={Globe2} title="暂无公网 IP" description="添加已经配置到服务器网卡上的公网 IPv4，再选择域名部署节点。" />}
    </Panel>
    <Notice>删除节点只停止对应 IP 的实例。删除 IP 清单不会解绑网卡或释放厂商 IP；有节点或清理任务的 IP 必须先完成节点删除。</Notice>
    <Panel title="主机身份"><dl className="details-list"><div><dt>SSH 指纹</dt><dd className="mono break-all">{server.hostKey || "尚未固定"}</dd></div><div><dt>系统</dt><dd>{server.platform} · {server.architecture}</dd></div><div><dt>独立节点数</dt><dd>{nodes.filter(n => n.lifecycle !== "deleted").length}</dd></div></dl></Panel>
    {edit && <ServerForm server={server} onClose={() => setEdit(false)} onSaved={() => { setEdit(false); void refresh(); }} />}
    {add && <Modal title="添加公网 IP" onClose={() => { if (!busy) setAdd(false); }}><form className="form-stack" onSubmit={addAddress}><Field label="公网 IPv4"><input required value={ip} onChange={e => setIP(e.target.value)} placeholder="已绑定到此 VPS 的公网 IP" /></Field><Notice>这里只登记地址。部署前会检查它是否在本机网卡上，并验证实际出口；不会自动修改系统网络。</Notice><ErrorNotice message={actionError} /><div className="modal-actions"><Button type="button" variant="secondary" onClick={() => setAdd(false)} disabled={busy}>取消</Button><Button type="submit" busy={busy}>添加 IP</Button></div></form></Modal>}
    {remove && <Modal title={remove === "server" ? "删除服务器" : "删除 IP"} onClose={() => { if (!busy) setRemove(null); }}><div className="form-stack"><Notice tone="warning">{remove === "server" ? "只有全部节点已清理完成的服务器才能删除。历史流量与审计记录保留。" : `将 ${remove.ip} 从 HQIP 清单移除。系统网卡和厂商分配不变。`}</Notice><ErrorNotice message={actionError} /><div className="modal-actions"><Button variant="secondary" onClick={() => setRemove(null)} disabled={busy}>取消</Button><Button variant="danger" busy={busy} onClick={() => void deleteEntry()}>确认删除</Button></div></div></Modal>}
  </>;
}
