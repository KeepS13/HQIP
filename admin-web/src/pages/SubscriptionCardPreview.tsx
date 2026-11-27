import { ArrowRight, Package } from "lucide-react";
import { Badge, Button, bytes } from "../components";
import type { SubscriptionTemplate } from "../portal/types";
import { parsePriceInput, priceText } from "../payments";

export function SubscriptionCardPreview({
  value,
  price,
}: {
  value: Pick<
    SubscriptionTemplate,
    "name" | "description" | "validityDays" | "quotaBytes" | "bandwidthMbps" | "maxConnections" | "nodesPerSubscription" | "nodeIds"
  >;
  price: string;
}) {
  let priceCents: number | null = null;
  try {
    priceCents = parsePriceInput(price);
  } catch {
    /* An incomplete draft can still be previewed. */
  }
  const quantity = (value: number) => (Number.isFinite(value) && value > 0 ? value : "—");
  const lines = value.description
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  return (
    <article className="subscription-product-preview">
      <div className="subscription-product-heading">
        <span className="subscription-icon">
          <Package size={23} />
        </span>
        <Badge tone="info">一次性流量包</Badge>
      </div>
      <h3>{value.name || "订阅名称"}</h3>
      <p className="subscription-product-subtitle">每份独立生效，同一套餐的节点不重复分配。</p>
      <div className="subscription-product-price">
        <strong>{priceCents === null ? "暂未定价" : priceText(priceCents)}</strong>
        {priceCents !== null && <span>/ 份</span>}
      </div>
      <dl className="subscription-facts">
        <div>
          <dt>一次性总流量</dt>
          <dd>
            {value.quotaBytes === null
              ? "不限量"
              : Number.isFinite(value.quotaBytes) && value.quotaBytes > 0
                ? bytes(value.quotaBytes)
                : "—"}
          </dd>
        </div>
        <div>
          <dt>有效期</dt>
          <dd>开通后 {quantity(value.validityDays)} 天</dd>
        </div>
        <div>
          <dt>共享带宽</dt>
          <dd>{value.bandwidthMbps === null ? "不限速" : `${quantity(value.bandwidthMbps)} Mbps`}</dd>
        </div>
        <div>
          <dt>同时在线</dt>
          <dd>{quantity(value.maxConnections)} 个</dd>
        </div>
        <div>
          <dt>每份分配节点</dt>
          <dd>{quantity(value.nodeIds.length ? Math.min(value.nodesPerSubscription, value.nodeIds.length) : value.nodesPerSubscription)} 个</dd>
        </div>
      </dl>
      {!!lines.length && (
        <ul className="subscription-product-description">
          {lines.map((line, index) => (
            <li key={index}>{line}</li>
          ))}
        </ul>
      )}
      <Button type="button" icon={ArrowRight} disabled className="subscription-product-buy">
        {priceCents === null ? "暂不可购买" : "购买订阅"}
      </Button>
    </article>
  );
}
