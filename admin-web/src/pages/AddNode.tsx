import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus } from "lucide-react";
import { api, usePolling } from "../api";
import { Button, ErrorNotice, Field, Modal, Notice } from "../components";
import { isPublicIPv4 } from "../public-ipv4";
import type { Node, ManagedServer, ServerAddress, CloudflareSettings, CloudflareZone } from "../types";

export function AddNode({ server, initialServerId = "", initialPublicIp = "", initialAddressId = "", onClose }: {
  server?: ManagedServer;
  initialServerId?: string;
  initialPublicIp?: string;
  initialAddressId?: string;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const ipEdited = useRef(false);
  const servers = usePolling<ManagedServer[]>("/servers"), settings = usePolling<CloudflareSettings>("/settings/cloudflare");
  const [serverId, setServerId] = useState(server?.id || initialServerId);
  const [zones, setZones] = useState<CloudflareZone[]>([]);
  const [addressLoading, setAddressLoading] = useState(!!initialAddressId);
  const [form, setForm] = useState({ name: "", publicIp: initialPublicIp, domain: "", port: "443", password: "", zoneId: "", dnsMode: "" });
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const change = (key: keyof typeof form, value: string) => setForm(p => ({ ...p, [key]: value }));
  useEffect(() => {
    if (!initialAddressId || serverId !== initialServerId) { setAddressLoading(false); return; }
    let alive = true;
    setAddressLoading(true);
    api<ServerAddress[]>(`/servers/${serverId}/addresses`).then(addresses => {
      if (!alive) return;
      const address = addresses.find(a => a.id === initialAddressId);
      if (!address) { setError("原链接中的 IP 已被移除，请手动填写公网 IPv4。"); return; }
      if (!ipEdited.current) setForm(p => ({ ...p, publicIp: address.ip }));
    }).catch(e => { if (alive) setError(e.message); }).finally(() => { if (alive) setAddressLoading(false); });
    return () => { alive = false; };
  }, [initialAddressId, initialServerId, serverId]);
  useEffect(() => { if (settings.data && !form.dnsMode) setForm(p => ({ ...p, dnsMode: settings.data!.configured ? "cloudflare" : "manual", zoneId: settings.data!.defaultZoneId })); }, [settings.data, form.dnsMode]);
  useEffect(() => { if (!settings.data?.configured) return; let alive = true; api<{zones: CloudflareZone[]}>("/settings/cloudflare/zones").then(r => { if (alive) setZones(r.zones); }).catch(e => { if (alive) setError(e.message); }); return () => { alive = false; }; }, [settings.data?.configured]);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!serverId) { setError("请选择服务器"); return; }
    if (!isPublicIPv4(form.publicIp)) { setError("请填写有效的公网 IPv4 地址"); return; }
    setBusy(true); setError("");
    try {
      const dnsMode = form.dnsMode || "manual";
      const node = await api<Node>("/nodes", "POST", { name: form.name.trim(), serverId, publicIp: form.publicIp.trim(), domain: dnsMode === "manual" ? form.domain.trim().toLowerCase() : "", dnsMode, zoneId: form.zoneId, port: Number(form.port), password: form.password });
      setForm(p => ({ ...p, password: "" })); onClose(); navigate(`/nodes/${node.id}`);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal title="添加节点" description="填写服务器已绑定的公网 IPv4，为其部署独立 Hysteria 2 实例。" wide onClose={() => { if (!busy) onClose(); }}>
    <form onSubmit={submit} className="form-stack">
      <Field label="节点名称"><input required maxLength={80} value={form.name} onChange={e => change("name", e.target.value)} placeholder="例如：香港 · IP 1" /></Field>
      <ErrorNotice message={server ? "" : servers.error} />
      <div className="form-grid">
        <Field label="服务器">{server ? <input readOnly value={`${server.name} · ${server.sshHost}`} /> : <select required value={serverId} onChange={e => { ipEdited.current = true; setServerId(e.target.value); change("publicIp", ""); setError(""); }}><option value="">选择服务器</option>{servers.data?.map(s => <option key={s.id} value={s.id}>{s.name} · {s.sshHost}</option>)}</select>}</Field>
        <Field label="公网 IPv4" hint="入口和出口均使用此地址"><input required inputMode="decimal" autoCapitalize="none" spellCheck={false} value={form.publicIp} onChange={e => { ipEdited.current = true; change("publicIp", e.target.value); }} placeholder="已绑定到此服务器的公网 IPv4" /></Field>
      </div>
      {!server && <p className="muted">还没有服务器？<Link to="/servers">添加服务器</Link>。</p>}
      <Field label="域名方式"><select value={form.dnsMode || "manual"} onChange={e => change("dnsMode", e.target.value)}><option value="cloudflare" disabled={!settings.data?.configured}>Cloudflare 自动分配</option><option value="manual">使用已有域名</option></select></Field>
      {form.dnsMode === "cloudflare" ? <Field label="Cloudflare 域名" hint="自动创建独立子域名和仅 DNS 的 A 记录"><select required value={form.zoneId} onChange={e => change("zoneId", e.target.value)}><option value="">选择域名</option>{settings.data?.defaultZoneId && !zones.some(z => z.id === settings.data!.defaultZoneId) && <option value={settings.data.defaultZoneId}>{settings.data.defaultZoneName}</option>}{zones.map(z => <option value={z.id} key={z.id} disabled={z.status !== "active"}>{z.name}</option>)}</select></Field> : <Field label="已有节点域名" hint="A 记录需指向填写的 IP；HTTP 证书验证需要此 IP 的 TCP 80 可用"><input required value={form.domain} pattern="[^/ :]+[.][^/ :]+" onChange={e => change("domain", e.target.value)} placeholder="node.example.com" autoCapitalize="none" /></Field>}
      {!settings.data?.configured && <p className="muted"><Link to="/domains">配置 Cloudflare Token</Link> 后可自动分配域名。</p>}
      <div className="form-grid"><Field label="本次部署的 SSH 密码" hint="留空使用服务器已保存的 SSH 凭据。"><input type="password" autoComplete="new-password" value={form.password} onChange={e => change("password", e.target.value)} /></Field><Field label="Hysteria UDP 端口"><input type="number" required min={1} max={65535} value={form.port} onChange={e => change("port", e.target.value)} /></Field></div>
      <Notice>部署前会检查 IP 是否绑定网卡并验证实际出口，不会自动修改系统网络。节点记录实际上传与下载流量，统计周期随所属服务器。已有 HY2 会先进行归属检查；发现旧安装时，可在节点详情预检后接管。</Notice>
      <ErrorNotice message={error} /><div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" busy={busy} disabled={addressLoading || !serverId} icon={Plus}>添加并部署</Button></div>
    </form>
  </Modal>;
}
