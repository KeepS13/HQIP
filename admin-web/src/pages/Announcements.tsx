import { useEffect, useRef, useState } from "react";
import { Megaphone, Plus, RefreshCw } from "lucide-react";
import { api } from "../api";
import { Badge, Button, Empty, ErrorNotice, Loading, Modal, PageHeader, Panel, date, useToast } from "../components";
import type { PortalUser } from "../portal/types";
import { announcementModeLabel, type Announcement, type AnnouncementPage } from "../../../web-shared/announcements";
import { AnnouncementForm } from "./AnnouncementForm";
import "./announcements.css";

/** Manual refresh keeps publication/receipts predictable without background polling. */
export function Announcements() {
  const [page, setPage] = useState<AnnouncementPage>({ items: [], nextCursor: "" });
  const [users, setUsers] = useState<PortalUser[]>([]), [usersLoaded, setUsersLoaded] = useState(false);
  const [usersError, setUsersError] = useState("");
  const [loading, setLoading] = useState(true), [error, setError] = useState("");
  const [compose, setCompose] = useState(false), [detail, setDetail] = useState<Announcement | null>(null);
  const [stop, setStop] = useState<Announcement | null>(null), [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const owner = useRef<AbortController | null>(null), notify = useToast();
  const audienceOwner = useRef<AbortController | null>(null);
  async function loadUsers() {
    audienceOwner.current?.abort();
    const controller = new AbortController(); audienceOwner.current = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setUsersError("");
    try {
      setUsers(await api<PortalUser[]>("/users", "GET", undefined, controller.signal));
      setUsersLoaded(true);
    } catch (e) {
      if (audienceOwner.current === controller) setUsersError(controller.signal.aborted ? "接收用户加载超时，请重试" : (e as Error).message);
    } finally { clearTimeout(timeout); if (audienceOwner.current === controller) audienceOwner.current = null; }
  }
  async function load(before = "") {
    owner.current?.abort();
    const controller = new AbortController(); owner.current = controller;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setLoading(true); setError("");
    try {
      const value = await api<AnnouncementPage>(`/announcements${before ? `?before=${encodeURIComponent(before)}` : ""}`, "GET", undefined, controller.signal);
      setPage(previous => before ? { items: [...previous.items, ...value.items], nextCursor: value.nextCursor } : value);
    } catch (e) { if (!controller.signal.aborted) setError((e as Error).message); else if (owner.current === controller) setError("公告加载超时，请重试"); }
    finally { clearTimeout(timeout); if (owner.current === controller) { owner.current = null; setLoading(false); } }
  }
  useEffect(() => {
    void load();
    void loadUsers();
    return () => {
      const previous = owner.current, audience = audienceOwner.current;
      owner.current = audienceOwner.current = null;
      previous?.abort(); audience?.abort();
    };
  }, []);
  async function openDetails(item: Announcement) {
    setBusy(true); setError("");
    try { setDetail(await api<Announcement>(`/announcements/${item.id}`, "GET", undefined, AbortSignal.timeout(15000))); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function stopDelivery() {
    if (!stop || busy) return;
    setBusy(true); setActionError("");
    try {
      await api(`/announcements/${stop.id}`, "DELETE", undefined, AbortSignal.timeout(15000));
      setPage(previous => ({ ...previous, items: previous.items.map(item => item.id === stop.id ? { ...item, active: false } : item) }));
      setStop(null); notify("公告已停用");
    } catch (e) { setActionError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <>
    <PageHeader eyebrow="ANNOUNCEMENTS" title="用户公告" description="把重要消息传达给指定用户，支持批量发布和按账号确认。" actions={<div className="announcement-select-actions"><Button variant="secondary" icon={RefreshCw} disabled={loading} onClick={() => void load()}>刷新</Button><Button icon={Plus} disabled={!usersLoaded} onClick={() => setCompose(true)}>发布公告</Button></div>} />
    <ErrorNotice message={error} retry={() => void load()} />
    <ErrorNotice message={usersError} retry={() => void loadUsers()} />
    <Panel title="已发布公告" subtitle="最新发布优先 · 用户登录或刷新网站后展示">
      {loading && !page.items.length ? <Loading /> : !page.items.length ? <Empty icon={Megaphone} title="暂无公告" description="发布第一条公告，选择需要通知的用户。" /> : <div className="announcement-cards">
        {page.items.map(item => <article className="announcement-card" key={item.id}>
          <div className="announcement-card-heading"><h3>{item.title}</h3><Badge tone={item.active ? "success" : "muted"}>{item.active ? "展示中" : "已停用"}</Badge></div>
          <p className="announcement-preview">{item.body}</p>
          <div className="announcement-summary"><span>{announcementModeLabel(item.mode)}</span><span>{item.recipientCount || 0} 位用户</span>{item.mode === "until_acknowledged" && <span>{item.acknowledgedCount || 0} 人已知晓</span>}<time>{date(item.createdAt)}</time></div>
          <div className="announcement-card-actions"><Button variant="secondary" disabled={busy} onClick={() => void openDetails(item)}>查看详情</Button>{item.active && <Button variant="secondary" onClick={() => { setStop(item); setActionError(""); }}>停用</Button>}</div>
        </article>)}
      </div>}
      {page.nextCursor && <Button variant="secondary" busy={loading} onClick={() => void load(page.nextCursor)}>加载更多公告</Button>}
    </Panel>
    {compose && <AnnouncementForm users={users} onClose={() => setCompose(false)} onPublished={() => { setCompose(false); notify("公告已发布"); void load(); }} />}
    {detail && <Modal wide title={detail.title} onClose={() => setDetail(null)} description={announcementModeLabel(detail.mode)}><div className="announcement-full-body">{detail.body}</div><details className="announcement-targets"><summary>接收用户（{detail.userIds?.length || 0}）</summary><ul>{detail.userIds?.map(id => <li key={id}>{users.find(user => user.id === id)?.email || id}</li>)}</ul></details><div className="modal-actions"><Button onClick={() => setDetail(null)}>关闭</Button></div></Modal>}
    {stop && <Modal title="停用公告" description={`停用“${stop.title}”后，下次加载将不再向用户展示。`} onClose={() => { if (!busy) setStop(null); }}><ErrorNotice message={actionError} /><div className="modal-actions"><Button variant="secondary" disabled={busy} onClick={() => setStop(null)}>取消</Button><Button busy={busy} onClick={() => void stopDelivery()}>确认停用</Button></div></Modal>}
  </>;
}
