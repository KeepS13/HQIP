import { useCallback, useEffect, useRef, useState } from "react";
import { userAPI } from "./api";

export type LinkAction = "create" | "rotate" | "revoke";

// A mounted selection owns its request order. Old reads must never restore a
// leaked token after rotation. Only initial entry prepares a missing link;
// periodic GETs cannot undo an explicit revocation in this or another tab.
export function useImportLink(id: string | null, active: boolean, onBusyChange: (busy: boolean) => void) {
  const [url, setURL] = useState("");
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const mounted = useRef(false), sequence = useRef(0), operation = useRef(0), mutating = useRef(false);
  const inFlight = useRef<number | null>(null), prepared = useRef(false);
  const activeRef = useRef(active);
  activeRef.current = active;
  const path = id === null ? "/import-link" : `/subscriptions/${encodeURIComponent(id)}/import-link`;

  const changeLink = useCallback(async (action: LinkAction) => {
    if (!mounted.current || mutating.current) return null;
    const currentOperation = ++operation.current;
    sequence.current++;
    inFlight.current = null;
    mutating.current = true;
    setBusy(true);
    onBusyChange(true);
    setRefreshing(false);
    setActionError("");
    try {
      const result = await userAPI<{ url: string }>(`${path}${action === "create" ? "" : `/${action}`}`, "POST", {});
      if (!mounted.current || currentOperation !== operation.current) return null;
      setURL(result.url);
      setError("");
      setUncertain(false);
      return result.url;
    } catch (e) {
      if (mounted.current && currentOperation === operation.current) {
        setActionError(`${(e as Error).message}；请刷新确认当前链接后再操作。`);
        setUncertain(true);
        setURL("");
      }
      return null;
    } finally {
      if (mounted.current && currentOperation === operation.current) {
        mutating.current = false;
        setBusy(false);
        setLoading(false);
        onBusyChange(false);
      }
    }
  }, [path, onBusyChange]);

  const refresh = useCallback(async () => {
    if (!mounted.current || mutating.current || inFlight.current !== null) return;
    const request = ++sequence.current;
    inFlight.current = request;
    setRefreshing(true);
    try {
      const result = await userAPI<{ url: string }>(path);
      if (mounted.current && request === sequence.current) {
        setURL(result.url);
        setError("");
        setUncertain(false);
        const prepare = !prepared.current && activeRef.current && !result.url;
        prepared.current = true;
        if (prepare) await changeLink("create");
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
  }, [path, changeLink]);

  useEffect(() => {
    mounted.current = true;
    void refresh();
    const visible = () => { if (!document.hidden) void refresh(); };
    // Links change much less often than usage; profile/status still refreshes
    // independently. This bounds combined-link reads to twice a minute.
    const timer = window.setInterval(visible, 30000);
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
  return { url, error, actionError, loading, refreshing, busy, uncertain, refresh, changeLink, clearActionError: () => setActionError("") };
}
