import { Link } from "react-router-dom";
import { ArrowRight, ClipboardList } from "lucide-react";
import { Badge, Empty, ErrorNotice, Loading, Notice, date } from "../components";
import { useUserData } from "./api";
import { requestLabels, type SubscriptionRequest } from "./types";

// Read-only history of the retired application flow; new purchases use orders.
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
export function MyRequests() {
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
