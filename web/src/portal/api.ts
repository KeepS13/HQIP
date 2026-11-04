import { useCallback, useEffect, useRef, useState } from "react";

let userCSRF = "";
export function setUserCSRF(value: string) {
  userCSRF = value;
}
export async function userAPI<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const response = await fetch(`/api/v1/user${path}`, {
    method,
    credentials: "same-origin",
    cache: "no-store",
    headers: {
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(method !== "GET" ? { "X-CSRF-Token": userCSRF } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response
    .json()
    .catch(() => ({ error: "服务器返回了无效响应，请稍后重试" }));
  if (!response.ok) {
    if (response.status === 401 && path !== "/login" && path !== "/register")
      window.dispatchEvent(new Event("hqip:user-unauthorized"));
    throw new Error(data.error || `请求失败 (${response.status})`);
  }
  return data as T;
}
export function useUserData<T>(path: string, interval = 10000) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const sequence = useRef(0),
    active = useRef(false);
  const refresh = useCallback(async () => {
    const generation = sequence.current;
    try {
      const result = await userAPI<T>(path);
      if (active.current && generation === sequence.current) {
        setData(result);
        setError("");
      }
    } catch (e) {
      if (active.current && generation === sequence.current)
        setError((e as Error).message);
    } finally {
      if (active.current && generation === sequence.current) setLoading(false);
    }
  }, [path]);
  useEffect(() => {
    active.current = true;
    sequence.current++;
    setData(null);
    setLoading(true);
    setError("");
    void refresh();
    const timer = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, interval);
    const visible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      active.current = false;
      sequence.current++;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh, interval]);
  return { data, loading, error, refresh };
}
