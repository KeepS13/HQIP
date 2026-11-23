import { API_URL } from "../config";
import { useCallback, useEffect, useRef, useState } from "react";
import { createPoller } from "../../../web-shared/polling";

let userCSRF = "";
export function setUserCSRF(value: string) {
  userCSRF = value;
}
export async function userAPI<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(`${API_URL}/api/v1/user${path}`, {
    method,
    credentials: "include",
    signal,
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
  // A response from a cancelled route must not expire the replacement session,
  // including cancellation while its JSON body was still being consumed.
  signal?.throwIfAborted();
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
  const owner = useRef<{ path: string; poller: ReturnType<typeof createPoller<T>> } | null>(null);
  const refresh = useCallback(() => owner.current?.path === path
    ? owner.current.poller.refresh() : Promise.resolve(), [path]);
  useEffect(() => {
    setData(null);
    setLoading(true);
    setError("");
    const poller = createPoller<T>({
      interval, read: signal => userAPI<T>(path, "GET", undefined, signal),
      success: result => { setData(result); setError(""); },
      failure: e => setError(e.message), settled: () => setLoading(false),
    });
    owner.current = { path, poller };
    return () => {
      poller.stop();
      owner.current = null;
    };
  }, [path, interval]);
  return { data, loading, error, refresh };
}
