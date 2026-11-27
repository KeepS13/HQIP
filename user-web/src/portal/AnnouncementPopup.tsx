import { useEffect, useRef, useState } from "react";
import { ArrowRight, Check, Megaphone } from "lucide-react";
import { Button, ErrorNotice, Modal, date } from "../components";
import { userAPI } from "./api";
import type { AnnouncementPage } from "../../../web-shared/announcements";
import "./announcements.css";

/** Mounted once per signed-in account, outside routes. Closing only dismisses
 * this document's queue; only a successful acknowledge request persists state. */
export function AnnouncementPopup() {
  const [page, setPage] = useState<AnnouncementPage>({ items: [], nextCursor: "" });
  const [index, setIndex] = useState(0), [seen, setSeen] = useState(0);
  const [closed, setClosed] = useState(false), [error, setError] = useState("");
  const [busy, setBusy] = useState(false), [acknowledged, setAcknowledged] = useState(false);
  const request = useRef<AbortController | null>(null);
  const current = page.items[index];
  const hasNext = index + 1 < page.items.length || !!page.nextCursor;

  async function load(before = "") {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    const timeout = setTimeout(() => controller.abort(new Error("公告加载超时，请重试")), 15000);
    setBusy(true); setError("");
    try {
      const result = await userAPI<AnnouncementPage>(`/announcements${before ? `?before=${encodeURIComponent(before)}` : ""}`, "GET", undefined, controller.signal);
      controller.signal.throwIfAborted();
      setPage(result); setIndex(0); setAcknowledged(false);
      if (!result.items.length) setClosed(true);
      return true;
    } catch (e) {
      if (request.current === controller) setError((e as Error).message);
      return false;
    } finally {
      clearTimeout(timeout);
      if (request.current === controller) { request.current = null; setBusy(false); }
    }
  }
  useEffect(() => {
    void load();
    return () => { const pending = request.current; request.current = null; pending?.abort(); };
  }, []);
  function close() {
    const pending = request.current; request.current = null; pending?.abort();
    setClosed(true);
  }
  async function next() {
    setError("");
    if (index + 1 < page.items.length) {
      setIndex(index + 1); setSeen(seen + 1); setAcknowledged(false);
    } else if (page.nextCursor) {
      if (await load(page.nextCursor)) setSeen(seen + 1);
    } else close();
  }
  async function acknowledge() {
    if (busy || !current) return;
    setBusy(true); setError("");
    const controller = new AbortController(); request.current = controller;
    const timeout = setTimeout(() => controller.abort(new Error("确认超时，请重试")), 15000);
    try {
      await userAPI(`/announcements/${encodeURIComponent(current.id)}/acknowledge`, "POST", {}, controller.signal);
      controller.signal.throwIfAborted();
      setAcknowledged(true);
      // Clear this request before fetching a following page. If that read fails,
      // the already acknowledged message remains visible with a retryable Next.
      request.current = null; setBusy(false);
      await next();
    } catch (e) {
      if (request.current === controller) setError((e as Error).message);
    } finally {
      clearTimeout(timeout);
      if (request.current === controller) { request.current = null; setBusy(false); }
    }
  }
  if (closed) return null;
  if (!current) return error ? <div className="announcement-load-error"><ErrorNotice message={error} retry={() => void load()} /><button className="text-link" onClick={close}>关闭提示</button></div> : null;
  return <Modal title="站内公告" onClose={close} description={`第 ${seen + 1} 条${hasNext ? "" : " · 最后一条"}`}>
    <article className="announcement-content">
      <div className="announcement-meta"><Megaphone size={18} aria-hidden="true" /><span>{date(current.createdAt)}</span></div>
      <h3 tabIndex={-1}>{current.title}</h3>
      <div className="announcement-body">{current.body}</div>
      <p className="announcement-hint">{current.mode === "always" ? "此公告每次登录或刷新后都会展示。" : acknowledged ? "已确认，下次不再展示。" : "点击“我已知晓”后，此公告将不再展示。"}</p>
    </article>
    <ErrorNotice message={error} />
    <div className="modal-actions">
      <Button variant="secondary" onClick={close}>关闭</Button>
      {hasNext && <Button variant="secondary" icon={ArrowRight} disabled={busy} onClick={() => void next()}>下一条</Button>}
      {current.mode === "until_acknowledged" && !acknowledged && <Button icon={Check} busy={busy} onClick={() => void acknowledge()}>我已知晓</Button>}
    </div>
  </Modal>;
}
