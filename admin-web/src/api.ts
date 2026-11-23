import { API_URL } from "./config";
import { useCallback, useEffect, useRef, useState } from "react";
import { createPoller } from "../../web-shared/polling";
let csrfToken = "";
export function setCSRF(value: string) {
  csrfToken = value;
}
// Preserve HTTP semantics for feature-specific recovery. Shared requests never
// retry writes automatically; the caller must own an idempotency key first.
export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly code = "") {
    super(message);
    this.name = "ApiError";
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(`${API_URL}/api/v1${path}`, {
    method,
    credentials: "include",
    signal,
    headers: {
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(method !== "GET" ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await response
    .json()
    .catch(() => ({ error: "服务器响应异常，请稍后重试" }));
  // Aborted reads have no authority to log out a newer route/session.
  signal?.throwIfAborted();
  if (!response.ok) {
    if (response.status === 401 && path !== "/login")
      window.dispatchEvent(new Event("hqip:unauthorized"));
    throw new ApiError(data.error || `请求失败 (${response.status})`, response.status, data.code || "");
  }
  return data as T;
}
export function usePolling<T>(path: string, interval = 5000) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const owner = useRef<{ path: string; poller: ReturnType<typeof createPoller<T>> } | null>(null);
  const refresh = useCallback(() => owner.current?.path === path
    ? owner.current.poller.refresh() : Promise.resolve(), [path]);
  useEffect(() => {
    setData(null);
    setLoading(true);
    setError("");
    const poller = createPoller<T>({
      interval, read: signal => api<T>(path, "GET", undefined, signal),
      success: result => { setData(result); setError(""); },
      failure: e => setError(e.message), settled: () => setLoading(false),
    });
    owner.current = { path, poller };
    return () => {
      poller.stop();
      owner.current = null;
    };
  }, [path, interval]);
  return { data, error, loading, refresh, setData };
}
