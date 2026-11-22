import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

export type Usage = { used: number; total: number };
type HostResourcesView = {
  sample: { sampledAt: string; cpuPercent: number | null; memory: Usage | null; disk: Usage | null } | null;
  refreshing: boolean;
};

// The endpoint starts asynchronous sampling and returns its previous snapshot.
// Follow refreshing briefly, then resume the normal cadence. One in-flight
// request owns both timers; hiding/unmounting aborts it and discards late data.
export function useHostResources() {
  const [data, setData] = useState<HostResourcesView | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const run = useRef<() => Promise<void>>(async () => {});
  const refresh = useCallback(() => run.current(), []);
  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let request: AbortController | undefined;
    let followingSince = 0;
    const poll = async () => {
      if (!alive || document.hidden || request) return;
      clearTimeout(timer);
      const controller = new AbortController();
      request = controller;
      let timedOut = false;
      const timeout = setTimeout(() => { timedOut = true; controller.abort(); }, 10_000);
      let delay = 5000;
      try {
        const result = await api<HostResourcesView>("/host-resources", "GET", undefined, controller.signal);
        if (!alive || request !== controller) return;
        setData(result);
        setError("");
        if (result.refreshing) {
          followingSince ||= Date.now();
          if (Date.now() - followingSince < 10_000) delay = 250;
        } else followingSince = 0;
      } catch (e) {
        if (alive && request === controller && (!controller.signal.aborted || timedOut))
          setError(timedOut ? "主机资源采集请求超时" : (e as Error).message);
      } finally {
        clearTimeout(timeout);
        if (alive && request === controller) {
          request = undefined;
          setLoading(false);
          if (!document.hidden) timer = setTimeout(() => { void poll(); }, delay);
        }
      }
    };
    run.current = poll;
    const visible = () => {
      clearTimeout(timer);
      request?.abort();
      request = undefined;
      followingSince = 0;
      if (!document.hidden) void poll();
    };
    document.addEventListener("visibilitychange", visible);
    void poll();
    return () => {
      alive = false;
      clearTimeout(timer);
      request?.abort();
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);
  return { data, error, loading, refresh };
}
