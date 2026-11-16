import { dateRangeShortcut, type DateRange, type DateRangePreset } from "./date-shortcuts";
import type { RuntimeOverview, TrafficAnalytics } from "./traffic-types";

export const trafficPresets: [DateRangePreset, string][] = [
  ["today", "今天"], ["yesterday", "昨天"], ["last7", "近 7 天"], ["last30", "近 30 天"], ["month", "本月"],
];

export function selectedTrafficRange(params: URLSearchParams, now = Date.now()) {
  const preset = params.get("trafficRange") || "last7";
  if (preset === "custom") return { preset, from: params.get("trafficFrom") || "", to: params.get("trafficTo") || "" };
  const validPreset = trafficPresets.some(([key]) => key === preset) ? preset as DateRangePreset : "last7";
  return { preset: validPreset, ...dateRangeShortcut(validPreset, now) };
}

export function validTrafficDates(range: DateRange, minDate: string, today: string) {
  const validDay = (day: string) => /^\d{4}-\d{2}-\d{2}$/.test(day)
    && Number.isFinite(Date.parse(`${day}T00:00:00Z`))
    && new Date(`${day}T00:00:00Z`).toISOString().slice(0, 10) === day;
  return validDay(range.from) && validDay(range.to) && range.from <= range.to && range.from >= minDate && range.to <= today;
}

export function trafficCSV(report: TrafficAnalytics) {
  const rows: (string | number)[][] = [["日期（北京时间）", "上传（字节）", "下载（字节）", "合计（字节）", "记录状态"]];
  for (const day of report.days) rows.push([
    day.date, day.hasData ? day.upload : "", day.hasData ? day.download : "", day.hasData ? day.total : "",
    day.hasData ? (report.includesToday && day.date === report.today ? "截至当前" : "已记录") : "暂无记录",
  ]);
  return "\uFEFF" + rows.map(row => row.map(value => `"${String(value).replace(/"/g, '""')}"`).join(",")).join("\r\n") + "\r\n";
}

export type TrafficLoadState = { key: string; data: TrafficAnalytics | null; loading: boolean; refreshing: boolean; error: string };

// One coordinator owns both range switches and overlapping refreshes. Older
// successes, failures and finally handlers cannot overwrite the latest request.
export function createTrafficLoader(fetcher: (key: string) => Promise<TrafficAnalytics>, publish: (state: TrafficLoadState) => void) {
  let version = 0;
  let state: TrafficLoadState = { key: "", data: null, loading: true, refreshing: false, error: "" };
  return {
    current: () => state,
    cancel: () => { version++; },
    async load(key: string) {
      const request = ++version;
      state = { key, data: state.key === key ? state.data : null, loading: state.key !== key || !state.data, refreshing: true, error: state.key === key ? state.error : "" };
      publish(state);
      try {
        const data = await fetcher(key);
        if (request !== version) return;
        state = { key, data, loading: false, refreshing: false, error: "" };
      } catch (error) {
        if (request !== version) return;
        state = { ...state, loading: false, refreshing: false, error: error instanceof Error ? error.message : "流量查询失败" };
      }
      publish(state);
    },
  };
}

export function formatTrafficRate(value: number) {
  if (!Number.isFinite(value) || value < 0) return "—";
  if (value === 0) return "0 B/s";
  if (value < 1) return "<1 B/s";
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1000)), units.length - 1);
  return `${(value / 1000 ** index).toLocaleString("zh-CN", { maximumFractionDigits: 1 })} ${units[index]}`;
}

export type RatePoint = { at: number; upload: number | null; download: number | null };
export function recordRatePoint(history: RatePoint[], runtime: RuntimeOverview | undefined, serverTime: string, stale: boolean): RatePoint[] {
  const at = Date.parse(serverTime);
  if (!Number.isFinite(at) || (history.length && at <= history[history.length - 1].at)) return history;
  const valid = !stale && runtime && runtime.ratesStatus !== "unknown" && runtime.ratesStatus !== "none"
    && [runtime.uploadRate, runtime.downloadRate].every(value => Number.isFinite(value) && value >= 0);
  return [...history.filter(point => point.at >= at - 60_000), {
    at, upload: valid ? runtime.uploadRate : null, download: valid ? runtime.downloadRate : null,
  }];
}

export function ratePaths(history: RatePoint[], direction: "upload" | "download", maximum: number) {
  const last = history.at(-1)?.at || 0;
  const paths: string[] = [];
  let path = "", previousAt = 0;
  for (const point of history) {
    const value = point[direction];
    if (value === null) { if (path) paths.push(path); path = ""; previousAt = 0; continue; }
    if (previousAt && point.at - previousAt > 10_000) { if (path) paths.push(path); path = ""; }
    const x = 2 + (point.at - (last - 60_000)) / 60_000 * 236;
    const y = 37 - value / maximum * 33;
    path += `${path ? " L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`;
    previousAt = point.at;
  }
  if (path) paths.push(path);
  return paths;
}
