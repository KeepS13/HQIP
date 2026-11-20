import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import { Button, ErrorNotice, Field, Modal, Notice } from "../components";
import type { ManagedServer, Node } from "../types";

export function NodeIPSwitch({ server, node, onClose }: { server: ManagedServer; node: Node; onClose: () => void }) {
  const navigate = useNavigate();
  const [password, setPassword] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const automaticDNS = node.dnsMode === "cloudflare";
  async function submit(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try {
      await api(`/nodes/${node.id}/public-ip`, "POST", {
        oldPublicIp: node.publicIp, publicIp: server.sshHost,
        expectedRevision: node.revision, expectedServerRevision: server.revision,
        password, manualDnsConfirmed: confirmed,
      });
      setPassword(""); onClose(); navigate(`/nodes/${node.id}`);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal title="是否切换节点公网 IP？" description="服务器连接已保存，SSH 和主机身份检查通过。" onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack" onSubmit={submit}>
      <p>旧 IP 上已部署节点 <strong>{node.name}</strong>，是否将此节点一起切换到新的公网 IP？</p>
      <dl className="details-list"><div><dt>原公网 IP</dt><dd className="mono">{node.publicIp}</dd></div><div><dt>新公网 IP</dt><dd className="mono">{server.sshHost}</dd></div><div><dt>节点域名</dt><dd className="break-all">{node.domain}</dd></div></dl>
      <Notice tone="warning">切换会重新部署此节点，期间连接可能中断。节点名称、订阅关系和历史用量保留；新 IP 必须已配置到服务器网卡。</Notice>
      {automaticDNS ? <Notice>将自动把此节点的 Cloudflare A 记录更新为新 IP，保留原域名。</Notice> : <><Notice>请先将 {node.domain} 的 A 记录改为 {server.sshHost}，并移除其他 A / AAAA 记录。部署时会检查解析和实际出口。</Notice><label className="assignment-option"><input type="checkbox" required checked={confirmed} onChange={e => setConfirmed(e.target.checked)} /><span>我已将节点域名解析改为新 IP</span></label></>}
      {node.installationVersion !== 2 ? <Notice tone="warning">此节点需要先升级为独立实例。请到 <Link to={`/nodes/${node.id}`}>节点详情</Link> 预检并升级后重试。</Notice> : <Field label="本次切换的 SSH 密码" hint="留空使用已保存的密码；未保存或密码已更换时请填写。"><input type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} /></Field>}
      <ErrorNotice message={error} />
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>暂不切换</Button><Button type="submit" busy={busy} disabled={node.installationVersion !== 2 || (!automaticDNS && !confirmed)}>确认切换并部署</Button></div>
    </form>
  </Modal>;
}
