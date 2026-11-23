// Shared scheduling only: each independently built web app owns its API,
// authentication and state. No response cache crosses routes, hooks or accounts.
export interface PollingTask<T> {
  interval: number;
  read: (signal: AbortSignal) => Promise<T>;
  success: (value: T) => void;
  failure: (error: Error) => void;
  settled: () => void;
}

export function createPoller<T>(task: PollingTask<T>) {
  let disposed = false, failures = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;
  let pending: Promise<void> | undefined, queued: Promise<void> | undefined;
  const interval = Math.max(1000, task.interval);

  function clearTimer() { clearTimeout(timer); timer = undefined; }
  function schedule() {
    clearTimer();
    if (disposed || document.hidden) return;
    // Wait after completion, so a slow response cannot accumulate work. Backoff
    // is bounded; explicit refresh and return to the foreground can wake it.
    const delay = Math.min(interval * 2 ** failures, Math.max(interval, 60000));
    timer = setTimeout(() => { void poll(); }, delay);
  }

  function run(): Promise<void> {
    if (disposed) return Promise.resolve();
    if (pending) return pending;
    clearTimer();
    const request = new AbortController();
    controller = request;
    let timedOut = false;
    const timeout = setTimeout(() => { timedOut = true; request.abort(); }, 15000);
    pending = (async () => {
      try {
        const result = await task.read(request.signal);
        if (timedOut) throw new Error("请求超时，请稍后重试");
        if (!disposed && !queued) { failures = 0; task.success(result); }
      } catch (error) {
        if (!disposed && !queued) {
          failures = Math.min(failures + 1, 6);
          task.failure(timedOut ? new Error("请求超时，请稍后重试") : error as Error);
        }
      } finally {
        clearTimeout(timeout);
        if (controller === request) controller = undefined;
        if (!disposed && !queued) task.settled();
      }
    })().finally(() => {
      pending = undefined;
      if (!queued) schedule();
    });
    return pending;
  }

  function poll() {
    if (disposed || document.hidden || pending) return;
    return run();
  }

  // A post-mutation refresh must observe a read started AFTER the mutation.
  // Coalesce repeated refreshes into one follow-up; never publish the older
  // in-flight snapshot, and resolve all waiters only after the new read settles.
  function refresh(): Promise<void> {
    if (disposed) return Promise.resolve();
    if (!pending) return run();
    if (!queued) queued = pending.then(() => { queued = undefined; return run(); });
    return queued;
  }

  function visible() {
    clearTimer();
    if (!document.hidden) void poll();
  }

  document.addEventListener("visibilitychange", visible);
  void poll();
  return {
    refresh,
    stop() {
      disposed = true;
      clearTimer();
      controller?.abort();
      document.removeEventListener("visibilitychange", visible);
    },
  };
}
