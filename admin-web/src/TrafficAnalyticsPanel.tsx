import { lazy, Suspense, useEffect, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ArrowDown, ArrowUp, CalendarDays, Download, RefreshCw } from "lucide-react";
import { Badge, Button, Empty, ErrorNotice, Loading, Panel, bytes, date } from "./components";
import { DateInput } from "./DatePicker";
import { dateRangeShortcut, type DateRange, type DateRangePreset } from "./date-shortcuts";
import { selectedTrafficRange, trafficCSV, trafficPresets, validTrafficDates } from "./traffic-analytics";
import { useTrafficReport } from "./useTrafficReport";
import "./traffic-analytics.css";

const DailyChart = lazy(() => import("./TrafficDailyChart"));

export default function TrafficAnalyticsPanel({ serverId, refreshVersion = 0 }: { serverId?: string; refreshVersion?: number }) {
  const [params, setParams] = useSearchParams();
  const range = selectedTrafficRange(params);
  const [validation, setValidation] = useState("");
  const [revision, setRevision] = useState(0);
  const [page, setPage] = useState(1);
  const [allContributors, setAllContributors] = useState(false);
  const query = new URLSearchParams({ from: range.from, to: range.to });
  if (serverId) query.set("serverId", serverId);
  const key = `/traffic/analytics?${query}`;
  const { data, loading, refreshing, error } = useTrafficReport(key, revision + refreshVersion, range.to === dateRangeShortcut("today").to);
  useEffect(() => {
    setValidation(""); setPage(1); setAllContributors(false);
  }, [range.from, range.to, serverId]);
  const today = data?.today || dateRangeShortcut("today").to;
  const earliest = new Date(`${today}T00:00:00Z`);
  earliest.setUTCMonth(earliest.getUTCMonth() - 13);
  const minDate = data?.minDate || earliest.toISOString().slice(0, 10);
  function choose(preset: DateRangePreset | "custom", dates?: DateRange) {
    const next = new URLSearchParams(params);
    next.set("trafficRange", preset);
    if (preset === "custom" && dates) { next.set("trafficFrom", dates.from); next.set("trafficTo", dates.to); }
    else { next.delete("trafficFrom"); next.delete("trafficTo"); }
    setParams(next);
    setValidation("");
  }
  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const dates = { from: String(form.get("from") || ""), to: String(form.get("to") || "") };
    if (!validTrafficDates(dates, minDate, today)) { setValidation(`请选择 ${minDate} 至 ${today} 之间有效的起止日期`); return; }
    choose("custom", dates);
    setRevision(value => value + 1);
  }
  function download() {
    if (!data) return;
    const url = URL.createObjectURL(new Blob([trafficCSV(data)], { type: "text/csv;charset=utf-8" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `HQIP-${serverId ? "server-traffic" : "traffic"}-${data.from}_${data.to}.csv`;
    anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  const days = data ? [...data.days].reverse() : [];
  const pages = Math.max(1, Math.ceil(days.length / 20));
  const dayPage = Math.min(page, pages);
  const summary = data?.summary;
  const summaryValue = (value: number | undefined) => summary?.hasData ? bytes(value) : "—";
  const selected = data ? `${data.from} 至 ${data.to}` : `${range.from} 至 ${range.to}`;
  return <Panel className="traffic-analytics" title={serverId ? "服务器代理流量统计" : "代理流量统计"}
    subtitle="按北京时间统计上传与下载 · 含已删除节点历史 · 不含探测" action={<div className="traffic-actions">
      <Button variant="secondary" icon={RefreshCw} busy={refreshing} onClick={() => setRevision(value => value + 1)}>刷新</Button>
      <Button variant="secondary" icon={Download} disabled={!data || refreshing || !!error} onClick={download}>导出 CSV</Button>
    </div>}>
    <div className="traffic-filter">
      <div className="traffic-presets" role="group" aria-label="流量统计快捷日期">
        {trafficPresets.map(([preset, label]) => <button type="button" key={preset} aria-pressed={range.preset === preset} onClick={() => choose(preset)}>{label}</button>)}
        <span className={range.preset === "custom" ? "is-custom" : ""}>自定义日期</span>
      </div>
      <form key={key} className="traffic-dates" onSubmit={search}>
        <CalendarDays size={16} />
        <label>开始日期<DateInput name="from" aria-label="流量开始日期" required min={minDate} max={today} defaultValue={range.from} /></label>
        <span>至</span>
        <label>结束日期<DateInput name="to" aria-label="流量结束日期" required min={minDate} max={today} defaultValue={range.to} /></label>
        <Button type="submit" variant="secondary">查询</Button><small>北京时间 · 最多保留 13 个月</small>
      </form>
      <ErrorNotice message={validation} />
    </div>
    <div className="traffic-range-caption"><span>{selected}{data && ` · ${data.dayCount} 天`}</span><span>{data?.includesToday && <Badge tone="info">今日截至当前</Badge>} {error && data && <Badge tone="warning">数据已过期</Badge>}</span></div>
    <ErrorNotice message={error ? `${error}${data ? "；显示上次成功数据（已过期）。" : ""}` : ""} retry={() => setRevision(value => value + 1)} />
    {loading ? <div className="traffic-loading"><Loading /></div> : data && <>
      <div className="traffic-totals">
        {[["区间总消耗", summaryValue(summary?.total)], ["上传流量", summaryValue(summary?.upload)], ["下载流量", summaryValue(summary?.download)], ["日均消耗", summaryValue(summary?.dailyAverage)]].map(([label, value]) => <div key={label}><small>{label}</small><strong>{value}</strong></div>)}
      </div>
      <div className="traffic-chart-heading"><div className="chart-legend"><span><i className="legend-down" />下载</span><span><i className="legend-up" />上传</span></div><small>{summary?.hasData ? `最高消耗日 ${summary.peakDate}` : "暂无用量记录"}</small></div>
      <div className="traffic-daily-chart" aria-label="每日代理用量趋势">
        {summary?.hasData ? <Suspense fallback={<Loading />}><DailyChart days={data.days} /></Suspense> : <Empty icon={CalendarDays} title="暂无用量记录" description="此区间可能尚未使用代理，或历史用量尚未上报。" />}
      </div>
      <div className="traffic-record-note">日均 = 已记录总量 ÷ 所选天数（含今天）；无记录日期不表示已确认零用量。离线补报后统计会更新。</div>
      <div className="traffic-breakdown">
        <section className="traffic-day-table"><h3>每日明细 <small>{data.summary.recordedDays} / {data.dayCount} 天有记录</small></h3>
          <div className="table-scroll"><table><thead><tr><th>日期</th><th><ArrowUp size={12} /> 上传</th><th><ArrowDown size={12} /> 下载</th><th>合计</th><th>状态</th></tr></thead>
            <tbody>{days.slice((dayPage - 1) * 20, dayPage * 20).map(day => <tr key={day.date}><td>{day.date}{day.hasData && day.date === summary?.peakDate && <span className="traffic-peak">最高</span>}</td><td>{day.hasData ? bytes(day.upload) : "—"}</td><td>{day.hasData ? bytes(day.download) : "—"}</td><td><strong>{day.hasData ? bytes(day.total) : "—"}</strong></td><td><span className={day.hasData ? "" : "muted"}>{day.hasData ? (day.date === data.today ? "截至当前" : "已记录") : "暂无记录"}</span></td></tr>)}</tbody>
          </table></div>
          {pages > 1 && <div className="traffic-pagination"><span>{dayPage} / {pages} 页 · 每页 20 天</span><Button variant="ghost" disabled={dayPage <= 1} onClick={() => setPage(dayPage - 1)}>上一页</Button><Button variant="ghost" disabled={dayPage >= pages} onClick={() => setPage(dayPage + 1)}>下一页</Button></div>}
        </section>
        <section className="traffic-ranking"><h3>{serverId ? "节点流量贡献" : "服务器消耗排行"}<small>当前所选区间</small></h3>
          {data.contributors.length ? <ol>{(allContributors ? data.contributors : data.contributors.slice(0, 10)).map((part, index) => {
            const share = summary?.total ? part.total / summary.total * 100 : 0;
            const destination = `/${data.contributorKind === "node" ? "nodes" : "servers"}/${encodeURIComponent(part.id)}?${new URLSearchParams({ trafficRange: "custom", trafficFrom: data.from, trafficTo: data.to })}`;
            return <li key={part.id}><span className="traffic-rank-number">{index + 1}</span><div className="traffic-rank-main"><div>{part.id && (!part.deleted || data.contributorKind === "node") ? <Link to={destination}>{part.name}</Link> : <span>{part.name}</span>}{part.deleted && <small>历史</small>}<strong>{bytes(part.total)}</strong></div><div className="traffic-rank-meter"><i style={{ width: `${share}%` }} /></div><p>↑ {bytes(part.upload)} · ↓ {bytes(part.download)} <span>{share.toFixed(1)}%</span></p></div></li>;
          })}</ol> : <p className="traffic-no-contributors">此区间暂无贡献记录</p>}
          {data.contributors.length > 10 && <button type="button" className="text-button" onClick={() => setAllContributors(value => !value)}>{allContributors ? "收起" : `查看全部 ${data.contributors.length} 项`}</button>}
        </section>
      </div>
      <div className="traffic-updated">最近更新 {date(data.generatedAt)} · {data.includesToday ? "每 30 秒刷新" : "历史区间，按需刷新"}</div>
    </>}
  </Panel>;
}
