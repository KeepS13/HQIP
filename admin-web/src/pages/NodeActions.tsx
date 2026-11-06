import { useState } from "react";
import { Link } from "react-router-dom";
import { api, usePolling } from "../api";
import { Badge, Button, ErrorNotice, Field, Loading, Modal, Notice } from "../components";
import type { Assignment, Node, NodePreflight, CloudflareSettings } from "../types";
import "./subscriptions.css";

export function DeployNode({ node, onClose, refresh }: { node: Node; onClose: () => void; refresh: () => Promise<void> }) {
  const [password, setPassword] = useState(""), [check, setCheck] = useState<NodePreflight | null>(null), [accept, setAccept] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const cf = usePolling<CloudflareSettings>("/settings/cloudflare");
  const [certificateMode,setCertificateMode] = useState(node.certificateMode || "http");
  const [zoneId,setZoneId] = useState("");
  const certOptions = certificateMode !== node.certificateMode ? {certificateMode,zoneId:zoneId || cf.data?.defaultZoneId || ""} : {};
  async function inspect() { setBusy(true); setError(""); setCheck(null); setAccept(false); try { setCheck(await api<NodePreflight>(`/nodes/${node.id}/preflight`, "POST", { password, ...certOptions })); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function deploy() {
    if (!check) return; setBusy(true); setError("");
    try { await api(`/nodes/${node.id}/retry`, "POST", { password, ...certOptions, takeover: check.canTakeover && accept, preflightFingerprint: check.fingerprint }); setPassword(""); await refresh(); onClose(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Modal title="预检与部署" description="确认当前主机、IP 与安装归属后再执行部署。" onClose={() => { if (!busy) onClose(); }}><div className="form-stack">
    <Field label={`${node.sshUser}@${node.sshHost}:${node.sshPort} 的密码`}><input type="password" autoComplete="new-password" value={password} onChange={e => { setPassword(e.target.value); setCheck(null); setAccept(false); }} /></Field>
    {node.dnsMode !== "cloudflare" && <Field label="证书验证方式"><select value={certificateMode} onChange={e=>{setCertificateMode(e.target.value);setCheck(null);}}><option value="http">HTTP 自动续期</option><option value="cloudflare" disabled={!cf.data?.configured}>Cloudflare DNS 自动续期</option></select></Field>}
    {certificateMode === "cloudflare" && node.certificateMode !== "cloudflare" && <Field label="Cloudflare Zone ID" hint="默认使用域名设置中的 Zone；现有域名和 A 记录保持不变。"><input value={zoneId} onChange={e=>{setZoneId(e.target.value);setCheck(null);}} placeholder={cf.data?.defaultZoneId || "域名所属的 Zone ID"}/></Field>}
    <div><Button variant="secondary" disabled={!password} busy={busy} onClick={() => void inspect()}>检查服务器</Button></div>
    {check && <><Notice>{check.platform} · {check.architecture}<br />目标 IP：{node.publicIp} · {check.boundIps?.includes(node.publicIp) ? "网卡已绑定" : "网卡未绑定"}</Notice>
      {check.conflicts?.length > 0 && <Notice tone="warning"><ul>{check.conflicts.map(c => <li key={c}>{c}</li>)}</ul></Notice>}
      {check.takeoverCandidates?.length > 0 && <div><strong>检测到已有安装</strong><ul>{check.takeoverCandidates.map(c => <li key={c.unit}>{c.unit} · <span className="mono">{c.configPath}</span></li>)}</ul></div>}
      {check.canTakeover && <label className="assignment-option"><input type="checkbox" checked={accept} onChange={e => setAccept(e.target.checked)} /><span>备份后接管以上服务。旧用户凭据将失效，新实例使用当前节点的授权；失败恢复原配置及启停状态。</span></label>}
      {!check.canInstall && !check.canTakeover && <Notice tone="warning">此服务器暂不满足部署条件，请先处理上方冲突。</Notice>}
    </>}
    <ErrorNotice message={error} /><div className="modal-actions"><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button busy={busy} disabled={!check || !(check.canInstall || (check.canTakeover && accept))} onClick={() => void deploy()}>{check?.canTakeover && accept ? "备份并接管" : "部署节点"}</Button></div>
  </div></Modal>;
}

interface DeletionPreview {
  node: Node;
  devices: {
    id: string;
    name: string;
    assignment: Assignment;
    candidates: Node[];
  }[];
  subscriptionTemplates: {
    id: string;
    name: string;
    defaultNode: boolean;
    enabled: boolean;
    listed: boolean;
  }[];
}
export function DeleteNode({
  node,
  onClose,
  refresh,
}: {
  node: Node;
  onClose: () => void;
  refresh: () => Promise<void>;
}) {
  const {
    data,
    error: loadError,
    loading,
    refresh: reload,
  } = usePolling<DeletionPreview>(`/nodes/${node.id}/delete-preview`, 60000);
  const [choices, setChoices] = useState<Record<string, string>>({}),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const needed =
    data?.devices.filter(
      (d) => d.assignment.defaultNodeId === node.id && d.candidates.length > 0,
    ) || [];
  const references = data?.subscriptionTemplates || [];
  async function remove() {
    if (!data || loading || loadError || references.length) return;
    setBusy(true);
    setError("");
    try {
      const replacements = Object.fromEntries(
        data.devices.map((d) => [
          d.id,
          {
            expectedRevision: d.assignment.revision,
            defaultNodeId:
              d.assignment.defaultNodeId === node.id
                ? choices[d.id] || ""
                : d.assignment.defaultNodeId,
          },
        ]),
      );
      await api(`/nodes/${node.id}`, "DELETE", {
        expectedRevision: data.node.revision,
        replacements,
      });
      await refresh();
      onClose();
    } catch (e) {
      setError((e as Error).message);
      await reload();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={`删除节点：${node.name}`}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <div className="form-stack">
        <Notice tone="warning">
          此 IP 节点将退出订阅，撤销连接并停止受管服务。其他 IP
          的节点不受影响；历史流量保留。离线节点在确认停止前显示“待停止”。
        </Notice>
        <ErrorNotice message={loadError} retry={() => void reload()} />
        {loading ? (
          <Loading />
        ) : (
          <>
            {!!references.length && (
              <section
                className="node-deletion-conflicts"
                aria-label="引用此节点的订阅模板"
              >
                <strong>
                  请先解除 {references.length} 个订阅模板的节点引用
                </strong>
                <p>
                  隐藏或停用的模板也需处理。打开模板，移除此节点并明确选择新的默认节点；也可清空全部节点。
                </p>
                {references.map((template) => (
                  <div className="node-deletion-reference" key={template.id}>
                    <div>
                      <strong>{template.name}</strong>
                      <div className="node-deletion-reference-badges">
                        <Badge tone={template.defaultNode ? "warning" : "info"}>
                          {template.defaultNode ? "默认节点" : "可用节点"}
                        </Badge>
                        {!template.enabled && (
                          <Badge tone="muted">已停用</Badge>
                        )}
                        {!template.listed && (
                          <Badge tone="muted">仅后台分配</Badge>
                        )}
                      </div>
                    </div>
                    <Link
                      className="button button-secondary"
                      to={`/subscriptions?edit=${encodeURIComponent(template.id)}`}
                      onClick={onClose}
                    >
                      编辑订阅模板
                    </Link>
                  </div>
                ))}
              </section>
            )}
            {data?.devices.length
              ? data.devices.map((d) => (
                  <div key={d.id}>
                    <strong>{d.name}</strong>
                    {d.assignment.defaultNodeId === node.id ? (
                      d.candidates.length ? (
                        <Field label="替代默认节点">
                          <select
                            required
                            value={choices[d.id] || ""}
                            onChange={(e) =>
                              setChoices((c) => ({
                                ...c,
                                [d.id]: e.target.value,
                              }))
                            }
                          >
                            <option value="">请选择该设备剩余授权节点</option>
                            {d.candidates.map((n) => (
                              <option key={n.id} value={n.id}>
                                {n.name} · {n.publicIp}
                              </option>
                            ))}
                          </select>
                        </Field>
                      ) : (
                        <p className="muted">
                          删除后无剩余节点，将停止该设备的代理。
                        </p>
                      )
                    ) : (
                      <p className="muted">移除此 IP 授权，保留原默认节点。</p>
                    )}
                  </div>
                ))
              : !references.length && (
                  <p className="muted">当前没有订阅模板引用此节点。</p>
                )}
          </>
        )}
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button
            variant="danger"
            busy={busy}
            disabled={
              !data ||
              loading ||
              !!loadError ||
              references.length > 0 ||
              needed.some((d) => !choices[d.id])
            }
            onClick={() => void remove()}
          >
            删除并停止节点
          </Button>
        </div>
      </div>
    </Modal>
  );
}

export function RetireNode({ node, onClose, refresh }: { node: Node; onClose: () => void; refresh: () => Promise<void> }) {
  const [password,setPassword]=useState(""), [busy,setBusy]=useState(false), [error,setError]=useState("");
  return <Modal title="通过 SSH 完成清理" onClose={() => {if (!busy) onClose();}}><form className="form-stack" onSubmit={async e => {e.preventDefault();setBusy(true);setError("");try {await api(`/nodes/${node.id}/retire`,"POST",{password});setPassword("");await refresh();onClose();}catch(e){setError((e as Error).message);}finally{setBusy(false);}}}><Notice>Agent 失联或版本较旧时，可通过 SSH 检查归属并停止本节点。无法验证归属的文件和服务不会被清理。</Notice><Field label="SSH 密码"><input type="password" required autoComplete="new-password" value={password} onChange={e=>setPassword(e.target.value)} /></Field><ErrorNotice message={error}/><div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" busy={busy}>检查并清理</Button></div></form></Modal>;
}

export function DeletedNodeNotice({ node }: { node: Node }) {
  return <Notice tone="success">此节点已删除，历史用量仍保留。<Link to={`/servers/${node.serverId}`}>返回服务器</Link>可为这个 IP 重新创建节点；新节点需要重新分配给设备。</Notice>;
}
