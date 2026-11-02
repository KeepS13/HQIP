import { useId, useState } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { Panel, bytes, date } from "./components";
import type { TrafficPoint } from "./types";
export default function TrafficChart({
  points = [],
  title = "代理流量趋势",
  subtitle = "上传 + 下载 · 按北京时间统计",
}: {
  points: TrafficPoint[];
  title?: string;
  subtitle?: string;
}) {
  const [range, setRange] = useState("24h"),
    id = useId().replace(/:/g, "");
  const cutoff = Date.now() / 1000 - (range === "24h" ? 86400 : 604800),
    data = (points || [])
      .filter((p) => p.at >= cutoff)
      .sort((a, b) => a.at - b.at);
  return (
    <Panel
      title={title}
      subtitle={subtitle}
      action={
        <div className="segmented" aria-label="流量时间范围">
          <button
            className={range === "24h" ? "active" : ""}
            onClick={() => setRange("24h")}
          >
            24 小时
          </button>
          <button
            className={range === "7d" ? "active" : ""}
            onClick={() => setRange("7d")}
          >
            7 天
          </button>
        </div>
      }
    >
      <div className="chart-legend">
        <span>
          <i className="legend-down" />
          下载流量
        </span>
        <span>
          <i className="legend-up" />
          上传流量
        </span>
      </div>
      <div className="traffic-chart">
        {data.length ? (
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart
              data={data}
              margin={{ top: 10, right: 16, bottom: 0, left: 4 }}
            >
              <defs>
                <linearGradient id={`${id}down`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#8e94ff" stopOpacity={0.27} />
                  <stop offset="100%" stopColor="#8e94ff" stopOpacity={0} />
                </linearGradient>
                <linearGradient id={`${id}up`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#65d9bb" stopOpacity={0.18} />
                  <stop offset="100%" stopColor="#65d9bb" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid
                vertical={false}
                stroke="#262c3c"
                strokeDasharray="3 5"
              />
              <XAxis
                dataKey="at"
                tickFormatter={(v) =>
                  new Date(v * 1000).toLocaleString("zh-CN", {
                    timeZone: "Asia/Shanghai",
                    ...(range === "7d"
                      ? { month: "numeric", day: "numeric" }
                      : { hour: "2-digit", minute: "2-digit", hour12: false }),
                  })
                }
                tick={{ fill: "#7e879d", fontSize: 11 }}
                axisLine={false}
                tickLine={false}
                minTickGap={50}
                dy={8}
              />
              <YAxis
                tickFormatter={(v) => bytes(v, 0)}
                tick={{ fill: "#7e879d", fontSize: 10 }}
                axisLine={false}
                tickLine={false}
                width={60}
              />
              <Tooltip
                contentStyle={{
                  background: "#202635",
                  border: "1px solid #3a4258",
                  borderRadius: 10,
                  fontSize: 12,
                  color: "#e1e5f2",
                }}
                labelFormatter={(label) => date(Number(label))}
                formatter={(value, name) => [
                  bytes(Number(value)),
                  name === "download" ? "下载" : "上传",
                ]}
              />
              <Area
                type="monotone"
                dataKey="download"
                stroke="#9296ff"
                strokeWidth={2}
                fill={`url(#${id}down)`}
                isAnimationActive={false}
              />
              <Area
                type="monotone"
                dataKey="upload"
                stroke="#65d9bb"
                strokeWidth={2}
                fill={`url(#${id}up)`}
                isAnimationActive={false}
              />
            </AreaChart>
          </ResponsiveContainer>
        ) : (
          <div className="chart-empty">
            <div className="chart-empty-line" />
            <span>尚无流量数据</span>
            <small>设备连接并产生流量后，趋势会显示在这里</small>
          </div>
        )}
      </div>
    </Panel>
  );
}
