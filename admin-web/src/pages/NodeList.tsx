import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ArrowRight, ChevronRight, Clock3, Globe2, Plus, Search, Server, ShieldCheck } from "lucide-react";
import { usePolling } from "../api";
import { billingFor } from "../billing";
import { BillingSummary } from "./ServerBilling";
import { Button, Empty, ErrorNotice, Loading, NodeBadge, PageHeader, Panel, Rate, RefreshButton, fresh } from "../components";
import { NodeAudience, type NodeListItem } from "../NodeAudience";
import { AddNode } from "./AddNode";
import { compareResourceNames } from "../resource-order";
import { NodeTrafficSummary, NodeTrafficTotals } from "../HostNetwork";

export function Nodes() {
  const { data, error, loading, refresh } = usePolling<NodeListItem[]>("/nodes"),
    [search, setSearch] = useState(""),
    [params, setParams] = useSearchParams();
  const add = params.get("add") === "1",
    nodes = (data || []).filter((n) =>
      `${n.name} ${n.domain} ${n.publicIp}`
        .toLowerCase()
        .includes(search.toLowerCase()),
    ).sort(compareResourceNames);
  function close() {
    setParams((p) => {
      p.delete("add");
      p.delete("server");
      p.delete("address");
      return p;
    });
  }
  return (
    <>
      <PageHeader
        eyebrow="NODE MANAGEMENT"
        title="节点管理"
        description="按公网 IP 独立部署、分配和管理节点。"
        actions={
          <>
            <RefreshButton refresh={refresh} />
            <Button icon={Plus} onClick={() => setParams({ add: "1" })}>
              添加节点
            </Button>
          </>
        }
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <div className="list-toolbar">
        <div className="list-count">
          全部节点 <span>{data?.length || 0}</span>
        </div>
        <div className="search-input">
          <Search size={16} />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="搜索节点"
            placeholder="搜索名称、域名或 IP"
          />
        </div>
      </div>
      {!loading && nodes.length > 0 && <NodeTrafficTotals nodes={nodes} />}
      {loading ? (
        <Loading />
      ) : nodes.length ? (
        <div className="node-grid">
          {nodes.map((n) => (
            <article className="node-card" key={n.id}>
              <div className="node-card-heading">
                <span className="node-card-icon">
                  <Server size={22} strokeWidth={1.6} />
                </span>
                <NodeBadge node={n} />
              </div>
              <Link to={`/nodes/${n.id}`} className="node-card-title">
                <h2>{n.name}</h2>
                <ChevronRight size={17} />
              </Link>
              <div className="node-address">
                <Globe2 size={13} />
                {n.domain}
              </div>
              <div className="node-card-divider" />
              <div className="node-card-data">
                <div>
                  <span>公网 IP</span>
                  <strong className="mono">{n.publicIp}</strong>
                </div>
                <div>
                  <span>传输协议</span>
                  <strong>
                    Hysteria 2 <small>UDP :{n.port}</small>
                  </strong>
                </div>
              </div>
              <NodeAudience value={n.audience} />
              <NodeTrafficSummary node={n} />
              {!billingFor(n).cycleComplete && <p className="billing-help">数据不完整，按已知用量计算</p>}
              <BillingSummary resource={n} compact followsServer />
              <div className="node-card-bottom">
                {fresh(n) ? (
                  <Rate
                    upload={n.metrics.uploadRate}
                    download={n.metrics.downloadRate}
                  />
                ) : (
                  <span className="muted">
                    <Clock3 size={12} /> 等待节点心跳
                  </span>
                )}
                <Link to={`/nodes/${n.id}`}>
                  管理 <ArrowRight size={14} />
                </Link>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <Panel title={search ? "搜索结果" : "建立你的私人网络"}>
          <Empty
            icon={Server}
            title={search ? "没有匹配的节点" : "还没有添加节点"}
            description={
              search
                ? "试试其他名称、域名或 IP。"
                : "先登记服务器与公网 IP，再选择域名部署独立 Hysteria 2 节点。"
            }
            action={
              !search && (
                <Button icon={Plus} onClick={() => setParams({ add: "1" })}>
                  添加第一个节点
                </Button>
              )
            }
          />
        </Panel>
      )}
      <div className="footnote">
        <ShieldCheck size={14} />
        网站仅显示通过真实代理测试的可用节点；节点代理用量不含探测，整机网卡总量包含探测及系统流量。
      </div>
      {add && <AddNode initialServerId={params.get("server") || ""} initialAddressId={params.get("address") || ""} onClose={close} />}
    </>
  );
}

