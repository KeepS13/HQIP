import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowUpRight,
  Globe2,
  Package,
  Plus,
} from "lucide-react";
import {
  Badge,
  Empty,
  ErrorNotice,
  Loading,
  Meter,
  Notice,
  PageHeader,
  Panel,
  RefreshButton,
  bytes,
  date,
} from "../components";
import { useUserData } from "./api";
import {
  subscriptionLabels,
  type SubscriptionDetail,
  type UserSubscription,
} from "./types";
import { SubscriptionCard, SubscriptionStatus } from "./SubscriptionCard";
import { MyRequests } from "./SubscriptionRequests";
export { UserOverview } from "./UserOverview";
import "./subscriptions.css";
import { RenewalButton } from "./Renewal";

export function OwnedSubscriptions() {
  const list = useUserData<UserSubscription[]>("/subscriptions", 5000);
  const [params, setParams] = useSearchParams(),
    [status, setStatus] = useState("active");
  const requests = params.get("tab") === "requests",
    items = (list.data || []).filter(
      (s) => status === "all" || s.status === "active",
    );
  return (
    <>
      <PageHeader
        eyebrow="MY SUBSCRIPTIONS"
        title="我的订阅"
        description="每份流量包都有独立的额度和截止时间，可以同时持有并在不同设备使用。"
        actions={
          <Link className="button button-primary" to="/catalog">
            <Plus size={16} />
            购买新订阅
          </Link>
        }
      />
      <div className="user-sub-tabs" role="tablist" aria-label="订阅与历史申请">
        <button
          role="tab"
          aria-selected={!requests}
          className={!requests ? "active" : ""}
          onClick={() => setParams({})}
        >
          持有订阅
        </button>
        <button
          role="tab"
          aria-selected={requests}
          className={requests ? "active" : ""}
          onClick={() => setParams({ tab: "requests" })}
        >
          历史申请
        </button>
      </div>
      {requests ? (
        <MyRequests />
      ) : (
        <>
          <ErrorNotice
            message={
              list.error ? `${list.error}；当前显示上次成功读取的数据。` : ""
            }
            retry={() => void list.refresh()}
          />
          <div className="user-sub-toolbar">
            <select
              aria-label="订阅状态"
              value={status}
              onChange={(e) => setStatus(e.target.value)}
            >
              <option value="active">有效订阅</option>
              <option value="all">全部订阅</option>
            </select>
            <RefreshButton refresh={list.refresh} />
          </div>
          {list.loading ? (
            <Loading />
          ) : items.length ? (
            <div className="user-sub-grid">
              {items.map((s) => (
                <SubscriptionCard key={s.id} value={s} />
              ))}
            </div>
          ) : (
            <Panel title="持有订阅">
              <Empty
                icon={Package}
                title={
                  status === "active" && list.data?.length
                    ? "目前没有有效订阅"
                    : "还没有开通订阅"
                }
                description="支付成功或由管理员分配后，会在这里显示。"
                action={
                  <Link to="/catalog" className="button button-primary">
                    浏览订阅商城
                  </Link>
                }
              />
            </Panel>
          )}
        </>
      )}
    </>
  );
}

export function UserSubscriptionDetail() {
  const { id } = useParams();
  const detail = useUserData<SubscriptionDetail>(
    `/subscriptions/${encodeURIComponent(id || "")}`,
    5000,
  );
  if (!detail.data)
    return detail.loading ? (
      <Loading />
    ) : (
      <ErrorNotice message={detail.error} retry={() => void detail.refresh()} />
    );
  const { subscription: value, nodes } = detail.data,
    active = value.status === "active";
  return (
    <>
      <Link className="back-link" to="/subscriptions">
        <ArrowLeft size={15} /> 返回我的订阅
      </Link>
      <PageHeader
        eyebrow={`SUBSCRIPTION · ${value.id.slice(-8)}`}
        title={value.templateName}
        description={`开通于 ${date(value.createdAt)} · 有效至 ${date(value.expiresAt)}`}
        actions={
          <>
            <SubscriptionStatus status={value.status} />
            <RenewalButton value={value} />
            <RefreshButton refresh={detail.refresh} />
          </>
        }
      />
      <ErrorNotice
        message={detail.error ? `${detail.error}；当前显示最近一次数据。` : ""}
        retry={() => void detail.refresh()}
      />
      <div className="user-sub-detail-grid">
        <Panel title="这份订阅的用量">
          <div className="user-sub-remaining">
            <strong>
              {value.quotaBytes === null
                ? "不限量"
                : bytes(Math.max(0, value.quotaBytes - value.usedBytes))}
            </strong>
            <span>本期剩余额度</span>
          </div>
          <Meter value={value.usedBytes} max={value.quotaBytes} />
          <div className="user-sub-usage">
            <span>已用 {bytes(value.usedBytes)}</span>
            <span>
              共{" "}
              {value.quotaBytes === null ? "不限量" : bytes(value.quotaBytes)}
            </span>
          </div>
          <p className="user-sub-reserved">
            待结算 {bytes(value.reservedBytes)}
            ；实际可继续分配的流量会扣除预留。
          </p>
        </Panel>
        <Panel title="使用参数">
          <dl className="user-sub-facts">
            <div>
              <dt>带宽（每个方向）</dt>
              <dd>
                {value.bandwidthMbps === null
                  ? "不限速"
                  : `${value.bandwidthMbps} Mbps`}
              </dd>
            </div>
            <div>
              <dt>同时在线</dt>
              <dd>
                {value.occupied} / {value.maxConnections}
              </dd>
            </div>
            <div>
              <dt>总到期时间</dt>
              <dd>{date(value.expiresAt)}</dd>
            </div>
            <div><dt>本期结束</dt><dd>{date(value.currentPeriodEndsAt)}</dd></div>
            {value.nextResetAt && <div><dt>下次流量重置</dt><dd>{date(value.nextResetAt)}</dd></div>}
          </dl>
          <small className="muted">
            HY2 每个在线客户端实例占一个名额，VLESS 按来源 IP 去重，两者合计。全部连接共享本订阅的上限、带宽与流量额度。
          </small>
        </Panel>
      </div>
      {!active && (
        <Notice tone="warning">
          这份订阅{subscriptionLabels[value.status] || "暂时不可用"}
          ，不会自动使用其他流量包。请在客户端手动选择有效订阅。
        </Notice>
      )}
      {value.periods?.length > 1 && <Panel title="订阅周期" subtitle="按顺序生效，每期单独计量；剩余流量不结转。">
        <div className="user-request-list">{value.periods.map(period => <article key={period.id}>
          <div className="user-request-heading"><strong>{period.id === value.currentPeriodId ? "本期" : Date.parse(period.startsAt) > Date.now() ? "待生效" : "已结束"}</strong>
            {period.orderId && <Link className="text-link" to={`/orders/${encodeURIComponent(period.orderId)}`}>续费订单 →</Link>}</div>
          <p>{date(period.startsAt)} — {date(period.endsAt)}</p>
        </article>)}</div>
      </Panel>}
      <Panel
        title="可选节点"
        subtitle="在 HQIP 客户端先选择这份订阅，再选择节点。网页查看不会改变设备选择。"
      >
        {nodes?.length ? (
          <div className="portal-node-grid">
            {nodes.map((n) => (
              <article key={n.id} className="portal-node-card">
                <div className="portal-node-top">
                  <Globe2 size={22} />
                  <Badge tone={active && n.ready ? "success" : "warning"}>
                    {!active ? "订阅不可用" : n.ready ? "可以连接" : "待就绪"}
                  </Badge>
                </div>
                <h3>{n.name}</h3>
                <p>可选节点</p>
                <small className="muted">
                  {n.server || n.domain || ""}
                  {n.sni || n.domain ? ` · ${n.sni || n.domain}` : ""}
                </small>
              </article>
            ))}
          </div>
        ) : (
          <Empty
            icon={Globe2}
            title="暂无可用节点"
            description="管理员更新节点后将自动同步。"
          />
        )}
      </Panel>
      <Panel title="第三方客户端" subtitle="在独立页面为这份订阅导入客户端或导出配置。">
        <div className="client-import-body">
          <Link className="button button-primary" to={`/imports?subscription=${encodeURIComponent(value.id)}`}>
            订阅导入导出 <ArrowUpRight size={16} />
          </Link>
        </div>
      </Panel>
    </>
  );
}
