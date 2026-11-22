import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  CalendarClock,
  CheckCircle2,
  CreditCard,
  ReceiptText,
  RefreshCw,
  Search,
} from "lucide-react";
import { api } from "../api";
import { DateInput, DateRangeShortcuts } from "../DatePicker";
import {
  Badge,
  Button,
  CopyButton,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Modal,
  Notice,
  PageHeader,
  Panel,
  RefreshButton,
  date,
  useToast,
} from "../components";
import {
  type PaymentOrder,
  type PaymentOrderPage,
  type PaymentSettings,
  orderStates,
  priceText,
  usePaymentData,
} from "../payments";
import "./payments.css";

function OrderBadge({ order }: { order: PaymentOrder }) {
  const state = orderStates[order.status] || {
    label: "状态待核实",
    tone: "warning",
  };
  return <Badge tone={state.tone}>{order.orderType === "renewal" && order.status === "fulfilled" ? "已续费" : order.orderType === "renewal" && order.status === "paid_pending" ? "已付款 · 待处理" : state.label}</Badge>;
}

function UpdatedAt({ at, stale }: { at: string | null; stale: boolean }) {
  return (
    <p
      className={`payment-updated ${stale ? "payment-stale" : ""}`}
      aria-live="polite"
    >
      {at
        ? `${stale ? "刷新失败，保留上次数据" : "每 5 秒刷新"} · 最近更新 ${date(at)}`
        : "正在读取订单状态"}
    </p>
  );
}

function nextBeijingMidnight(day: string) {
  // Date inputs are calendar dates. Advance in UTC to avoid browser timezone
  // and daylight-saving changes, then explicitly interpret the next day in UTC+8.
  const next = new Date(`${day}T00:00:00Z`);
  next.setUTCDate(next.getUTCDate() + 1);
  return `${next.toISOString().slice(0, 10)}T00:00:00+08:00`;
}

export function Orders() {
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState("");
  const [search, setSearch] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [draft, setDraft] = useState({ search: "", from: "", to: "" });
  const [filterError, setFilterError] = useState("");
  const query = new URLSearchParams({ page: String(page), pageSize: "20" });
  if (status) query.set("status", status);
  if (search) query.set("search", search);
  if (from) query.set("from", `${from}T00:00:00+08:00`);
  if (to) query.set("to", nextBeijingMidnight(to));
  const orders = usePaymentData<PaymentOrderPage>(`/orders?${query}`);
  const list = orders.data?.items || [];
  const total = orders.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / 20));

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    if (new TextEncoder().encode(draft.search.trim()).length > 120) {
      setFilterError(
        "搜索内容最多 120 字节（通常为 120 个英文字符或 40 个汉字），请缩短后再筛选",
      );
      return;
    }
    if (draft.from && draft.to && draft.from > draft.to) {
      setFilterError("开始日期不能晚于结束日期");
      return;
    }
    setFilterError("");
    setSearch(draft.search.trim());
    setFrom(draft.from);
    setTo(draft.to);
    setPage(1);
  }

  return (
    <>
      <PageHeader
        eyebrow="PAYMENT ORDERS"
        title="订单管理"
        description="查看新购、续费及付款核验结果，处理已付款异常订单。"
        actions={
          <>
            <RefreshButton refresh={orders.refresh} />
            <Link to="/payments" className="button button-secondary">
              支付设置 <ArrowRight size={15} />
            </Link>
          </>
        }
      />
      <ErrorNotice
        message={
          orders.error
            ? `${orders.error}；${orders.data ? "当前保留最近一次数据。" : "请稍后重试。"}`
            : ""
        }
        retry={() => void orders.refresh()}
      />
      <Panel
        className="payment-panel"
        title="全部订单"
        subtitle="按创建时间筛选，日期使用北京时间。"
        action={
          <Link to="/subscription-requests" className="text-link">
            旧开通申请历史
          </Link>
        }
      >
        <form className="payment-filters" onSubmit={applyFilters}>
          <Field label="查找订单" hint="最多 120 字节，汉字通常占 3 字节。">
            <input
              value={draft.search}
              onChange={(e) => setDraft({ ...draft, search: e.target.value })}
              placeholder="订单号、邮箱或订阅名称"
            />
          </Field>
          <Field label="订单状态">
            <select
              value={status}
              onChange={(e) => {
                setStatus(e.target.value);
                setPage(1);
              }}
            >
              <option value="">全部状态</option>
              {Object.entries(orderStates).map(([value, state]) => (
                <option value={value} key={value}>
                  {state.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="开始日期">
            <DateInput
              value={draft.from}
              onChange={(e) => setDraft({ ...draft, from: e.target.value })}
              max={draft.to || undefined}
            />
          </Field>
          <Field label="结束日期">
            <DateInput
              value={draft.to}
              onChange={(e) => setDraft({ ...draft, to: e.target.value })}
              min={draft.from || undefined}
            />
          </Field>
          <Button type="submit" icon={Search}>
            筛选
          </Button>
          <DateRangeShortcuts value={draft} onChange={(range) => {
            setDraft(current => ({ ...current, ...range }));
            setFilterError("");
          }} />
        </form>
        <ErrorNotice message={filterError} />
        {orders.loading ? (
          <Loading />
        ) : list.length ? (
          <div className="payment-orders-list">
            {list.map((order) => (
              <Link
                className="payment-order-card"
                to={`/orders/${order.id}`}
                key={order.id}
              >
                <span className="payment-order-icon">
                  <ReceiptText size={21} />
                </span>
                <div className="payment-order-main">
                  <strong>{order.templateName} · {order.orderType === "renewal" ? "续费" : "新购"}</strong>
                  <span>{order.email || order.userId}</span>
                  <code>{order.orderNo}</code>
                </div>
                <div className="payment-order-value">
                  <strong>{priceText(order.amountCents)}</strong>
                  <span>{date(order.createdAt)}</span>
                </div>
                <div className="payment-order-outcome">
                  <OrderBadge order={order} />
                  <ArrowRight size={17} />
                </div>
              </Link>
            ))}
          </div>
        ) : (
          !orders.error && (
            <Empty
              icon={ReceiptText}
              title={
                status || search || from || to
                  ? "没有匹配的订单"
                  : "还没有付款订单"
              }
              description="用户在订阅商城购买后，订单和开通进度会出现在这里。"
            />
          )
        )}
        <div className="payment-pagination">
          <span>
            {orders.data
              ? `共 ${total} 笔 · 第 ${orders.data.page} / ${pages} 页`
              : "尚未读取订单"}
          </span>
          <div>
            <Button
              variant="secondary"
              disabled={page <= 1 || orders.loading}
              onClick={() => setPage((p) => p - 1)}
            >
              上一页
            </Button>
            <Button
              variant="secondary"
              disabled={page >= pages || orders.loading}
              onClick={() => setPage((p) => p + 1)}
            >
              下一页
            </Button>
          </div>
        </div>
        <UpdatedAt at={orders.updatedAt} stale={!!orders.error} />
      </Panel>
    </>
  );
}

function OrderAction({
  order,
  kind,
  onClose,
  onDone,
}: {
  order: PaymentOrder;
  kind: "query" | "retry-fulfillment";
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const toast = useToast();
  const retry = kind === "retry-fulfillment";
  return (
    <Modal
      title={retry ? order.orderType === "renewal" ? "重试套餐续费" : "重试订阅开通" : "向支付服务商查单"}
      description={order.orderNo}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <Notice>
        {retry
          ? order.orderType === "renewal" ? "只对这笔已确认付款的订单重试追加周期，不会重复扣款或重复续期。已到期或移除的订阅仍不能恢复。" : "只对这笔已确认付款的订单重试发放，不会重复扣款，也不会重复创建订阅。"
          : "查询服务商记录并核对金额与交易信息；未查到已付款记录时，不会手动标记订单成功。"}
      </Notice>
      <ErrorNotice message={error} />
      <div className="modal-actions">
        <Button variant="secondary" disabled={busy} onClick={onClose}>
          取消
        </Button>
        <Button
          icon={RefreshCw}
          busy={busy}
          onClick={async () => {
            if (busy) return;
            setBusy(true);
            setError("");
            try {
              await api(`/orders/${order.id}/${kind}`, "POST", {
                expectedRevision: order.revision,
              });
              await onDone();
              toast(
                retry
                  ? "已重试，请查看最新处理状态"
                  : "查单已完成，请查看最新结果",
              );
              onClose();
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          {retry ? "确认重试" : "确认查单"}
        </Button>
      </div>
    </Modal>
  );
}

export function OrderDetail() {
  const { id = "" } = useParams();
  const details = usePaymentData<PaymentOrder>(
    `/orders/${encodeURIComponent(id)}`,
  );
  const settings = usePaymentData<PaymentSettings>("/settings/payment");
  const [action, setAction] = useState<{
    order: PaymentOrder;
    kind: "query" | "retry-fulfillment";
  } | null>(null);
  const order = details.data;
  const canRetry =
    order?.paymentState === "paid" && order.fulfillmentState !== "fulfilled";
  const creationLabel: Record<string, string> = {
    creating: "创建中",
    ready: "已创建",
    unknown: "结果待核实",
    failed: "创建失败",
  };
  const fulfillmentLabel: Record<string, string> = {
    none: "等待付款",
    pending: "等待开通",
    blocked: "开通受阻",
    fulfilled: "订阅已生成",
  };
  return (
    <>
      <Link to="/orders" className="text-link payment-back">
        <ArrowLeft size={15} /> 返回订单列表
      </Link>
      <PageHeader
        eyebrow="ORDER DETAILS"
        title="订单详情"
        description={order ? order.orderNo : "核对支付与订阅开通状态。"}
        actions={<RefreshButton refresh={details.refresh} />}
      />
      <ErrorNotice
        message={
          details.error
            ? `${details.error}${order ? "；以下为最近一次有效数据。" : ""}`
            : ""
        }
        retry={() => void details.refresh()}
      />
      {details.loading ? (
        <Loading />
      ) : (
        order && (
          <>
            <section className="payment-detail-hero">
              <span className="payment-panel-icon">
                <CreditCard size={25} />
              </span>
              <div>
                <h2>{order.templateName}</h2>
                <p>{order.orderType === "renewal" ? "原订阅续费 · 下期重置流量 · 顺延" : "每次购买独立计量 · 开通后"} {order.validityDays} 天</p>
              </div>
              <div className="payment-detail-amount">
                <strong>{priceText(order.amountCents)}</strong>
                <OrderBadge order={order} />
              </div>
            </section>
            <div className="payment-detail-grid">
              <Panel className="payment-panel" title="订单信息">
                <dl className="payment-facts">
                  <div><dt>订单类型</dt><dd>{order.orderType === "renewal" ? "续费" : "新购"}</dd></div>
                  {order.renewalSubscriptionId && <div><dt>续费目标</dt><dd><Link to={`/users/${order.userId}#subscription-${order.renewalSubscriptionId}`}>#{order.renewalSubscriptionId.slice(-8)}</Link></dd></div>}
                  {order.periodStartsAt && <div><dt>续费周期</dt><dd>{date(order.periodStartsAt)} — {date(order.periodEndsAt)}</dd></div>}
                  <div>
                    <dt>商户订单号</dt>
                    <dd className="payment-copy">
                      <code>{order.orderNo}</code>
                      <CopyButton value={order.orderNo} label="复制" />
                    </dd>
                  </div>
                  <div>
                    <dt>所属用户</dt>
                    <dd>
                      <Link to={`/users/${order.userId}`}>
                        {order.email || order.userId}
                      </Link>
                    </dd>
                  </div>
                  <div>
                    <dt>订阅模板</dt>
                    <dd>
                      <Link
                        to={`/subscriptions?edit=${encodeURIComponent(order.templateId)}`}
                      >
                        {order.templateName}
                      </Link>
                    </dd>
                  </div>
                  <div>
                    <dt>订单金额</dt>
                    <dd>
                      {priceText(order.amountCents)} · {order.currency}
                    </dd>
                  </div>
                  <div>
                    <dt>创建时间</dt>
                    <dd>{date(order.createdAt)}</dd>
                  </div>
                  <div>
                    <dt>付款截止</dt>
                    <dd>{date(order.expiresAt)}</dd>
                  </div>
                  <div>
                    <dt>平台订单号</dt>
                    <dd>
                      <code>{order.platformOrderNo || "尚未取得"}</code>
                    </dd>
                  </div>
                </dl>
              </Panel>
              <Panel className="payment-panel" title="付款与处理结果">
                <div className="payment-progress">
                  <div>
                    <span>
                      <ReceiptText size={18} />
                    </span>
                    <section>
                      <strong>订单创建</strong>
                      <p>
                        {creationLabel[order.creationState] || "待核实"} ·{" "}
                        {date(order.createdAt)}
                      </p>
                    </section>
                  </div>
                  <div
                    className={
                      order.paymentState === "paid" ? "is-complete" : ""
                    }
                  >
                    <span>
                      <CreditCard size={18} />
                    </span>
                    <section>
                      <strong>
                        {order.paymentState === "paid"
                          ? "已确认付款"
                          : "尚未确认付款"}
                      </strong>
                      <p>
                        {order.paidAt
                          ? date(order.paidAt)
                          : "以服务端核验结果为准"}
                      </p>
                    </section>
                  </div>
                  <div
                    className={
                      order.fulfillmentState === "fulfilled"
                        ? "is-complete"
                        : ""
                    }
                  >
                    <span>
                      <CheckCircle2 size={18} />
                    </span>
                    <section>
                      <strong>
                        {order.orderType === "renewal" && order.fulfillmentState === "fulfilled" ? "已续费" : fulfillmentLabel[order.fulfillmentState] || "待核实"}
                      </strong>
                      <p>
                        {order.fulfilledAt
                          ? date(order.fulfilledAt)
                          : order.orderType === "renewal" ? "到期前核验付款后追加一期；逾期收款转人工处理" : "付款后生成独立订阅；定制订阅待分配节点并开通"}
                      </p>
                    </section>
                  </div>
                </div>
                {order.subscriptionId && (
                  <Link
                    className="button button-secondary full-width"
                    to={`/users/${order.userId}#subscription-${order.subscriptionId}`}
                  >
                    查看关联订阅 <ArrowRight size={15} />
                  </Link>
                )}
              </Panel>
            </div>
            <Panel
              className="payment-panel"
              title="核对与处理"
              subtitle="所有操作均记录审计；订单不会通过浏览器返回参数直接确认到账。"
            >
              {order.lastError && <ErrorNotice message={order.lastError} />}
              {order.status === "paid_pending" && (
                <Notice tone="warning">
                  {order.orderType === "renewal" ? "续费收款已确认，请查看具体原因。已过期或移除的订阅不能通过重试恢复，需要管理员人工核对处理。" : "已确认付款，订阅尚未开通。请先解决下方错误或检查用户、模板状态，再重试开通。"}
                </Notice>
              )}
              <div className="payment-processing">
                <span>
                  <CalendarClock size={16} />
                  最近支付通知：{date(order.lastNotifyAt)}
                </span>
                <div>
                  {canRetry && (
                    <Button
                      icon={RefreshCw}
                      disabled={!!details.error}
                      onClick={() =>
                        setAction({ order, kind: "retry-fulfillment" })
                      }
                    >
                      {order.orderType === "renewal" ? "重试续费" : "重试开通"}
                    </Button>
                  )}
                  <Button
                    variant="secondary"
                    icon={Search}
                    disabled={
                      order.paymentState === "paid" ||
                      !settings.data?.queryEnabled ||
                      !!settings.error ||
                      !!details.error
                    }
                    onClick={() => setAction({ order, kind: "query" })}
                  >
                    向服务商查单
                  </Button>
                </div>
              </div>
              {order.paymentState === "paid" ? (
                <p className="muted">
                  此订单已核验付款，无需再次向服务商查单。尚未完成时可在解决异常后重试处理。
                </p>
              ) : settings.error ? (
                <ErrorNotice
                  message={`无法读取查单配置：${settings.error}`}
                  retry={() => void settings.refresh()}
                />
              ) : (
                !settings.data?.queryEnabled && (
                  <p className="muted">
                    主动查单未启用，可在<Link to="/payments">支付设置</Link>
                    中配置。
                  </p>
                )
              )}
              <UpdatedAt at={details.updatedAt} stale={!!details.error} />
            </Panel>
            <Panel
              className="payment-panel"
              title="订单活动"
              subtitle="记录支付通知、核对与开通过程；提供方报告实付不改变订单金额。"
            >
              {order.activities?.length ? (
                <ol className="payment-activities">
                  {order.activities.map((activity) => (
                    <li key={activity.id}>
                      <time dateTime={activity.at}>{date(activity.at)}</time>
                      <div>
                        <p>{activity.message}</p>
                        {activity.actualAmountCents !== undefined && (
                          <p className="payment-reported-amount">
                            提供方报告实付{" "}
                            {priceText(activity.actualAmountCents)}
                            <span>（可能浮动，订单金额不变）</span>
                          </p>
                        )}
                      </div>
                    </li>
                  ))}
                </ol>
              ) : (
                <p className="muted">尚无订单活动记录。</p>
              )}
            </Panel>
          </>
        )
      )}
      {action && (
        <OrderAction
          {...action}
          onClose={() => setAction(null)}
          onDone={() => details.refresh(true)}
        />
      )}
    </>
  );
}
