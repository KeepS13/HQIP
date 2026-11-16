import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { bytes } from "./components";
import type { TrafficDay } from "./traffic-types";

export default function TrafficDailyChart({ days }: { days: TrafficDay[] }) {
  const data = days.map(day => ({ ...day, upload: day.hasData ? day.upload : null, download: day.hasData ? day.download : null }));
  return <ResponsiveContainer width="100%" height="100%">
    <BarChart data={data} margin={{ top: 12, right: 18, bottom: 8, left: 4 }} accessibilityLayer>
      <CartesianGrid vertical={false} stroke="#2b3242" strokeDasharray="3 5" />
      <XAxis dataKey="date" tickFormatter={day => String(day).slice(5)} tick={{ fill: "#98a4bf", fontSize: 11 }} axisLine={false} tickLine={false} minTickGap={35} dy={8} />
      <YAxis tickFormatter={value => bytes(Number(value), 1)} tick={{ fill: "#98a4bf", fontSize: 10 }} axisLine={false} tickLine={false} width={65} />
      <Tooltip cursor={{ fill: "#9195ff0c" }} contentStyle={{ background: "#202635", border: "1px solid #3a4258", borderRadius: 10, color: "#e1e5f2", fontSize: 12 }}
        labelFormatter={label => `${label} · 北京时间`} formatter={(value, name) => [bytes(Number(value)), name === "download" ? "下载" : "上传"]} />
      <Bar dataKey="download" stackId="usage" fill="#999bff" maxBarSize={38} isAnimationActive={false} />
      <Bar dataKey="upload" stackId="usage" fill="#65d9bb" maxBarSize={38} radius={[4, 4, 0, 0]} isAnimationActive={false} />
    </BarChart>
  </ResponsiveContainer>;
}
