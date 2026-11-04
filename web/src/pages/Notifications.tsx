import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ArrowUpRight, BellRing, CheckCircle2, Clock3, History, KeyRound, RefreshCw, Save, Send, Settings2, ShieldCheck, SlidersHorizontal, Trash2 } from "lucide-react";
import { api, usePolling } from "../api";
import { Badge, Button, Empty, ErrorNotice, Field, Loading, Modal, Notice, PageHeader, Panel, RefreshButton, date, useToast } from "../components";
import "./notifications.css";

type Category = "system" | "capacity" | "certificate" | "package" | "recovery";
type Categories = Record<Category, boolean>;
type Settings = {
  enabled: boolean;
  webhookConfigured: boolean;
  webhookMasked: string;
  secretConfigured: boolean;
  categories: Categories;
  revision: number;
  lastSuccessAt: string | null;
  lastError: string;
  queueDepth: number;
};
type Delivery = {
  id: string;
  category: Category | "test" | "summary";
  level: string;
  title: string;
  message: string;
  detailPath: string;
  status: string;
  attempts: number;
  createdAt: string;
  nextAttempt: string | null;
  lastError: string;
  canRetry: boolean;
};
type DeliveryPage = { items: Delivery[]; total: number; page: number; pageSize: number };
type Draft = { enabled: boolean; categories: Categories; revision: number; webhook: string; secret: string; clearCredentials: boolean };
type Tab = "robot" | "categories" | "history";

const categories: { key: Category; title: string; description: string; thresholds: string }[] = [
  { key: "system", title: "系统告警", description: "及时发现节点、部署任务与磁盘异常。", thresholds: "心跳或内核异常持续 60 秒；任务最终失败；可用磁盘不足 512 MiB，同服务器合并。" },
  { key: "capacity", title: "服务器与节点容量", description: "监控 VPS 和 IP 节点的月度流量。", thresholds: "北京时间自然月流量达到 80%、100%；仅提醒，不限速、不断网。无限额度不触发。" },
  { key: "certificate", title: "域名与证书", description: "提前关注节点证书有效期与续期结果。", thresholds: "到期前 14、7、3 天及已过期；签发、续期或节点应用失败。" },
  { key: "package", title: "用户套餐提醒", description: "跟进用户本次开通的用量与到期时间。", thresholds: "累计已确认流量达到 80%、90%、100%；到期前 7、3、1 天及已到期。待结算预留单独展示。" },
  { key: "recovery", title: "恢复通知", description: "让已通知的运行异常形成完整记录。", thresholds: "异常恢复后发送一次；节点健康需稳定 30 秒。未发送过的异常不单独通知恢复。" },
];
const categoryNames: Record<string, string> = Object.fromEntries(categories.map(c => [c.key, c.title]));
const statuses: Record<string, { label: string; tone: string }> = {
  pending: { label: "等待投递", tone: "info" },
  sending: { label: "发送中", tone: "info" },
  retry: { label: "等待重试", tone: "warning" },
  sent: { label: "飞书已接收", tone: "success" },
  failed: { label: "投递失败", tone: "danger" },
  cancelled: { label: "已取消", tone: "muted" },
};
const tabs: { key: Tab; title: string; icon: typeof Settings2 }[] = [
  { key: "robot", title: "机器人设置", icon: Settings2 },
  { key: "categories", title: "通知类型", icon: SlidersHorizontal },
  { key: "history", title: "投递记录", icon: History },
];
function makeDraft(value: Settings): Draft {
  return { enabled: value.enabled, categories: { ...value.categories }, revision: value.revision, webhook: "", secret: "", clearCredentials: false };
}
function displayDate(value: string | null | undefined) {
  return value && Date.parse(value) > 0 ? date(value) : "尚无记录";
}
function detailLink(path: string) {
  return /^\/admin\/(?:nodes|servers|users)\/[A-Za-z0-9_-]+$/.test(path) || path === "/admin/domains" || path === "/admin/notifications"
    ? path.slice("/admin".length) : null;
}

export function Notifications() {
  const settings = usePolling<Settings>("/settings/feishu");
  const [page, setPage] = useState(1);
  const deliveries = usePolling<DeliveryPage>(`/notifications?page=${page}&pageSize=20`);
  const [tab, setTab] = useState<Tab>("robot");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [dirty, setDirty] = useState(false);
  const [replaceWebhook, setReplaceWebhook] = useState(false);
  const [replaceSecret, setReplaceSecret] = useState(false);
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const [retrying, setRetrying] = useState("");
  const [actionError, setActionError] = useState("");
  const [confirmation, setConfirmation] = useState<"clear" | "discard" | null>(null);
  const [test, setTest] = useState<Delivery | null>(null);
  const toast = useToast();
  const data = settings.data;
  useEffect(() => {
    if (data && !dirty) {
      setDraft(previous => previous && data.revision < previous.revision ? previous : makeDraft(data));
    }
  }, [data, dirty]);
  useEffect(() => {
    if (!test) return;
    const latest = deliveries.data?.items.find(item => item.id === test.id);
    if (latest && (latest.status !== test.status || latest.attempts !== test.attempts)) setTest(latest);
  }, [deliveries.data, test]);
  const changedElsewhere = Boolean(data && draft && data.revision > draft.revision);
  const credentialsReady = Boolean(draft && !draft.clearCredentials && (draft.webhook.trim() || data?.webhookConfigured) && (draft.secret.trim() || data?.secretConfigured));
  const canSave = Boolean(draft && dirty && !changedElsewhere && (!draft.enabled || credentialsReady));
  function edit(patch: Partial<Draft>) {
    setDraft(current => current ? { ...current, ...patch } : current);
    setDirty(true);
    setActionError("");
  }
  async function refresh() {
    await Promise.all([settings.refresh(), deliveries.refresh()]);
  }
  async function discard() {
    setBusy(true);
    setActionError("");
    try {
      const latest = await api<Settings>("/settings/feishu");
      settings.setData(latest);
      setDraft(makeDraft(latest));
      setDirty(false);
      setReplaceWebhook(false);
      setReplaceSecret(false);
      setConfirmation(null);
    } catch (error) {
      setActionError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!draft || !canSave) return;
    setBusy(true);
    setActionError("");
    try {
      const saved = await api<Settings>("/settings/feishu", "PUT", {
        enabled: draft.enabled,
        categories: draft.categories,
        expectedRevision: draft.revision,
        ...(draft.clearCredentials ? { clearCredentials: true } : {
          ...(draft.webhook.trim() ? { webhook: draft.webhook.trim() } : {}),
          ...(draft.secret.trim() ? { secret: draft.secret.trim() } : {}),
        }),
      });
      settings.setData(saved);
      setDraft(makeDraft(saved));
      setDirty(false);
      setReplaceWebhook(false);
      setReplaceSecret(false);
      setTest(null);
      toast("飞书通知设置已保存");
      await deliveries.refresh();
    } catch (error) {
      setActionError((error as Error).message);
      await settings.refresh();
    } finally {
      setBusy(false);
    }
  }
  async function sendTest() {
    setTesting(true);
    setActionError("");
    try {
      const record = await api<Delivery>("/settings/feishu/test", "POST", {});
      setTest(record);
      setPage(1);
      toast("测试消息已排队，正在等待飞书响应");
      await refresh();
    } catch (error) {
      setActionError((error as Error).message);
    } finally {
      setTesting(false);
    }
  }
  async function retry(record: Delivery) {
    setRetrying(record.id);
    setActionError("");
    try {
      await api(`/notifications/${encodeURIComponent(record.id)}/retry`, "POST", {});
      toast("已重新排队；投递前会复核事件是否仍然有效");
      await refresh();
    } catch (error) {
      setActionError((error as Error).message);
    } finally {
      setRetrying("");
    }
  }
  return <>
    <PageHeader title="告警与通知" eyebrow="ALERTS & NOTIFICATIONS" description="把系统异常、容量变化和到期提醒，送达你的飞书群。" actions={<RefreshButton refresh={refresh} />} />
    <ErrorNotice message={settings.error ? `设置刷新失败，当前展示最近一次数据：${settings.error}` : ""} retry={() => void settings.refresh()} />
    {settings.loading || !draft || !data ? settings.error ? null : <Loading /> : <>
      <section className="notification-overview" aria-label="飞书通知状态">
        <div className="notification-overview-brand"><span className="notification-icon"><BellRing size={25} /></span><div><span className="eyebrow">FEISHU BOT</span><h2>你的网络，有消息了</h2><p>固定中文通知 · 签名保护 · 可追溯投递</p></div></div>
        <dl className="notification-overview-stats">
          <div><dt>自动通知</dt><dd><Badge tone={data.enabled ? "success" : "muted"}>{data.enabled ? "已启用" : "已停用"}</Badge></dd></div>
          <div><dt>等待投递</dt><dd>{data.queueDepth}<small> 条</small></dd></div>
          <div><dt>最近成功接收 · 北京时间</dt><dd className="notification-last-time">{displayDate(data.lastSuccessAt)}</dd></div>
        </dl>
      </section>
      <div className="notification-tabs" role="tablist" aria-label="通知管理">
        {tabs.map(({ key, title, icon: Icon }, index) => <button type="button" key={key} id={`notification-tab-${key}`} role="tab" tabIndex={tab === key ? 0 : -1} aria-selected={tab === key} aria-controls={`notification-panel-${key}`} className={tab === key ? "active" : ""} onClick={() => setTab(key)} onKeyDown={event => {
          const next = event.key === "ArrowRight" ? (index + 1) % tabs.length : event.key === "ArrowLeft" ? (index + tabs.length - 1) % tabs.length : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : null;
          if (next === null) return;
          event.preventDefault(); setTab(tabs[next].key); document.getElementById(`notification-tab-${tabs[next].key}`)?.focus();
        }}><Icon size={16} />{title}{key === "history" && !!data.queueDepth && <span>{data.queueDepth}</span>}</button>)}
      </div>
      {changedElsewhere && <Notice tone="warning">设置已被其他页面修改。你的输入仍保留；请先“重新读取设置”，核对最新内容后再编辑，避免覆盖他人的修改。<button type="button" className="text-button" onClick={() => setConfirmation("discard")}>重新读取设置</button></Notice>}
      <ErrorNotice message={actionError} />
      {tab !== "history" ? <form onSubmit={save} className="notification-form">
        <fieldset disabled={busy}>
          <div role="tabpanel" id={`notification-panel-${tab}`} aria-labelledby={`notification-tab-${tab}`}>
            {tab === "robot" ? <div className="notification-settings-grid">
              <Panel title="飞书群机器人" subtitle="Webhook 和签名密钥仅在控制服务内加密保存" action={<Badge tone={data.webhookConfigured && data.secretConfigured ? "success" : "muted"}>{data.webhookConfigured && data.secretConfigured ? "凭据已配置" : "尚未完成配置"}</Badge>}>
                <div className="notification-settings-body form-stack">
                  <label className="notification-master"><div><strong>启用自动通知</strong><small>保存后生效；首次启用将汇总当前异常。</small></div><input type="checkbox" role="switch" checked={draft.enabled} onChange={e => edit({ enabled: e.target.checked, clearCredentials: false })} /><span className="notification-switch" aria-hidden="true" /></label>
                  {draft.clearCredentials ? <Notice tone="warning">保存后将清除 Webhook 和签名密钥，并取消旧待发任务。<button className="text-button" type="button" onClick={() => edit({ clearCredentials: false })}>撤销清除</button></Notice> : <>
                    <div className="notification-credential">
                      <div className="notification-credential-heading"><strong>Webhook 地址</strong>{data.webhookConfigured && <Button type="button" variant="ghost" onClick={() => { setReplaceWebhook(!replaceWebhook); if (replaceWebhook) edit({ webhook: "" }); }}>{replaceWebhook ? "取消替换" : "替换地址"}</Button>}</div>
                      {data.webhookConfigured && !replaceWebhook ? <div className="notification-secret-mask"><ShieldCheck size={16} /><span>{data.webhookMasked || "Webhook 已加密保存"}</span></div> : <Field label={data.webhookConfigured ? "新的 Webhook 地址" : "飞书机器人 Webhook"} hint="仅接受 open.feishu.cn 的自定义机器人 HTTPS 地址。"><input type="password" autoComplete="new-password" spellCheck={false} value={draft.webhook} onChange={e => edit({ webhook: e.target.value })} placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/…" maxLength={1024} /></Field>}
                    </div>
                    <div className="notification-credential">
                      <div className="notification-credential-heading"><strong>签名密钥</strong>{data.secretConfigured && <Button type="button" variant="ghost" onClick={() => { setReplaceSecret(!replaceSecret); if (replaceSecret) edit({ secret: "" }); }}>{replaceSecret ? "取消替换" : "替换密钥"}</Button>}</div>
                      {data.secretConfigured && !replaceSecret ? <div className="notification-secret-mask"><KeyRound size={16} /><span>••••••••••••••••</span><small>已加密保存，不可查看</small></div> : <Field label={data.secretConfigured ? "新的签名密钥" : "机器人签名密钥"} hint="在飞书机器人安全设置中启用“签名校验”，复制生成的密钥。"><input type="password" autoComplete="new-password" spellCheck={false} value={draft.secret} onChange={e => edit({ secret: e.target.value })} placeholder="输入签名密钥" maxLength={1024} /></Field>}
                    </div>
                  </>}
                  {draft.enabled && !credentialsReady && <Notice tone="warning">启用通知前，请同时填写 Webhook 地址和签名密钥。</Notice>}
                  {(data.webhookConfigured || data.secretConfigured) && !draft.clearCredentials && <div><Button type="button" variant="ghost" icon={Trash2} onClick={() => setConfirmation("clear")}>清除已保存的凭据</Button></div>}
                </div>
              </Panel>
              <div className="notification-side">
                <Panel title="连接测试" subtitle="发送一条固定测试消息，确认配置有效">
                  <div className="notification-settings-body form-stack">
                    <p className="notification-help-copy">测试使用已保存的设置。自动通知停用时也可测试，每 30 秒最多一次。</p>
                    <Button type="button" icon={Send} variant="secondary" busy={testing} disabled={dirty || !data.webhookConfigured || !data.secretConfigured} onClick={() => void sendTest()}>发送测试消息</Button>
                    {dirty && <small className="muted">请先保存或放弃修改，再发送测试。</small>}
                    {test && <div className={`notification-test-result ${test.status === "failed" ? "failed" : ""}`} role="status"><Badge tone={statuses[test.status]?.tone || "info"}>{statuses[test.status]?.label || "已排队"}</Badge><p>{test.status === "sent" ? "飞书已接收测试消息，请在群内查看。" : test.lastError || "测试消息已进入投递队列，结果会自动更新。"}</p><small>{displayDate(test.createdAt)} · 北京时间</small></div>}
                    {!!data.lastError && <ErrorNotice message={`最近投递错误：${data.lastError}`} />}
                  </div>
                </Panel>
                <aside className="notification-setup"><span><ShieldCheck size={18} />三步完成配置</span><ol><li>在飞书群内添加“自定义机器人”，复制 Webhook。</li><li>开启机器人的“签名校验”，将密钥填入左侧并保存。</li><li>发送测试消息，确认后启用需要的通知类型。</li></ol><a href="https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot" target="_blank" rel="noreferrer">查看飞书配置指南 <ArrowUpRight size={14} /></a></aside>
              </div>
            </div> : <Panel title="选择通知类型" subtitle="阈值固定，每类通知独立开关；修改后保存生效" action={<span className="notification-category-count">已选择 {Object.values(draft.categories).filter(Boolean).length} / 5</span>}>
              <div className="notification-category-list">{categories.map(category => <label key={category.key} className={`notification-category ${draft.categories[category.key] ? "selected" : ""}`}><input type="checkbox" checked={draft.categories[category.key]} onChange={e => edit({ categories: { ...draft.categories, [category.key]: e.target.checked } })} /><span><strong>{category.title}</strong><p>{category.description}</p><small>{category.thresholds}</small></span><CheckCircle2 size={19} aria-hidden="true" /></label>)}</div>
              <div className="notification-category-note"><Clock3 size={17} /><p>持续运行异常每 6 小时最多重复一次；同周期阈值不重复发送。控制服务完全停机期间无法通知，恢复后汇总当前状态。</p></div>
            </Panel>}
          </div>
          <div className="notification-save-bar"><span>{dirty ? "有未保存的修改" : `设置已保存 · 版本 ${draft.revision}`}</span><div><Button type="button" variant="ghost" disabled={!dirty} onClick={() => setConfirmation("discard")}>放弃修改</Button><Button type="submit" icon={Save} busy={busy} disabled={!canSave}>保存设置</Button></div></div>
        </fieldset>
      </form> : <div role="tabpanel" id="notification-panel-history" aria-labelledby="notification-tab-history">
        {dirty && <Notice tone="warning">机器人设置有未保存的修改，已为你保留；发送仍使用当前已保存的设置。</Notice>}
        <ErrorNotice message={deliveries.error ? `投递记录刷新失败，保留最近一次结果：${deliveries.error}` : ""} retry={() => void deliveries.refresh()} />
        <Panel title="投递记录" subtitle="保留最近 90 天 · “飞书已接收”表示接口接收成功，不代表群成员已读" action={<span className="notification-category-count">共 {deliveries.data?.total || 0} 条</span>}>
          {deliveries.loading ? <Loading /> : deliveries.data?.items.length ? <div className="notification-deliveries">{deliveries.data.items.map(record => {
            const status = statuses[record.status] || { label: record.status, tone: "muted" };
            const link = detailLink(record.detailPath);
            return <article className="notification-delivery" key={record.id}>
              <div className="notification-delivery-heading"><div><span className={`notification-level ${record.level === "critical" || record.level === "error" ? "danger" : record.level === "warning" ? "warning" : "info"}`}>{record.level === "critical" || record.level === "error" ? "严重" : record.level === "warning" ? "警告" : "通知"}</span><span className="notification-delivery-category">{categoryNames[record.category] || (record.category === "test" ? "连接测试" : "状态汇总")}</span><time>{displayDate(record.createdAt)}</time></div><Badge tone={status.tone}>{status.label}</Badge></div>
              <h3>{record.title}</h3><p className="notification-delivery-message">{record.message}</p>
              {record.lastError && <p className="notification-delivery-error">{record.lastError}</p>}
              <div className="notification-delivery-footer"><span className="notification-event-id">事件 {record.id}</span><span>已尝试 {record.attempts} 次</span>{record.nextAttempt && Date.parse(record.nextAttempt) > 0 && <span>下次 {date(record.nextAttempt)}</span>}<div>{link && <Link to={link}>查看详情 <ArrowUpRight size={13} /></Link>}{record.canRetry && <Button type="button" variant="secondary" icon={RefreshCw} busy={retrying === record.id} disabled={!!retrying} onClick={() => void retry(record)}>重试投递</Button>}</div></div>
            </article>;
          })}</div> : <Empty icon={BellRing} title="还没有投递记录" description="保存机器人配置并发送测试消息，或启用通知后等待系统事件。" action={<Button type="button" variant="secondary" onClick={() => setTab("robot")}>前往机器人设置</Button>} />}
          {Boolean(deliveries.data?.total) && <div className="notification-pagination"><span>第 {page} / {Math.max(1, Math.ceil((deliveries.data?.total || 0) / 20))} 页 · 北京时间</span><div><Button type="button" variant="secondary" disabled={page <= 1 || deliveries.loading} onClick={() => setPage(page - 1)}>上一页</Button><Button type="button" variant="secondary" disabled={page * 20 >= (deliveries.data?.total || 0) || deliveries.loading} onClick={() => setPage(page + 1)}>下一页</Button></div></div>}
        </Panel>
        <p className="notification-footnote">网络超时重试可能产生重复消息，可通过事件编号识别。过时事件、停用或更换地址前的待发任务会取消。</p>
      </div>}
    </>}
    {confirmation && <Modal title={confirmation === "clear" ? "清除机器人凭据" : "放弃修改并读取最新设置"} description={confirmation === "clear" ? "将停用自动通知，并清除已保存的 Webhook 和签名密钥。历史投递记录保留。" : "尚未保存的地址、密钥和通知类型修改将被丢弃。"} onClose={() => !busy && setConfirmation(null)}>
      {confirmation === "clear" && <Notice tone="warning">确认后仍需点击“保存设置”才能生效。</Notice>}
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={() => setConfirmation(null)}>取消</Button><Button type="button" variant={confirmation === "clear" ? "danger" : "primary"} busy={busy} onClick={() => {
        if (confirmation === "discard") { void discard(); return; }
        edit({ enabled: false, clearCredentials: true, webhook: "", secret: "" });
        setReplaceWebhook(false); setReplaceSecret(false); setConfirmation(null);
      }}>{confirmation === "clear" ? "确认清除" : "重新读取设置"}</Button></div>
    </Modal>}
  </>;
}
