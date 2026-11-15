import { useState, type FormEvent } from "react";
import { CalendarClock } from "lucide-react";
import { api } from "../api";
import { Button, ErrorNotice, Field, Modal, Notice, date } from "../components";
import { DateInput } from "../DatePicker";
import type { UserSubscription } from "../portal/types";
import { canAdjustSubscriptionExpiry, parseSubscriptionExpiry, subscriptionExpiryFields } from "../subscription-expiry";

export function SubscriptionExpiry({ userId, value, onClose, onSaved }: {
  userId: string; value: UserSubscription; onClose: () => void; onSaved: () => Promise<void>;
}) {
  // Freeze the reviewed subscription and revision while the page keeps polling.
  const [original] = useState(value);
  const [fields, setFields] = useState(() => subscriptionExpiryFields(value.expiresAt));
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const next = parseSubscriptionExpiry(fields.day, fields.time);
  const originalFields = subscriptionExpiryFields(original.expiresAt);
  const changed = fields.day !== originalFields.day || fields.time !== originalFields.time;
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!canAdjustSubscriptionExpiry(original.status, original.expiresAt)) {
      setError("订阅已到期或已移除，不能调整到期时间"); return;
    }
    if (!next || Date.parse(next) <= Date.now()) {
      setError("请选择晚于当前时间的到期时间"); return;
    }
    if (!changed) return;
    setBusy(true); setError("");
    try {
      await api(`/users/${userId}/subscriptions/${original.id}/expiry`, "PATCH", { expiresAt: next, expectedRevision: original.revision });
      await onSaved(); onClose();
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(false); }
  }
  return <Modal title="调整到期时间" description={`${original.templateName} · #${original.id.slice(-8)}`} onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack" onSubmit={submit}>
      <div className="form-grid">
        <Field label="新到期日期（北京时间）"><DateInput required value={fields.day} onChange={event => setFields(old => ({ ...old, day: event.target.value }))} /></Field>
        <Field label="新到期时间（北京时间）"><input type="time" step={60} required value={fields.time} onChange={event => setFields(old => ({ ...old, time: event.target.value }))} /></Field>
      </div>
      <dl className="subscription-expiry-preview"><div><dt>原到期时间</dt><dd>{date(original.expiresAt)}</dd></div><div><dt>调整后</dt><dd>{next ? date(next) : "请选择日期和时间"}</dd></div></dl>
      <Notice>仅调整这一份订阅的有效期，保留节点与已用流量。已到期订阅不能调整，新到期时间必须晚于当前时间。</Notice>
      {next && Date.parse(next) < Date.parse(original.expiresAt) && changed && <Notice tone="warning">本次将缩短有效期，连接将按新时间到期；已有连接通过租约更新，最长有 120 秒同步窗口。</Notice>}
      <ErrorNotice message={error} />
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" icon={CalendarClock} busy={busy} disabled={!next || !changed}>保存到期时间</Button></div>
    </form>
  </Modal>;
}
