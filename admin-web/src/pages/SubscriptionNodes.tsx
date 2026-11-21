import { useEffect, useState, type FormEvent } from "react";
import { Network, RefreshCw, Search } from "lucide-react";
import { api } from "../api";
import { Badge, Button, ErrorNotice, Field, Loading, Modal, Notice } from "../components";
import type { UserSubscription } from "../portal/types";
import "./subscription-nodes.css";

interface NodeOption {
  id: string; name: string; publicIp: string; domain: string;
  inTemplate: boolean; selected: boolean; selectable: boolean; reason: string;
}
interface NodeOptions { subscription: UserSubscription; nodes: NodeOption[] }

export function SubscriptionNodes({ userId, value, onClose, onSaved }: {
  userId: string; value: UserSubscription; onClose: () => void; onSaved: () => Promise<void>;
}) {
  const [snapshot, setSnapshot] = useState<NodeOptions | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const path = `/users/${userId}/subscriptions/${value.id}/nodes`;
  useEffect(() => {
    let alive = true;
    setLoading(true); setError(""); setSnapshot(null);
    void api<NodeOptions>(path).then(data => {
      if (alive) { setSnapshot(data); setSelected(data.subscription.nodeIds); }
    }).catch(e => { if (alive) setError((e as Error).message); })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [path, reload]);
  const original = snapshot?.subscription;
  // Use this subscription's saved allocation count, even if the template default
  // changed later or its current nodes have a shortfall. The server rechecks it.
  const requiredCount = original?.nodesPerSubscription ?? 0;
  const countMatches = selected.length === requiredCount;
  const editable = !!original && !["removed", "revoked"].includes(original.status) && Date.parse(original.expiresAt) > Date.now();
  const added = selected.filter(id => !original?.nodeIds.includes(id));
  const removed = original?.nodeIds.filter(id => !selected.includes(id)) || [];
  const changed = added.length > 0 || removed.length > 0;
  const invalid = selected.some(id => !snapshot?.nodes.find(node => node.id === id)?.selectable);
  const search = query.trim().toLowerCase();
  const nodes = snapshot?.nodes.filter(node => `${node.name} ${node.publicIp} ${node.domain} ${node.id}`.toLowerCase().includes(search)) || [];
  const names = (ids: string[]) => ids.map(id => {
    const node = snapshot?.nodes.find(item => item.id === id);
    return node ? `${node.name}${node.publicIp ? ` (${node.publicIp})` : ""}` : id;
  }).join("、") || "无";
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!original || !editable || !changed || invalid || !countMatches) return;
    setBusy(true); setError("");
    try {
      await api(path, "PATCH", { nodeIds: selected, expectedRevision: original.revision });
      await onSaved(); onClose();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <Modal title="修改节点" description={`${value.templateName} · #${value.id.slice(-8)}`} onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack subscription-node-editor" onSubmit={submit}>
      <Notice>仅更换这一份订阅使用的节点，分配数量保持不变。已用流量、剩余额度、计费周期及到期时间保持不变，新旧节点的流量继续记入同一份订阅。可选择套餐外节点；同用户的同套餐订阅和订单不能重复占用节点。</Notice>
      {loading ? <Loading /> : snapshot && <>
        {!editable && <Notice tone="warning">订阅已到期或已移除，不能修改节点。</Notice>}
        <Field label="搜索节点" hint="支持节点名称、IP 和域名"><div className="subscription-node-search"><Search size={16} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索全站节点" /></div></Field>
        <div className="subscription-node-count" role="status"><strong>已选 {selected.length} / {requiredCount} 个节点</strong><span>先取消原节点，再勾选替换节点</span></div>
        <div className="subscription-assignment-options" role="group" aria-label="可分配节点">
          {nodes.map(node => {
            const checked = selected.includes(node.id);
            return <label key={node.id} className={`subscription-assignment-option${checked ? " selected" : ""}`}>
              <input type="checkbox" checked={checked} disabled={busy || !editable || !checked && (!node.selectable || selected.length >= requiredCount)} onChange={e => setSelected(ids => e.target.checked ? [...ids, node.id] : ids.filter(id => id !== node.id))} />
              <span className="subscription-node-details"><strong>{node.name}</strong><span className="mono">{node.publicIp || node.id}{node.domain ? ` · ${node.domain}` : ""}</span>{node.reason && <small>{node.reason}</small>}</span>
              <span className="subscription-node-badges"><Badge tone={node.inTemplate ? "info" : "muted"}>{node.inTemplate ? "套餐内" : "套餐外"}</Badge>{node.selected && <Badge tone="success">当前节点</Badge>}</span>
            </label>;
          })}
          {!nodes.length && <p className="muted">{search ? "没有匹配的节点" : "暂无可分配节点"}</p>}
        </div>
        <dl className="subscription-node-preview" aria-label="节点变更预览"><div><dt>新增节点</dt><dd>{names(added)}</dd></div><div><dt>移除节点</dt><dd>{names(removed)}</dd></div><div><dt>分配数量</dt><dd>{requiredCount} 个（保持不变）</dd></div></dl>
        {!countMatches && <Notice tone="warning">此订阅须选择 {requiredCount} 个节点，当前已选 {selected.length} 个；选满后才能保存。</Notice>}
        {!!removed.length && !!selected.length && <Notice tone="warning">移除节点上的连接将被撤销。HQIP 客户端同步后切换到可用节点；第三方客户端需要刷新订阅。离线节点最多有 120 秒同步窗口。</Notice>}
        {invalid && changed && <Notice tone="warning">请先取消勾选不可分配的节点，再保存。</Notice>}
      </>}
      <ErrorNotice message={error} />
      <div className="subscription-node-reload"><Button type="button" variant="secondary" icon={RefreshCw} disabled={busy || loading} onClick={() => setReload(n => n + 1)}>重新加载</Button><small>重新加载会清除未保存的选择。</small></div>
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" icon={Network} busy={busy} disabled={loading || !editable || !changed || invalid || !countMatches}>保存节点</Button></div>
    </form>
  </Modal>;
}
