import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  ClipboardList,
  Globe2,
  Package,
  Plus,
  ShieldCheck,
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
  requestLabels,
  subscriptionLabels,
  type SubscriptionDetail,
  type SubscriptionRequest,
  type UserProfile,
  type UserSubscription,
} from "./types";
import "./subscriptions.css";

function SubscriptionStatus({ status }: { status: string }) {
  return (
    <Badge
      tone={
        status === "active"
          ? "success"
          : ["removed", "revoked"].includes(status)
            ? "muted"
            : "warning"
      }
    >
      {subscriptionLabels[status] || "待同步"}
    </Badge>
  );
}
function RequestStatus({ status }: { status: string }) {
  return (
    <Badge
      tone={
        status === "approved"
          ? "success"
          : status === "pending"
            ? "info"
            : "muted"
      }
    >
      {requestLabels[status] || status}
    </Badge>
  );
}
function SubscriptionCard({ value }: { value: UserSubscription }) {
  return (
    <article className="user-sub-card">
      <div className="user-sub-heading">
        <span className="user-sub-icon">
          <Package size={23} />
        </span>
        <SubscriptionStatus status={value.status} />
      </div>
      <h2>{value.templateName}</h2>
      <small className="user-sub-id">
        #{value.id.slice(-8)} · {date(value.createdAt)} 开通
      </small>
      <div className="user-sub-remaining">
        <strong>
          {value.quotaBytes === null
            ? "不限量"
            : bytes(Math.max(0, value.quotaBytes - value.usedBytes))}
        </strong>
        <span>剩余总额度</span>
      </div>
      <Meter value={value.usedBytes} max={value.quotaBytes} />
      <div className="user-sub-usage">
        <span>已用 {bytes(value.usedBytes)}</span>
        <span>
          共 {value.quotaBytes === null ? "不限量" : bytes(value.quotaBytes)}
        </span>
      </div>
      <p className="user-sub-reserved">
        离线应急已用 {bytes(value.emergencyUsedBytes || 0)} · 许可预留 {value.offlineReservations || 0} 个名额 · 待结算预留 {bytes(value.reservedBytes)}，与已用流量分别记录。
      </p>
      <dl className="user-sub-facts">
        <div>
          <dt>截止时间</dt>
          <dd>{date(value.expiresAt)}</dd>
        </div>
        <div>
          <dt>带宽</dt>
          <dd>
            {value.bandwidthMbps === null
              ? "不限速"
              : `${value.bandwidthMbps} Mbps`}
          </dd>
        </div>
        <div>
          <dt>VPN 连接</dt>
          <dd>
            {value.occupied} / {value.maxConnections}
          </dd>
        </div>
      </dl>
      <Link
        className="button button-secondary full-width"
        to={`/subscriptions/${value.id}`}
      >
        查看节点与导入链接 <ArrowRight size={16} />
      </Link>
    </article>
  );
}

export function UserOverview({ profile }: { profile: UserProfile }) {
  const subscriptions = profile.subscriptions || [],
    active = subscriptions.filter((s) => s.status === "active");
  return (
    <>
      <div className="portal-welcome">
        <div>
          <span className="eyebrow">YOUR CONNECTION, AT A GLANCE</span>
          <h1>每一份连接，都有自己的空间。</h1>
          <p>订阅独立计量，在客户端选择这一台设备要使用的流量包。</p>
        </div>
        <div className="portal-welcome-icon">
          <Globe2 size={64} strokeWidth={1} />
          <span />
          <i />
        </div>
      </div>
      <div className="user-sub-overview">
        <div>
          <Package size={22} />
          <section>
            <span>有效订阅</span>
            <strong>
              {active.length}
              <small> / {subscriptions.length} 份全部</small>
            </strong>
          </section>
        </div>
        <div>
          <ShieldCheck size={22} />
          <section>
            <span>各份独立使用</span>
            <strong>额度 · 有效期 · 带宽</strong>
          </section>
        </div>
        <Link to="/catalog">
          <Plus size={22} />
          <section>
            <span>发现下一份订阅</span>
            <strong>浏览订阅商城 →</strong>
          </section>
        </Link>
      </div>
      {active.length ? (
        <>
          <div className="user-sub-section-title">
            <h2>正在生效的订阅</h2>
            <Link to="/subscriptions" className="text-link">
              查看全部 →
            </Link>
          </div>
          <div className="user-sub-grid">
            {active.map((s) => (
              <SubscriptionCard key={s.id} value={s} />
            ))}
          </div>
        </>
      ) : (
        <Panel title="开始你的连接">
          <Empty
            icon={Package}
            title="还没有有效订阅"
            description="在商城选择一份订阅，付款核验成功后即可使用。"
            action={
              <Link to="/catalog" className="button button-primary">
                浏览订阅商城 <ArrowRight size={16} />
              </Link>
            }
          />
        </Panel>
      )}
      <Panel title="从这里开始">
        <div className="portal-start-grid">
          <Link to="/downloads">
            <span>
              <ArrowUpRight size={25} />
            </span>
            <div>
              <h3>下载 HQIP 客户端</h3>
              <p>登录后，先选订阅，再选节点。</p>
            </div>
            <ArrowRight size={19} />
          </Link>
          <Link to="/orders">
            <span>
              <ClipboardList size={25} />
            </span>
            <div>
              <h3>查看我的订单</h3>
              <p>付款与开通结果自动同步，开通时开始计时。</p>
            </div>
            <ArrowRight size={19} />
          </Link>
        </div>
      </Panel>
    </>
  );
}

function MyRequests() {
  const list = useUserData<SubscriptionRequest[]>(
    "/subscription-requests",
    10000,
  );
  return (
    <>
      <Notice>
        这里保留旧开通申请供查询。新的订阅请在商城购买，付款核验成功后自动开通。
      </Notice>
      <ErrorNotice
        message={
          list.error ? `${list.error}；当前显示上次成功读取的数据。` : ""
        }
        retry={() => void list.refresh()}
      />
      {list.loading ? (
        <Loading />
      ) : list.data?.length ? (
        <div className="user-request-list">
          {list.data.map((r) => (
            <article key={r.id}>
              <div className="user-request-heading">
                <div>
                  <h3>{r.templateName}</h3>
                  <small>
                    提交于 {date(r.requestedAt)} · #{r.id.slice(-8)}
                  </small>
                </div>
                <RequestStatus status={r.status} />
              </div>
              {r.note && (
                <p className="user-request-note">处理备注：{r.note}</p>
              )}
              {r.decidedAt && (
                <small className="muted">处理于 {date(r.decidedAt)}</small>
              )}
              {r.status === "approved" && r.subscriptionId && (
                <div className="user-sub-actions">
                  <Link
                    className="button button-secondary"
                    to={`/subscriptions/${r.subscriptionId}`}
                  >
                    查看已开通订阅 <ArrowRight size={15} />
                  </Link>
                </div>
              )}
            </article>
          ))}
        </div>
      ) : (
        <Empty
          icon={ClipboardList}
          title="没有历史申请"
          description="后续购买记录可在我的订单中查看。"
          action={
            <Link to="/orders" className="button button-secondary">
              我的订单
            </Link>
          }
        />
      )}
    </>
  );
}

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
            <span>剩余总额度</span>
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
            离线应急已用 {bytes(value.emergencyUsedBytes || 0)} · 许可预留 {value.offlineReservations || 0} 个名额 · 待结算预留 {bytes(value.reservedBytes)}
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
              <dt>VPN 连接</dt>
              <dd>
                {value.occupied} / {value.maxConnections}
              </dd>
            </div>
            <div>
              <dt>到期时间</dt>
              <dd>{date(value.expiresAt)}</dd>
            </div>
          </dl>
          <small className="muted">
            本订阅的全部连接共享带宽与额度；其他订阅独立计算。
          </small>
        </Panel>
      </div>
      {!active && (
        <Notice tone="warning">
          这份订阅{subscriptionLabels[value.status] || "暂时不可用"}
          ，不会自动使用其他流量包。请在客户端手动选择有效订阅。
        </Notice>
      )}
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
                  {n.publicIp || n.server || ""}
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
