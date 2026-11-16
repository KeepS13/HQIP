import { useEffect, useState } from "react";
import { Activity, ArrowDown, ArrowUp, Users } from "lucide-react";
import { StatCard, date } from "./components";
import { formatTrafficRate, ratePaths, recordRatePoint, type RatePoint } from "./traffic-analytics";
import type { RuntimeOverview } from "./traffic-types";
import "./traffic-analytics.css";

export default function RuntimeCards({ runtime, serverTime, stale }: { runtime?: RuntimeOverview; serverTime: string; stale: boolean }) {
  const [history, setHistory] = useState<RatePoint[]>([]);
  useEffect(() => { setHistory(previous => recordRatePoint(previous, runtime, serverTime, stale)); }, [runtime, serverTime, stale]);
  const onlineUnknown = !runtime || runtime.onlineStatus === "unknown";
  const onlinePartial = runtime?.onlineStatus === "partial";
  const rateUnknown = !runtime || runtime.ratesStatus === "unknown";
  const ratePartial = runtime?.ratesStatus === "partial";
  const maximum = Math.max(1, ...history.flatMap(point => [point.upload || 0, point.download || 0]));
  const idle = runtime?.ratesStatus === "complete" && runtime.uploadRate === 0 && runtime.downloadRate === 0;
  return <>
    <StatCard label="在线用户" value={onlineUnknown ? "—" : `${onlinePartial ? "≥ " : ""}${runtime!.onlineUsers}`} unit={onlineUnknown ? "" : "人"} icon={Users} color="green"
      hint={<><span>{onlineUnknown ? "在线状态未知" : `${runtime!.activeSubscriptions} 份活跃订阅 · ${runtime!.instances} 个客户端连接`}</span>
        {onlinePartial && <span>· 数据不完整</span>}{!!runtime?.unmappedInstances && <span>· {runtime.unmappedInstances} 个连接身份待识别</span>}{stale && <span>· 已过期</span>}</>} />
    <div className={`stat-card runtime-rate-card${stale ? " is-stale" : ""}`}>
      <div className="stat-top"><span>实时代理速率（含探测）</span><div className="stat-icon icon-amber"><Activity size={18} /></div></div>
      <div className="runtime-rates"><div><span><ArrowDown size={13} />下载</span><strong>{rateUnknown ? "—" : `${ratePartial ? "≥ " : ""}${formatTrafficRate(runtime!.downloadRate)}`}</strong></div><div><span><ArrowUp size={13} />上传</span><strong>{rateUnknown ? "—" : `${ratePartial ? "≥ " : ""}${formatTrafficRate(runtime!.uploadRate)}`}</strong></div></div>
      <svg className="runtime-sparkline" viewBox="0 0 240 42" preserveAspectRatio="none" role="img" aria-label="最近一分钟上传与下载速率，缺失采样留空">
        <path d="M0 38H240" stroke="#343c50" strokeWidth="0.5" />
        {(["download", "upload"] as const).map(direction => ratePaths(history, direction, maximum).map((path, index) => <path key={`${direction}-${index}`} d={path} fill="none" stroke={direction === "download" ? "#999bff" : "#65d9bb"} strokeWidth="1.7" vectorEffect="non-scaling-stroke" strokeLinecap="round" />))}
      </svg>
      <div className="stat-hint">{rateUnknown ? "当前速率未知" : runtime?.ratesStatus === "none" ? "尚无已安装节点" : idle ? "当前空闲" : ratePartial ? "已知速率 · 数据不完整" : "最近 1 分钟 · 每 5 秒刷新"}{stale && <span>· 已过期</span>}</div>
      <div className="runtime-reported">最近上报 {runtime?.lastReportedAt ? date(runtime.lastReportedAt) : "尚无有效报告"}</div>
    </div>
  </>;
}
