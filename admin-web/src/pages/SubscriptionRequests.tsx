import { useState } from "react";
import { Link } from "react-router-dom";
import { ArrowLeft, ClipboardList } from "lucide-react";
import { usePolling } from "../api";
import {
  Empty,
  ErrorNotice,
  Loading,
  Notice,
  PageHeader,
  Panel,
  RefreshButton,
  date,
} from "../components";
import { RequestBadge } from "../portal/Badge";
import type { SubscriptionRequest } from "../portal/types";

export function SubscriptionRequests() {
  const requests = usePolling<SubscriptionRequest[]>("/subscription-requests");
  const [search, setSearch] = useState("");
  const items = (requests.data || []).filter((request) =>
    `${request.email} ${request.templateName}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="REQUEST ARCHIVE"
        title="旧申请历史"
        description="保留过去的人工开通记录，仅供查询。"
        actions={<RefreshButton refresh={requests.refresh} />}
      />
      <Notice>
        新的订阅通过商城支付后自动开通。旧申请不再受理，历史记录不提供审批操作。
      </Notice>
      <ErrorNotice
        message={
          requests.error ? `${requests.error}；保留最近一次有效数据。` : ""
        }
        retry={() => void requests.refresh()}
      />
      <Panel
        className="payment-panel"
        title="历史开通申请"
        action={
          <Link to="/orders" className="text-link">
            <ArrowLeft size={14} /> 订单管理
          </Link>
        }
      >
        <div className="subscription-toolbar">
          <input
            aria-label="搜索历史申请"
            placeholder="搜索邮箱或订阅名称"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        {requests.loading ? (
          <Loading />
        ) : items.length ? (
          <div className="subscription-request-list">
            {items.map((request) => (
              <article key={request.id}>
                <div className="subscription-request-title">
                  <div>
                    <Link
                      className="table-name"
                      to={`/users/${request.userId}`}
                    >
                      {request.email}
                    </Link>
                    <h3>{request.templateName}</h3>
                    <small>
                      提交于 {date(request.requestedAt)} · #
                      {request.id.slice(-8)}
                    </small>
                  </div>
                  <RequestBadge status={request.status} />
                </div>
                {request.note && (
                  <p className="subscription-note">{request.note}</p>
                )}
                {request.decidedAt && (
                  <small className="muted">
                    处理于 {date(request.decidedAt)}
                  </small>
                )}
                {request.subscriptionId && (
                  <div className="subscription-actions">
                    <Link
                      to={`/users/${request.userId}#subscription-${request.subscriptionId}`}
                      className="text-link"
                    >
                      查看已开通订阅
                    </Link>
                  </div>
                )}
              </article>
            ))}
          </div>
        ) : (
          <Empty
            icon={ClipboardList}
            title="暂无匹配的历史申请"
            description="新的购买记录请前往订单管理查看。"
          />
        )}
      </Panel>
    </>
  );
}
