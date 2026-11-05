import { useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  ClipboardList,
  Globe2,
  Link2,
  Package,
  Plus,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import {
  Badge,
  Button,
  CopyButton,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Meter,
  Modal,
  Notice,
  PageHeader,
  Panel,
  RefreshButton,
  bytes,
  date,
  useToast,
} from "../components";
import { userAPI, useUserData } from "./api";
import {
  requestLabels,
  subscriptionLabels,
  type SubscriptionDetail,
  type SubscriptionRequest,
  type SubscriptionTemplate,
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
function TemplateSummary({ value }: { value: SubscriptionTemplate }) {
  return (
    <dl className="user-sub-facts">
      <div>
        <dt>一次性流量</dt>
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
        待结算预留 {bytes(value.reservedBytes)}，与已用流量分别记录。
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
            description="在商城选择一份订阅并提交申请，管理员确认后即可使用。"
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
          <Link to="/subscriptions?tab=requests">
            <span>
              <ClipboardList size={25} />
            </span>
            <div>
              <h3>查看开通申请</h3>
              <p>申请结果会自动同步，开通时开始计时。</p>
            </div>
            <ArrowRight size={19} />
          </Link>
        </div>
      </Panel>
    </>
  );
}

function RequestDialog({
  template,
  onClose,
  onDone,
}: {
  template: SubscriptionTemplate;
  onClose: () => void;
  onDone: () => void;
}) {
  const [requestId] = useState(() => crypto.randomUUID()),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await userAPI("/subscription-requests", "POST", {
        templateId: template.id,
        templateRevision: template.revision,
        requestId,
      });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title="申请开通订阅"
      description={template.name}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={submit}>
        <TemplateSummary value={template} />
        <Notice>
          本次提交开通申请，由管理员确认。确认开通后开始计时；重复开通会得到另一份独立订阅。
        </Notice>
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
          <Button type="submit" busy={busy}>
            提交申请
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function SubscriptionCatalog() {
  const catalog = useUserData<SubscriptionTemplate[]>("/catalog", 5000),
    [selected, setSelected] = useState<SubscriptionTemplate | null>(null),
    [search, setSearch] = useState("");
  const toast = useToast();
  const items = (catalog.data || []).filter((t) =>
    t.name.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="SUBSCRIPTION CATALOG"
        title="订阅商城"
        description="选择适合你的流量包。管理员确认开通后，即可在客户端使用。"
        actions={<RefreshButton refresh={catalog.refresh} />}
      />
      <ErrorNotice
        message={
          catalog.error ? `${catalog.error}；当前保留最近一次商城信息。` : ""
        }
        retry={() => void catalog.refresh()}
      />
      <div className="user-sub-toolbar">
        <input
          aria-label="搜索订阅"
          placeholder="搜索订阅名称"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Link to="/subscriptions?tab=requests" className="text-link">
          我的开通申请 →
        </Link>
      </div>
      {catalog.loading ? (
        <Loading />
      ) : items.length ? (
        <div className="user-sub-grid">
          {items.map((t) => (
            <article key={t.id} className="user-sub-card">
              <div className="user-sub-heading">
                <span className="user-sub-icon">
                  <Package size={23} />
                </span>
                <Badge tone="info">一次性流量包</Badge>
              </div>
              <h2>{t.name}</h2>
              <p className="muted">可重复开通，每份独立生效。</p>
              <TemplateSummary value={t} />
              <Button
                className="full-width"
                icon={Plus}
                onClick={() => setSelected(t)}
              >
                申请开通
              </Button>
            </article>
          ))}
        </div>
      ) : (
        <Panel title="可申请订阅">
          <Empty
            icon={Package}
            title={search ? "没有匹配的订阅" : "暂时没有展示的订阅"}
            description="管理员上架后即可在此提交申请。"
          />
        </Panel>
      )}
      {selected && (
        <RequestDialog
          template={selected}
          onClose={() => setSelected(null)}
          onDone={() => {
            setSelected(null);
            toast("申请已提交，请在我的申请中查看结果");
          }}
        />
      )}
    </>
  );
}

function MyRequests() {
  const list = useUserData<SubscriptionRequest[]>(
      "/subscription-requests",
      5000,
    ),
    [cancel, setCancel] = useState<SubscriptionRequest | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const toast = useToast();
  return (
    <>
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
                <p className="user-request-note">管理员备注：{r.note}</p>
              )}
              {r.decidedAt && (
                <small className="muted">处理于 {date(r.decidedAt)}</small>
              )}
              <div className="user-sub-actions">
                {r.status === "pending" && (
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setError("");
                      setCancel(r);
                    }}
                  >
                    取消申请
                  </Button>
                )}
                {r.status === "approved" && r.subscriptionId && (
                  <Link
                    className="button button-secondary"
                    to={`/subscriptions/${r.subscriptionId}`}
                  >
                    查看已开通订阅 <ArrowRight size={15} />
                  </Link>
                )}
              </div>
            </article>
          ))}
        </div>
      ) : (
        <Empty
          icon={ClipboardList}
          title="还没有开通申请"
          description="浏览商城，选择想要的订阅后提交申请。"
          action={
            <Link to="/catalog" className="button button-secondary">
              浏览商城
            </Link>
          }
        />
      )}
      {cancel && (
        <Modal
          title="取消这条申请"
          description={`${cancel.templateName} · #${cancel.id.slice(-8)}`}
          onClose={() => {
            if (!busy) setCancel(null);
          }}
        >
          <p>只取消尚未处理的申请，已持有订阅不会改变。</p>
          <ErrorNotice message={error} />
          <div className="modal-actions">
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => setCancel(null)}
            >
              保留申请
            </Button>
            <Button
              busy={busy}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await userAPI(
                    `/subscription-requests/${cancel.id}/cancel`,
                    "POST",
                    { expectedRevision: cancel.revision },
                  );
                  setCancel(null);
                  await list.refresh();
                  toast("申请已取消");
                } catch (e) {
                  setError((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            >
              确认取消
            </Button>
          </div>
        </Modal>
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
            申请新订阅
          </Link>
        }
      />
      <div className="user-sub-tabs" role="tablist" aria-label="订阅与申请">
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
          开通申请
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
                description="申请经管理员确认后，会在这里显示。"
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
  const [action, setAction] = useState<"rotate" | "revoke" | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const toast = useToast();
  if (!detail.data)
    return detail.loading ? (
      <Loading />
    ) : (
      <ErrorNotice message={detail.error} retry={() => void detail.refresh()} />
    );
  const { subscription: value, nodes } = detail.data,
    url = detail.data.subscriptionUrl,
    active = value.status === "active";
  async function changeLink(kind?: "rotate" | "revoke") {
    setBusy(true);
    setError("");
    try {
      await userAPI<{ url: string }>(
        `/subscriptions/${id}/import-link${kind ? `/${kind}` : ""}`,
        "POST",
        {},
      );
      setAction(null);
      await detail.refresh();
      toast(
        kind === "revoke"
          ? "导入链接已撤销，流量包仍然保留"
          : "导入链接已准备好",
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
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
            待结算预留 {bytes(value.reservedBytes)}
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
                <p>{n.id === value.defaultNodeId ? "默认节点" : "可选节点"}</p>
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
      <Panel
        title="第三方客户端导入"
        subtitle="Clash / Mihomo 导入链接仅使用这一份订阅的流量和连接名额。"
      >
        <ErrorNotice message={error} />
        {url ? (
          <div className="form-stack">
            <Field label="导入链接">
              <input
                type="password"
                readOnly
                value={url}
                aria-label="此订阅的导入链接"
              />
            </Field>
            <div className="user-sub-actions">
              <CopyButton value={url} label="复制链接" />
              {active && (
                <a
                  className="button button-primary"
                  href={`clash://install-config?url=${encodeURIComponent(url)}&name=${encodeURIComponent(`HQIP ${value.templateName} ${value.id.slice(-6)}`)}`}
                >
                  一键导入 <ArrowUpRight size={16} />
                </a>
              )}
              <Button
                variant="secondary"
                icon={RefreshCw}
                disabled={busy || !active}
                onClick={() => setAction("rotate")}
              >
                轮换链接
              </Button>
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => setAction("revoke")}
              >
                撤销导入链接
              </Button>
            </div>
            <Notice>
              HQIP
              客户端使用账户登录。撤销此导入链接不会移除订阅，也不会影响其他订阅的导入链接。
            </Notice>
          </div>
        ) : (
          <Empty
            icon={Link2}
            title="需要使用第三方客户端？"
            description="为这份订阅创建独立导入链接。请妥善保管链接，不要公开分享。"
            action={
              <Button
                icon={Link2}
                busy={busy}
                disabled={!active}
                onClick={() => void changeLink()}
              >
                创建导入链接
              </Button>
            }
          />
        )}
      </Panel>
      {action && (
        <Modal
          title={action === "rotate" ? "轮换导入链接" : "撤销导入链接"}
          description="此操作只影响这份订阅的第三方导入入口，流量包继续保留。"
          onClose={() => {
            if (!busy) setAction(null);
          }}
        >
          <p>
            旧链接及关联连接凭据将失效。
            {action === "rotate"
              ? "完成后请在第三方客户端重新导入新链接。"
              : "以后仍可为这份有效订阅创建新的链接。"}
          </p>
          <ErrorNotice message={error} />
          <div className="modal-actions">
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => setAction(null)}
            >
              取消
            </Button>
            <Button busy={busy} onClick={() => void changeLink(action)}>
              {action === "rotate" ? "确认轮换" : "确认撤销"}
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}
