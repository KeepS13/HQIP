import { useEffect, useReducer } from "react";
import { Link } from "react-router-dom";
import { Panel, Notice, bytes, date } from "./components";
import { billingTotal, billingUsageLabel } from "./billing";
import { formatTrafficRate } from "./traffic-analytics";
import { hostNetworkFresh, uniqueHostNetworks, type HostNetworkUsage } from "./host-network";
import type { Node } from "./types";
import "./host-network.css";

// One local expiry timer, no network request. It also expires a cached snapshot
// while polling backs off or pauses in a hidden tab; cleanup owns the timer.
function useHostNetworkClock(expiresAt: number) {
  const [, render] = useReducer((v: number) => v + 1, 0);
  useEffect(() => {
    const delay = expiresAt - Date.now();
    if (!Number.isFinite(delay) || delay < 0) return;
    const timer = setTimeout(render, Math.min(delay + 1, 2_147_483_647));
    return () => clearTimeout(timer);
  }, [expiresAt]);
  return Date.now();
}

/** These are different measurement boundaries, deliberately shown side by side. */
export function HostNetworkCompact({ value }: { value?: HostNetworkUsage }) {
  const now = useHostNetworkClock(Date.parse(value?.sample.at || "") + 30_000);
  return <div className="node-traffic-summary">
    <span>整机网卡累计 · 接收＋发送</span>
    <strong>{value ? bytes(value.receive + value.send) : "尚未采集"}</strong>
    <small>{value ? `接收 ${bytes(value.receive)} · 发送 ${bytes(value.send)}${hostNetworkFresh(value, now) ? "" : " · 数据过期"}` : "需升级 Agent 后采集"}</small>
    <small>包含用户代理、服务器自身及其他流量</small>
  </div>;
}

export function NodeTrafficSummary({ node }: { node: Node }) {
  return <>
    <div className="node-traffic-summary">
      <span>节点用户代理 · {billingUsageLabel(node)}</span>
      <strong>{bytes(billingTotal(node))}</strong>
      <small>累计 {bytes(node.totalUpload + node.totalDownload)} · 实际有效载荷</small>
    </div>
    <HostNetworkCompact value={node.hostNetwork} />
    <small className="muted">网卡为所属整机共享值，同机多个节点不重复相加</small>
  </>;
}

export function NodeTrafficTotals({ nodes }: { nodes: Node[] }) {
  const hosts = uniqueHostNetworks(nodes), known = hosts.filter((v): v is HostNetworkUsage => !!v);
  const expires = known.map(v => Date.parse(v.sample.at) + 30_000).filter(at => at >= Date.now());
  const now = useHostNetworkClock(Math.min(...expires));
  const total = known.reduce((sum, v) => sum + v.receive + v.send, 0);
  return <Panel className="host-network-panel" title="流量总览" subtitle="当前筛选范围 · 节点代理用量与网卡流量分别统计">
    <div className="usage-breakdown">
      <div><span>节点本周期代理合计</span><strong>{bytes(nodes.reduce((sum, n) => sum + billingTotal(n), 0))}</strong></div>
      <div><span>网卡已观测累计 · 按服务器去重</span><strong>{known.length ? bytes(total) : "尚未采集"}</strong></div>
    </div>
    <p className="billing-help">{known.length} / {hosts.length} 台服务器有网卡记录{known.some(v => !hostNetworkFresh(v, now)) ? "，含过期快照" : ""}。节点周期可能不同；网卡累计含首次采集时已有计数，不代表本月或本周期。</p>
  </Panel>;
}

export function HostNetworkPanel({ value, serverId }: { value?: HostNetworkUsage; serverId: string }) {
  const now = useHostNetworkClock(Date.parse(value?.sample.at || "") + 30_000);
  const live = hostNetworkFresh(value, now);
  return <Panel className="host-network-panel" title="服务器网卡总流量" subtitle="整台服务器 · 接收＋发送 · 不应用订阅计费倍率">
    {!value ? <Notice>尚未收到网卡采集记录。升级所属服务器的 Agent 后开始显示；未知用量不等于 0。</Notice> : <>
      {!live && <Notice tone="warning">网卡采集已过期，保留最近确认的累计；当前速率未知。</Notice>}
      <div className="usage-hero"><strong>{bytes(value.receive + value.send)}</strong></div>
      <div className="usage-breakdown">
        <div><span>累计接收</span><strong>{bytes(value.receive)}</strong></div>
        <div><span>累计发送</span><strong>{bytes(value.send)}</strong></div>
        <div><span>当前接收</span><strong>{!live || value.receiveRate == null ? "—" : formatTrafficRate(value.receiveRate)}</strong></div>
        <div><span>当前发送</span><strong>{!live || value.sendRate == null ? "—" : formatTrafficRate(value.sendRate)}</strong></div>
      </div>
      <p className="billing-help">首次采集 {date(value.firstObserved)} · 最近采样 {date(value.sample.at)}。累计包含首次采集时本次启动已有的网卡计数，未采集的历史不能还原。</p>
      {value.discontinuity && <Notice tone="warning">曾发生重启、网卡变化或计数器重置，保留已观测累计；断点前最后采样之后的流量可能缺失。</Notice>}
      <div className="table-wrap"><table><caption>当前启动的网卡计数 · 启动于 {date(value.sample.bootAt)}</caption><thead><tr><th>网卡</th><th>接收</th><th>发送</th><th>合计</th></tr></thead><tbody>
        {value.sample.interfaces.map(n => <tr key={n.index}><td>{n.name}</td><td>{bytes(n.receive)}</td><td>{bytes(n.send)}</td><td>{bytes(n.receive + n.send)}</td></tr>)}
      </tbody></table></div>
      {!!value.sample.excluded?.length && <p className="billing-help">排除重复转发层与回环：{value.sample.excluded.join("、")}</p>}
    </>}
    <dl className="details-list">
      <div><dt>节点用户流量</dt><dd>单独展示代理有效载荷；已包含在网卡总量内</dd></div>
      <div><dt>服务器自身与其他流量</dt><dd>已包含；独立分项未分类，不能用两类总量相减</dd></div>
      <div><dt>范围</dt><dd>系统更新、SSH、DNS、探测、管理同步、其他应用、协议开销与重传</dd></div>
    </dl>
    <p className="billing-help">同机所有节点共享该总量。网卡统计与节点代理统计的传输层级、起点不同，不重复相加。<Link to={`/servers/${serverId}`}>查看所属服务器</Link></p>
  </Panel>;
}
