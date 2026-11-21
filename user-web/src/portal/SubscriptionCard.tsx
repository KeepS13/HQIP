import { Link } from "react-router-dom";
import { ArrowRight, Package } from "lucide-react";
import { Badge, Meter, bytes, date } from "../components";
import { subscriptionLabels, type UserSubscription } from "./types";

// Shared summary and status rendering keep overview/list values consistent.
export function SubscriptionStatus({ status }: { status: string }) {
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
export function SubscriptionCard({ value }: { value: UserSubscription }) {
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
        待结算 {bytes(value.reservedBytes)}，与已用流量分别记录。
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
          <dt>同时在线</dt>
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
