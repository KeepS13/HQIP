import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { createTrafficLoader, type TrafficLoadState } from "./traffic-analytics";
import type { TrafficAnalytics } from "./traffic-types";

export function useTrafficReport(key: string, refreshVersion: number, includesToday: boolean) {
  const [state, setState] = useState<TrafficLoadState>({ key: "", data: null, loading: true, refreshing: false, error: "" });
  const loader = useRef<ReturnType<typeof createTrafficLoader> | null>(null);
  if (!loader.current) loader.current = createTrafficLoader(path => api<TrafficAnalytics>(path), setState);
  useEffect(() => {
    const current = loader.current!;
    void current.load(key);
    const refreshCurrent = () => {
      if (!document.hidden && (current.current().data?.includesToday ?? includesToday)) void current.load(key);
    };
    const timer = setInterval(refreshCurrent, 30_000);
    document.addEventListener("visibilitychange", refreshCurrent);
    return () => {
      current.cancel();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshCurrent);
    };
  }, [key, refreshVersion, includesToday]);
  return state.key === key ? state : { key, data: null, loading: true, refreshing: false, error: "" };
}
