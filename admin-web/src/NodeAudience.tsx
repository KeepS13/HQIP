import { Radio, UsersRound } from "lucide-react";
import type { Node } from "./types";
import "./node-audience.css";

export type NodeAudienceStats = {
  subscribedUsers: number;
  activeSubscriptions: number;
  onlineUsers: number | null;
  onlineConnections: number | null;
  onlineReportedAt: string | null;
};
export type NodeListItem = Node & { audience?: NodeAudienceStats };

export function NodeAudience({ value }: { value?: NodeAudienceStats }) {
  const reported = value?.onlineReportedAt ? Date.parse(value.onlineReportedAt) : NaN;
  const known = value?.onlineUsers != null && value.onlineConnections != null &&
    Number.isFinite(reported) && Date.now() - reported < 30000;
  return <div className="node-audience" role="group" aria-label="订阅与在线使用">
    <div title="当前有效订阅覆盖此节点的用户数；同一用户持有多份订阅只计一人。">
      <span className="node-audience-label"><UsersRound size={13} />订阅用户</span>
      <strong>{value ? <>{value.subscribedUsers}<small>人</small></> : "—"}</strong>
      <span className="node-audience-note">{value ? `${value.activeSubscriptions} 份有效订阅` : "等待统计"}</span>
    </div>
    <div title="按最近 30 秒的健康节点在线报告统计，同一用户的多个连接合并计人；不含系统探测。">
      <span className="node-audience-label"><Radio size={13} />正在使用</span>
      <strong className={!known ? "node-audience-unknown" : ""}>{known ? <>{value!.onlineUsers}<small>人</small></> : "待更新"}</strong>
      <span className="node-audience-note">{known ? `${value!.onlineConnections} 个在线连接` : "等待有效在线报告"}</span>
    </div>
  </div>;
}
