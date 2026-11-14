import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowDown, ArrowRight, ArrowUp, CalendarClock, Clock3, Globe2, Plus, Search, Server, Settings2, ShieldCheck, Trash2 } from "lucide-react";
import { api, usePolling } from "../api";
import { Badge, Breadcrumb, Button, Empty, ErrorNotice, Field, Loading, Meter, Modal, NodeBadge, Notice, PageHeader, Panel, RefreshButton, Resource, TrafficChart, bytes, date, useToast } from "../components";
import QuotaFields from "../QuotaFields";
import { hasNodeReport } from "../monitoring";
import { compareResourceNames } from "../resource-order";
import { billingExpiry, billingFor, billingTotal, billingUsageLabel, defaultServerExpiry, parseBeijingExpiry } from "../billing";
import { BillingSummary, ExpiryFields, ServerBillingSettings } from "./ServerBilling";
import type { ManagedServer, ServerAddress, Node, TrafficPoint } from "../types";
import { AgentUpgradeDialog, OperationHistory, ServerCredentialPanel } from "./ServerOperations";

function useQuotaValues(server?: ManagedServer) {
  const [bandwidth, setBandwidth] = useState(server?.bandwidthMbps == null ? "" : String(server.bandwidthMbps));
  const [unit, setUnit] = useState(server?.monthlyQuotaBytes && server.monthlyQuotaBytes >= 1e12 ? "TB" : "GB");
  const [quota, setQuota] = useState(server?.monthlyQuotaBytes == null ? "" : String(server.monthlyQuotaBytes / (server.monthlyQuotaBytes >= 1e12 ? 1e12 : 1e9)));
  return { bandwidth, setBandwidth, quota, setQuota, unit, setUnit };
}

function ServerForm({ server, onClose, onSaved }: { server?: ManagedServer; onClose: () => void; onSaved: (server: ManagedServer) => void }) {
	const [saveCredential,setSaveCredential]=useState(true);
  const [form, setForm] = useState({ name: server?.name || "", sshHost: server?.sshHost || "", sshPort: String(server?.sshPort || 22), sshUser: server?.sshUser || "root", password: "" });
  const quota = useQuotaValues(server);
  const [expiry, setExpiry] = useState(defaultServerExpiry);
  const [expectedRevision] = useState(server?.revision || 0);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const change = (key: keyof typeof form, value: string) => setForm(p => ({ ...p, [key]: value }));
  async function submit(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const result = await api<ManagedServer>(server ? `/servers/${server.id}` : "/servers", server ? "PATCH" : "POST", {
        ...form, sshPort: Number(form.sshPort), expectedRevision, saveCredential,
        bandwidthMbps: server ? server.bandwidthMbps : quota.bandwidth === "" ? null : Number(quota.bandwidth),
        monthlyQuotaBytes: server ? server.monthlyQuotaBytes : quota.quota === "" ? null : Math.round(Number(quota.quota) * (quota.unit === "GB" ? 1e9 : 1e12)),
        ...(!server ? { expiresAt: parseBeijingExpiry(expiry.day, expiry.hour) } : {}),
      });
      setForm(p => ({ ...p, password: "" })); onSaved(result);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal wide={!server} title={server ? "编辑服务器连接" : "添加服务器"} description="检查 SSH 连接与主机身份，节点将在下一步单独部署。" onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack" onSubmit={submit}>
      <div className={server ? "form-stack" : "server-form-sections"}>
      <section className={server ? "form-stack" : "server-form-section"} aria-label="服务器连接信息">
      {!server && <h3><Server size={17} />连接与身份</h3>}
      <Field label="服务器名称"><input required maxLength={80} value={form.name} onChange={e => change("name", e.target.value)} placeholder="例如：香港 VPS" /></Field>
      <Field label="SSH 管理地址"><input required value={form.sshHost} onChange={e => change("sshHost", e.target.value)} placeholder="IP 或完整域名" autoCapitalize="none" /></Field>
      <div className="form-grid"><Field label="SSH 用户"><input required value={form.sshUser} onChange={e => change("sshUser", e.target.value)} /></Field><Field label="SSH 端口"><input type="number" min={1} max={65535} required value={form.sshPort} onChange={e => change("sshPort", e.target.value)} /></Field></div>
      <Field label="SSH 密码" hint="验证此服务器的登录凭据。"><input type="password" autoComplete="new-password" required value={form.password} onChange={e => change("password", e.target.value)} /></Field>
      <label className="assignment-option"><input type="checkbox" checked={saveCredential} onChange={e=>setSaveCredential(e.target.checked)} /><span>加密保存凭据，用于后续运维及每小时自动巡检</span></label>
      </section>
      {!server && <section className="server-form-section" aria-label="服务器额度与有效期"><h3><CalendarClock size={17} />额度与有效期</h3><QuotaFields {...quota} quotaLabel="每周期流量额度" /><ExpiryFields {...expiry} onChange={setExpiry} /><p className="billing-help">默认到期时间为当前时间向上取整小时后加 30 天，请按厂商实际有效期核对。</p><Notice>服务器容量仅作提醒，所属节点分别统计实际用量。到期不停止代理；未经续期确认，不会自动清零用量。</Notice></section>}
      </div>
      <p className="billing-help">HQIP 会固定 SSH 主机指纹。新增公网 IP 请先在 VPS 厂商和系统中配置，再加入此服务器的 IP 清单。</p>
      <ErrorNotice message={error} /><div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" busy={busy}>检查并保存</Button></div>
    </form>
  </Modal>;
}

function ServerSettings({ server, onClose, onSaved }: { server: ManagedServer; onClose: () => void; onSaved: (message: string) => void }) {
  return <ServerBillingSettings server={server} onClose={onClose} onSaved={onSaved} />;
}

function serverHealth(server: ManagedServer, stale = false): { label: string; tone: "success" | "warning" | "muted" } {
  if (stale) return { label: "数据已过期", tone: "warning" };
  if (server.ratesStatus === "none") return { label: server.nodeCount ? "有节点待处理" : "未部署", tone: server.nodeCount ? "warning" : "muted" };
  if (server.ratesStatus === "unknown") return { label: "节点心跳未知", tone: "warning" };
  if (server.ratesStatus === "partial") return { label: "部分节点上报", tone: "warning" };
  if (server.nodeCount > server.expectedNodes) return { label: "有节点待处理", tone: "warning" };
  if (!server.metricsFresh) return { label: "主机指标过期", tone: "warning" };
  return { label: "上报正常", tone: "success" };
}

function ServerResources({ server, stale = false }: { server: ManagedServer; stale?: boolean }) {
  const { metrics } = server;
  const hasSample = server.metricsFresh;
  return <div className={`server-resources ${!server.metricsFresh || stale ? "snapshot" : ""}`}>
    {hasSample ? <>
      <Resource label="CPU" value={metrics.cpuPercent} text={`${metrics.cpuPercent.toFixed(1)}%`} />
      {metrics.memoryTotal > 0 ? <Resource label="内存" value={metrics.memoryUsed / metrics.memoryTotal * 100} text={`${bytes(metrics.memoryUsed)} / ${bytes(metrics.memoryTotal)}`} color="green" /> : <div className="server-unknown-resource"><span>内存</span><strong>未知</strong></div>}
      {metrics.diskTotal > 0 ? <Resource label="磁盘（Agent 数据目录）" value={metrics.diskUsed / metrics.diskTotal * 100} text={`${bytes(metrics.diskUsed)} / ${bytes(metrics.diskTotal)}`} color="blue" /> : <div className="server-unknown-resource"><span>磁盘（Agent 数据目录）</span><strong>未知</strong></div>}
    </> : <>{["CPU", "内存", "磁盘（Agent 数据目录）"].map(label => <div className="server-unknown-resource" key={label}><span>{label}</span><strong>未知</strong></div>)}<span className="server-snapshot-label">尚无有效主机资源上报</span></>}
    {hasSample && stale && <span className="server-snapshot-label">主机资源为最近快照，当前状态未知</span>}
  </div>;
}

function ServerRates({ server, stale = false }: { server: ManagedServer; stale?: boolean }) {
  const known = server.ratesStatus === "complete" || server.ratesStatus === "partial";
  const partial = server.ratesStatus === "partial";
  return <div className="server-rates">
    {(["upload", "download"] as const).map(direction => {
      const value = server.metrics[direction === "upload" ? "uploadRate" : "downloadRate"] * 8 / 1e6;
      const occupancy = server.bandwidthMbps ? value / server.bandwidthMbps * 100 : null;
      const Icon = direction === "upload" ? ArrowUp : ArrowDown;
      return <div className={`server-rate ${direction}`} key={direction}>
        <span><Icon size={14} />{direction === "upload" ? "上传" : "下载"}</span>
        <strong>{known ? `${partial ? "≥ " : ""}${value.toFixed(2)}` : "—"}<small> Mbps</small></strong>
        <span className="server-rate-capacity">{partial ? "数据不完整 · 占用未知" : !known ? "占用未知" : occupancy === null ? "带宽不限量" : `${occupancy.toFixed(1)}% 占用`}{stale && known ? " · 快照" : ""}</span>
        {known && !partial && occupancy !== null && <div className="resource-bar bar-purple"><i style={{ width: `${Math.min(100, Math.max(0, occupancy))}%` }} /></div>}
      </div>;
    })}
    <p className="server-rate-note">{stale ? "刷新失败 · 最近速率快照已过期" : server.ratesStatus === "none" ? "暂无运行节点，速率未知" : server.ratesStatus === "unknown" ? "无有效节点心跳，速率未知" : `${server.reportingNodes} / ${server.expectedNodes} 个节点上报${partial ? " · 合计为已知下限" : " · 实时速率含探测"}`}</p>
  </div>;
}

function ServerQuota({ server }: { server: ManagedServer }) {
  const billing = billingFor(server), total = billingTotal(server);
  const percent = server.monthlyQuotaBytes ? total / server.monthlyQuotaBytes * 100 : null;
  return <div className="server-quota"><div className="server-section-label"><span>{billingUsageLabel(server)}</span><strong>{percent === null ? "不限量" : `${percent.toFixed(1)}%`}</strong></div><Meter value={total} max={server.monthlyQuotaBytes} label /><small>上传 {bytes(billing.cycleUpload)} · 下载 {bytes(billing.cycleDownload)} · 不含探测{!billing.cycleComplete ? " · 数据不完整，按已知用量计算" : ""}</small></div>;
}

export function Servers() {
	const [upgrade,setUpgrade]=useState(false);
  const { data, error, loading, refresh } = usePolling<ManagedServer[]>("/servers");
  const [add, setAdd] = useState(false), [search, setSearch] = useState(""), [abnormal, setAbnormal] = useState(false);
  const navigate = useNavigate();
  const query = search.trim().toLowerCase();
  const visible = (data || []).filter(server => (!query || [server.name, server.sshHost, ...(server.addresses || []).map(address => address.ip)].some(value => value.toLowerCase().includes(query))) && (!abnormal || serverHealth(server, !!error).tone === "warning" || billingExpiry(billingFor(server)).abnormal)).sort(compareResourceNames);
  return <>
    <PageHeader title="服务器" eyebrow="SERVERS" description="按 VPS 汇总资源、节点速率与代理用量，独立管理每个公网 IP。" actions={<><RefreshButton refresh={refresh} /><Button variant="secondary" onClick={()=>setUpgrade(true)}>批量升级 Agent</Button><Button icon={Plus} onClick={() => setAdd(true)}>添加服务器</Button></>} />
    <ErrorNotice message={error ? `${error}${data ? "；以下保留上次成功数据，已标记过期。" : ""}` : ""} retry={() => void refresh()} />
    <div className="list-toolbar server-toolbar"><span className="list-count">服务器 <span>{data?.length || 0}</span></span><div className="server-filters"><label className="search-input"><Search size={15} /><input aria-label="搜索服务器名称或 IP" placeholder="搜索服务器名称 / IP" value={search} onChange={e => setSearch(e.target.value)} /></label><button type="button" className={`server-filter ${abnormal ? "active" : ""}`} aria-pressed={abnormal} onClick={() => setAbnormal(value => !value)}>仅看异常</button></div></div>
    {loading ? <Loading /> : !data ? null : !data.length ? <Panel title="添加第一台服务器"><Empty icon={Server} title="还没有服务器" description="登记 SSH 地址并完成身份检查后，可以添加一个或多个公网 IP。" action={<Button onClick={() => setAdd(true)} icon={Plus}>添加服务器</Button>} /></Panel> : !visible.length ? <Panel title="筛选结果"><Empty icon={Search} title="没有匹配的服务器" description="尝试更换名称、IP，或关闭异常筛选。" /></Panel> : <div className="server-grid">{visible.map(server => {
      const health = serverHealth(server, !!error);
      return <Link className="server-card" to={`/servers/${server.id}`} key={server.id} aria-label={`管理服务器 ${server.name}`}>
        <div className="server-card-body">
          <div className="node-card-heading"><span className="node-card-icon"><Server size={22} /></span><Badge tone={health.tone}>{health.label}</Badge></div>
          <h2>{server.name}</h2><p className="server-address mono">{server.sshHost}:{server.sshPort}</p>
          <div className="server-identity"><ShieldCheck size={13} /><span>{server.machineId ? "身份已验证" : "身份待验证"}</span><span>{server.platform || "系统待检测"}</span></div>
          <BillingSummary resource={server} compact />
          <ServerResources server={server} stale={!!error} /><ServerRates server={server} stale={!!error} /><ServerQuota server={server} />
        </div>
        <div className="server-card-footer"><span>{server.nodeCount} 个节点 <i /> {server.ipCount} 个 IP</span><strong>管理 IP 与节点 <ArrowRight size={15} /></strong></div>
      </Link>;
    })}</div>}
    <p className="footnote">异常筛选包含已到期及 48 小时内到期。速率含探测，周期用量不含探测；CPU、内存、磁盘仅取一个最新主机样本。</p>
    {add && <ServerForm onClose={() => setAdd(false)} onSaved={server => navigate(`/servers/${server.id}`)} />}
    <OperationHistory />
    {upgrade && <AgentUpgradeDialog onClose={()=>setUpgrade(false)} />}
  </>;
}

export function ServerPage() {
	const [upgrade,setUpgrade]=useState(false);
  const { id } = useParams(); const navigate = useNavigate(); const toast = useToast();
  const { data, error, loading, refresh } = usePolling<{ server: ManagedServer; addresses: ServerAddress[]; nodes: Node[]; nodeUsage: Node[] }>(`/servers/${id}`);
  const [period, setPeriod] = useState<"24h" | "7d">("24h");
  const traffic = usePolling<TrafficPoint[]>(`/servers/${id}/traffic?period=${period}`);
  const [edit, setEdit] = useState(false), [settings, setSettings] = useState(false), [add, setAdd] = useState(false), [ip, setIP] = useState(""), [busy, setBusy] = useState(false), [actionError, setActionError] = useState("");
  const [remove, setRemove] = useState<ServerAddress | "server" | null>(null);
  if (loading) return <Loading />;
  if (!data) return <ErrorNotice message={error} retry={() => void refresh()} />;
  const { server, addresses, nodes } = data;
  const health = serverHealth(server, !!error);
  const nodeUsage = data.nodeUsage || nodes;
  const activeUsage = nodeUsage.filter(node => node.lifecycle !== "deleted");
  const deletedUsage = nodeUsage.filter(node => node.lifecycle === "deleted");
  const sourceNode = nodeUsage.find(node => node.id === server.metricsSourceNodeId);
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
    <PageHeader title={server.name} eyebrow="SERVER DETAILS" description={`${server.sshUser}@${server.sshHost}:${server.sshPort} · ${server.platform || "待验证"}`} actions={<><RefreshButton refresh={async () => { await Promise.all([refresh(), traffic.refresh()]); }} /><Button variant="secondary" icon={Settings2} onClick={() => setSettings(true)}>额度与续期</Button><Button variant="secondary" onClick={() => setEdit(true)}>编辑连接</Button><Button variant="danger" icon={Trash2} onClick={() => { setActionError(""); setRemove("server"); }}>删除服务器</Button></>} />
    <ErrorNotice message={error ? `${error}；以下为上次成功数据，当前状态未知。` : ""} retry={() => void refresh()} />
    <div className="server-detail-summary"><Badge tone={health.tone}>{health.label}</Badge><span><ShieldCheck size={14} />{server.machineId ? "主机身份已验证" : "主机身份待验证"}</span><span>{server.nodeCount} 个节点 / {server.ipCount} 个 IP</span><span>带宽容量：{server.bandwidthMbps == null ? "不限量" : `${server.bandwidthMbps} Mbps`}</span></div>
    <BillingSummary resource={server} />
    <ServerCredentialPanel server={server} />
    <div className="row-actions"><Button variant="secondary" onClick={()=>setUpgrade(true)}>升级本机 Agent</Button></div>
    <div className="two-column server-detail-metrics">
      <Panel title="服务器资源" subtitle="采用一个最新主机样本；磁盘为 Agent 数据目录所在文件系统">
        <ServerResources server={server} stale={!!error} />
        <div className="server-sample-source"><Clock3 size={14} /><span>最近上报：{server.metricsReportedAt || server.lastSeen ? date(server.metricsReportedAt || server.lastSeen) : "尚未收到"}<br />样本来源：{sourceNode?.name || (server.metricsSourceNodeId ? server.metricsSourceNodeId : "未知")}</span></div>
      </Panel>
      <Panel title="服务器带宽" subtitle="上传、下载独立计算占用，节点速率合计含探测">
        <ServerRates server={server} stale={!!error} /><div className="server-detail-quota"><ServerQuota server={server} /><p>自然月流量 {bytes(server.monthUpload + server.monthDownload)} · 累计代理流量 {bytes(server.totalUpload + server.totalDownload)}<br />累计上传 {bytes(server.totalUpload)} / 下载 {bytes(server.totalDownload)}</p></div>
      </Panel>
    </div>
    <TrafficChart points={traffic.data || []} title="服务器代理流量" subtitle="所有节点合计 · 包含已删除节点历史 · 不含探测 · 北京时间" period={period} onPeriodChange={setPeriod} loading={traffic.loading} error={traffic.error} />
    <Panel title="节点流量贡献" subtitle="各节点实际上传与下载；实时速率含探测，代理用量不含探测">
      {activeUsage.length ? <div className="table-scroll"><table><thead><tr><th>节点 / 公网 IP / 域名</th><th>状态</th><th>实时速率</th><th>代理用量</th><th>本月上传 / 下载</th><th>累计代理流量</th></tr></thead><tbody>{activeUsage.map(node => <tr key={node.id}>
        <td><Link to={`/nodes/${node.id}`}>{node.name}</Link><div className="table-small mono">{node.publicIp}</div><div className="table-small mono">{node.domain}</div></td>
        <td><NodeBadge node={node} /></td>
        <td>{!error && hasNodeReport(node) ? <div className="server-node-rates"><span>↑ {(node.metrics.uploadRate * 8 / 1e6).toFixed(2)} Mbps</span><span>↓ {(node.metrics.downloadRate * 8 / 1e6).toFixed(2)} Mbps</span></div> : <span className="muted">未知</span>}</td>
        <td className="quota-cell"><div className="table-small">{billingFor(node).status === "unconfigured" ? "自然月" : "本周期"}</div><strong>{bytes(billingTotal(node))}</strong>{!billingFor(node).cycleComplete && <div className="table-small">数据不完整，按已知用量计算</div>}</td>
        <td>{bytes(node.monthUpload)} / {bytes(node.monthDownload)}</td><td>{bytes(node.totalUpload + node.totalDownload)}</td>
      </tr>)}</tbody></table></div> : <Empty icon={Server} title="暂无活动节点" description="部署节点后，这里会展示每个 IP 节点对服务器用量的贡献。" />}
    </Panel>
    {deletedUsage.length > 0 && <details className="server-history"><summary>已删除节点的历史贡献 <span>{deletedUsage.length} 个节点</span></summary><p>历史用量继续计入服务器汇总，不再展示实时速率。</p><div className="table-scroll"><table><thead><tr><th>节点 / 公网 IP / 域名</th><th>本月上传</th><th>本月下载</th><th>本月合计</th><th>累计代理流量</th></tr></thead><tbody>{deletedUsage.map(node => <tr key={node.id}><td><Link to={`/nodes/${node.id}`}>{node.name}</Link><div className="table-small mono">{node.publicIp}</div><div className="table-small mono">{node.domain}</div></td><td>{bytes(node.monthUpload)}</td><td>{bytes(node.monthDownload)}</td><td>{bytes(node.monthUpload + node.monthDownload)}</td><td>{bytes(node.totalUpload + node.totalDownload)}</td></tr>)}</tbody></table></div></details>}
    <Panel title="公网 IP" subtitle="一个 IP 对应一个独立节点；入口和出口使用同一地址" action={<Button icon={Plus} onClick={() => { setActionError(""); setAdd(true); }}>添加 IP</Button>}>
      {addresses.length ? <div className="table-scroll"><table><thead><tr><th>公网 IPv4</th><th>节点</th><th>操作</th></tr></thead><tbody>{addresses.map(address => { const node = nodes.find(node => node.addressId === address.id && node.lifecycle !== "deleted"); return <tr key={address.id}><td className="mono">{address.ip}</td><td>{node ? <div><Link to={`/nodes/${node.id}`}>{node.name}</Link><div className="table-small"><NodeBadge node={node} /></div></div> : <span className="muted">未部署</span>}</td><td><div className="row-actions">{node ? <Link className="button button-secondary" to={`/nodes/${node.id}`}>管理节点</Link> : <Link className="button button-primary" to={`/nodes?add=1&server=${server.id}&address=${address.id}`}>部署节点</Link>}<Button variant="ghost" icon={Trash2} disabled={!!node} onClick={() => { setActionError(""); setRemove(address); }}>删除 IP</Button></div></td></tr>; })}</tbody></table></div> : <Empty icon={Globe2} title="暂无公网 IP" description="添加已经配置到服务器网卡上的公网 IPv4，再选择域名部署节点。" />}
    </Panel>
    <Notice>删除节点只停止对应 IP 的实例。删除 IP 清单不会解绑网卡或释放厂商 IP；有节点或清理任务的 IP 必须先完成节点删除。</Notice>
    <Panel title="主机身份"><dl className="details-list"><div><dt>SSH 指纹</dt><dd className="mono break-all">{server.hostKey || "尚未固定"}</dd></div><div><dt>系统</dt><dd>{server.platform} · {server.architecture}</dd></div><div><dt>登记时间</dt><dd>{date(server.createdAt)}</dd></div></dl></Panel>
    {settings && <ServerSettings server={server} onClose={() => setSettings(false)} onSaved={message => { setSettings(false); void refresh(); toast(message); }} />}
    {upgrade && <AgentUpgradeDialog nodes={nodes} onClose={()=>setUpgrade(false)} />}
    {edit && <ServerForm server={server} onClose={() => setEdit(false)} onSaved={() => { setEdit(false); void refresh(); }} />}
    {add && <Modal title="添加公网 IP" onClose={() => { if (!busy) setAdd(false); }}><form className="form-stack" onSubmit={addAddress}><Field label="公网 IPv4"><input required value={ip} onChange={e => setIP(e.target.value)} placeholder="已绑定到此 VPS 的公网 IP" /></Field><Notice>这里只登记地址。部署前会检查它是否在本机网卡上，并验证实际出口；不会自动修改系统网络。</Notice><ErrorNotice message={actionError} /><div className="modal-actions"><Button type="button" variant="secondary" onClick={() => setAdd(false)} disabled={busy}>取消</Button><Button type="submit" busy={busy}>添加 IP</Button></div></form></Modal>}
    {remove && <Modal title={remove === "server" ? "删除服务器" : "删除 IP"} onClose={() => { if (!busy) setRemove(null); }}><div className="form-stack"><Notice tone="warning">{remove === "server" ? "只有全部节点已清理完成的服务器才能删除。历史流量与审计记录保留。" : `将 ${remove.ip} 从 HQIP 清单移除。系统网卡和厂商分配不变。`}</Notice><ErrorNotice message={actionError} /><div className="modal-actions"><Button variant="secondary" onClick={() => setRemove(null)} disabled={busy}>取消</Button><Button variant="danger" busy={busy} onClick={() => void deleteEntry()}>确认删除</Button></div></div></Modal>}
  </>;
}
