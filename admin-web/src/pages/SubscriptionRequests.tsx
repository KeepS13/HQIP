import { useState } from "react";
import { Link } from "react-router-dom";
import { Check, ClipboardList, X } from "lucide-react";
import { api, usePolling } from "../api";
import {
  Button,
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
import { RequestBadge } from "../portal/Badge";
import type {
  SubscriptionRequest,
  SubscriptionTemplate,
} from "../portal/types";
import { TemplateFacts } from "./SubscriptionTemplates";

function DecideRequest({
  request,
  template,
  decision,
  onClose,
  onDone,
}: {
  request: SubscriptionRequest;
  template?: SubscriptionTemplate;
  decision: "approve" | "reject";
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [note, setNote] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title={decision === "approve" ? "确认开通订阅" : "拒绝开通申请"}
      description={`${request.email} · ${request.templateName}`}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      {decision === "approve" &&
        (template ? (
          <>
            <TemplateFacts value={template} />
            <Notice>
              按以上当前模板开通一份全新订阅，有效期从确认时开始。已有订阅继续独立使用。
            </Notice>
            {!template.enabled && (
              <Notice tone="warning">该模板已停用，无法批准开通。</Notice>
            )}
          </>
        ) : (
          <Notice tone="warning">模板无法读取，请刷新后重试。</Notice>
        ))}
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            await api(
              `/subscription-requests/${request.id}/${decision}`,
              "POST",
              {
                expectedRevision: request.revision,
                templateRevision: template?.revision || 0,
                note: note.trim(),
              },
            );
            await onDone();
            onClose();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field
          label={decision === "reject" ? "拒绝原因" : "处理备注（选填）"}
          hint="处理结果与备注会展示给申请用户。"
        >
          <textarea
            rows={3}
            maxLength={1000}
            required={decision === "reject"}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
        </Field>
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button
            type="button"
            variant="secondary"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </Button>
          <Button
            type="submit"
            busy={busy}
            disabled={decision === "approve" && !template?.enabled}
            icon={decision === "approve" ? Check : X}
          >
            {decision === "approve" ? "确认开通" : "确认拒绝"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function SubscriptionRequests() {
  const requests = usePolling<SubscriptionRequest[]>("/subscription-requests"),
    templates = usePolling<SubscriptionTemplate[]>("/subscription-templates");
  const [status, setStatus] = useState("pending"),
    [search, setSearch] = useState("");
  const [selection, setSelection] = useState<{
    request: SubscriptionRequest;
    template?: SubscriptionTemplate;
    decision: "approve" | "reject";
  } | null>(null);
  const toast = useToast();
  const items = (requests.data || []).filter(
    (r) =>
      (!status || r.status === status) &&
      `${r.email} ${r.templateName}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="REQUESTS"
        title="开通申请"
        description="核对当前订阅规则，确认后为用户新增一份独立流量包。"
        actions={<RefreshButton refresh={requests.refresh} />}
      />
      <ErrorNotice
        message={requests.error || templates.error}
        retry={() => {
          void requests.refresh();
          void templates.refresh();
        }}
      />
      {requests.error && requests.data && (
        <Notice>当前显示最近一次有效申请列表。</Notice>
      )}
      <Panel
        title="用户申请"
        subtitle={`待处理 ${(requests.data || []).filter((r) => r.status === "pending").length} 条`}
      >
        <div className="subscription-toolbar">
          <input
            aria-label="搜索申请"
            placeholder="搜索邮箱或订阅名称"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <select
            aria-label="申请状态"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            <option value="pending">待审核</option>
            <option value="approved">已开通</option>
            <option value="rejected">未通过</option>
            <option value="cancelled">已取消</option>
            <option value="">全部状态</option>
          </select>
        </div>
        {requests.loading ? (
          <Loading />
        ) : items.length ? (
          <div className="subscription-request-list">
            {items.map((r) => (
              <article key={r.id}>
                <div className="subscription-request-title">
                  <div>
                    <Link className="table-name" to={`/users/${r.userId}`}>
                      {r.email}
                    </Link>
                    <h3>{r.templateName}</h3>
                    <small>
                      提交于 {date(r.requestedAt)} · #{r.id.slice(-8)}
                    </small>
                  </div>
                  <RequestBadge status={r.status} />
                </div>
                {r.note && <p className="subscription-note">{r.note}</p>}
                {r.decidedAt && (
                  <small className="muted">处理于 {date(r.decidedAt)}</small>
                )}
                <div className="subscription-actions">
                  {r.status === "pending" ? (
                    <>
                      <Button
                        variant="secondary"
                        icon={X}
                        disabled={
                          !templates.data?.some((t) => t.id === r.templateId)
                        }
                        onClick={() =>
                          setSelection({
                            request: r,
                            template: templates.data?.find(
                              (t) => t.id === r.templateId,
                            ),
                            decision: "reject",
                          })
                        }
                      >
                        拒绝申请
                      </Button>
                      <Button
                        icon={Check}
                        disabled={
                          !templates.data?.some((t) => t.id === r.templateId)
                        }
                        onClick={() =>
                          setSelection({
                            request: r,
                            template: templates.data?.find(
                              (t) => t.id === r.templateId,
                            ),
                            decision: "approve",
                          })
                        }
                      >
                        核对并开通
                      </Button>
                    </>
                  ) : (
                    r.subscriptionId && (
                      <Link
                        to={`/users/${r.userId}#subscription-${r.subscriptionId}`}
                        className="text-link"
                      >
                        查看已开通订阅
                      </Link>
                    )
                  )}
                </div>
              </article>
            ))}
          </div>
        ) : (
          <Empty
            icon={ClipboardList}
            title="暂无符合条件的申请"
            description="用户在商城提交后，申请会自动出现在这里。"
          />
        )}
      </Panel>
      {selection && (
        <DecideRequest
          {...selection}
          onClose={() => setSelection(null)}
          onDone={async () => {
            await requests.refresh();
            toast(
              selection.decision === "approve" ? "订阅已开通" : "申请已拒绝",
            );
          }}
        />
      )}
    </>
  );
}
