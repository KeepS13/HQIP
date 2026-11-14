import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { KeyRound, RefreshCw, Upload } from "lucide-react";
import { api, usePolling } from "../api";
import { Badge, Button, ErrorNotice, Field, Loading, Modal, Notice, Panel, date } from "../components";
import type { ManagedServer, Node } from "../types";

interface Credential {
  serverId: string; configured: boolean; revision: number; status: string; message: string;
  updatedAt: string | null; lastChecked: string | null; nextCheck: string | null;
}
interface Operation {
  id: string; serverId: string; nodeId: string; kind: string; state: string; target: string;
  message: string; createdAt: number; finishedAt: number; serverName?: string; nodeName?: string;
}
const credentialLabels: Record<string, string> = { "not-configured": "未保存", healthy: "验证通过", unchecked: "待检查", invalid: "密码认证失败", unreachable: "连接异常", "identity-changed": "主机身份待确认" };
const stateLabels: Record<string, string> = { pending: "排队中", running: "执行中", succeeded: "已完成", failed: "失败", uncertain: "已停用" };
const kindLabels: Record<string, string> = { check: "SSH 巡检", password: "已停用的远程改密", "agent-upgrade": "Agent 升级" };

export function OperationHistory({ serverId }: { serverId?: string }) {
  const { data, error } = usePolling<Operation[]>(`/server-operations${serverId ? `?serverId=${encodeURIComponent(serverId)}` : ""}`);
  return <Panel title="运维任务" subtitle="Agent 升级与每小时 SSH 凭据巡检的最近记录">
    <ErrorNotice message={error} />
    {!data?.length ? <p className="muted">暂无运维任务</p> : <div className="table-scroll"><table><thead><tr><th>任务</th><th>对象</th><th>状态</th><th>结果</th><th>发起时间</th></tr></thead><tbody>{data.map(j => <tr key={j.id}>
      <td>{kindLabels[j.kind] || j.kind}{j.target && <div className="table-small mono">{j.target.slice(0, 8)}</div>}</td>
      <td><Link to={j.nodeId ? `/nodes/${j.nodeId}` : `/servers/${j.serverId}`}>{j.nodeId ? j.nodeName || j.nodeId : j.serverName || j.serverId}</Link></td>
      <td><Badge tone={j.state === "succeeded" ? "success" : j.state === "failed" || j.state === "uncertain" ? "warning" : "muted"}>{stateLabels[j.state] || j.state}</Badge></td>
      <td>{j.message || "等待执行"}</td>
      <td>{date(new Date(j.createdAt * 1000).toISOString())}</td>
    </tr>)}</tbody></table></div>}
  </Panel>;
}

export function ServerCredentialPanel({ server }: { server: ManagedServer }) {
  const { data, error, refresh } = usePolling<Credential>(`/servers/${server.id}/credential`);
  const [mode, setMode] = useState<"save" | "delete" | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false), [actionError, setActionError] = useState("");
  const [message, setMessage] = useState("");
  function open(next: typeof mode) { setPassword(""); setActionError(""); setMode(next); }
  async function check() {
    setBusy(true); setActionError(""); setMessage("");
    try { await api(`/servers/${server.id}/credential/check`, "POST", {}); setMessage("已加入巡检队列，可在下方查看结果"); await refresh(); }
    catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  }
  async function submit(e: FormEvent) {
    e.preventDefault(); if (!data) return;
    setBusy(true); setActionError(""); setMessage("");
    try {
      await api(`/servers/${server.id}/credential`, mode === "delete" ? "DELETE" : "PUT", { ...(mode === "save" ? { password } : {}), expectedRevision: server.revision, credentialRevision: data.revision });
      setMessage(mode === "delete" ? "已移除保存的凭据" : "SSH 密码已验证并更新保存");
      setMode(null); setPassword(""); await refresh();
    } catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  }
  return <><Panel title="SSH 凭据与自动运维" subtitle="保存后每小时检查一次；认证失败与网络不可达分别记录" action={<Button icon={KeyRound} variant="secondary" onClick={() => open("save")}>{data?.configured ? "更新 SSH 密码" : "保存 SSH 密码"}</Button>}>
    <ErrorNotice message={error || (!mode ? actionError : "")} />
    {message && <Notice>{message}</Notice>}
    {data && <><div className="row-actions"><Badge tone={data.status === "healthy" ? "success" : data.configured ? "warning" : "muted"}>{credentialLabels[data.status] || data.status}</Badge><span>{data.message}</span></div>
      <dl className="details-list"><div><dt>登录账号</dt><dd>{server.sshUser}@{server.sshHost}:{server.sshPort}</dd></div><div><dt>上次巡检</dt><dd>{data.lastChecked ? date(data.lastChecked) : "尚未检查"}</dd></div><div><dt>下次巡检</dt><dd>{data.configured && data.nextCheck ? date(data.nextCheck) : "保存凭据后启用"}</dd></div></dl>
      {data.configured && <div className="row-actions"><Button variant="secondary" icon={RefreshCw} busy={busy && !mode} onClick={() => void check()}>立即检查</Button><Button variant="ghost" onClick={() => open("delete")}>移除托管凭据</Button></div>}
    </>}
  </Panel>
  {mode && <Modal title={mode === "save" ? "更新保存的 SSH 密码" : "移除托管凭据"} onClose={() => { if (!busy) open(null); }}><form className="form-stack" onSubmit={submit}>
    <Notice>{mode === "save" ? "在服务器厂商处修改密码后，请在这里更新。系统只验证 SSH 登录并加密保存，不会修改服务器上的密码。" : "移除后，自动巡检和免输密码运维将停止；服务器上的密码保持有效。"}</Notice>
    {mode !== "delete" && <Field label="服务器当前 SSH 密码"><input type="password" autoComplete="new-password" required minLength={1} maxLength={1024} value={password} onChange={e => setPassword(e.target.value)} /></Field>}
    <ErrorNotice message={actionError} /><div className="modal-actions"><Button variant="secondary" type="button" disabled={busy} onClick={() => open(null)}>取消</Button><Button type="submit" busy={busy}>{mode === "save" ? "验证并更新保存" : "移除凭据"}</Button></div>
  </form></Modal>}
  <OperationHistory serverId={server.id} /></>;
}

export function AgentUpgradeDialog({ nodes, onClose }: { nodes?: Node[]; onClose: () => void }) {
  const inventory = usePolling<Node[]>("/nodes");
  const release = usePolling<{ sourceSHA: string }>("/agent-release");
  const available = (nodes || inventory.data || []).filter(n => n.lifecycle === "active" && n.managed);
  const [selected, setSelected] = useState<string[]>(nodes?.filter(n => n.lifecycle === "active" && n.managed).map(n => n.id) || []);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [results, setResults] = useState<{ nodeId: string; job?: Operation; error?: string }[] | null>(null);
  async function submit() {
    if (!release.data) return; setBusy(true); setError("");
    try { setResults(await api("/agent-upgrades", "POST", { nodeIds: selected, sourceSHA: release.data.sourceSHA })); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal wide title="升级 Agent" description="保留流量账本与节点配置，转发核心继续运行。" onClose={() => { if (!busy) onClose(); }}>
    <div className="form-stack"><ErrorNotice message={error || release.error || (!nodes ? inventory.error : "")} />
      {release.data && <Notice>目标版本 {release.data.sourceSHA.slice(0, 8)}。服务器需先保存 SSH 凭据；同一服务器上的任务依次执行。</Notice>}
      {results ? <><ul>{results.map(r => <li key={r.nodeId}>{available.find(n => n.id === r.nodeId)?.name || r.nodeId}：{r.job ? "已排队，可在运维任务中查看进度" : r.error}</li>)}</ul><OperationHistory /></> : <>{inventory.loading && !nodes ? <Loading /> : <><label className="assignment-option"><input type="checkbox" checked={available.length > 0 && selected.length === available.length} onChange={e => setSelected(e.target.checked ? available.map(n => n.id) : [])} /><span>选择全部 {available.length} 个可升级节点</span></label>{available.map(n => <label className="assignment-option" key={n.id}><input type="checkbox" checked={selected.includes(n.id)} onChange={e => setSelected(old => e.target.checked ? [...old, n.id] : old.filter(id => id !== n.id))} /><span>{n.name} · {n.publicIp}</span></label>)}</>}</>}
      <div className="modal-actions"><Button variant="secondary" disabled={busy} onClick={onClose}>{results ? "关闭" : "取消"}</Button>{!results && <Button icon={Upload} disabled={!selected.length || !release.data} busy={busy} onClick={() => void submit()}>升级所选 {selected.length} 个节点</Button>}</div>
    </div>
  </Modal>;
}
