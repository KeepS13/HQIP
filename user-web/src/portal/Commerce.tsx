import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  ArrowRight,
  CreditCard,
  Package,
  ReceiptText,
  ShieldCheck,
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
  RefreshButton,
  bytes,
} from "../components";
import { userAPI, useUserData } from "./api";
import {
  checkoutDraft,
  clearCheckoutDraft,
  money,
  saveCheckoutDraft,
  type PaymentInfo,
  type PaymentOrder,
} from "./payment-data";
import type { SubscriptionTemplate } from "./types";
import "./commerce.css";

function ProductFacts({ value }: { value: SubscriptionTemplate }) {
  return (
    <dl className="user-sub-facts">
      <div>
        <dt>一次性总流量</dt>
        <dd>
          {value.quotaBytes === null ? "不限量" : bytes(value.quotaBytes)}
        </dd>
      </div>
      <div>
        <dt>有效期</dt>
        <dd>开通后 {value.validityDays} 天</dd>
      </div>
      <div>
        <dt>共享带宽</dt>
        <dd>
          {value.bandwidthMbps === null
            ? "不限速"
            : `${value.bandwidthMbps} Mbps`}
        </dd>
      </div>
      <div>
        <dt>同时在线连接</dt>
        <dd>{value.maxConnections} 个</dd>
      </div>
      <div>
        <dt>可选节点</dt>
        <dd>{value.nodeIds.length} 个</dd>
      </div>
    </dl>
  );
}

function PurchaseDialog({
  template,
  userId,
  duration,
  onClose,
}: {
  template: SubscriptionTemplate;
  userId: string;
  duration: number;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState(() => checkoutDraft(userId, template));
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [discard, setDiscard] = useState(false);
  const submitting = useRef(false);
  const navigate = useNavigate();
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const next = { ...draft, attempted: true };
      try {
        saveCheckoutDraft(userId, next);
      } catch {
        throw new Error(
          "浏览器无法保存本次购买请求，请允许本站使用存储后重试。尚未发起支付订单。",
        );
      }
      setDraft(next);
      const order = await userAPI<PaymentOrder>("/orders", "POST", {
        templateId: draft.template.id,
        templateRevision: draft.template.revision,
        requestId: draft.requestId,
      });
      clearCheckoutDraft(userId, template.id);
      navigate(`/orders/${encodeURIComponent(order.id)}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }
  return (
    <Modal
      title="确认购买"
      description={draft.template.name}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={submit}>
        {draft.attempted && (
          <Notice>
            继续确认上一次购买请求，仍使用相同订单标识和下单参数。请勿重复付款。
          </Notice>
        )}
        <div className="commerce-checkout-total">
          <span>本次支付</span>
          <strong>{money(draft.template.priceCents!)}</strong>
        </div>
        <ProductFacts value={draft.template} />
        <Notice>
          本次购买一份独立订阅，支付核验成功后自动开通。有效期从实际开通时起算，不会合并或延长已有订阅。
        </Notice>
        <p className="commerce-hint">
          下单后请在 {duration}{" "}
          分钟内付款。价格和有效天数在下单时锁定；节点及使用限额以订阅当前设置为准。
        </p>
        <ErrorNotice message={error} />
        {error && (
          <p className="commerce-hint">
            可以重试确认同一笔订单；若已完成付款，请先在
            <Link to="/orders">我的订单</Link>查看结果，避免重复付款。
          </p>
        )}
        {draft.attempted && !discard && (
          <Button
            type="button"
            variant="ghost"
            disabled={busy}
            onClick={() => setDiscard(true)}
          >
            需要另建一笔订单？
          </Button>
        )}
        {discard && (
          <Notice tone="warning">
            请先检查<Link to="/orders">我的订单</Link>
            。创建另一笔订单不会撤销已有支付，两笔均付款时会分别开通、分别计费。
            <Button
              type="button"
              variant="secondary"
              disabled={busy}
              onClick={() => {
                try {
                  clearCheckoutDraft(userId, template.id);
                  setDraft(checkoutDraft(userId, template));
                  setDiscard(false);
                  setError("");
                } catch {
                  setError("浏览器暂时无法清理购买请求，请稍后重试。");
                }
              }}
            >
              我已核对，另建一笔订单
            </Button>
          </Notice>
        )}
        <div className="modal-actions">
          <Button
            type="button"
            variant="secondary"
            disabled={busy}
            onClick={onClose}
          >
            返回商城
          </Button>
          <Button type="submit" busy={busy} icon={CreditCard}>
            {error ? "重试确认订单" : "创建订单"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function SubscriptionCatalog({ userId }: { userId: string }) {
  const catalog = useUserData<SubscriptionTemplate[]>("/catalog", 5000);
  const payment = useUserData<PaymentInfo>("/payment-info", 15000);
  const [selected, setSelected] = useState<SubscriptionTemplate | null>(null),
    [search, setSearch] = useState("");
  const items = (catalog.data || []).filter((t) =>
    t.name.toLowerCase().includes(search.toLowerCase()),
  );
  const available = payment.data?.enabled && !payment.error;
  return (
    <>
      <PageHeader
        eyebrow="SUBSCRIPTION CATALOG"
        title="订阅商城"
        description="选择一份适合你的流量包，付款后自动开通，每份独立生效。"
        actions={
          <RefreshButton
            refresh={async () => {
              await Promise.all([catalog.refresh(), payment.refresh()]);
            }}
          />
        }
      />
      <div className="commerce-banner">
        <span className="user-sub-icon">
          <ShieldCheck size={23} />
        </span>
        <div>
          <strong>一份订阅，一份独立额度。</strong>
          <p>一次购买 · 到期前使用 · 无自动续费</p>
        </div>
        <Link to="/orders" className="button button-secondary">
          <ReceiptText size={16} />
          我的订单
        </Link>
      </div>
      <ErrorNotice
        message={
          catalog.error ? `${catalog.error}；当前保留最近一次商城信息。` : ""
        }
        retry={() => void catalog.refresh()}
      />
      <ErrorNotice
        message={payment.error}
        retry={() => void payment.refresh()}
      />
      {!payment.loading && payment.data && !payment.data.enabled && (
        <Notice tone="warning">
          在线支付暂未开放，请稍后再来。已持有订阅可继续使用。
        </Notice>
      )}
      <div className="user-sub-toolbar">
        <input
          aria-label="搜索订阅"
          placeholder="搜索订阅名称"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <span className="commerce-hint">人民币支付 · 每份分别计量</span>
      </div>
      {catalog.loading ? (
        <Loading />
      ) : items.length ? (
        <div className="user-sub-grid">
          {items.map((t) => {
            const priced =
              Number.isSafeInteger(t.priceCents) &&
              t.priceCents !== null &&
              t.priceCents >= 100;
            const hasNodes = t.nodeIds.length > 0;
            return (
              <article key={t.id} className="user-sub-card commerce-product">
                <div className="user-sub-heading">
                  <span className="user-sub-icon">
                    <Package size={23} />
                  </span>
                  <Badge tone="info">一次性流量包</Badge>
                </div>
                <h2>{t.name}</h2>
                <p className="muted">可重复购买，每份独立生效。</p>
                <div className="commerce-price">
                  <strong>{priced ? money(t.priceCents!) : "暂未定价"}</strong>
                  {priced && <span>/ 份</span>}
                </div>
                <ProductFacts value={t} />
                {!hasNodes && (
                  <p className="commerce-hint">
                    此订阅尚未配置可用节点，暂不可购买。
                  </p>
                )}
                <Button
                  className="full-width"
                  icon={ArrowRight}
                  disabled={
                    !priced || !hasNodes || !available || !!catalog.error
                  }
                  onClick={() => setSelected(t)}
                >
                  {!hasNodes
                    ? "暂无可用节点"
                    : !priced
                      ? "暂不可购买"
                      : payment.loading
                        ? "正在读取支付状态"
                        : available
                          ? "购买订阅"
                          : "支付暂不可用"}
                </Button>
              </article>
            );
          })}
        </div>
      ) : (
        <Panel title="可购买订阅">
          <Empty
            icon={Package}
            title={search ? "没有匹配的订阅" : "暂时没有上架的订阅"}
            description="管理员上架订阅后，可在这里选购。"
          />
        </Panel>
      )}
      {selected && (
        <PurchaseDialog
          key={`${userId}:${selected.id}`}
          userId={userId}
          template={selected}
          duration={payment.data?.payDurationMinutes || 10}
          onClose={() => setSelected(null)}
        />
      )}
    </>
  );
}
