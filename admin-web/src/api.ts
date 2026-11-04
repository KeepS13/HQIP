import { API_URL } from "./config";
import { useCallback, useEffect, useRef, useState } from "react";
let csrfToken = "";
export function setCSRF(value: string) {
  csrfToken = value;
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const response = await fetch(`${API_URL}/api/v1${path}`, {
    method,
    credentials: "include",
    headers: {
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(method !== "GET" ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await response
    .json()
    .catch(() => ({ error: "服务器响应异常，请稍后重试" }));
  if (!response.ok) {
    if (response.status === 401 && path !== "/login")
      window.dispatchEvent(new Event("hqip:unauthorized"));
    throw new Error(data.error || `请求失败 (${response.status})`);
  }
  return data as T;
}
export function usePolling<T>(path: string, interval = 5000) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const alive = useRef(true),
    generation = useRef(0);
  const refresh = useCallback(async () => {
    const current = generation.current;
    try {
      const result = await api<T>(path);
      if (alive.current && current === generation.current) {
        setData(result);
        setError("");
      }
    } catch (e) {
      if (alive.current && current === generation.current)
        setError((e as Error).message);
    } finally {
      if (alive.current && current === generation.current) setLoading(false);
    }
  }, [path]);
  useEffect(() => {
    alive.current = true;
    generation.current++;
    setData(null);
    setLoading(true);
    void refresh();
    const id = setInterval(() => {
      if (!document.hidden) void refresh();
    }, interval);
    const visible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      alive.current = false;
      generation.current++;
      clearInterval(id);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh, interval]);
  return { data, error, loading, refresh, setData };
}
