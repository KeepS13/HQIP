import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ArrowRight, ArrowUpRight, Copy, Download, Link2, Package, RefreshCw } from "lucide-react";
import { Badge, Button, CopyButton, Empty, ErrorNotice, Field, Loading, Modal, Notice, PageHeader, Panel, date, useToast } from "../components";
import { userAPI } from "./api";
import { clientImportScheme, importClients, selectedImportSubscription, subscriptionImportURL, type ImportClient, type ImportClientID } from "./client-imports";
import { subscriptionLabels, type SubscriptionDetail, type UserSubscription } from "./types";
import "./client-imports.css";

type LinkAction = "create" | "rotate" | "revoke";

// This page owns its request order: an old poll must never restore a rotated token.
function useImportSubscription(id: string, onBusyChange: (busy: boolean) => void) {
  const [data, setData] = useState<SubscriptionDetail | null>(null);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const mounted = useRef(false), sequence = useRef(0), operation = useRef(0), mutating = useRef(false);
  const inFlight = useRef<number | null>(null);
  const path = `/subscriptions/${encodeURIComponent(id)}`;
  const refresh = useCallback(async () => {
    if (!mounted.current || mutating.current || inFlight.current !== null) return;
    const request = ++sequence.current;
    inFlight.current = request;
    setRefreshing(true);
    try {
      const result = await userAPI<SubscriptionDetail>(path);
      if (mounted.current && request === sequence.current) {
        setData(result);
        setError("");
        setUncertain(false);
      }
    } catch (e) {
      if (mounted.current && request === sequence.current) setError((e as Error).message);
    } finally {
      if (inFlight.current === request) inFlight.current = null;
      if (mounted.current && request === sequence.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [path]);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    const visible = () => { if (!document.hidden) void refresh(); };
    const timer = window.setInterval(visible, 5000);
    document.addEventListener("visibilitychange", visible);
    return () => {
      mounted.current = false;
      sequence.current++;
      inFlight.current = null;
      operation.current++;
      mutating.current = false;
      onBusyChange(false);
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh, onBusyChange]);

  async function changeLink(action: LinkAction) {
    if (!mounted.current || mutating.current) return false;
    const currentOperation = ++operation.current;
    sequence.current++;
    inFlight.current = null;
    mutating.current = true;
    setBusy(true);
    onBusyChange(true);
    setRefreshing(false);
    setActionError("");
    try {
      const result = await userAPI<{ url: string }>(`${path}/import-link${action === "create" ? "" : `/${action}`}`, "POST", {});
      if (!mounted.current || currentOperation !== operation.current) return false;
      setData((previous) => previous ? { ...previous, subscriptionUrl: action === "revoke" ? "" : result.url } : previous);
      setError("");
      setUncertain(false);
      return true;
    } catch (e) {
      if (mounted.current && currentOperation === operation.current) {
        setActionError(`${(e as Error).message}；请刷新确认当前链接后再操作。`);
        setUncertain(true);
        setData((previous) => previous ? { ...previous, subscriptionUrl: "" } : previous);
      }
      return false;
    } finally {
      if (mounted.current && currentOperation === operation.current) {
        mutating.current = false;
        setBusy(false);
        onBusyChange(false);
        void refresh();
      }
    }
  }
  return { data, error, actionError, loading, refreshing, busy, uncertain, refresh, changeLink, clearActionError: () => setActionError("") };
}

function ImportSubscription({ subscription, client, onBusyChange }: { subscription: UserSubscription; client: ImportClient; onBusyChange: (busy: boolean) => void }) {
  const detail = useImportSubscription(subscription.id, onBusyChange);
  const [action, setAction] = useState<LinkAction | null>(null);
  const toast = useToast();
  const mounted = useRef(false);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  if (!detail.data) return detail.loading ? <Loading /> : <ErrorNotice message={detail.error} retry={() => void detail.refresh()} />;
  const value = detail.data.subscription;
  const status = subscription.status === "active" ? value.status : subscription.status;
  const active = status === "active";
  const ready = !detail.busy && !detail.error && !detail.uncertain;
  const canUse = active && ready;
  const url = subscriptionImportURL(detail.data.subscriptionUrl, client.format);
  const downloadURL = subscriptionImportURL(detail.data.subscriptionUrl, client.format, true);
  const name = `HQIP ${value.templateName} ${value.id.slice(-6)}`;
  const scheme = clientImportScheme(client.id, url, name);
  async function confirmAction() {
    if (!action) return;
    const completed = await detail.changeLink(action);
    if (!mounted.current || !completed) return;
    setAction(null);
    toast(action === "revoke" ? "这份订阅的导入链接已撤销" : action === "rotate" ? "链接已轮换，请重新导入客户端" : "导入链接已准备好");
  }
  function openAction(next: LinkAction) {
    detail.clearActionError();
    setAction(next);
  }
  return <>
    <Panel title={value.templateName} subtitle={`订阅 #${value.id.slice(-8)} · 有效至 ${date(value.expiresAt)}`} action={<Badge tone={active ? "success" : "warning"}>{subscriptionLabels[status] || "暂时不可用"}</Badge>}>
      <div className="client-import-body">
        <div className="client-import-context">
          <span>当前客户端：<strong>{client.name}</strong></span>
          <Link to={`/subscriptions/${encodeURIComponent(value.id)}`}>查看订阅详情 <ArrowRight size={14} /></Link>
        </div>
        <ErrorNotice message={detail.error ? `${detail.error}；请刷新后再导入或导出。` : ""} retry={() => void detail.refresh()} />
        <ErrorNotice message={action ? "" : detail.actionError} />
        {!active && <Notice tone="warning">这份订阅{subscriptionLabels[status] || "暂时不可用"}，不能创建、轮换、导入或导出配置。已有链接仍可撤销。</Notice>}
        {url ? <>
          <Field label="订阅链接">
            <input type="password" readOnly value={detail.busy ? "" : url} aria-label="此订阅的导入链接" />
          </Field>
          <div className="client-import-actions">
            {canUse ? <CopyButton key={url} value={url} label="复制链接" /> : <Button variant="secondary" icon={Copy} disabled>复制链接</Button>}
            {scheme && (canUse ? <a className="button button-primary" href={scheme}>一键导入 <ArrowUpRight size={16} /></a> : <Button disabled>一键导入</Button>)}
            {canUse ? <a className="button button-secondary" href={downloadURL}>导出配置 <Download size={16} /></a> : <Button variant="secondary" icon={Download} disabled>导出配置</Button>}
          </div>
          <div className="client-import-actions client-import-management">
            <Button variant="secondary" icon={RefreshCw} disabled={!canUse} onClick={() => openAction("rotate")}>轮换链接</Button>
            <Button variant="ghost" disabled={!ready} onClick={() => openAction("revoke")}>撤销导入链接</Button>
          </div>
        </> : <Empty icon={Link2} title={detail.uncertain ? "正在确认导入链接" : "尚未创建导入链接"} description={detail.uncertain ? "本次操作结果尚未确认，刷新成功后可继续。" : "为当前这份订阅创建独立链接，再复制、导入或导出客户端配置。"} action={<Button icon={Link2} disabled={!canUse} onClick={() => openAction("create")}>创建导入链接</Button>} />}
        <p className="client-import-help">{client.help}</p>
        <Notice>链接和配置文件包含连接凭据，请妥善保管。所有客户端格式共享这份订阅的额度及连接名额；轮换或撤销会同时影响它们，其他订阅不受影响。</Notice>
        <div className="client-import-actions"><Button variant="ghost" icon={RefreshCw} busy={detail.refreshing} disabled={detail.busy} onClick={() => void detail.refresh()}>刷新链接状态</Button></div>
      </div>
    </Panel>
    {action && <Modal title={action === "create" ? "创建导入链接" : action === "rotate" ? "轮换导入链接" : "撤销导入链接"} description={`${value.templateName} · 订阅 #${value.id.slice(-8)}`} onClose={() => { if (!detail.busy) setAction(null); }}>
      <p>{action === "create" ? "将为这份订阅创建第三方客户端入口，链接仅使用它自己的流量和连接名额。" : "旧链接及关联连接凭据将失效，流量包继续保留。"}</p>
      {action === "rotate" && <p>完成后请在第三方客户端重新导入新链接。</p>}
      <ErrorNotice message={detail.actionError} />
      <div className="modal-actions">
        <Button variant="secondary" disabled={detail.busy} onClick={() => setAction(null)}>取消</Button>
        <Button busy={detail.busy} disabled={!ready || (action !== "revoke" && !active)} onClick={() => void confirmAction()}>{action === "create" ? "确认创建" : action === "rotate" ? "确认轮换" : "确认撤销"}</Button>
      </div>
    </Modal>}
  </>;
}

export function ClientImports({ subscriptions }: { subscriptions: UserSubscription[] }) {
  const [params, setParams] = useSearchParams();
  const [clientID, setClientID] = useState<ImportClientID>("clash");
  const [busy, setBusy] = useState(false);
  const requested = params.get("subscription");
  const selected = selectedImportSubscription(subscriptions, requested);
  const client = importClients.find((item) => item.id === clientID)!;
  useEffect(() => {
    if (requested === null && selected) {
      const next = new URLSearchParams(params);
      next.set("subscription", selected.id);
      setParams(next, { replace: true });
    }
  }, [params, requested, selected, setParams]);
  return <>
    <PageHeader eyebrow="CLIENT IMPORT & EXPORT" title="订阅导入导出" description="选择一份订阅和客户端，复制订阅链接、一键导入或下载配置文件。" />
    {!subscriptions.length ? <Panel title="选择订阅"><Empty icon={Package} title="还没有开通订阅" description="开通订阅后，可在这里为不同设备准备配置。" action={<Link className="button button-primary" to="/catalog">浏览订阅商城 <ArrowRight size={16} /></Link>} /></Panel> : <>
      <Panel title="1 · 选择订阅" subtitle="每份订阅有独立的流量、期限和连接名额。">
        <div className="client-import-body">
          <Field label="选择订阅"><select aria-label="选择订阅" value={selected?.id || ""} disabled={busy} onChange={(event) => {
            const next = new URLSearchParams(params);
            if (event.target.value) next.set("subscription", event.target.value);
            else next.delete("subscription");
            setParams(next);
          }}>
            <option value="" disabled>请选择订阅</option>
            {subscriptions.map((item) => <option key={item.id} value={item.id}>{item.templateName} · #{item.id.slice(-8)} · {subscriptionLabels[item.status] || item.status}</option>)}
          </select></Field>
          {requested !== null && !selected && <Notice tone="warning">指定的订阅不存在或已不可访问，请重新选择；不会自动使用其他订阅。</Notice>}
        </div>
      </Panel>
      <Panel title="2 · 选择客户端" subtitle="可为其他设备准备链接或配置，按设备上安装的软件选择。">
        <div className="client-import-grid" role="group" aria-label="导入客户端">
          {importClients.map((item) => <button key={item.id} type="button" aria-label={item.name} aria-pressed={clientID === item.id} disabled={busy} onClick={() => setClientID(item.id)}>
            <strong>{item.name}</strong><span>{item.platforms}</span>
          </button>)}
        </div>
      </Panel>
      {selected ? <ImportSubscription key={selected.id} subscription={selected} client={client} onBusyChange={setBusy} /> : <Notice>先选择要使用的订阅，再创建或管理导入链接。</Notice>}
    </>}
  </>;
}
