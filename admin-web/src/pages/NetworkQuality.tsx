import { useEffect, useRef, useState, type FormEvent } from "react";
import { Clock3, Download, ExternalLink, Globe2, Image, Network, RefreshCw, ShieldCheck, ZoomIn, ZoomOut } from "lucide-react";
import { api, usePolling } from "../api";
import { API_URL } from "../config";
import { Badge, Button, Empty, ErrorNotice, Field, Loading, Modal, Notice, Panel, date, useToast } from "../components";
import type { Node } from "../types";
import "./network-quality.css";

type Report = Record<string, unknown>;
type Quality = {
  nodeId: string;
  status: "not-run" | "queued" | "running" | "complete" | "failed" | "cancelled";
  error: string;
  sourceIp: string;
  requestedAt: string | null;
  reportSourceIp: string;
  startedAt: string | null;
  finishedAt: string | null;
  reportAt: string | null;
  report: Report | null;
  reportImageAvailable: boolean;
  reportImageVersion: string;
};
const statusNames: Record<Quality["status"], string> = {
  "not-run": "尚未检测", queued: "等待检测", running: "检测中", complete: "报告已生成", failed: "本次检测失败", cancelled: "检测已取消",
};
const mediaNames: Record<string, string> = { DisneyPlus: "Disney+", Youtube: "YouTube", AmazonPrimeVideo: "Prime Video" };
const factorNames: Record<string, string> = { CountryCode: "地区", Proxy: "代理", Tor: "Tor", VPN: "VPN", Server: "数据中心", Abuser: "滥用", Robot: "机器人" };
function object(value: unknown): Report {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Report : {};
}
function scalar(value: unknown, fallback = "未知"): string {
  if (typeof value === "boolean") return value ? "是" : "否";
  if (typeof value === "number") return Number.isFinite(value) ? String(value) : fallback;
  if (typeof value !== "string") return fallback;
  const text = value.replace(/[\u0000-\u001f\u007f-\u009f]/g, "").trim();
  return !text || /^(null|undefined|n\/a|unknown)$/i.test(text) ? fallback : text;
}
function statusTone(value: unknown) {
  const text = scalar(value).toLowerCase();
  if (["yes", "解锁"].includes(text)) return "success";
  if (["block", "屏蔽"].includes(text)) return "danger";
  if (["failed", "失败", "pending", "待定"].includes(text)) return "warning";
  return "muted";
}
function mediaStatus(value: unknown) {
  const text = scalar(value);
  return ({ Yes: "可访问", Block: "受限", Failed: "检测失败", Pending: "待确认", "解锁": "可访问", "屏蔽": "受限", "失败": "检测失败" } as Record<string, string>)[text] || text;
}

function ReportBody({ report, reportAt, sourceIp, imageAvailable, onViewImage }: { report: Report; reportAt: string | null; sourceIp: string; imageAvailable: boolean; onViewImage: () => void }) {
  const head = object(report.Head), info = object(report.Info), region = object(info.Region), city = object(info.City);
  const media = object(report.Media), scores = object(report.Score), usage = object(object(report.Type).Usage);
  const factors = object(report.Factor), mail = object(report.Mail), dnsbl = object(mail.DNSBlacklist);
  const location = [scalar(region.Name, ""), scalar(city.Name, "")].filter(Boolean).join(" · ") || "未知";
  return <div className="nq-report">
    <div className="nq-facts">
      <div><span><Globe2 size={14} />检测出口 IP</span><strong className="mono">{scalar(head.IP, sourceIp || "未知")}</strong><small>报告生成于 {date(reportAt)}</small></div>
      <div><span>网络归属</span><strong>{scalar(info.ASN, "") ? `AS${scalar(info.ASN).replace(/^AS/i, "")}` : "未知"}</strong><small>{scalar(info.Organization)}</small></div>
      <div><span>IP 地理位置</span><strong>{location}</strong><small>注册地区 · {scalar(object(info.RegisteredRegion).Name)}</small></div>
    </div>
    <div className="nq-section-heading"><ShieldCheck size={18} /><div><h3>IP 信誉与用途</h3><p>按数据源展示原始结果，各评分口径不同，不能直接相加或比较。</p></div></div>
    <div className="nq-score-grid">
      {Array.from(new Set([...Object.keys(scores), ...Object.keys(usage)])).map(provider => <div className="nq-score" key={provider}>
        <span>{provider}</span><strong>{scalar(scores[provider])}</strong><small>{scalar(usage[provider], "用途未知")}</small>
      </div>)}
      {!Object.keys(scores).length && !Object.keys(usage).length && <p className="muted">未获得有效信誉数据。</p>}
    </div>
    <div className="nq-section-heading"><Globe2 size={18} /><div><h3>流媒体与 AI 可访问性</h3><p>以检测当时的出口 IP 和服务响应为准；未知或失败不代表可用。</p></div></div>
    <div className="nq-media-grid">
      {Object.entries(media).map(([name, value]) => {
        const result = object(value);
        return <div className="nq-media" key={name}><div><strong>{mediaNames[name] || name}</strong><Badge tone={statusTone(result.Status)}>{mediaStatus(result.Status)}</Badge></div><small>地区 {scalar(result.Region)} · 类型 {scalar(result.Type)}</small></div>;
      })}
      {!Object.keys(media).length && <p className="muted">未获得服务可访问性结果。</p>}
    </div>
    <details className="nq-details"><summary>风险标记与黑名单参考</summary>
      <div className="nq-details-content">
        <h4>各来源风险标记</h4>
        <div className="table-scroll"><table><thead><tr><th>项目</th><th>数据来源</th><th>来源判断</th></tr></thead><tbody>
          {Object.entries(factors).flatMap(([kind, sources]) => Object.entries(object(sources)).map(([provider, value]) => <tr key={`${kind}:${provider}`}><td>{factorNames[kind] || kind}</td><td>{provider}</td><td>{scalar(value)}</td></tr>))}
        </tbody></table></div>
        {!Object.keys(factors).length && <p className="muted">未获得风险标记。</p>}
        <h4>DNS 黑名单</h4>
        <dl className="nq-dnsbl">{[["查询总数", "Total"], ["来源标记", "Marked"], ["来源列黑", "Blacklisted"], ["查询未知", "Unknown"]].map(([label, key]) => <div key={key}><dt>{label}</dt><dd>{scalar(dnsbl[key])}</dd></div>)}</dl>
        <p className="nq-note">DNS 查询失败单独列为未知；未列黑不等于 IP 安全证明。旧版报告可能未区分查询失败，请结合报告版本查看。</p>
        <h4>邮件端口连通性</h4>
        {mail.Status === "smtp-not-tested" ? <p className="nq-note">SMTP 未检测。HQIP 使用无特权检测环境，省略需要监听本机端口的邮件探测。</p> : <><p className="nq-note">旧版邮件探测可能使用服务器默认出口，不保证绑定到此节点 IP；以下结果仅作服务器层面的参考。</p><div className="nq-mail-grid">{Object.entries(mail).filter(([name]) => name !== "DNSBlacklist").map(([name, value]) => <div key={name}><span>{name === "Port25" ? "TCP 25 出站" : name}</span><strong>{typeof value === "boolean" ? value ? "可连接" : "未连接" : scalar(value)}</strong></div>)}</div></>}
      </div>
    </details>
    <details className="nq-details"><summary>查看完整 JSON 报告</summary><pre tabIndex={0} aria-label="完整 IP 质量报告">{JSON.stringify(report, null, 2)}</pre></details>
    <div className="nq-source"><span>检测来源 · xykt/IPQuality {scalar(head.Version, "")}</span><a href="https://github.com/xykt/IPQuality" target="_blank" rel="noreferrer">上游项目 <ExternalLink size={12}/></a><a href="https://github.com/KeepS13/HQIP/tree/main/internal/provision/ipquality" target="_blank" rel="noreferrer">适配源码 · AGPL-3.0 <ExternalLink size={12}/></a><button type="button" className="nq-image-link" disabled={!imageAvailable} onClick={onViewImage} title={imageAvailable ? undefined : "图片尚未生成"}><Image size={13}/>查看图片</button></div>
  </div>;
}

function ReportImage({ nodeId, nodeName, version, reportAt, onClose, onStale }: { nodeId: string; nodeName: string; version: string; reportAt: string | null; onClose: () => void; onStale: () => Promise<void> }) {
  const [url, setURL] = useState(""), [preview, setPreview] = useState(""), [error, setError] = useState(""), [loading, setLoading] = useState(true), [attempt, setAttempt] = useState(0), [originalSize, setOriginalSize] = useState(false);
  const staleHandler = useRef(onStale);
  staleHandler.current = onStale;
  useEffect(() => {
    const controller = new AbortController();
    let objectURL = "";
    let reader: FileReader | undefined;
    setLoading(true);setError("");setURL("");setPreview("");
    async function load() {
      try {
        const response = await fetch(`${API_URL}/api/v1/nodes/${encodeURIComponent(nodeId)}/network-quality/image?version=${encodeURIComponent(version)}`, { credentials: "include", cache: "no-store", signal: controller.signal });
        if (response.status === 401) {
          window.dispatchEvent(new Event("hqip:unauthorized"));
          throw new Error("登录已过期，请重新登录后查看图片。");
        }
        if (response.status === 409) {
          void staleHandler.current();
          throw new Error("检测报告已更新，正在刷新，请稍后重新查看。");
        }
        if (!response.ok) throw new Error(response.status === 404 ? "暂未找到报告图片，请刷新报告后重试。" : "图片加载失败，请稍后重试。");
        if (response.headers.get("Content-Type")?.split(";")[0].trim() !== "image/svg+xml") throw new Error("报告图片格式异常，请稍后重试。");
        const blob = await response.blob();
        if (!blob.size || blob.size > 8 * 1024 * 1024) throw new Error("报告图片大小异常，请稍后重试。");
        if (controller.signal.aborted) return;
        // The admin CSP permits data images, while blob URLs remain download-only.
        const dataURL = await new Promise<string>((resolve, reject) => {
          reader = new FileReader();
          reader.onload = () => typeof reader?.result === "string" ? resolve(reader.result) : reject(new Error("图片读取失败，请重试。"));
          reader.onerror = () => reject(new Error("图片读取失败，请重试。"));
          reader.onabort = () => reject(new DOMException("Aborted", "AbortError"));
          reader.readAsDataURL(blob);
        });
        if (controller.signal.aborted) return;
        objectURL = URL.createObjectURL(blob);
        setURL(objectURL);
        setPreview(dataURL);
      } catch (e) {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : "图片加载失败，请稍后重试。");
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    }
    void load();
    return () => { controller.abort();reader?.abort();if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [nodeId, version, attempt]);
  return <Modal title="IP 质量报告图片" description={`${nodeName} · 报告生成于 ${date(reportAt)}`} wide onClose={onClose}>
    <div className="nq-image-viewer">
      {loading && <div role="status" aria-live="polite"><Loading/></div>}
      <ErrorNotice message={error} retry={() => setAttempt(value => value + 1)}/>
      {url && !error && <>
        <div className={`nq-image-canvas ${originalSize ? "nq-image-original" : ""}`} tabIndex={0} aria-label="报告图片，可滚动查看"><img src={preview} alt={`${nodeName} 的 IP 质量报告，生成于 ${date(reportAt)}`} onError={() => setError("图片无法显示，请重试或重新检测。")}/></div>
        <div className="nq-image-actions"><Button variant="secondary" type="button" icon={originalSize ? ZoomOut : ZoomIn} onClick={() => setOriginalSize(value => !value)}>{originalSize ? "适应宽度" : "原始大小"}</Button><a className="button button-primary" href={url} download={`HQIP-IP-质量报告-${nodeId}-${version.slice(0,12)}.svg`}><Download size={15}/>保存图片</a></div>
      </>}
    </div>
  </Modal>;
}

function StartQuality({ node, onClose, onQueued }: { node: Node; onClose: () => void; onQueued: (value: Quality) => void }) {
  const [password, setPassword] = useState(""), [busy, setBusy] = useState(false), [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();setBusy(true);setError("");
    try {
      const value = await api<Quality>(`/nodes/${node.id}/network-quality`, "POST", { password, expectedRevision: node.revision });
      setPassword("");onQueued(value);onClose();
    } catch (e) { setError((e as Error).message);setPassword(""); }
    finally { setBusy(false); }
  }
  return <Modal title="检测节点 IP 质量" description={`在 ${node.name} 所属服务器运行，检测出口 ${node.publicIp}。`} onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack" onSubmit={submit}>
      <Notice>检测会访问第三方 IP 数据库及服务，预计需要几分钟。不会公开生成报告链接，也不会重启代理。每个节点的手动检测间隔至少 10 分钟。</Notice>
      <Field label="本次检测的 SSH 密码" hint="仅用于本次任务连接服务器；任务结束后清除。"><input type="password" required autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} /></Field>
      <ErrorNotice message={error}/>
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" icon={Network} busy={busy}>开始检测</Button></div>
    </form>
  </Modal>;
}

export function NetworkQuality({ node }: { node: Node }) {
  const { data, error, loading, refresh, setData } = usePolling<Quality>(`/nodes/${node.id}/network-quality`);
  const [open, setOpen] = useState(false), [imageOpen, setImageOpen] = useState(false);const toast = useToast();
  const status = data?.status || "not-run", running = status === "queued" || status === "running";
  const report = data?.report && Object.keys(object(data.report)).length ? data.report : null;
  const cooldown = data?.requestedAt ? Math.max(0, Math.ceil((Date.parse(data.requestedAt) + 600000 - Date.now()) / 60000)) : 0;
  const available = node.lifecycle === "active" && node.managed && ["ready", "awaiting_external"].includes(node.status);
  return <Panel title="IP 质量体检" subtitle="查看节点出口的归属、信誉与服务可访问性" className="nq-panel" action={<div className="nq-actions"><Badge tone={running ? "info" : status === "failed" ? "warning" : "muted"}>{statusNames[status] || "状态未知"}</Badge><Button variant="secondary" icon={RefreshCw} disabled={!available || running || cooldown > 0} onClick={() => setOpen(true)}>{cooldown > 0 && !running ? `${cooldown} 分钟后可重测` : report ? "重新检测" : "检测 IP 质量"}</Button></div>}>
    <p className="nq-scope">这是 IP 信誉和服务访问快照，不测量带宽、延迟或丢包。部署成功后自动检测；体检失败不改变 VPN 节点状态。</p>
    <ErrorNotice message={error} retry={() => void refresh()}/>
    {error && data && <Notice tone="warning">刷新失败，保留上次读取的数据；请留意报告时间。</Notice>}
    {data?.error && <ErrorNotice message={data.error}/>}
    {running && <Notice><Clock3 size={15}/> {status === "queued" ? "任务已排队，等待同服务器的其他操作完成。" : "正在检测此节点的公网 IP，完成后报告会自动更新。"}{report && " 下方仍显示上次成功报告。"}</Notice>}
    {!running && report && ["failed", "cancelled"].includes(status) && <Notice tone="warning">本次检测没有产生新报告，下方保留上次成功结果。</Notice>}
    {loading ? <Loading/> : report ? <ReportBody report={report} reportAt={data?.reportAt || null} sourceIp={data?.reportSourceIp || ""} imageAvailable={!!data?.reportImageAvailable && !!data.reportImageVersion} onViewImage={() => setImageOpen(true)}/> : !error && <Empty icon={Network} title={running ? "等待检测报告" : "尚无 IP 质量报告"} description={available ? "部署成功后自动生成报告，已有节点也可使用上方按钮开始检测。" : "节点完成部署后会自动检测；尚未部署或已删除的节点不能发起检测。"}/>}
    <p className="nq-footnote">检测请求直接从服务器发出，不计入代理用量；云厂商可能计入服务器流量账单。数据源无法响应的项目显示未知。</p>
    {open && <StartQuality node={node} onClose={() => setOpen(false)} onQueued={value => {setData(value);toast("已加入 IP 质量检测队列");}}/>}
    {imageOpen && data?.reportImageAvailable && data.reportImageVersion && <ReportImage key={`${node.id}:${data.reportImageVersion}`} nodeId={node.id} nodeName={node.name} version={data.reportImageVersion} reportAt={data.reportAt} onClose={() => setImageOpen(false)} onStale={refresh}/>}
  </Panel>;
}
