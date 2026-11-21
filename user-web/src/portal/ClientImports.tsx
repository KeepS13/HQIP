import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ArrowRight, ArrowUpRight, Copy, Download, Package, RefreshCw } from "lucide-react";
import { Badge, Button, CopyButton, Empty, ErrorNotice, Field, Loading, Modal, Notice, PageHeader, Panel, date, useToast } from "../components";
import { clientImportScheme, importClients, selectedImportSubscription, subscriptionImportURL, type ImportClient, type ImportClientID } from "./client-imports";
import { subscriptionLabels, type UserSubscription } from "./types";
import { useImportLink, type LinkAction } from "./useImportLink";
import "./client-imports.css";

function ImportSubscription({ subscription, subscriptions, client, onBusyChange }: { subscription: UserSubscription | null; subscriptions: UserSubscription[]; client: ImportClient; onBusyChange: (busy: boolean) => void }) {
  const activeCount = subscriptions.filter((item) => item.status === "active").length;
  const active = subscription ? subscription.status === "active" : activeCount > 0;
  const detail = useImportLink(subscription?.id ?? null, active, onBusyChange);
  const [action, setAction] = useState<LinkAction | null>(null);
  const toast = useToast();
  const mounted = useRef(false);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const ready = !detail.loading && !detail.busy && !detail.error && !detail.uncertain;
  const canUse = active && ready;
  const url = subscriptionImportURL(detail.url, client.format);
  const downloadURL = subscriptionImportURL(detail.url, client.format, true);
  const name = subscription ? `HQIP ${subscription.templateName} ${subscription.id.slice(-6)}` : "HQIP 全部订阅";
  const scheme = clientImportScheme(client.id, url, name);
  async function confirmAction() {
    if (!action) return;
    const result = await detail.changeLink(action);
    if (!mounted.current || result === null) return;
    setAction(null);
    toast(action === "revoke" ? "导入链接已撤销，旧配置不能继续连接" : "链接已轮换，请复制新链接并重新导入客户端");
  }
  async function prepareAndCopy() {
    const result = await detail.changeLink("create");
    if (!mounted.current || !result) return;
    try {
      await navigator.clipboard.writeText(subscriptionImportURL(result, client.format));
      if (mounted.current) toast("已复制到剪贴板");
    } catch {
      if (mounted.current) toast("链接已准备好，请再次点击复制链接");
    }
  }
  function openAction(next: LinkAction) {
    detail.clearActionError();
    setAction(next);
  }
  const scope = subscription ? "这份订阅" : "全部订阅";
  return <>
    <Panel title={subscription?.templateName || "全部订阅的合并链接"} subtitle={subscription ? `订阅 #${subscription.id.slice(-8)} · 有效至 ${date(subscription.expiresAt)}` : `包含当前 ${activeCount} 份有效订阅，节点按所属订阅独立计量。`} action={<Badge tone={active ? "success" : "warning"}>{subscription ? subscriptionLabels[subscription.status] || "暂时不可用" : `${activeCount} 份有效订阅`}</Badge>}>
      <div className="client-import-body">
        <div className="client-import-context">
          <span>当前客户端：<strong>{client.name}</strong></span>
          {subscription && <Link to={`/subscriptions/${encodeURIComponent(subscription.id)}`}>查看订阅详情 <ArrowRight size={14} /></Link>}
        </div>
        <ErrorNotice message={detail.error ? `${detail.error}；请刷新后再导入或导出。` : ""} retry={() => void detail.refresh()} />
        <ErrorNotice message={action ? "" : detail.actionError} />
        {!active && <Notice tone="warning">{subscription ? `这份订阅${subscriptionLabels[subscription.status] || "暂时不可用"}` : "目前没有有效订阅"}，暂时不能复制、轮换、导入或导出配置。</Notice>}
        {detail.loading ? <Loading /> : <>
          {url && <Field label="订阅链接"><input type="password" readOnly value={detail.busy ? "" : url} aria-label="此订阅的导入链接" /></Field>}
          <div className="client-import-actions">
            {canUse && url ? <CopyButton key={url} value={url} label="复制链接" /> : <Button variant="secondary" icon={Copy} busy={detail.busy && !action} disabled={!canUse} onClick={() => void prepareAndCopy()}>复制链接</Button>}
            {scheme && (canUse ? <a className="button button-primary" href={scheme}>一键导入 <ArrowUpRight size={16} /></a> : <Button disabled>一键导入</Button>)}
            {url && (canUse ? <a className="button button-secondary" href={downloadURL}>导出配置 <Download size={16} /></a> : <Button variant="secondary" icon={Download} disabled>导出配置</Button>)}
          </div>
          <div className="client-import-actions client-import-management">
            <Button variant="secondary" icon={RefreshCw} disabled={!canUse || !url} onClick={() => openAction("rotate")}>轮换链接</Button>
            <Button variant="ghost" disabled={!ready} onClick={() => openAction("revoke")}>撤销导入链接</Button>
          </div>
        </>}
        <p className="client-import-help">{client.help}</p>
        <Notice tone="warning">怀疑链接泄露时，请轮换链接。{scope}的旧链接会立即作废，旧配置中的节点凭据也会失效；正在使用这些凭据的连接会被断开，必须重新导入新链接才能继续使用。</Notice>
        <p className="client-import-help">所有客户端格式共用所选订阅的链接和凭据，轮换会同时影响它们。订阅额度与有效期保留，HQIP 官方客户端不受影响。连接断开由节点同步撤销状态后执行，可能有短暂延迟。</p>
        {!subscription && <p className="client-import-help">合并链接包含本次复制时的有效订阅。购买新订阅后请重新复制合并链接；其中任一订阅轮换或撤销后，也需要重新复制合并链接。</p>}
        <div className="client-import-actions"><Button variant="ghost" icon={RefreshCw} busy={detail.refreshing} disabled={detail.busy} onClick={() => void detail.refresh()}>刷新链接状态</Button></div>
      </div>
    </Panel>
    {action && <Modal title={action === "rotate" ? "轮换导入链接" : "撤销导入链接"} description={subscription ? `${subscription.templateName} · 订阅 #${subscription.id.slice(-8)}` : "影响账户下全部订阅的第三方链接和连接"} onClose={() => { if (!detail.busy) setAction(null); }}>
      <p>{action === "rotate" ? "将生成新链接，旧链接立即作废且无法恢复。" : "旧链接立即作废，之后可点击复制链接获取新链接。"}旧配置中的节点凭据将失效，正在使用的相关连接会被断开。</p>
      <p>请在所有受影响的第三方客户端中重新导入新链接。订阅额度、有效期及 HQIP 官方客户端不受影响。</p>
      <ErrorNotice message={detail.actionError} />
      {detail.uncertain && <Button variant="secondary" busy={detail.refreshing} onClick={() => void detail.refresh()}>刷新确认链接状态</Button>}
      <div className="modal-actions">
        <Button variant="secondary" disabled={detail.busy} onClick={() => setAction(null)}>取消</Button>
        <Button busy={detail.busy} disabled={!ready || (action !== "revoke" && !active)} onClick={() => void confirmAction()}>{action === "rotate" ? "确认轮换" : "确认撤销"}</Button>
      </div>
    </Modal>}
  </>;
}

export function ClientImports({ subscriptions }: { subscriptions: UserSubscription[] }) {
  const [params, setParams] = useSearchParams();
  const [clientID, setClientID] = useState<ImportClientID>("clash");
  const [busy, setBusy] = useState(false);
  const requested = params.get("subscription");
  const all = requested === null;
  const selected = selectedImportSubscription(subscriptions, requested);
  const client = importClients.find((item) => item.id === clientID)!;
  return <>
    <PageHeader eyebrow="CLIENT IMPORT & EXPORT" title="复制订阅链接" description="无需手动创建链接。默认复制全部有效订阅的合并链接，也可选择单份订阅。" />
    {!subscriptions.length ? <Panel title="选择订阅"><Empty icon={Package} title="还没有开通订阅" description="开通订阅后，可在这里为不同设备准备配置。" action={<Link className="button button-primary" to="/catalog">浏览订阅商城 <ArrowRight size={16} /></Link>} /></Panel> : <>
      <Panel title="1 · 选择订阅" subtitle="合并导入后，每份订阅仍保留独立的流量、期限和连接名额。">
        <div className="client-import-body">
          <Field label="选择订阅"><select aria-label="选择订阅" value={all ? "all" : selected?.id || ""} disabled={busy} onChange={(event) => {
            const next = new URLSearchParams(params);
            if (event.target.value === "all") next.delete("subscription");
            else next.set("subscription", event.target.value);
            setParams(next);
          }}>
            <option value="all">全部订阅（默认）</option>
            {!all && !selected && <option value="" disabled>请选择订阅</option>}
            {subscriptions.map((item) => <option key={item.id} value={item.id}>{item.templateName} · #{item.id.slice(-8)} · {subscriptionLabels[item.status] || item.status}</option>)}
          </select></Field>
          {!all && !selected && <Notice tone="warning">指定的订阅不存在或已不可访问，请重新选择；不会自动使用其他订阅。</Notice>}
        </div>
      </Panel>
      <Panel title="2 · 选择客户端" subtitle="按设备上安装的软件选择，复制链接后即可导入。">
        <div className="client-import-grid" role="group" aria-label="导入客户端">
          {importClients.map((item) => <button key={item.id} type="button" aria-label={item.name} aria-pressed={clientID === item.id} disabled={busy} onClick={() => setClientID(item.id)}>
            <strong>{item.name}</strong><span>{item.platforms}</span>
          </button>)}
        </div>
      </Panel>
      {(all || selected) && <ImportSubscription key={selected?.id || "all"} subscription={selected} subscriptions={subscriptions} client={client} onBusyChange={setBusy} />}
    </>}
  </>;
}
