import { useState } from "react";
import { Link } from "react-router-dom";
import { ArrowDown, ArrowRight, ChevronLeft, ChevronRight } from "lucide-react";
import { usePolling } from "./api";
import { Badge, Button, ErrorNotice, Loading, Panel, bytes, date } from "./components";
import type { NodeUserTrafficPage } from "./node-user-traffic";
import "./node-user-traffic.css";

const pageSize = 20;

// The caller keys this panel by nodeId; the page key also discards the previous
// page's cache immediately, so navigation never renders another page's users.
export function NodeUserTrafficPanel({ nodeId }: { nodeId: string }) {
  const [offset, setOffset] = useState(0);
  return <section id="user-traffic" aria-label="节点用户流量" className="node-user-traffic">
    <Panel title="用户流量排行" subtitle="该节点累计实际代理流量 · 上传 + 下载 · 不乘计费倍率">
      <p className="node-user-traffic-help">同一用户的多份订阅合并统计，保留历史使用记录；不含服务器自身流量及无法归属用户的流量。</p>
      <NodeUserTrafficPageView key={`${nodeId}:${offset}`} nodeId={nodeId} offset={offset} onPage={setOffset} />
    </Panel>
  </section>;
}

function NodeUserTrafficPageView({ nodeId, offset, onPage }: { nodeId: string; offset: number; onPage: (offset: number) => void }) {
  const { data, error, loading, refresh } = usePolling<NodeUserTrafficPage>(`/nodes/${encodeURIComponent(nodeId)}/user-traffic?offset=${offset}&limit=${pageSize}`, 30000);
  if (loading) return <Loading />;
  if (!data) return <ErrorNotice message={error} retry={() => void refresh()} />;
  return <>
    <ErrorNotice message={error ? `刷新失败，当前显示上次成功读取的记录。${error}` : ""} retry={() => void refresh()} />
    <div className="node-user-traffic-meta">
      <span>共 {data.totalUsers.toLocaleString("zh-CN")} 位用户</span>
      <span>读取时间 {date(data.generatedAt)} · 每 30 秒刷新</span>
    </div>
    {data.items.length > 0 && <p className="node-user-traffic-swipe">左右滑动表格查看上传、下载和总流量</p>}
    <NodeUserTrafficTable data={data} />
    <nav className="node-user-traffic-pages" aria-label="用户流量分页">
      <span>{data.items.length ? `第 ${offset + 1}–${offset + data.items.length} 位` : "当前页无记录"}</span>
      <div>
        <Button variant="secondary" icon={ChevronLeft} disabled={offset === 0} onClick={() => onPage(Math.max(0, offset - pageSize))}>上一页</Button>
        <Button variant="secondary" icon={ChevronRight} disabled={offset + pageSize >= data.totalUsers} onClick={() => onPage(offset + pageSize)}>下一页</Button>
      </div>
    </nav>
  </>;
}

export function NodeUserTrafficTable({ data }: { data: NodeUserTrafficPage }) {
  if (!data.items.length) return <p className="node-user-traffic-empty">{data.totalUsers ? "本页暂无用户，可返回上一页查看。" : "暂无用户使用记录。用户连接该节点后，会显示在此处。"}</p>;
  return <div className="node-user-traffic-scroll" role="region" aria-label="用户累计流量明细" tabIndex={0}>
    <table>
      <thead><tr><th scope="col">排名</th><th scope="col">用户</th><th scope="col">上传</th><th scope="col">下载</th><th scope="col" aria-sort="descending">总流量 <ArrowDown size={13} aria-hidden="true" /></th></tr></thead>
      <tbody>{data.items.map((user, index) => <tr key={user.userId}>
        <td className="node-user-traffic-rank">{data.offset + index + 1}</td>
        <td><Link className="node-user-traffic-user" to={`/users/${encodeURIComponent(user.userId)}`} title={user.email}><span>{user.email}</span><ArrowRight size={14} aria-hidden="true" /></Link>{!user.enabled && <Badge tone="muted">已停用</Badge>}</td>
        <td>{user.hasData ? bytes(user.upload) : "—"}</td>
        <td>{user.hasData ? bytes(user.download) : "—"}</td>
        <td><strong>{user.hasData ? bytes(user.total) : "暂无记录"}</strong></td>
      </tr>)}</tbody>
    </table>
  </div>;
}
