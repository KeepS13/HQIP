import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ArrowLeft,
  CalendarClock,
  KeyRound,
  Monitor,
  Package,
  Plus,
  Search,
  ShieldCheck,
  Trash2,
  Unplug,
  UsersRound,
} from "lucide-react";
import { api, usePolling } from "../api";
import { passwordError } from "../config";
import {
  Badge,
  Button,
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
  StatCard,
  bytes,
  date,
  useToast,
} from "../components";
import { SubscriptionBadge } from "../portal/Badge";
import {
  connectionActive,
  connectionState,
  type SubscriptionTemplate,
  type UserProfile,
  type UserRow,
  type UserSubscription,
} from "../portal/types";
import { TemplateFacts } from "./SubscriptionTemplates";
import { SubscriptionExpiry } from "./SubscriptionExpiry";
import { canAdjustSubscriptionExpiry } from "../subscription-expiry";
import "./accounts.css";
import "./subscriptions.css";

function Confirm({
  title,
  description,
  onClose,
  action,
}: {
  title: string;
  description: string;
  onClose: () => void;
  action: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title={title}
      description={description}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
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
          busy={busy}
          onClick={async () => {
            setBusy(true);
            setError("");
            try {
              await action();
              onClose();
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          确认操作
        </Button>
      </div>
    </Modal>
  );
}

export function Users() {
  const users = usePolling<UserRow[]>("/users");
  const [search, setSearch] = useState("");
  const rows = (users.data || []).filter((u) =>
    u.email.toLowerCase().includes(search.toLowerCase()),
  );
  const active = (users.data || [])
    .flatMap((u) => u.subscriptions || [])
    .filter((s) => s.status === "active");
  return (
    <>
      <PageHeader
        eyebrow="USERS"
        title="用户管理"
        description="查看用户持有的各份订阅，分别管理额度和使用权限。"
        actions={<RefreshButton refresh={users.refresh} />}
      />
      <ErrorNotice
        message={users.error ? `${users.error}；当前显示最近一次数据。` : ""}
        retry={() => void users.refresh()}
      />
      <div className="stats-grid account-stats">
        <StatCard
          label="注册用户"
          value={users.data?.length ?? "—"}
          icon={UsersRound}
          hint="邮箱作为登录账号"
        />
        <StatCard
          label="有效订阅"
          value={users.data ? active.length : "—"}
          icon={Package}
          hint="每一份独立计量"
          color="green"
        />
        <StatCard
          label="待开通用户"
          value={
            users.data
              ? users.data.filter(
                  (u) =>
                    !(u.subscriptions || []).some((s) => s.status === "active"),
                ).length
              : "—"
          }
          icon={ShieldCheck}
          hint="用户可购买，管理员可赠送"
          color="amber"
        />
      </div>
      <Panel
        title="所有用户"
        action={
          <div className="account-search">
            <Search size={16} />
            <input
              aria-label="搜索邮箱"
              placeholder="搜索邮箱…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
        }
      >
        {users.loading ? (
          <Loading />
        ) : rows.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>用户</th>
                  <th>账户状态</th>
                  <th>持有订阅</th>
                  <th>当前使用</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {rows.map((u) => {
                  const subscriptions = u.subscriptions || [];
                  return (
                    <tr key={u.id}>
                      <td>
                        <Link className="table-name" to={`/users/${u.id}`}>
                          {u.email}
                        </Link>
                        <div className="table-small">
                          注册于 {date(u.createdAt)}
                        </div>
                      </td>
                      <td>
                        <Badge tone={u.enabled ? "success" : "muted"}>
                          {u.enabled ? "已启用" : "已停用"}
                        </Badge>
                      </td>
                      <td>
                        {
                          subscriptions.filter((s) => s.status === "active")
                            .length
                        }{" "}
                        份有效 / {subscriptions.length} 份全部
                        <div className="table-small">
                          {subscriptions
                            .slice(0, 2)
                            .map((s) => s.templateName)
                            .join("、") || "尚未开通"}
                        </div>
                      </td>
                      <td>
                        {subscriptions.reduce((sum, s) => sum + s.occupied, 0)}{" "}
                        个 VPN 连接
                      </td>
                      <td>
                        <Link className="text-link" to={`/users/${u.id}`}>
                          管理 →
                        </Link>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty
            icon={UsersRound}
            title={search ? "没有匹配的账户" : "暂无用户"}
            description="用户注册后可在商城购买订阅，或由管理员直接赠送。"
          />
        )}
      </Panel>
    </>
  );
}

function GrantSubscription({
  user,
  templates,
  onClose,
  onDone,
}: {
  user: UserProfile;
  templates: SubscriptionTemplate[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [templateId, setTemplate] = useState("");
  const [requestId] = useState(() => crypto.randomUUID());
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const selected = templates.find((t) => t.id === templateId);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!selected) return;
    setBusy(true);
    setError("");
    try {
      await api(`/users/${user.user.id}/subscriptions`, "POST", {
        templateId,
        templateRevision: selected.revision,
        expectedRevision: user.user.revision,
        requestId,
      });
      await onDone();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title="为用户赠送订阅"
      description={user.user.email}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={submit}>
        <Field label="订阅模板">
          <select
            required
            value={templateId}
            onChange={(e) => setTemplate(e.target.value)}
          >
            <option value="">选择订阅模板</option>
            {templates
              .filter((t) => t.enabled)
              .map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                  {!t.listed ? "（仅后台分配）" : ""}
                </option>
              ))}
          </select>
        </Field>
        {selected && <TemplateFacts value={selected} />}
        <Notice>
          本次为管理员赠送，不收取费用、不生成付款订单。新增一份独立订阅，从现在开始计时，已有订阅继续有效。
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
          <Button
            type="submit"
            busy={busy}
            disabled={!selected?.enabled}
            icon={Plus}
          >
            确认开通
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function OwnedSubscription({
  value,
  onRemove,
  onExpiry,
}: {
  value: UserSubscription;
  onRemove: () => void;
  onExpiry: () => void;
}) {
  return (
    <article
      className="account-plan-card subscription-owned"
      id={`subscription-${value.id}`}
    >
      <div>
        <span className="subscription-icon">
          <Package size={22} />
        </span>
        <SubscriptionBadge status={value.status} />
      </div>
      <h2>{value.templateName}</h2>
      <p>
        #{value.id.slice(-8)} · 开通于 {date(value.createdAt)}
      </p>
      <dl className="subscription-facts">
        <div>
          <dt>到期时间</dt>
          <dd>{date(value.expiresAt)}</dd>
        </div>
        <div>
          <dt>独立共享带宽</dt>
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
          <dt>可用节点</dt>
          <dd>{value.nodeIds.length} 个</dd>
        </div>
      </dl>
      <div className="subscription-usage">
        <strong>
          {bytes(value.usedBytes)}
          <small>
            {" "}
            / {value.quotaBytes === null ? "不限量" : bytes(value.quotaBytes)}
          </small>
        </strong>
        <Meter value={value.usedBytes} max={value.quotaBytes} />
        <p>一次性上传＋下载 · 待结算预留 {bytes(value.reservedBytes)}</p>
      </div>
      {!["removed", "revoked"].includes(value.status) && (
        <div className="subscription-owned-actions">
          <Button variant="secondary" icon={CalendarClock} disabled={!canAdjustSubscriptionExpiry(value.status, value.expiresAt)} onClick={onExpiry}>调整到期时间</Button>
          <Button variant="danger" icon={Trash2} onClick={onRemove}>移除这一份订阅</Button>
          {!canAdjustSubscriptionExpiry(value.status, value.expiresAt) && <small>已到期订阅不能调整时间</small>}
        </div>
      )}
    </article>
  );
}
function ResetPassword({ id, onClose }: { id: string; onClose: () => void }) {
  const [password, setPassword] = useState(""),
    [confirm, setConfirm] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const toast = useToast();
  return (
    <Modal
      title="重置用户密码"
      description="重置后将撤销该用户的登录会话。请通过可信方式交付新密码。"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          if (passwordError(password)) {
            setError(passwordError(password));
            return;
          }
          if (password !== confirm) {
            setError("两次输入的密码不一致");
            return;
          }
          setBusy(true);
          setError("");
          try {
            await api(`/users/${id}/password`, "POST", { password });
            toast("用户密码已重置");
            onClose();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field label="新密码" hint="至少 6 个字符">
          <input
            type="password"
            autoComplete="new-password"
            minLength={6}
            maxLength={1024}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field label="确认新密码">
          <input
            type="password"
            autoComplete="new-password"
            minLength={6}
            maxLength={1024}
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </Field>
        <ErrorNotice message={error} />
        <Button type="submit" busy={busy} icon={KeyRound}>
          重置密码
        </Button>
      </form>
    </Modal>
  );
}

export function UserPage() {
  const { id } = useParams();
  const profile = usePolling<UserProfile>(`/users/${id}`),
    templates = usePolling<SubscriptionTemplate[]>("/subscription-templates");
  const [grant, setGrant] = useState(false),
    [reset, setReset] = useState(false),
    [filter, setFilter] = useState("active");
  const [expiry, setExpiry] = useState<UserSubscription | null>(null);
  const [confirm, setConfirm] = useState<{
    title: string;
    description: string;
    action: () => Promise<void>;
  } | null>(null);
  const toast = useToast();
  if (!profile.data)
    return profile.loading ? (
      <Loading />
    ) : (
      <ErrorNotice
        message={profile.error}
        retry={() => void profile.refresh()}
      />
    );
  const data = profile.data,
    subscriptions = data.subscriptions || [],
    sessions = data.sessions || [],
    connections = data.connections || [];
  const shown = subscriptions.filter(
    (s) => filter === "all" || s.status === "active",
  );
  return (
    <>
      <Link className="back-link" to="/users">
        <ArrowLeft size={15} /> 返回用户列表
      </Link>
      <PageHeader
        eyebrow="USER DETAILS"
        title={data.user.email}
        description={`注册于 ${date(data.user.createdAt)}`}
        actions={
          <>
            <RefreshButton refresh={profile.refresh} />
            <Button icon={Plus} onClick={() => setGrant(true)}>
              添加订阅
            </Button>
          </>
        }
      />
      <ErrorNotice
        message={
          profile.error ? `${profile.error}；当前显示最近一次数据。` : ""
        }
        retry={() => void profile.refresh()}
      />
      <Panel
        title="持有订阅"
        subtitle="每份流量、有效期、带宽与连接上限独立计算"
        action={
          <select
            className="subscription-status-filter"
            aria-label="用户订阅状态"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          >
            <option value="active">有效订阅</option>
            <option value="all">全部订阅</option>
          </select>
        }
      >
        {shown.length ? (
          <div className="subscription-owned-grid">
            {shown.map((s) => (
              <OwnedSubscription
                key={s.id}
                value={s}
                onExpiry={() => setExpiry(s)}
                onRemove={() =>
                  setConfirm({
                    title: "移除这一份订阅",
                    description: `${s.templateName} · #${s.id.slice(-8)}。此份订阅的连接与导入凭据将失效，其他订阅不受影响。`,
                    action: async () => {
                      await api(
                        `/users/${id}/subscriptions/${s.id}`,
                        "DELETE",
                        { expectedRevision: s.revision },
                      );
                      await profile.refresh();
                      toast("订阅已移除，节点正在撤销相关连接");
                    },
                  })
                }
              />
            ))}
          </div>
        ) : (
          <Empty
            icon={Package}
            title={subscriptions.length ? "没有有效订阅" : "尚未开通订阅"}
            description="用户付款后自动开通；管理员也可直接赠送已设置的订阅。"
            action={
              <Button icon={Plus} onClick={() => setGrant(true)}>
                添加订阅
              </Button>
            }
          />
        )}
      </Panel>
      <Panel title="账户管理">
        <div className="account-control subscription-account-control">
          <div>
            <Badge tone={data.user.enabled ? "success" : "warning"}>
              {data.user.enabled ? "账户已启用" : "账户已停用"}
            </Badge>
            <p>停用账户会停止所有订阅的连接权限。</p>
          </div>
          <Button
            variant="secondary"
            onClick={() =>
              setConfirm({
                title: data.user.enabled ? "停用账户" : "启用账户",
                description: data.user.enabled
                  ? "将撤销该账户的登录和全部订阅连接权限。"
                  : "恢复账户使用权限，各份订阅仍须在有效期和额度内。",
                action: async () => {
                  await api(`/users/${id}`, "PATCH", {
                    enabled: !data.user.enabled,
                    expectedRevision: data.user.revision,
                  });
                  await profile.refresh();
                  toast("账户状态已更新");
                },
              })
            }
          >
            {data.user.enabled ? "停用账户" : "启用账户"}
          </Button>
          <Button
            variant="secondary"
            icon={KeyRound}
            onClick={() => setReset(true)}
          >
            重置用户密码
          </Button>
        </div>
      </Panel>
      <div className="account-detail-grid">
        <Panel
          title="连接记录"
          subtitle="用于核对各份订阅的准入与计量；断开确认前仍占名额"
        >
          {connections.length ? (
            <div className="account-records">
              {connections.map((c) => (
                <div key={c.id}>
                  <Unplug size={19} />
                  <section>
                    <strong>{c.nodeName || c.nodeId}</strong>
                    <p>
                      {subscriptions.find((s) => s.id === c.subscriptionId)
                        ?.templateName || "订阅"}{" "}
                      · #{c.subscriptionId?.slice(-8)}
                      <br />
                      建立于 {date(c.createdAt)}
                    </p>
                  </section>
                  <Badge tone={connectionActive(c) ? "success" : "muted"}>
                    {connectionState(c)}
                  </Badge>
                  <Button
                    variant="secondary"
                    disabled={!connectionActive(c)}
                    onClick={() =>
                      setConfirm({
                        title: "断开连接",
                        description:
                          "仅断开所选 VPN 连接，客户端可能尝试重新连接。",
                        action: async () => {
                          await api(
                            `/users/${id}/connections/${c.id}/revoke`,
                            "POST",
                            {},
                          );
                          await profile.refresh();
                        },
                      })
                    }
                  >
                    断开
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <Empty
              title="暂无连接记录"
              description="连接建立后显示所属订阅与节点。"
            />
          )}
        </Panel>
        <Panel title="登录会话">
          {sessions.length ? (
            <div className="account-records">
              {sessions.map((s) => (
                <div key={s.id}>
                  <Monitor size={19} />
                  <section>
                    <strong>
                      {s.clientName ||
                        (s.kind === "browser" ? "网页浏览器" : "HQIP 客户端")}
                    </strong>
                    <p>最近活动 {date(s.lastSeen)}</p>
                  </section>
                  <Badge tone={s.revoked ? "muted" : "info"}>
                    {s.revoked ? "已退出" : "已登录"}
                  </Badge>
                  {!s.revoked && (
                    <Button
                      variant="ghost"
                      onClick={() =>
                        setConfirm({
                          title: "退出登录会话",
                          description: "该会话需重新登录才能继续使用。",
                          action: async () => {
                            await api(
                              `/users/${id}/sessions/${s.id}/revoke`,
                              "POST",
                              {},
                            );
                            await profile.refresh();
                          },
                        })
                      }
                    >
                      退出
                    </Button>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <Empty
              title="暂无登录记录"
              description="用户登录后显示会话状态。"
            />
          )}
        </Panel>
      </div>
      {grant &&
        (templates.data ? (
          <GrantSubscription
            user={data}
            templates={templates.data}
            onClose={() => setGrant(false)}
            onDone={async () => {
              await profile.refresh();
              toast("新的订阅已开通");
            }}
          />
        ) : (
          <Modal title="读取订阅模板" onClose={() => setGrant(false)}>
            {templates.error ? (
              <ErrorNotice
                message={templates.error}
                retry={() => void templates.refresh()}
              />
            ) : (
              <Loading />
            )}
          </Modal>
        ))}
      {reset && (
        <ResetPassword
          id={id!}
          onClose={() => {
            setReset(false);
            void profile.refresh();
          }}
        />
      )}
      {confirm && <Confirm {...confirm} onClose={() => setConfirm(null)} />}
      {expiry && <SubscriptionExpiry key={expiry.id} userId={id!} value={expiry} onClose={() => setExpiry(null)} onSaved={async () => { await profile.refresh(); toast("这一份订阅的到期时间已更新"); }} />}
    </>
  );
}
