import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Globe2, ShieldCheck, Save } from "lucide-react";
import { api, usePolling } from "../api";
import { Badge, Button, Empty, ErrorNotice, Field, Loading, Notice, PageHeader, Panel, RefreshButton, date, useToast } from "../components";
import type { CertificateInfo, CloudflareSettings, CloudflareZone } from "../types";

export function Domains() {
  const { data, error, loading, refresh } = usePolling<CloudflareSettings>("/settings/cloudflare");
  const certificates = usePolling<{ certificates: (CertificateInfo & { mode?: string })[] }>("/settings/cloudflare/certificates");
  const records = usePolling<{records: {id:string;nodeId:string;domain:string;ip:string;zoneName:string;state:string;error:string;nodeLifecycle:string}[]}>("/settings/cloudflare/dns");
  const [token, setToken] = useState(""), [zoneId, setZoneId] = useState(""), [email, setEmail] = useState(""), [version, setVersion] = useState<number | null>(null);
  const [zones, setZones] = useState<CloudflareZone[]>([]), [busy, setBusy] = useState(false), [actionError, setActionError] = useState("");
  const toast = useToast();
  useEffect(() => {
    if (data && version === null) { setZoneId(data.defaultZoneId); setEmail(data.email); setVersion(data.version); }
  }, [data, version]);
  async function verify() {
    setBusy(true); setActionError("");
    try { const r = await api<{ ok: boolean; zones: CloudflareZone[] }>("/settings/cloudflare/verify", "POST", { token }); setZones(r.zones); if (!zoneId && r.zones.length === 1) setZoneId(r.zones[0].id); toast("Token 有效且可读取域名；DNS 写权限将在首次创建记录时校验"); } catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  }
  async function save(e: FormEvent) {
    e.preventDefault(); setBusy(true); setActionError("");
    try { await api("/settings/cloudflare", "PUT", { token, defaultZoneId: zoneId, email: email.trim(), expectedVersion: version || 0 }); setToken(""); setVersion(null); await refresh(); toast("域名设置已保存"); } catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  }
  return <>
    <PageHeader title="域名与证书" eyebrow="DOMAINS & CERTIFICATES" description="连接 Cloudflare，为各 IP 节点分配独立域名并自动续期证书。" actions={<RefreshButton refresh={async () => { await Promise.all([refresh(), certificates.refresh(), records.refresh()]); }} />} />
    <ErrorNotice message={error} retry={() => void refresh()} />
    {loading ? <Loading /> : <Panel title="Cloudflare" subtitle="仅管理 HQIP 创建的节点记录" action={<Badge tone={data?.configured ? "success" : "muted"}>{data?.configured ? "已配置" : "未配置"}</Badge>}>
      <form className="form-stack settings-form" onSubmit={save}>
        <Field label={data?.configured ? "替换 Custom API Token（留空保留）" : "Custom API Token"} hint="权限：所选域名的 Zone:Read 与 DNS:Edit。Token 保存后不可查看。"><input type="password" autoComplete="new-password" required={!data?.configured} value={token} onChange={e => { setToken(e.target.value); setZones([]); }} placeholder={data?.configured ? "已加密保存；输入新 Token 可替换" : "在 Cloudflare 创建受限 Token"} /></Field>
        <div><Button type="button" variant="secondary" icon={ShieldCheck} busy={busy} onClick={() => void verify()}>验证 Token 并读取域名</Button></div>
        <p className="field-hint">此处验证 Token 状态及域名读取权限；DNS:Edit 写权限在首次创建节点记录时校验。</p>
        <div className="form-grid"><Field label="默认域名"><select required value={zoneId} onChange={e => setZoneId(e.target.value)}><option value="">请选择域名</option>{data?.defaultZoneId && !zones.some(z => z.id === data.defaultZoneId) && <option value={data.defaultZoneId}>{data.defaultZoneName || data.defaultZoneId}</option>}{zones.map(z => <option key={z.id} value={z.id} disabled={z.status !== "active"}>{z.name}{z.status !== "active" ? "（尚未激活）" : ""}</option>)}</select></Field><Field label="证书联系邮箱"><input type="email" required value={email} onChange={e => setEmail(e.target.value)} placeholder="用于 ACME 证书账号" /></Field></div>
        <Notice>节点域名采用“仅 DNS”记录，直接指向对应 IP。证书使用 DNS 验证，不要求节点开放 TCP 80。Token 不会发送到节点，也不会修改其他网站的 DNS。</Notice>
        <ErrorNotice message={actionError} /><div className="modal-actions"><Button type="submit" icon={Save} busy={busy} disabled={!zoneId}>保存设置</Button></div>
      </form>
    </Panel>}
    <ErrorNotice message={certificates.error} retry={() => void certificates.refresh()} />
    <ErrorNotice message={records.error} retry={() => void records.refresh()} />
    {!!records.data?.records.length && <Panel title="节点 DNS" subtitle="节点停止后的清理任务也会保留在这里，直到 Cloudflare 确认完成"><div className="table-scroll"><table><thead><tr><th>域名</th><th>公网 IP</th><th>状态</th><th>详情</th></tr></thead><tbody>{records.data.records.map(r=><tr key={r.id}><td><Link to={`/nodes/${r.nodeId}`}>{r.domain}</Link></td><td className="mono">{r.ip}</td><td><Badge tone={r.state === "active" ? "success" : "warning"}>{r.nodeLifecycle === "deleted" ? "待清理" : r.state === "active" ? "已创建" : r.state === "cleanup" ? "清理中" : "待处理"}</Badge></td><td>{r.error || r.zoneName}</td></tr>)}</tbody></table></div></Panel>}
    <Panel title="节点证书" subtitle="签发与节点应用分别确认；续期失败时保留有效旧证书">
      {certificates.loading ? <Loading /> : certificates.data?.certificates.length ? <div className="table-scroll"><table><thead><tr><th>域名 / 节点</th><th>证书有效期</th><th>同步状态</th><th>最近结果</th></tr></thead><tbody>{certificates.data.certificates.map(c => {
        const hasExpiry = Number.isFinite(Date.parse(c.notAfter)) && Date.parse(c.notAfter) > 0;
        const hasNextAttempt = Number.isFinite(Date.parse(c.nextAttempt)) && Date.parse(c.nextAttempt) > 0;
        const httpMode = c.mode === "http" || (!c.mode && c.issuedVersion === 0 && hasExpiry);
        const applied = c.issuedVersion > 0 && c.appliedVersion >= c.issuedVersion;
        const healthy = hasExpiry && c.expiresInDays > 0 && !c.appliedError && !c.warning && !c.error;
        return <tr key={c.nodeId}><td><Link to={`/nodes/${c.nodeId}`}>{c.domain || c.nodeId}</Link></td><td>{hasExpiry ? date(c.notAfter) : "尚未签发或上报"}{hasExpiry && <div className="table-small">{c.expiresInDays > 0 ? `剩余 ${c.expiresInDays} 天` : "已到期"}</div>}</td><td><Badge tone={httpMode ? healthy ? "success" : "warning" : applied && healthy ? "success" : "warning"}>{httpMode ? "HTTP 自动续期" : applied ? "已应用" : c.issuedVersion > 0 ? "待应用" : "尚未签发"}</Badge>{!httpMode && c.issuedVersion > 0 && <div className="table-small">签发 v{c.issuedVersion} / 节点 v{c.appliedVersion}</div>}</td><td>{c.appliedError || c.error || c.warning || (hasExpiry ? "正常" : "等待证书状态")}{!httpMode && hasNextAttempt && <div className="table-small">下次检查 {date(c.nextAttempt)}</div>}</td></tr>;
      })}</tbody></table></div> : <Empty icon={Globe2} title="暂无证书状态" description="部署节点后，将在这里显示 HTTP 或 Cloudflare 证书的有效期和续期结果。" />}
    </Panel>
  </>;
}
