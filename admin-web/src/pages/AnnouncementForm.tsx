import { useState, type FormEvent } from "react";
import { Send } from "lucide-react";
import { ApiError, api } from "../api";
import { Button, ErrorNotice, Field, Modal, Notice } from "../components";
import type { PortalUser } from "../portal/types";
import type { AnnouncementInput } from "../../../web-shared/announcements";
import { AnnouncementAudience } from "./AnnouncementAudience";

const draftKey = "hqip:announcement-publication";
function readDraft(): AnnouncementInput | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(draftKey) || "null");
    return value && typeof value.id === "string" && typeof value.title === "string" && typeof value.body === "string" &&
      ["always", "until_acknowledged"].includes(value.mode) && Array.isArray(value.userIds) && value.userIds.every((id: unknown) => typeof id === "string") ? value : null;
  } catch { return null; }
}

/** Persist the immutable attempted request before sending. An uncertain result
 * can be retried after closing/reopening without sending a duplicate notice. */
export function AnnouncementForm({ users, onClose, onPublished }: {
  users: PortalUser[]; onClose: () => void; onPublished: () => void;
}) {
  const [pending, setPending] = useState(readDraft);
  const [title, setTitle] = useState(pending?.title || ""), [body, setBody] = useState(pending?.body || "");
  const [mode, setMode] = useState<AnnouncementInput["mode"]>(pending?.mode || "until_acknowledged");
  const [selected, setSelected] = useState<string[]>(pending?.userIds || []);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const locked = busy || !!pending;
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setBusy(true); setError("");
    try {
      const input: AnnouncementInput = pending || { id: crypto.randomUUID(), title: title.trim(), body: body.trim(), mode, userIds: selected };
      if (!input.title || !input.body || !input.userIds.length || input.userIds.length > 5000) throw new Error("请填写标题、正文，并选择 1–5000 位接收用户");
      sessionStorage.setItem(draftKey, JSON.stringify(input));
      setPending(input);
      await api("/announcements", "POST", input, AbortSignal.timeout(15000));
      sessionStorage.removeItem(draftKey);
      onPublished();
    } catch (e) {
      if (e instanceof ApiError && [400, 409].includes(e.status)) {
        sessionStorage.removeItem(draftKey); setPending(null);
      }
      setError((e as Error).message);
    } finally { setBusy(false); }
  }
  return <Modal wide title="发布用户公告" description="指定接收用户，公告将在用户下次登录或刷新网站时弹出。" onClose={() => { if (!busy) onClose(); }}>
    <form className="form-stack announcement-form" onSubmit={submit}>
      {pending && <Notice>正在确认本次发布结果，内容已锁定。重试会恢复同一条公告。</Notice>}
      <Field label="公告标题"><input autoFocus required maxLength={120} value={title} disabled={locked} onChange={e => setTitle(e.target.value)} placeholder="例如：本周服务维护通知" /></Field>
      <Field label="公告正文"><textarea required rows={6} maxLength={10000} value={body} disabled={locked} onChange={e => setBody(e.target.value)} placeholder="输入要通知用户的内容，支持换行。" /></Field>
      <Field label="弹窗展示方式"><select value={mode} disabled={locked} onChange={e => setMode(e.target.value as AnnouncementInput["mode"])}>
        <option value="until_acknowledged">点击“我已知晓”后不再弹出</option>
        <option value="always">每次登录或刷新都弹出</option>
      </select></Field>
      <AnnouncementAudience users={users} selected={selected} onChange={setSelected} disabled={locked} />
      <p className="muted">仅发送给当前选中的用户；关闭或查看下一条不会视为已知晓。</p>
      <ErrorNotice message={error} />
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" icon={Send} busy={busy} disabled={!title.trim() || !body.trim() || !selected.length || selected.length > 5000}>{pending ? "重试本次发布" : `发布给 ${selected.length} 位用户`}</Button></div>
    </form>
  </Modal>;
}
