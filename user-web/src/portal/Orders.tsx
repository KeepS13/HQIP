import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  Clock3,
  CreditCard,
  ExternalLink,
  ReceiptText,
  RefreshCw,
} from "lucide-react";
import {
  Badge,
  Button,
  Empty,
  ErrorNotice,
  Loading,
  Modal,
  Notice,
  PageHeader,
  Panel,
  date,
} from "../components";
import { userAPI } from "./api";
import {
  money,
  orderLabels,
  paymentURL,
  usePaymentData,
  type PaymentOrder,
  type PaymentOrderList,
} from "./payment-data";
import "./commerce.css";

function OrderStatus({ order }: { order: PaymentOrder }) {
  const tone =
    order.status === "fulfilled"
      ? "success"
      : order.paymentState === "paid"
        ? "info"
        : ["failed", "unknown"].includes(order.status)
          ? "warning"
          : "muted";
  const label =
    order.paymentState === "paid" && order.fulfillmentState === "blocked"
      ? "已付款，待处理"
      : order.orderType === "renewal" && order.status === "fulfilled" ? "已续费"
      : order.orderType === "renewal" && order.status === "paid_pending" ? "已付款，待续费"
      : orderLabels[order.status] || "待核实";
  return <Badge tone={tone}>{label}</Badge>;
}

function LastRefresh({ at, stale }: { at: number | null; stale: boolean }) {
  return at ? (
    <span className={`commerce-sync${stale ? " stale" : ""}`}>
      {stale ? "数据可能已过期 · " : "每 5 秒同步 · "}最近更新{" "}
      {date(new Date(at).toISOString())}
    </span>
  ) : null;
}

export function UserOrders() {
  const [page, setPage] = useState(1),
    [status, setStatus] = useState("");
  const list = usePaymentData<PaymentOrderList>(
    `/orders?page=${page}&pageSize=20&status=${encodeURIComponent(status)}`,
  );
  return (
    <>
      <PageHeader
        eyebrow="MY ORDERS"
        title="我的订单"
        description="查看新购与续费记录，以及付款核验和处理结果。"
        actions={
          <Link to="/catalog" className="button button-primary">
            购买订阅 <ArrowRight size={16} />
          </Link>
        }
      />
      <ErrorNotice
        message={list.error ? `${list.error}；保留最近一次订单状态。` : ""}
        retry={() => void list.refresh()}
      />
      <div className="user-sub-toolbar">
        <select
          aria-label="订单状态"
          value={status}
          onChange={(e) => {
            setPage(1);
            setStatus(e.target.value);
          }}
        >
          <option value="">全部状态</option>
          <option value="pending">待付款</option>
          <option value="paid_pending">已付款待处理</option>
          <option value="fulfilled">已完成</option>
          <option value="unknown">创建结果待核实</option>
          <option value="expired">付款入口已过期</option>
          <option value="closed">付款入口已关闭</option>
          <option value="failed">创建失败</option>
        </select>
        <Button
          icon={RefreshCw}
          variant="secondary"
          busy={list.refreshing}
          onClick={() => void list.refresh()}
        >
          刷新
        </Button>
      </div>
      <LastRefresh at={list.updatedAt} stale={!!list.error} />
      {list.loading ? (
        <Loading />
      ) : list.data?.items.length ? (
        <div className="commerce-orders">
          {list.data.items.map((order) => (
            <Link
              className="commerce-order-card"
              key={order.id}
              to={`/orders/${encodeURIComponent(order.id)}`}
            >
              <span className="user-sub-icon">
                <ReceiptText size={22} />
              </span>
              <div className="commerce-order-title">
                <h2>{order.templateName} · {order.orderType === "renewal" ? "续费" : "新购"}</h2>
                <small>{order.orderNo}</small>
                <span>
                  {date(order.createdAt)} · {order.orderType === "renewal" ? "续费" : "开通后"} {order.validityDays} 天
                </span>
              </div>
              <div className="commerce-order-end">
                <strong>{money(order.amountCents)}</strong>
                <OrderStatus order={order} />
              </div>
              <ArrowRight className="commerce-order-arrow" size={18} />
            </Link>
          ))}
        </div>
      ) : (
        <Panel title="订单记录">
          <Empty
            icon={ReceiptText}
            title={status ? "没有此状态的订单" : "还没有购买记录"}
            description="选择订阅并创建订单后，可以在这里继续付款和查看结果。"
            action={
              <Link to="/catalog" className="button button-secondary">
                浏览商城
              </Link>
            }
          />
        </Panel>
      )}
      {!!list.data?.total && (
        <div className="commerce-pagination">
          <span>
            共 {list.data.total} 笔 · 第 {list.data.page} 页
          </span>
          <div>
            <Button
              variant="secondary"
              disabled={page <= 1 || list.loading}
              onClick={() => setPage((p) => p - 1)}
            >
              上一页
            </Button>
            <Button
              variant="secondary"
              disabled={
                page * list.data.pageSize >= list.data.total || list.loading
              }
              onClick={() => setPage((p) => p + 1)}
            >
              下一页
            </Button>
          </div>
        </div>
      )}
    </>
  );
}

export function UserOrderDetail() {
  const { id = "" } = useParams();
  const detail = usePaymentData<PaymentOrder>(
    `/orders/${encodeURIComponent(id)}`,
  );
  const [now, setNow] = useState(Date.now()),
    [confirmClose, setConfirmClose] = useState<PaymentOrder | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const mutation = useRef(false);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  useEffect(() => {
    setConfirmClose(null);
    setError("");
  }, [id]);
  if (detail.loading) return <Loading />;
  if (!detail.data)
    return (
      <ErrorNotice
        message={detail.error || "订单不存在"}
        retry={() => void detail.refresh()}
      />
    );
  const order = detail.data;
  const left = Math.max(
    0,
    Math.ceil((Date.parse(order.expiresAt) - now) / 1000),
  );
  const url = paymentURL(order.payUrl);
  const canPay =
    order.status === "pending" &&
    order.paymentState === "unpaid" &&
    left > 0 &&
    !!url &&
    !detail.error;
  const canClose =
    order.paymentState === "unpaid" &&
    ["creating", "pending", "unknown"].includes(order.status);
  const paid = order.paymentState === "paid",
    fulfilled = order.fulfillmentState === "fulfilled";
  return (
    <>
      <Link className="back-link" to="/orders">
        <ArrowLeft size={15} /> 返回我的订单
      </Link>
      <PageHeader
        eyebrow="ORDER DETAILS"
        title={order.templateName}
        description={`订单号 ${order.orderNo}`}
        actions={
          <Button
            variant="secondary"
            icon={RefreshCw}
            busy={detail.refreshing}
            onClick={() => void detail.refresh()}
          >
            刷新结果
          </Button>
        }
      />
      <ErrorNotice
        message={
          detail.error
            ? `${detail.error}；当前状态可能已过期，恢复同步前暂停付款入口。`
            : ""
        }
        retry={() => void detail.refresh()}
      />
      <LastRefresh at={detail.updatedAt} stale={!!detail.error} />
      <section
        className={`commerce-payment-hero${fulfilled ? " complete" : ""}`}
      >
        <span className="commerce-payment-symbol">
          {fulfilled ? (
            <CheckCircle2 size={34} />
          ) : paid ? (
            <Clock3 size={34} />
          ) : (
            <CreditCard size={34} />
          )}
        </span>
        <div>
          <OrderStatus order={order} />
          <h2>
            {fulfilled
              ? order.orderType === "renewal" ? "套餐续费成功" : "你的新订阅已就绪"
              : paid
                ? "付款已确认"
                : order.status === "pending" && left > 0
                  ? "完成付款，开启新的连接"
                  : order.status === "unknown"
                    ? "正在等待支付创建结果"
                    : "查看本笔订单状态"}
          </h2>
          <p>
            {fulfilled
              ? order.orderType === "renewal" ? "总到期时间已延长，本期流量不变，下一期开始时重置。原节点与导入链接继续使用。" : "可在客户端手动选择这份订阅和节点，开始使用。"
              : paid
                ? order.orderType === "renewal" ? "续费付款已确认，请查看处理结果，勿再次付款。" : "订阅从实际开通时开始计时，请勿再次付款。"
                : order.status === "unknown"
                  ? "尚不能确认支付订单是否创建成功。请稍后刷新结果，避免重复下单。"
                  : "付款结果以服务器核验为准，支付页面返回不会直接改变订单状态。"}
          </p>
        </div>
        <strong className="commerce-order-amount">
          {money(order.amountCents)}
        </strong>
      </section>
      <div className="commerce-detail-grid">
        <Panel title="订单信息">
          <dl className="commerce-detail-facts">
            <div>
              <dt>购买内容</dt>
              <dd>{order.templateName} × 1 · {order.orderType === "renewal" ? "续费" : "新购"}</dd>
            </div>
            <div>
              <dt>购买有效期</dt>
              <dd>{order.orderType === "renewal" ? "顺延" : "实际开通后"} {order.validityDays} 天</dd>
            </div>
            {order.renewalSubscriptionId && <div><dt>续费目标</dt><dd><Link to={`/subscriptions/${encodeURIComponent(order.renewalSubscriptionId)}`}>#{order.renewalSubscriptionId.slice(-8)}</Link></dd></div>}
            {order.periodStartsAt && <div><dt>续费周期</dt><dd>{date(order.periodStartsAt)} — {date(order.periodEndsAt)}</dd></div>}
            <div>
              <dt>订单金额</dt>
              <dd>{money(order.amountCents)}</dd>
            </div>
            <div>
              <dt>创建时间</dt>
              <dd>{date(order.createdAt)}</dd>
            </div>
            <div>
              <dt>付款入口截止</dt>
              <dd>{date(order.expiresAt)}</dd>
            </div>
            {order.paidAt && (
              <div>
                <dt>付款确认</dt>
                <dd>{date(order.paidAt)}</dd>
              </div>
            )}
            {order.fulfilledAt && (
              <div>
                <dt>{order.orderType === "renewal" ? "续费完成" : "订阅开通"}</dt>
                <dd>{date(order.fulfilledAt)}</dd>
              </div>
            )}
          </dl>
        </Panel>
        <Panel title={fulfilled ? "查看订阅" : "付款与处理结果"}>
          <div className="commerce-payment-actions">
            {canPay && (
              <>
                <p className="commerce-countdown">
                  <Clock3 size={17} /> 剩余 {Math.floor(left / 60)} 分{" "}
                  {left % 60} 秒
                </p>
                <a
                  className="button button-primary full-width"
                  href={url!}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  前往支付宝付款 <ExternalLink size={16} />
                </a>
                <p className="commerce-hint">
                  请按收银台显示的金额付款，免签收款金额可能浮动。付款后回到此页查看结果。
                </p>
              </>
            )}
            {order.status === "pending" && left === 0 && (
              <Notice tone="warning">
                本笔订单的付款入口已过期，请勿继续使用旧收银台付款。已完成付款的通知核验后仍会处理。
              </Notice>
            )}
            {order.status === "pending" && left > 0 && !url && (
              <Notice tone="warning">
                暂时无法提供有效的支付链接，请刷新结果或联系管理员。
              </Notice>
            )}
            {["expired", "closed"].includes(order.status) && (
              <Notice>
                此处仅关闭付款入口；若已经付款，服务器收到有效通知后仍会处理，请勿重复付款。
              </Notice>
            )}
            {order.lastError && (
              <Notice tone="warning">{order.lastError}</Notice>
            )}
            {paid && !fulfilled && (
              <Notice
                tone={order.fulfillmentState === "blocked" ? "warning" : "info"}
              >
                {order.fulfillmentState === "blocked"
                  ? "本笔订单需要管理员处理。付款记录已保存，请提供订单号联系管理员。"
                  : order.orderType === "renewal" ? "付款已确认，正在处理续费。此页会自动更新结果。" : "付款已确认，正在开通订阅。此页会自动更新结果。"}
              </Notice>
            )}
            {fulfilled && order.subscriptionId && (
              <Link
                className="button button-primary full-width"
                to={`/subscriptions/${encodeURIComponent(order.subscriptionId)}`}
              >
                {order.orderType === "renewal" ? "查看已续费订阅" : "查看新订阅"} <ArrowRight size={16} />
              </Link>
            )}
            {canClose && (
              <Button
                variant="secondary"
                className="full-width"
                disabled={busy || !!detail.error}
                onClick={() => {
                  setError("");
                  setConfirmClose(order);
                }}
              >
                关闭付款入口
              </Button>
            )}
            {!canPay && !paid && (
              <Link to="/catalog" className="text-link">
                返回订阅商城 →
              </Link>
            )}
          </div>
        </Panel>
      </div>
      {confirmClose && (
        <Modal
          title="关闭本笔付款入口"
          description={confirmClose.orderNo}
          onClose={() => {
            if (!busy) setConfirmClose(null);
          }}
        >
          <p>
            关闭后不能从 HQIP
            继续付款。这不会撤销支付平台订单；{confirmClose.orderType === "renewal" ? "如果已经付款，将继续核验处理。订阅到期后核验的收款转人工处理，不会恢复过期订阅。" : "如果已经付款，有效通知到达后仍会开通订阅。"}
          </p>
          <ErrorNotice message={error} />
          <div className="modal-actions">
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => setConfirmClose(null)}
            >
              返回
            </Button>
            <Button
              busy={busy}
              onClick={async () => {
                if (mutation.current) return;
                mutation.current = true;
                setBusy(true);
                setError("");
                try {
                  await userAPI(
                    `/orders/${encodeURIComponent(order.id)}/close`,
                    "POST",
                    { expectedRevision: confirmClose.revision },
                  );
                  setConfirmClose(null);
                  await detail.refresh();
                } catch (e) {
                  setError((e as Error).message);
                  await detail.refresh();
                } finally {
                  mutation.current = false;
                  setBusy(false);
                }
              }}
            >
              确认关闭
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}
