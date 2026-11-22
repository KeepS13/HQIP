import { useState } from "react";
import { Cpu, HardDrive, MemoryStick, RefreshCw } from "lucide-react";
import { useHostResources, type Usage } from "./useHostResources";
import "./HostResources.css";

function capacity(value: number) {
  if (value >= 1024 ** 4) return `${(value / 1024 ** 4).toFixed(1)} TiB`;
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)} GiB`;
  return `${(value / 1024 ** 2).toFixed(0)} MiB`;
}

export function HostResources() {
  const { data, error, loading, refresh } = useHostResources();
  const [busy, setBusy] = useState(false);
  const sample = data?.sample;
  const age = sample ? Date.now() - Date.parse(sample.sampledAt) : 0;
  const stale = !!error || !!sample && (!Number.isFinite(age) || age > 20_000);
  const validUsage = (value: Usage | null | undefined) => value && Number.isFinite(value.used) && Number.isFinite(value.total) && value.total > 0 && value.used >= 0 && value.used <= value.total ? value : null;
  const memory = validUsage(sample?.memory), disk = validUsage(sample?.disk);
  const cpu = sample?.cpuPercent != null && Number.isFinite(sample.cpuPercent) && sample.cpuPercent >= 0 && sample.cpuPercent <= 100 ? sample.cpuPercent : null;
  const hasSample = cpu !== null || memory !== null || disk !== null;
  const status = stale ? (sample && hasSample ? "数据已过期" : "暂不可用") : loading || !sample ? "正在采集…" : !hasSample ? "暂不可用" : cpu === null || !memory || !disk ? "部分指标待采集" : "每 5 秒更新";
  const rows = [
    { label: "CPU", icon: Cpu, value: cpu === null ? "—" : `${cpu.toFixed(1)}%`, percent: cpu, title: "控制服务所在主机的 CPU 使用率；首次需要两次采样" },
    { label: "内存", icon: MemoryStick, value: memory ? `${capacity(memory.used)} / ${capacity(memory.total)}` : "—", percent: memory ? memory.used / memory.total * 100 : null, title: "控制服务所在主机的已用 / 总内存" },
    { label: "磁盘", icon: HardDrive, value: disk ? `${capacity(disk.used)} / ${capacity(disk.total)}` : "—", percent: disk ? disk.used / disk.total * 100 : null, title: "服务数据分区的已用 / 总容量" },
  ];
  return <section className={`host-resources${stale ? " is-stale" : ""}`} aria-label="控制台主机资源">
    <div className="host-resources-heading"><strong>控制台主机</strong><button type="button" aria-label="刷新主机资源" title="刷新主机资源" disabled={busy || loading} onClick={async () => { setBusy(true); try { await refresh(); } finally { setBusy(false); } }}><RefreshCw size={13} className={busy ? "is-refreshing" : ""} /></button></div>
    {rows.map(({ label, icon: Icon, value, percent, title }) => <div className="host-resource-row" key={label} title={title}>
      <div className="host-resource-value"><span><Icon size={12} />{label}</span><strong>{value}</strong></div>
      <div className={`host-resource-track${percent === null ? " unavailable" : ""}`} role="progressbar" aria-label={`${label}使用率`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent ?? undefined} aria-valuetext={percent === null ? "暂无数据" : stale ? `${value}，数据已过期` : value}><span style={{ width: `${percent ?? 0}%` }} /></div>
    </div>)}
    <div className="host-resources-footnote"><span>{status}</span><span title="磁盘仅统计控制服务数据目录所在分区">服务数据分区</span></div>
  </section>;
}
