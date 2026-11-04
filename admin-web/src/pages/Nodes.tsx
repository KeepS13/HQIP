import { API_URL } from "../config";
import { useEffect, useState, type FormEvent } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  Activity,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  CheckCircle2,
  ChevronRight,
  Clock3,
  Globe2,
  LoaderCircle,
  Monitor,
  Plus,
  Search,
  Server,
  Settings2,
  ShieldCheck,
  Terminal,
  Trash2,
  Wifi,
} from "lucide-react";
import { api, usePolling } from "../api";
import QuotaFields from "../QuotaFields";
import {
  Badge,
  Breadcrumb,
  Button,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Meter,
  Modal,
  NodeBadge,
  Notice,
  PageHeader,
  Panel,
  Rate,
  RefreshButton,
  StatCard,
  TrafficChart,
  bytes,
  date,
  fresh,
  onlineKnown,
  useToast,
} from "../components";
import { DeployNode, DeleteNode, RetireNode, DeletedNodeNotice } from "./NodeActions";
import type { Node, NodeDetail, TaskEvent, ManagedServer, ServerAddress, CloudflareSettings, CloudflareZone } from "../types";

function AddNode({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate(); const [params] = useSearchParams();
  const servers = usePolling<ManagedServer[]>("/servers"), settings = usePolling<CloudflareSettings>("/settings/cloudflare");
  const [serverId, setServerId] = useState(params.get("server") || "");
  const [addresses, setAddresses] = useState<ServerAddress[]>([]), [zones, setZones] = useState<CloudflareZone[]>([]);
  const [form, setForm] = useState({ name: "", addressId: params.get("address") || "", domain: "", port: "443", password: "", zoneId: "", dnsMode: "" });
  const [bandwidth, setBandwidth] = useState(""), [quota, setQuota] = useState(""), [unit, setUnit] = useState("GB"), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const change = (key: keyof typeof form, value: string) => setForm(p => ({ ...p, [key]: value }));
  useEffect(() => { if (!serverId) { setAddresses([]); return; } let alive = true; setAddresses([]); api<ServerAddress[]>(`/servers/${serverId}/addresses`).then(a => { if (alive) setAddresses(a); }).catch(e => { if (alive) setError(e.message); }); return () => { alive = false; }; }, [serverId]);
  useEffect(() => { if (settings.data && !form.dnsMode) setForm(p => ({ ...p, dnsMode: settings.data!.configured ? "cloudflare" : "manual", zoneId: settings.data!.defaultZoneId })); }, [settings.data, form.dnsMode]);
  useEffect(() => { if (!settings.data?.configured) return; let alive = true; api<{zones: CloudflareZone[]}>("/settings/cloudflare/zones").then(r => { if (alive) setZones(r.zones); }).catch(e => { if (alive) setError(e.message); }); return () => { alive = false; }; }, [settings.data?.configured]);
  async function submit(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const node = await api<Node>("/nodes", "POST", { name: form.name.trim(), serverId, addressId: form.addressId, domain: form.dnsMode === "manual" ? form.domain.trim().toLowerCase() : "", dnsMode: form.dnsMode, zoneId: form.zoneId, port: Number(form.port), password: form.password, bandwidthMbps: bandwidth === "" ? null : Number(bandwidth), monthlyQuotaBytes: quota === "" ? null : Math.round(Number(quota) * (unit === "GB" ? 1e9 : 1e12)) });
      setForm(p => ({ ...p, password: "" })); onClose(); navigate(`/nodes/${node.id}`);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal title="添加 IP 节点" description="选择服务器和已绑定的 IP，为其部署独立 Hysteria 2 实例。" wide onClose={() => { if (!busy) onClose(); }}>
    <form onSubmit={submit} className="form-stack">
      <Field label="节点名称"><input required maxLength={80} value={form.name} onChange={e => change("name", e.target.value)} placeholder="例如：香港 · IP 1" /></Field>
      <ErrorNotice message={servers.error} />
      <div className="form-grid"><Field label="服务器"><select required value={serverId} onChange={e => { setServerId(e.target.value); change("addressId", ""); }}><option value="">选择服务器</option>{servers.data?.map(s => <option key={s.id} value={s.id}>{s.name} · {s.sshHost}</option>)}</select></Field><Field label="公网 IP" hint="入口和出口均使用此地址"><select required value={form.addressId} onChange={e => change("addressId", e.target.value)}><option value="">选择已登记的 IP</option>{addresses.map(a => <option key={a.id} value={a.id} disabled={!!a.nodeIds?.length}>{a.ip}{a.nodeIds?.length ? "（已部署）" : ""}</option>)}</select></Field></div>
      <p className="muted">没有可选项？<Link to="/servers">先添加服务器和公网 IP</Link>。</p>
      <Field label="域名方式"><select value={form.dnsMode || "manual"} onChange={e => change("dnsMode", e.target.value)}><option value="cloudflare" disabled={!settings.data?.configured}>Cloudflare 自动分配</option><option value="manual">使用已有域名</option></select></Field>
      {form.dnsMode === "cloudflare" ? <Field label="Cloudflare 域名" hint="自动创建独立子域名和仅 DNS 的 A 记录"><select required value={form.zoneId} onChange={e => change("zoneId", e.target.value)}><option value="">选择域名</option>{settings.data?.defaultZoneId && !zones.some(z => z.id === settings.data!.defaultZoneId) && <option value={settings.data.defaultZoneId}>{settings.data.defaultZoneName}</option>}{zones.map(z => <option value={z.id} key={z.id} disabled={z.status !== "active"}>{z.name}</option>)}</select></Field> : <Field label="已有节点域名" hint="A 记录需指向所选 IP；HTTP 证书验证需要此 IP 的 TCP 80 可用"><input required value={form.domain} pattern="[^/ :]+[.][^/ :]+" onChange={e => change("domain", e.target.value)} placeholder="node.example.com" autoCapitalize="none" /></Field>}
      {!settings.data?.configured && <p className="muted"><Link to="/domains">配置 Cloudflare Token</Link> 后可自动分配域名。</p>}
      <div className="form-grid"><Field label="本次部署的 SSH 密码"><input type="password" required autoComplete="new-password" value={form.password} onChange={e => change("password", e.target.value)} /></Field><Field label="Hysteria UDP 端口"><input type="number" required min={1} max={65535} value={form.port} onChange={e => change("port", e.target.value)} /></Field></div>
      <QuotaFields {...{ bandwidth, setBandwidth, quota, setQuota, unit, setUnit }} />
      <Notice>每个 IP 的节点、设备授权和流量独立管理。已有 HY2 会先进行归属检查；发现旧安装时，可在节点详情预检后接管。带宽和流量额度只作提醒。</Notice>
      <ErrorNotice message={error} /><div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" busy={busy} icon={Plus}>添加并部署</Button></div>
    </form>
  </Modal>;
}

export function Nodes() {
  const { data, error, loading, refresh } = usePolling<Node[]>("/nodes"),
    [search, setSearch] = useState(""),
    [params, setParams] = useSearchParams();
  const add = params.get("add") === "1",
    nodes = (data || []).filter((n) =>
      `${n.name} ${n.domain} ${n.publicIp}`
        .toLowerCase()
        .includes(search.toLowerCase()),
    );
  function close() {
    setParams((p) => {
      p.delete("add");
      return p;
    });
  }
  return (
    <>
      <PageHeader
        eyebrow="NODE MANAGEMENT"
        title="节点管理"
        description="按公网 IP 独立部署、分配和管理节点。"
        actions={
          <>
            <RefreshButton refresh={refresh} />
            <Button icon={Plus} onClick={() => setParams({ add: "1" })}>
              添加节点
            </Button>
          </>
        }
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <div className="list-toolbar">
        <div className="list-count">
          全部节点 <span>{data?.length || 0}</span>
        </div>
        <div className="search-input">
          <Search size={16} />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="搜索节点"
            placeholder="搜索名称、域名或 IP"
          />
        </div>
      </div>
      {loading ? (
        <Loading />
      ) : nodes.length ? (
        <div className="node-grid">
          {nodes.map((n) => (
            <article className="node-card" key={n.id}>
              <div className="node-card-heading">
                <span className="node-card-icon">
                  <Server size={22} strokeWidth={1.6} />
                </span>
                <NodeBadge node={n} />
              </div>
              <Link to={`/nodes/${n.id}`} className="node-card-title">
                <h2>{n.name}</h2>
                <ChevronRight size={17} />
              </Link>
              <div className="node-address">
                <Globe2 size={13} />
                {n.domain}
              </div>
              <div className="node-card-divider" />
              <div className="node-card-data">
                <div>
                  <span>公网 IP</span>
                  <strong className="mono">{n.publicIp}</strong>
                </div>
                <div>
                  <span>传输协议</span>
                  <strong>
                    Hysteria 2 <small>UDP :{n.port}</small>
                  </strong>
                </div>
              </div>
              <div className="node-quota-title">
                <span>本月代理流量</span>
                <span>
                  {n.monthlyQuotaBytes
                    ? `${Math.round(((n.monthDownload + n.monthUpload) / n.monthlyQuotaBytes) * 100)}%`
                    : "不限量"}
                </span>
              </div>
              <Meter
                value={n.monthDownload + n.monthUpload}
                max={n.monthlyQuotaBytes}
                label
              />
              <div className="node-card-bottom">
                {fresh(n) ? (
                  <Rate
                    upload={n.metrics.uploadRate}
                    download={n.metrics.downloadRate}
                  />
                ) : (
                  <span className="muted">
                    <Clock3 size={12} /> 等待节点心跳
                  </span>
                )}
                <Link to={`/nodes/${n.id}`}>
                  管理 <ArrowRight size={14} />
                </Link>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <Panel title={search ? "搜索结果" : "建立你的私人网络"}>
          <Empty
            icon={Server}
            title={search ? "没有匹配的节点" : "还没有添加节点"}
            description={
              search
                ? "试试其他名称、域名或 IP。"
                : "先登记服务器与公网 IP，再选择域名部署独立 Hysteria 2 节点。"
            }
            action={
              !search && (
                <Button icon={Plus} onClick={() => setParams({ add: "1" })}>
                  添加第一个节点
                </Button>
              )
            }
          />
        </Panel>
      )}
      <div className="footnote">
        <ShieldCheck size={14} />
        网站仅显示通过真实代理测试的可用节点；带宽与流量额度仅作提醒。
      </div>
      {add && <AddNode onClose={close} />}
    </>
  );
}

function EditNode({
  node,
  onClose,
  refresh,
}: {
  node: Node;
  onClose: () => void;
  refresh: () => Promise<void>;
}) {
  const [name, setName] = useState(node.name),
    [bandwidth, setBandwidth] = useState(
      node.bandwidthMbps === null ? "" : String(node.bandwidthMbps),
    ),
    [unit, setUnit] = useState(
      node.monthlyQuotaBytes && node.monthlyQuotaBytes >= 1e12 ? "TB" : "GB",
    ),
    [quota, setQuota] = useState(
      node.monthlyQuotaBytes === null
        ? ""
        : String(
            node.monthlyQuotaBytes /
              (node.monthlyQuotaBytes >= 1e12 ? 1e12 : 1e9),
          ),
    ),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title="编辑节点设置"
      description="调整名称和容量记录，变更不会中断现有连接。"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            await api(`/nodes/${node.id}`, "PATCH", {
              name: name.trim(),
              bandwidthMbps: bandwidth === "" ? null : Number(bandwidth),
              monthlyQuotaBytes:
                quota === ""
                  ? null
                  : Math.round(Number(quota) * (unit === "GB" ? 1e9 : 1e12)),
            });
            await refresh();
            onClose();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field label="节点名称">
          <input
            required
            maxLength={80}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <QuotaFields
          {...{ bandwidth, setBandwidth, quota, setQuota, unit, setUnit }}
        />
        <Notice>额度仅提醒，不限速、不断网；修改额度不会清除本月统计。</Notice>
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button variant="secondary" type="button" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy}>
            保存设置
          </Button>
        </div>
      </form>
    </Modal>
  );
}
function RetryNode(props: { node: Node; onClose: () => void; refresh: () => Promise<void> }) {
  return <DeployNode {...props} />;
}

const stageLabels: Record<string, string> = {
  preflight: "环境预检",
  connect: "SSH 连接",
  ssh: "SSH 连接",
  install: "安装服务",
  certificate: "申请证书",
  tls: "申请证书",
  start: "启动服务",
  verify: "连接验证",
  done: "部署完成",
  ready: "部署完成",
  error: "部署失败",
  queued: "等待执行",
  deploy: "部署服务",
  external: "外部验证",
  warning: "提醒",
};
function TaskLog({ node, events }: { node: Node; events: TaskEvent[] }) {
  const [liveEvents, setLiveEvents] = useState<TaskEvent[]>([]),
    [connected, setConnected] = useState(false);
  useEffect(() => {
    setLiveEvents([]);
    const stream = new EventSource(`${API_URL}/api/v1/nodes/${node.id}/events`, { withCredentials: true });
    stream.onopen = () => setConnected(true);
    stream.onerror = () => setConnected(false);
    stream.addEventListener("progress", (event) => {
      try {
        const value = JSON.parse((event as MessageEvent).data) as TaskEvent;
        setLiveEvents((previous) =>
          [...previous.filter((e) => e.id !== value.id), value].slice(-200),
        );
      } catch {
        /* periodic detail refresh supplies a fallback */
      }
    });
    return () => stream.close();
  }, [node.id]);
  const all = Array.from(
    new Map([...(events || []), ...liveEvents].map((e) => [e.id, e])).values(),
  )
    .sort((a, b) => a.id - b.id)
    .slice(-100);
  return (
    <Panel
      title="部署与验证日志"
      subtitle="任务进度实时更新，敏感信息由服务端脱敏"
      action={
        <span className="log-connection">
          <span className={`status-dot ${connected ? "green" : "amber"}`} />
          {connected ? "实时连接" : "轮询更新"}
        </span>
      }
    >
      <div className="task-log">
        {all.length ? (
          all.map((event, i) => (
            <div
              className={`task-event ${event.stage === "error" ? "task-error" : ""}`}
              key={event.id}
            >
              <span className="task-marker">
                {event.stage === "error" ? (
                  "!"
                ) : i === all.length - 1 &&
                  ["pending", "installing", "verifying"].includes(
                    node.status,
                  ) ? (
                  <LoaderCircle size={13} className="spin" />
                ) : (
                  <CheckCircle2 size={13} />
                )}
              </span>
              <span className="task-time">
                {new Date(event.createdAt).toLocaleTimeString("zh-CN", {
                  timeZone: "Asia/Shanghai",
                  hour12: false,
                })}
              </span>
              <div>
                <strong>{stageLabels[event.stage] || event.stage}</strong>
                <p>{event.message}</p>
              </div>
            </div>
          ))
        ) : (
          <div className="log-empty">
            <Terminal size={19} />
            部署启动后，进度日志将显示在这里。
          </div>
        )}
      </div>
    </Panel>
  );
}

export function NodePage() {
  const { id } = useParams(),
    { data, error, loading, refresh } = usePolling<NodeDetail>(`/nodes/${id}`),
    [edit, setEdit] = useState(false),
    [remove, setRemove] = useState(false),
    [retire, setRetire] = useState(false),
    [retry, setRetry] = useState(false),
    [verifying, setVerifying] = useState(false);
  const toast = useToast();
  if (loading) return <Loading />;
  if (!data)
    return <ErrorNotice message={error} retry={() => void refresh()} />;
  const { node, devices } = data,
    live = fresh(node),
    known = onlineKnown(node),
    metrics = node.metrics,
    month = node.monthUpload + node.monthDownload;
  async function verify() {
    setVerifying(true);
    try {
      await api(`/nodes/${id}/verify`, "POST");
      await refresh();
      toast("已开始代理连通性验证");
    } catch (e) {
      toast((e as Error).message);
    } finally {
      setVerifying(false);
    }
  }
  return (
    <>
      <Breadcrumb to="/nodes" label="节点管理" current={node.name} />
      <PageHeader
        eyebrow="NODE DETAILS"
        title={node.name}
        description={`${node.domain} · ${node.publicIp} · Hysteria 2 / UDP ${node.port}`}
        actions={
          <>
            <NodeBadge node={node} />
            {node.lifecycle === "active" && ["ready", "awaiting_external"].includes(node.status) && <Button variant="secondary" icon={Terminal} onClick={() => setRetry(true)}>重新部署 / 证书方式</Button>}
            {node.lifecycle === "active" && <Button variant="danger" icon={Trash2} onClick={() => setRemove(true)}>删除节点</Button>}
            <Button
              variant="secondary"
              icon={Settings2}
              disabled={node.lifecycle !== "active"}
              onClick={() => setEdit(true)}
            >
              编辑设置
            </Button>
            {node.lifecycle === "retiring" ? <Button variant="secondary" onClick={() => setRetire(true)}>SSH 清理</Button> : node.lifecycle === "deleted" ? null : node.status === "error" ? (
              <Button onClick={() => setRetry(true)} icon={Terminal}>
                重试部署
              </Button>
            ) : (
              <Button
                icon={Wifi}
                variant="secondary"
                busy={verifying || node.status === "verifying"}
                disabled={["pending", "installing"].includes(node.status)}
                onClick={() => void verify()}
              >
                验证连接
              </Button>
            )}
          </>
        }
      />
      <ErrorNotice message={error} />
      {node.lifecycle === "deleted" && <DeletedNodeNotice node={node} />}
      {node.lifecycle === "retiring" && <Notice tone="warning">正在撤销授权并等待本节点停止。Agent 失联或版本较旧时，请使用“SSH 清理”；完成前不会将其他节点或此 IP 释放给新部署。</Notice>}
      {node.status === "error" && (
        <ErrorNotice
          message={node.message || "节点部署失败，请查看下方部署日志。"}
        />
      )}{" "}
      {node.status === "awaiting_external" && (
        <Notice tone="warning">
          <strong>还需要从外部网络验证</strong>
          <p>
            此节点与控制台部署在同一台服务器。本机探测成功仍不足以确认公网 UDP
            可用；请使用交付的外部验证脚本，从电脑通过独立 Mihomo 实例检查真实
            HTTPS 连接和出口
            IP。验证通过后，节点才会加入设备订阅。点击「验证连接」只会重跑控制台探测。
          </p>
        </Notice>
      )}
      {node.lastSeen && !live && (
        <Notice tone="warning">
          超过 30
          秒未收到心跳，在线设备与实时速率标记为未知。历史流量仍保留，节点恢复后会自动补报。
        </Notice>
      )}
      <div className="stats-grid">
        <StatCard
          label="本月代理流量"
          value={bytes(month).split(" ")[0]}
          unit={bytes(month).split(" ")[1]}
          icon={Activity}
          hint={
            node.monthlyQuotaBytes
              ? `额度 ${bytes(node.monthlyQuotaBytes)} · 仅提醒`
              : "本月额度无限制"
          }
        />
        <StatCard
          label="实时下载"
          value={live ? bytes(metrics.downloadRate).split(" ")[0] : "—"}
          unit={live ? `${bytes(metrics.downloadRate).split(" ")[1]}/s` : ""}
          icon={ArrowDown}
          color="blue"
          hint={live ? "节点当前下载速率" : "等待有效心跳"}
        />
        <StatCard
          label="实时上传"
          value={live ? bytes(metrics.uploadRate).split(" ")[0] : "—"}
          unit={live ? `${bytes(metrics.uploadRate).split(" ")[1]}/s` : ""}
          icon={ArrowUp}
          color="green"
          hint={live ? "节点当前上传速率" : "等待有效心跳"}
        />
        <StatCard
          label="在线登记设备"
          value={known ? node.onlineDevices : "—"}
          icon={Monitor}
          color="amber"
          hint={
            known
              ? `${node.instances} 个客户端连接实例`
              : "心跳或核心异常，在线状态未知"
          }
        />
      </div>
      <div className="overview-middle">
        <TrafficChart points={data.traffic} />
        <Panel
          title="服务器资源"
          subtitle="同一 VPS 的 CPU、内存和磁盘由各 IP 节点共享"
        >
          <Link className="button button-secondary" to={`/servers/${node.serverId}`}><Server size={16} />查看服务器资源与 IP 清单<ArrowRight size={16} /></Link>
          <div className="resource-footer">
            <Clock3 size={14} />
            <span>
              最近心跳
              <br />
              <strong>{date(node.lastSeen)}</strong>
            </span>
          </div>
        </Panel>
      </div>
      <div className="two-column">
        <Panel title="节点信息" subtitle="连接信息与配置同步状态">
          <dl className="details-list">
            <div><dt>所属服务器</dt><dd><Link to={`/servers/${node.serverId}`}>查看服务器与 IP 清单</Link></dd></div>
            <div><dt>证书有效期</dt><dd>{date(node.certificate?.notAfter)}{node.certificate?.error && <span className="muted"> · {node.certificate.error}</span>}</dd></div>
            <div>
              <dt>公网地址</dt>
              <dd className="mono">{node.publicIp}</dd>
            </div>
            <div>
              <dt>节点域名</dt>
              <dd>{node.domain}</dd>
            </div>
            <div>
              <dt>SSH 连接</dt>
              <dd className="mono">
                {node.sshUser}@{node.sshHost}:{node.sshPort}
              </dd>
            </div>
            <div>
              <dt>带宽容量</dt>
              <dd>
                {node.bandwidthMbps === null
                  ? "无限制"
                  : `${node.bandwidthMbps} Mbps`}{" "}
                <span className="muted">· 参考值</span>
              </dd>
            </div>
            <div>
              <dt>配置同步</dt>
              <dd>
                <Badge
                  tone={
                    node.appliedRevision >= node.desiredRevision
                      ? "success"
                      : "warning"
                  }
                >
                  {node.appliedRevision >= node.desiredRevision
                    ? "已同步"
                    : "待同步"}
                </Badge>
              </dd>
            </div>
            <div>
              <dt>累计流量</dt>
              <dd>{bytes(node.totalUpload + node.totalDownload)}</dd>
            </div>
            <div>
              <dt>创建时间</dt>
              <dd>{date(node.createdAt)}</dd>
            </div>
          </dl>
          {node.hostKey && (
            <div className="fingerprint">
              <span>已固定 SSH 主机指纹</span>
              <code>{node.hostKey}</code>
            </div>
          )}
        </Panel>
        <Panel title="本月用量" subtitle="北京时间自然月 · 上传 + 下载">
          <div className="usage-hero">
            <strong>{bytes(month)}</strong>
            <span>
              {node.monthlyQuotaBytes
                ? `/ ${bytes(node.monthlyQuotaBytes)}`
                : "无限额度"}
            </span>
          </div>
          <div className="usage-meter">
            <Meter value={month} max={node.monthlyQuotaBytes} />
          </div>
          <div className="usage-breakdown">
            <div>
              <span>
                <ArrowUp size={15} />
                上传
              </span>
              <strong>{bytes(node.monthUpload)}</strong>
            </div>
            <div>
              <span>
                <ArrowDown size={15} />
                下载
              </span>
              <strong>{bytes(node.monthDownload)}</strong>
            </div>
          </div>
          <div className="usage-note">
            代理流量不等同服务商账单。额度到达 80% / 100%
            时提醒，连接继续正常工作。
          </div>
        </Panel>
      </div>
      <TaskLog node={node} events={data.events} />
      <Panel
        title="凭据用量记录"
        subtitle="保留此节点的历史流量归属；用户权限在套餐中管理"
        action={
          <Link className="text-link" to="/users">
            管理用户 <ArrowRight size={15} />
          </Link>
        }
      >
        {devices?.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>记录名称</th>
                  <th>状态</th>
                  <th>本月上传</th>
                  <th>本月下载</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {devices.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <span className="table-name">
                        {d.name}
                      </span>
                    </td>
                    <td>
                      <Badge
                        tone={
                          d.pendingSync
                            ? "warning"
                            : !d.enabled
                              ? "muted"
                              : !known
                                ? "warning"
                                : d.online
                                  ? "success"
                                  : "muted"
                        }
                      >
                        {d.pendingSync
                          ? "待同步"
                          : !d.enabled
                            ? "已停用"
                            : !known
                              ? "在线未知"
                              : d.online
                                ? "在线"
                                : "离线"}
                      </Badge>
                    </td>
                    <td>{bytes(d.monthUpload)}</td>
                    <td>{bytes(d.monthDownload)}</td>
                    <td>
                      <Link
                        to="/users"
                        className="icon-button"
                        aria-label="前往用户管理"
                      >
                        <ChevronRight size={16} />
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty
            title="暂无凭据用量记录"
            description="将此节点加入套餐后，用户可建立连接。"
            action={
              <Link to="/plans" className="button button-secondary">
                <Plus size={15} />
                管理套餐
              </Link>
            }
          />
        )}
      </Panel>
      {remove && <DeleteNode node={node} onClose={() => setRemove(false)} refresh={refresh} />}
      {retire && <RetireNode node={node} onClose={() => setRetire(false)} refresh={refresh} />}
      {edit && (
        <EditNode
          node={node}
          onClose={() => setEdit(false)}
          refresh={refresh}
        />
      )}{" "}
      {retry && (
        <RetryNode
          node={node}
          onClose={() => setRetry(false)}
          refresh={refresh}
        />
      )}
    </>
  );
}
