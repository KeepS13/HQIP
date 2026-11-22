import { useRef, useState, type FormEvent } from "react";
import { Plus } from "lucide-react";
import { api } from "../api";
import { Button, ErrorNotice, Field, Modal, Notice } from "../components";
import type { SubscriptionTemplate, UserProfile } from "../portal/types";
import { clearPendingGrant, grantWasRejected, readPendingGrant, savePendingGrant, sendSubscriptionGrant, type PendingGrant } from "../subscription-grant";
import { TemplateFacts } from "./SubscriptionTemplates";

export function GrantSubscription({ user, templates, onClose, onDone }: {
  user: UserProfile;
  templates: SubscriptionTemplate[];
  onClose: () => void;
  onDone: () => void;
}) {
  const userId = user.user.id;
  const [initial] = useState(() => {
    try { return { pending: readPendingGrant(userId), error: "" }; }
    catch (error) { return { pending: null, error: (error as Error).message }; }
  });
  const [pending, setPending] = useState<PendingGrant | null>(initial.pending);
  // Background polling must not replace the terms the administrator reviewed.
  const [catalog, setCatalog] = useState(templates);
  const [account, setAccount] = useState(user.user);
  const [templateId, setTemplate] = useState(initial.pending?.body.templateId || "");
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const [error, setError] = useState(initial.error);
  const [needsReload, setNeedsReload] = useState(false);
  const selected = pending?.template || catalog.find(t => t.id === templateId);

  async function reload() {
    setBusy(true);
    setError("");
    try {
      const [freshUser, freshTemplates] = await Promise.all([
        api<UserProfile>(`/users/${encodeURIComponent(userId)}`),
        api<SubscriptionTemplate[]>("/subscription-templates"),
      ]);
      setAccount(freshUser.user);
      setCatalog(freshTemplates);
      setNeedsReload(false);
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(false); }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current || busy || initial.error || needsReload || !selected ||
      (!pending && (!selected.enabled || !account.enabled))) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    const operation = pending || {
      userId, template: selected,
      body: { templateId: selected.id, templateRevision: selected.revision, expectedRevision: account.revision, requestId: crypto.randomUUID() },
    };
    let sent = false;
    try {
      if (!pending) savePendingGrant(operation);
      setPending(operation);
      sent = true;
      await sendSubscriptionGrant(operation);
    } catch (error) {
      if (grantWasRejected(error)) {
        try {
          clearPendingGrant(userId);
          setPending(null);
          setNeedsReload(true);
          setError(`${(error as Error).message}。请重新加载并确认后再开通。`);
        } catch {
          setError("请求已被拒绝，但浏览器未能清理记录，请重试本次开通以恢复。");
        }
      } else {
        setError(sent ? `${(error as Error).message}。结果尚未确认，请重试本次开通；不会重复赠送。` : (error as Error).message);
      }
      submitting.current = false;
      setBusy(false);
      return;
    }
    try { clearPendingGrant(userId); }
    catch {
      setError("订阅已开通，但浏览器未能清理恢复记录。请重试本次开通以确认已有结果。");
      submitting.current = false;
      setBusy(false);
      return;
    }
    // Receipt success is final. Profile refresh is presentation work and must
    // not send this completed gift back through the failure/retry path.
    onClose();
    onDone();
  }

  return (
    <Modal title="为用户赠送订阅" description={user.user.email} onClose={() => { if (!busy) onClose(); }}>
      <form className="form-stack" onSubmit={submit}>
        <Field label="订阅模板">
          <select required aria-label="订阅模板" value={templateId} disabled={busy || !!pending || needsReload || !!initial.error} onChange={event => setTemplate(event.target.value)}>
            <option value="">选择订阅模板</option>
            {(pending ? [pending.template] : catalog.filter(t => t.enabled)).map(t => (
              <option key={t.id} value={t.id}>{t.name}{!t.listed ? "（仅后台分配）" : ""}</option>
            ))}
          </select>
        </Field>
        {selected && <TemplateFacts value={selected} />}
        <Notice>本次为管理员赠送，不收取费用、不生成付款订单。新增一份独立订阅，从现在开始计时，已有订阅继续有效。</Notice>
        {pending && <Notice>有一笔开通待确认。请在当前标签页重试本次开通；关闭弹窗或刷新页面后仍可继续确认。</Notice>}
        {!pending && !account.enabled && <ErrorNotice message="账户已停用，请先启用账户再开通。" />}
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button type="button" variant="secondary" disabled={busy} onClick={onClose}>{pending ? "稍后确认" : "取消"}</Button>
          {needsReload ? <Button type="button" busy={busy} onClick={() => void reload()}>重新加载</Button> : (
            <Button type="submit" busy={busy} disabled={!!initial.error || !selected || (!pending && (!selected.enabled || !account.enabled))} icon={Plus}>
              {pending ? "重试本次开通" : "确认开通"}
            </Button>
          )}
        </div>
      </form>
    </Modal>
  );
}
