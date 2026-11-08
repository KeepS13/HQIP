import { useEffect, useState } from "react";
import { api } from "./api";

const readEvent = "hqip:alert-read";

// Mounted only inside the authenticated layout, so login and logout never
// leave a background alert poller running.
export function AlertUnreadBadge() {
  const [unread, setUnread] = useState(0);
  useEffect(() => {
    let alive = true;
    let inFlight = false;
    let queued = false;
    const refresh = async () => {
      if (!alive || document.hidden) return;
      if (inFlight) {
        queued = true;
        return;
      }
      inFlight = true;
      try {
        const value = await api<{ unread: number }>("/alerts/unread");
        if (alive) setUnread(Math.max(0, value.unread));
      } catch {
        // Keep the last known count; the next poll will retry.
      } finally {
        inFlight = false;
        if (queued && alive) {
          queued = false;
          void refresh();
        }
      }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 5000);
    const changed = () => void refresh();
    window.addEventListener(readEvent, changed);
    document.addEventListener("visibilitychange", changed);
    return () => {
      alive = false;
      clearInterval(timer);
      window.removeEventListener(readEvent, changed);
      document.removeEventListener("visibilitychange", changed);
    };
  }, []);
  return unread > 0 ? (
    <span className="nav-unread-badge" aria-label={`${unread} 条未读消息`} title={`${unread} 条未读消息`}>
      {unread > 99 ? "99+" : unread}
    </span>
  ) : null;
}

export function useMarkAlertRead(id: string, revision: number | undefined) {
  useEffect(() => {
    if (!id || revision === undefined) return;
    let alive = true;
    let done = false;
    let inFlight = false;
    const markRead = async () => {
      if (!alive || done || inFlight || document.hidden) return;
      inFlight = true;
      try {
        await api(`/alerts/${encodeURIComponent(id)}/read`, "POST", { expectedRevision: revision });
        done = true;
        if (alive) window.dispatchEvent(new Event(readEvent));
      } catch {
        // Do not clear unread on a failed or stale read. A new revision from
        // detail polling starts a new attempt; transient failures retry below.
      } finally {
        inFlight = false;
      }
    };
    void markRead();
    const timer = setInterval(() => void markRead(), 5000);
    const visible = () => void markRead();
    document.addEventListener("visibilitychange", visible);
    return () => {
      alive = false;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [id, revision]);
}
