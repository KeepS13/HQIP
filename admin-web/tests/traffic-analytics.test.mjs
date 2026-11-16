import assert from "node:assert/strict";
import { test } from "node:test";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { outputFiles } = await build({ entryPoints: [fileURLToPath(new URL("../src/traffic-analytics.ts", import.meta.url))], bundle: true, write: false, platform: "node", format: "esm" });
const { createTrafficLoader, formatTrafficRate, ratePaths, recordRatePoint, selectedTrafficRange, trafficCSV, validTrafficDates } = await import(`data:text/javascript;base64,${Buffer.from(outputFiles[0].text).toString("base64")}`);

function report(from = "2026-11-15", to = "2026-11-16") {
  return { from, to, today: "2026-11-16", includesToday: to === "2026-11-16", summary: { total: 42 }, days: [
    { date: from, upload: 12, download: 30, total: 42, hasData: true },
    { date: to, upload: 0, download: 0, total: 0, hasData: true },
    { date: "2026-11-14", upload: 0, download: 0, total: 0, hasData: false },
  ] };
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((ok, fail) => { resolve = ok; reject = fail; });
  return { promise, resolve, reject };
}

test("traffic presets use inclusive Beijing days and explicit custom URLs preserve selection", () => {
  const old = process.env.TZ;
  try {
    for (const zone of ["UTC", "America/Los_Angeles", "Asia/Tokyo"]) {
      process.env.TZ = zone;
      const now = Date.parse("2027-01-01T00:15:00+08:00");
      assert.deepEqual(selectedTrafficRange(new URLSearchParams(), now), { preset: "last7", from: "2026-12-26", to: "2027-01-01" });
      assert.deepEqual(selectedTrafficRange(new URLSearchParams("trafficRange=yesterday"), now), { preset: "yesterday", from: "2026-12-31", to: "2026-12-31" });
      assert.deepEqual(selectedTrafficRange(new URLSearchParams("trafficRange=custom&trafficFrom=2026-10-01&trafficTo=2026-10-31"), now), { preset: "custom", from: "2026-10-01", to: "2026-10-31" });
    }
  } finally { if (old === undefined) delete process.env.TZ; else process.env.TZ = old; }
});

test("traffic date validation rejects normalized invalid dates, future, reversed and expired ranges", () => {
  const valid = (from, to) => validTrafficDates({ from, to }, "2027-02-01", "2028-03-01");
  assert.equal(valid("2028-02-29", "2028-03-01"), true);
  assert.equal(valid("2028-03-01", "2028-03-01"), true);
  for (const [from, to] of [["2028-02-30", "2028-03-01"], ["2027-02-29", "2028-03-01"], ["2028-03-01", "2028-02-29"], ["2028-03-01", "2028-03-02"], ["2027-01-31", "2028-03-01"], ["", "2028-03-01"], ["2028-3-1", "2028-03-01"]]) assert.equal(valid(from, to), false, `${from}/${to}`);
});

test("CSV exports raw bytes for the entire range, keeps valid zero and marks absent records", () => {
  const data = report();
  data.days.push(...Array.from({ length: 28 }, (_, index) => ({ date: `2026-10-${String(index + 1).padStart(2, "0")}`, upload: 1, download: 2, total: 3, hasData: true })));
  const csv = trafficCSV(data);
  assert.ok(csv.startsWith("\uFEFF"));
  const rows = csv.slice(1).trimEnd().split("\r\n").map(row => row.split(",").map(cell => JSON.parse(cell)));
  assert.equal(rows.length, data.days.length + 1);
  assert.deepEqual(rows[2], ["2026-11-16", "0", "0", "0", "截至当前"]);
  assert.deepEqual(rows[3], ["2026-11-14", "", "", "", "暂无记录"]);
  assert.equal(rows.slice(1).reduce((total, row) => total + Number(row[3]), 0), data.days.reduce((total, day) => total + day.total, 0));
});

test("changing dates discards old successes, errors and completion state", async () => {
  const a = deferred(), b = deferred();
  let state;
  const loader = createTrafficLoader(key => key === "a" ? a.promise : b.promise, next => { state = next; });
  const first = loader.load("a"), second = loader.load("b");
  b.resolve(report("2026-10-01", "2026-10-31")); await second;
  const good = state;
  a.reject(new Error("old range failed")); await first;
  assert.equal(state, good);
  assert.equal(state.key, "b"); assert.equal(state.error, ""); assert.equal(state.refreshing, false);
  const c = deferred();
  const nextLoader = createTrafficLoader(key => key === "a" ? c.promise : Promise.resolve(report()), next => { state = next; });
  const late = nextLoader.load("a"); await nextLoader.load("b");
  c.resolve(report("2020-01-01", "2020-01-02")); await late;
  assert.equal(state.data.from, "2026-11-15");
});

test("same-range refresh failure preserves last success and overlapping refreshes keep the newest result", async () => {
  let state, fail = false;
  const loader = createTrafficLoader(async () => { if (fail) throw new Error("network unavailable"); return report(); }, next => { state = next; });
  await loader.load("a"); const old = state.data;
  fail = true; await loader.load("a");
  assert.equal(state.data, old); assert.equal(state.error, "network unavailable"); assert.equal(state.loading, false);
  const retry = loader.load("a");
  assert.equal(state.data, old); assert.equal(state.error, "network unavailable"); assert.equal(state.refreshing, true);
  await retry;
  await loader.load("b"); assert.equal(state.data, null);
  const a = deferred(), b = deferred(); let calls = 0;
  const overlap = createTrafficLoader(() => (++calls === 1 ? a.promise : b.promise), next => { state = next; });
  const first = overlap.load("same"), second = overlap.load("same");
  b.resolve(report("2026-11-16", "2026-11-16")); await second;
  a.resolve(report("2026-10-01", "2026-10-01")); await first;
  assert.equal(state.data.from, "2026-11-16");
  const pending = deferred(); const cancelled = createTrafficLoader(() => pending.promise, next => { state = next; });
  const request = cancelled.load("cancelled"); const beforeCancel = state;
  cancelled.cancel(); pending.resolve(report()); await request;
  assert.equal(state, beforeCancel);
});

test("low rates remain visible and invalid telemetry never formats as zero", () => {
  assert.equal(formatTrafficRate(0), "0 B/s"); assert.equal(formatTrafficRate(0.2), "<1 B/s");
  assert.equal(formatTrafficRate(28), "28 B/s"); assert.equal(formatTrafficRate(1000), "1 KB/s");
  assert.equal(formatTrafficRate(1_500_000), "1.5 MB/s"); assert.equal(formatTrafficRate(2_000_000_000), "2 GB/s");
  for (const value of [-1, NaN, Infinity]) assert.equal(formatTrafficRate(value), "—");
});

test("one-minute sparkline preserves idle zero, breaks at missing samples and expires old points", () => {
  const runtime = { ratesStatus: "complete", uploadRate: 0, downloadRate: 28 };
  const at = Date.parse("2026-11-16T08:00:00Z");
  let history = recordRatePoint([], runtime, new Date(at).toISOString(), false);
  history = recordRatePoint(history, { ...runtime, ratesStatus: "unknown" }, new Date(at + 5000).toISOString(), false);
  history = recordRatePoint(history, runtime, new Date(at + 10_000).toISOString(), false);
  assert.equal(history[0].upload, 0); assert.equal(history[1].download, null);
  assert.equal(ratePaths(history, "download", 28).length, 2);
  assert.equal(recordRatePoint(history, runtime, new Date(at).toISOString(), false), history);
  history = recordRatePoint(history, runtime, new Date(at + 75_000).toISOString(), false);
  assert.equal(history.length, 1);
  const gap = [{ at, upload: 1, download: 1 }, { at: at + 20_000, upload: 1, download: 1 }];
  assert.equal(ratePaths(gap, "download", 1).length, 2);
});

const cardsBuild = await build({ entryPoints: [fileURLToPath(new URL("../src/RuntimeCards.tsx", import.meta.url))], bundle: true, write: false, platform: "node", format: "cjs", packages: "external", loader: { ".css": "empty" }, define: { "import.meta.env": "{}" } });
const compiled = { exports: {} };
new Function("require", "module", "exports", cardsBuild.outputFiles[0].text)(createRequire(import.meta.url), compiled, compiled.exports);
const RuntimeCards = compiled.exports.default;

test("runtime cards render deduplicated users, meaningful low speeds, partial and stale states", () => {
  const runtime = { onlineStatus: "complete", ratesStatus: "complete", onlineUsers: 1, activeSubscriptions: 2, instances: 3, unmappedInstances: 0, uploadRate: 24, downloadRate: 28, lastReportedAt: "2026-11-16T08:00:00Z" };
  const render = (next, stale = false) => renderToStaticMarkup(React.createElement(RuntimeCards, { runtime: next, stale, serverTime: "2026-11-16T08:00:00Z" }));
  const html = render(runtime);
  assert.ok(html.includes("在线用户")); assert.ok(html.includes("2 份活跃订阅 · 3 个客户端连接")); assert.ok(html.includes("28 B/s")); assert.ok(!html.includes("Mbps"));
  const partial = render({ ...runtime, onlineStatus: "partial", ratesStatus: "partial", unmappedInstances: 1 }, true);
  assert.ok(partial.includes("≥ 1")); assert.ok(partial.includes("数据不完整")); assert.ok(partial.includes("已过期"));
  const unknown = render({ ...runtime, onlineStatus: "unknown", ratesStatus: "unknown" });
  assert.ok(unknown.includes("在线状态未知")); assert.ok(unknown.includes("当前速率未知")); assert.ok(!unknown.includes("28 B/s"));
});
