import { passwordError } from "../config";
import "./accounts.css";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  Cable,
  Check,
  ChevronRight,
  KeyRound,
  Package,
  Plus,
  Save,
  Search,
  Settings2,
  ShieldCheck,
  Unplug,
  UserRound,
  UsersRound,
} from "lucide-react";
import { api, usePolling } from "../api";
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
import { EntitlementBadge } from "../portal/Badge";
import {
  connectionActive,
  connectionState,
  normalizeProfile,
  type Plan,
  type TurnstileSettings,
  type UserProfile,
  type UserProfileResponse,
  type UserRow,
} from "../portal/types";
import type { Node } from "../types";

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
        <Button variant="secondary" disabled={busy} onClick={onClose}>
          取消
        </Button>
        <Button
          busy={busy}
          onClick={async () => {
            setBusy(true);
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
  const { data, error, loading, refresh } = usePolling<UserRow[]>("/users");
  const [search, setSearch] = useState("");
  const users = (data || []).filter((user) =>
    user.email.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        eyebrow="USERS"
        title="用户管理"
        description="为账户开通套餐，统一管理使用权限和有效期。"
        actions={<RefreshButton refresh={refresh} />}
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <div className="stats-grid account-stats">
        <StatCard
          label="注册用户"
          value={data?.length ?? "—"}
          icon={UsersRound}
          hint="邮箱作为账户登录名"
        />
        <StatCard
          label="有效套餐"
          value={
            data?.filter((user) => user.entitlement?.status === "active")
              .length ?? "—"
          }
          icon={Package}
          color="green"
          hint="用户独立累计套餐流量"
        />
        <StatCard
          label="待开通"
          value={
            data?.filter(
              (user) =>
                !user.entitlement || user.entitlement.status === "unassigned",
            ).length ?? "—"
          }
          icon={UserRound}
          color="amber"
          hint="管理员分配后开始使用"
        />
      </div>
      <Panel
        title="所有用户"
        subtitle="公开注册的账户不会获得管理员权限"
        action={
          <div className="account-search">
            <Search size={16} />
            <input
              aria-label="搜索邮箱"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="搜索邮箱…"
            />
          </div>
        }
      >
        {loading ? (
          <Loading />
        ) : users.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>用户</th>
                  <th>套餐 / 状态</th>
                  <th>一次性流量</th>
                  <th>有效期</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {users.map((user) => (
                  <tr key={user.id}>
                    <td>
                      <Link className="table-name" to={`/users/${user.id}`}>
                        {user.email}
                      </Link>
                      <div className="table-small">
                        注册于 {date(user.createdAt)}
                      </div>
                    </td>
                    <td>
                      <strong>
                        {user.entitlement?.planName || "尚未分配"}
                      </strong>
                      <div className="table-small">
                        <EntitlementBadge value={user.entitlement} />
                        {!user.enabled && (
                          <Badge tone="warning">账户停用</Badge>
                        )}
                      </div>
                    </td>
                    <td>
                      {user.entitlement ? (
                        <>
                          <span>
                            {bytes(user.entitlement.usedBytes)} /{" "}
                            {user.entitlement.quotaBytes === null
                              ? "不限量"
                              : bytes(user.entitlement.quotaBytes)}
                          </span>
                          <Meter
                            value={user.entitlement.usedBytes}
                            max={user.entitlement.quotaBytes}
                          />
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td>
                      {user.entitlement?.expiresAt
                        ? date(user.entitlement.expiresAt)
                        : "未设置"}
                    </td>
                    <td>
                      <Link className="text-link" to={`/users/${user.id}`}>
                        管理 <ChevronRight size={15} />
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty
            icon={UsersRound}
            title={search ? "没有匹配的账户" : "等待第一位用户"}
            description={
              search
                ? "试试其他邮箱或清空搜索。"
                : "启用用户注册并配置 Turnstile 后，用户可以在网站首页注册。"
            }
            action={
              !search && (
                <Link to="/turnstile" className="button button-secondary">
                  配置用户注册 <ArrowRight size={15} />
                </Link>
              )
            }
          />
        )}
      </Panel>
    </>
  );
}
function Assignment({
  user,
  plans,
  onClose,
  onDone,
}: {
  user: UserProfile;
  plans: Plan[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [planId, setPlanId] = useState(user.entitlement.planId || ""),
    [expires, setExpires] = useState(
      user.entitlement.expiresAt
        ? new Date(new Date(user.entitlement.expiresAt).getTime() + 8 * 3600000)
            .toISOString()
            .slice(0, 16)
        : "",
    ),
    [reset, setReset] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const selected = plans.find((plan) => plan.id === planId);
  const mayAssign =
    selected?.enabled &&
    (selected.saleEnabled ||
      (selected.id === user.entitlement.planId && !reset));
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (!mayAssign)
        throw new Error(
          "该套餐已停售，仅可延长现有套餐，不能新开通、换入或重开",
        );
      const expiresAt = new Date(`${expires}:00+08:00`);
      if (
        !Number.isFinite(expiresAt.getTime()) ||
        expiresAt.getTime() <= Date.now()
      )
        throw new Error("请选择未来的到期时间");
      await api(`/users/${user.user.id}/assignment`, "POST", {
        planId,
        expiresAt: expiresAt.toISOString(),
        resetUsage: reset,
        expectedRevision: user.user.revision,
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
      title={user.entitlement.planId ? "调整用户套餐" : "开通用户套餐"}
      description={user.user.email}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={save}>
        <Field label="套餐">
          <select
            required
            value={planId}
            onChange={(e) => setPlanId(e.target.value)}
          >
            <option value="">选择套餐</option>
            {plans
              .filter(
                (plan) =>
                  (plan.enabled && plan.saleEnabled) ||
                  plan.id === user.entitlement.planId,
              )
              .map((plan) => (
                <option key={plan.id} value={plan.id}>
                  {plan.name}
                  {!plan.enabled
                    ? "（已停用）"
                    : !plan.saleEnabled
                      ? "（已停售，仅可延长）"
                      : ""}
                </option>
              ))}
          </select>
        </Field>
        {selected && (
          <div className="account-plan-summary">
            <span>
              {selected.bandwidthMbps === null
                ? "不限速"
                : `${selected.bandwidthMbps} Mbps 共享`}
            </span>
            <span>
              {selected.quotaBytes === null
                ? "不限流量"
                : bytes(selected.quotaBytes)}
            </span>
            <span>{selected.maxConnections} 个连接</span>
            <span>{selected.nodeIds.length} 个节点</span>
          </div>
        )}
        <Field
          label="到期时间（北京时间 UTC+8）"
          hint="有效期单独为用户设置，不随套餐编辑改变。"
        >
          <input
            type="datetime-local"
            required
            value={expires}
            onChange={(e) => setExpires(e.target.value)}
          />
        </Field>
        {user.entitlement.planId && (
          <label className="account-checkbox">
            <input
              type="checkbox"
              checked={reset}
              onChange={(e) => setReset(e.target.checked)}
            />
            <span>
              <strong>重开套餐并清零本次使用量</strong>
              <small>开始新的计费周期，历史流量记录仍然保留。</small>
            </span>
          </label>
        )}
        <Notice tone={reset ? "warning" : "info"}>
          {reset
            ? "本次开通会重置套餐可用流量，并使旧连接权限失效。请确认这是一次明确的重新开通。"
            : "已用流量将保留。更改套餐后，该用户的额度、带宽、连接上限和节点权限会同步更新。"}
        </Notice>
        {selected && !selected.saleEnabled && (
          <Notice tone="warning">
            套餐已停售，可保留原套餐并调整有效期。重新开通或换入此套餐需要先恢复销售。
          </Notice>
        )}
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button
            variant="secondary"
            type="button"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </Button>
          <Button type="submit" busy={busy} disabled={!mayAssign} icon={Check}>
            {reset ? "确认重新开通" : "保存开通设置"}
          </Button>
        </div>
      </form>
    </Modal>
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
          if (passwordError(password)) { setError(passwordError(password)); return; }
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
  const { id } = useParams(),
    {
      data: response,
      error,
      loading,
      refresh,
    } = usePolling<UserProfileResponse>(`/users/${id}`),
    plans = usePolling<Plan[]>("/plans", 30000);
  const [assignment, setAssignment] = useState(false),
    [password, setPassword] = useState(false),
    [confirm, setConfirm] = useState<{
      title: string;
      description: string;
      action: () => Promise<void>;
    } | null>(null);
  const toast = useToast();
  if (loading) return <Loading />;
  if (!response)
    return <ErrorNotice message={error} retry={() => void refresh()} />;
  const data = normalizeProfile(response);
  const value = data.entitlement;
  return (
    <>
      <Link className="text-link" to="/users">
        <ArrowLeft size={15} /> 返回用户列表
      </Link>
      <PageHeader
        eyebrow="USER DETAILS"
        title={data.user.email}
        description={`注册于 ${date(data.user.createdAt)}`}
        actions={
          <>
            <RefreshButton refresh={refresh} />
            <Button icon={Settings2} onClick={() => setAssignment(true)}>
              {value.planId ? "调整套餐" : "开通套餐"}
            </Button>
          </>
        }
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <ErrorNotice message={plans.error} retry={() => void plans.refresh()} />
      <div className="account-detail-grid">
        <Panel title="当前套餐" action={<EntitlementBadge value={value} />}>
          <div className="account-entitlement">
            <h2>{value.planName || "尚未分配套餐"}</h2>
            <dl>
              <div>
                <dt>到期时间</dt>
                <dd>{value.expiresAt ? date(value.expiresAt) : "未设置"}</dd>
              </div>
              <div>
                <dt>共享带宽</dt>
                <dd>
                  {!value.planId
                    ? "待开通"
                    : value.bandwidthMbps === null
                      ? "不限速"
                      : `${value.bandwidthMbps} Mbps`}
                </dd>
              </div>
              <div>
                <dt>并发 VPN 连接</dt>
                <dd>
                  {value.occupied} / {value.maxConnections || 0}
                </dd>
              </div>
              <div>
                <dt>可用节点</dt>
                <dd>{data.nodes.length} 个</dd>
              </div>
            </dl>
            <Meter value={value.usedBytes} max={value.quotaBytes} label />
            <p className="muted">
              一次性流量，不按月重置。预留 {bytes(value.reservedBytes)}。
            </p>
          </div>
        </Panel>
        <Panel
          title="账户状态"
          action={
            <Badge tone={data.user.enabled ? "success" : "warning"}>
              {data.user.enabled ? "已启用" : "已停用"}
            </Badge>
          }
        >
          <div className="account-control">
            <p>停用账户将拒绝新的登录与连接，并撤销现有连接权限。</p>
            <Button
              variant={data.user.enabled ? "secondary" : "primary"}
              onClick={() =>
                setConfirm({
                  title: data.user.enabled ? "停用这个账户" : "启用这个账户",
                  description: data.user.enabled
                    ? "用户将无法使用当前套餐，已有连接会被撤销。"
                    : "恢复账户使用权限。套餐仍需在有效期和额度范围内。",
                  action: async () => {
                    await api(`/users/${id}`, "PATCH", {
                      enabled: !data.user.enabled,
                      expectedRevision: data.user.revision,
                    });
                    await refresh();
                    toast("账户状态已更新");
                  },
                })
              }
            >
              {data.user.enabled ? "停用账户" : "启用账户"}
            </Button>
            <div className="account-divider" />
            <strong>密码管理</strong>
            <p>当前不提供邮件找回。管理员可以为用户设置新密码。</p>
            <Button
              variant="secondary"
              icon={KeyRound}
              onClick={() => setPassword(true)}
            >
              重置用户密码
            </Button>
          </div>
        </Panel>
      </div>
      <Panel title="连接记录" subtitle="断开中连接在节点确认关闭前仍占用名额">
        <div className="portal-records">
          {data.connections.length ? (
            data.connections.map((connection) => (
              <div key={connection.id}>
                <Cable size={20} />
                <section>
                  <strong>
                    {data.nodes.find((node) => node.id === connection.nodeId)
                      ?.name || connection.nodeId}
                  </strong>
                  <p>开始于 {date(connection.createdAt)}</p>
                </section>
                <Badge
                  tone={
                    connectionActive(connection)
                      ? connection.ratePending
                        ? "warning"
                        : "success"
                      : "muted"
                  }
                >
                  {connectionState(connection)}
                </Badge>
                <Button
                  variant="secondary"
                  disabled={!connectionActive(connection)}
                  icon={Unplug}
                  onClick={() =>
                    setConfirm({
                      title: "断开用户连接",
                      description:
                        "将撤销这个连接。客户端仍可在额度与连接上限内重新连接。",
                      action: async () => {
                        await api(
                          `/users/${id}/connections/${encodeURIComponent(connection.id)}/revoke`,
                          "POST",
                          {},
                        );
                        await refresh();
                      },
                    })
                  }
                >
                  断开
                </Button>
              </div>
            ))
          ) : (
            <Empty
              icon={Cable}
              title="暂无在线连接"
              description="此用户建立 VPN 连接后会显示在这里。"
            />
          )}
        </div>
      </Panel>
      <Panel title="登录记录">
        <div className="portal-records">
          {data.sessions.length ? (
            data.sessions.map((session) => (
              <div key={session.id}>
                <UserRound size={19} />
                <section>
                  <strong>
                    {session.clientName ||
                      (session.kind === "browser"
                        ? "网页浏览器"
                        : "官方客户端")}
                  </strong>
                  <p>最近活动 {date(session.lastSeen)}</p>
                </section>
              </div>
            ))
          ) : (
            <Empty title="暂无会话" description="用户登录后可查看最近活动。" />
          )}
        </div>
      </Panel>
      {assignment && plans.data && (
        <Assignment
          user={data}
          plans={plans.data}
          onClose={() => setAssignment(false)}
          onDone={async () => {
            await refresh();
            toast("用户套餐已更新");
          }}
        />
      )}
      {assignment && !plans.data && (
        <Modal title="读取套餐" onClose={() => setAssignment(false)}>
          {plans.loading ? (
            <Loading />
          ) : (
            <ErrorNotice
              message={plans.error || "无法读取套餐列表"}
              retry={() => void plans.refresh()}
            />
          )}
        </Modal>
      )}
      {password && (
        <ResetPassword id={data.user.id} onClose={() => setPassword(false)} />
      )}
      {confirm && <Confirm {...confirm} onClose={() => setConfirm(null)} />}
    </>
  );
}
function PlanEditor({
  plan,
  nodes,
  onClose,
  onDone,
}: {
  plan?: Plan;
  nodes: Node[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [name, setName] = useState(plan?.name || ""),
    [enabled, setEnabled] = useState(plan?.enabled ?? true),
    [saleEnabled, setSaleEnabled] = useState(plan?.saleEnabled ?? true),
    [bandwidth, setBandwidth] = useState(plan?.bandwidthMbps?.toString() || ""),
    [quota, setQuota] = useState(
      plan?.quotaBytes === null || !plan ? "" : String(plan.quotaBytes / 1e9),
    ),
    [maxConnections, setMaxConnections] = useState(
      String(plan?.maxConnections || 3),
    ),
    [selected, setSelected] = useState(plan?.nodeIds || []),
    [defaultNodeId, setDefault] = useState(plan?.defaultNodeId || "");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [preview, setPreview] = useState<{
      affectedUsers: number;
      activeConnections: number;
      affectedUserIds?: string[];
      connectionsToClose?: {
        id: string;
        userId: string;
        nodeId: string;
        reason: string;
      }[];
    } | null>(null);
  const body = {
    name: name.trim(),
    enabled,
    saleEnabled,
    ...(plan ? { expectedRevision: plan.revision } : {}),
    bandwidthMbps: bandwidth === "" ? null : Number(bandwidth),
    quotaBytes: quota === "" ? null : Math.round(Number(quota) * 1e9),
    maxConnections: Number(maxConnections),
    nodeIds: selected,
    defaultNodeId,
  };
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (!selected.length || !selected.includes(defaultNodeId))
        throw new Error("请至少选择一个节点，并设置集合内的默认节点");
      if (selected.some((id) => !nodes.some((node) => node.id === id && node.lifecycle === "active" && node.capabilities?.includes("admission-v1"))))
        throw new Error("所选节点必须处于活动状态并已支持用户套餐准入，请先升级节点或移除不可用节点");
      if (plan && !preview) {
        setPreview(await api(`/plans/${plan.id}/preview`, "POST", body));
        return;
      }
      await api(
        plan ? `/plans/${plan.id}` : "/plans",
        plan ? "PUT" : "POST",
        body,
      );
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
      title={plan ? "编辑套餐" : "新建套餐"}
      description="套餐规则独立定义，流量按用户分别累计。"
      wide
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="form-stack"
        onSubmit={save}
        onChange={() => setPreview(null)}
      >
        <div className="form-grid">
          <Field label="套餐名称">
            <input
              required
              maxLength={80}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如：畅享套餐"
            />
          </Field>
          <Field label="同时在线 VPN 连接数">
            <input
              type="number"
              required
              min={1}
              max={1000}
              step={1}
              value={maxConnections}
              onChange={(e) => setMaxConnections(e.target.value)}
            />
          </Field>
        </div>
        <div className="form-grid">
          <Field
            label="用户共享带宽（Mbps）"
            hint="留空不限速；跨设备与节点共享。"
          >
            <input
              type="number"
              min={0.001}
              step="any"
              value={bandwidth}
              onChange={(e) => setBandwidth(e.target.value)}
              placeholder="不限速"
            />
          </Field>
          <Field
            label="一次性总流量（GB）"
            hint="1 GB = 1,000,000,000 B；留空不限量。"
          >
            <input
              type="number"
              min={0.000001}
              step="any"
              value={quota}
              onChange={(e) => setQuota(e.target.value)}
              placeholder="不限量"
            />
          </Field>
        </div>
        <fieldset className="account-node-picker">
          <legend>
            可用节点 <span>{selected.length} 个已选</span>
          </legend>
          {nodes
            .filter(
              (node) =>
                node.lifecycle === "active" || selected.includes(node.id),
            )
            .map((node) => (
              <label key={node.id}>
                <input
                  type="checkbox"
                  checked={selected.includes(node.id)}
                  disabled={!selected.includes(node.id) && !node.capabilities?.includes("admission-v1")}
                  onChange={(e) => {
                    setSelected(
                      e.target.checked
                        ? [...selected, node.id]
                        : selected.filter((id) => id !== node.id),
                    );
                    if (!e.target.checked && defaultNodeId === node.id)
                      setDefault("");
                  }}
                />
                <span>
                  <strong>{node.name}</strong>
                  <small>{node.domain || node.publicIp}</small>
                </span>
                <Badge tone={node.capabilities?.includes("admission-v1") && node.status === "ready" ? "success" : "warning"}>
                  {!node.capabilities?.includes("admission-v1") ? "需升级" : node.lifecycle !== "active" ? "已退役" : node.status === "ready" ? "已就绪" : "待就绪"}
                </Badge>
              </label>
            ))}
          {!nodes.length && <p className="muted">暂无节点，请先添加节点。</p>}
        </fieldset>
        <Field label="默认节点" hint="不会强制切换用户仍可用的当前节点。">
          <select
            required
            value={defaultNodeId}
            onChange={(e) => setDefault(e.target.value)}
          >
            <option value="">选择默认节点</option>
            {nodes
              .filter((node) => selected.includes(node.id))
              .map((node) => (
                <option key={node.id} value={node.id}>
                  {node.name}
                </option>
              ))}
          </select>
        </Field>
        <label className="account-checkbox">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>
            <strong>启用套餐服务</strong>
            <small>关闭会停止已分配用户的套餐权益。</small>
          </span>
        </label>
        <label className="account-checkbox">
          <input
            type="checkbox"
            checked={saleEnabled}
            onChange={(e) => setSaleEnabled(e.target.checked)}
          />
          <span>
            <strong>允许新开通</strong>
            <small>
              停售禁止新开通、换入和重开；已有用户继续使用并可延长有效期。
            </small>
          </span>
        </label>
        {preview ? (
          <Notice tone="warning">
            将同步更新 {preview.affectedUsers} 位已分配用户，当前涉及{" "}
            {preview.activeConnections}{" "}
            个在线连接。已用流量与到期时间保留；减少额度或权限可能使现有连接停止。
          </Notice>
        ) : (
          plan && (
            <Notice>
              修改会同步到所有已分配用户。下一步会先显示影响范围。
            </Notice>
          )
        )}
        {preview?.connectionsToClose?.length ? (
          <details className="account-impact-details">
            <summary>
              查看将断开的 {preview.connectionsToClose.length} 个连接
            </summary>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>用户</th>
                    <th>节点</th>
                    <th>原因</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.connectionsToClose.map((connection) => (
                    <tr key={connection.id}>
                      <td>
                        <Link
                          to={`/users/${connection.userId}`}
                          target="_blank"
                        >
                          {connection.userId}
                        </Link>
                      </td>
                      <td>
                        {nodes.find((node) => node.id === connection.nodeId)
                          ?.name || connection.nodeId}
                      </td>
                      <td>{connection.reason}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        ) : (
          preview && <p className="muted">当前没有需要立即断开的连接。</p>
        )}
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
            icon={preview || !plan ? Save : ArrowRight}
            busy={busy}
          >
            {plan && !preview
              ? "预览影响"
              : preview
                ? "确认同步修改"
                : "创建套餐"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
export function Plans() {
  const { data, error, loading, refresh } = usePolling<Plan[]>("/plans", 30000),
    nodes = usePolling<Node[]>("/nodes", 30000);
  const [editor, setEditor] = useState<Plan | "new" | null>(null),
    toast = useToast();
  return (
    <>
      <PageHeader
        eyebrow="PLANS"
        title="套餐管理"
        description="定义共享带宽、一次性流量、连接上限与节点范围。"
        actions={
          <>
            <RefreshButton refresh={refresh} />
            <Button icon={Plus} onClick={() => setEditor("new")}>
              新建套餐
            </Button>
          </>
        }
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <ErrorNotice message={nodes.error} retry={() => void nodes.refresh()} />
      {loading ? (
        <Loading />
      ) : data?.length ? (
        <div className="account-plans-grid">
          {data.map((plan) => (
            <section className="account-plan-card" key={plan.id}>
              <div>
                <span className="portal-icon">
                  <Package size={23} />
                </span>
                <Badge tone={plan.enabled ? "success" : "muted"}>
                  {!plan.enabled
                    ? "已停用"
                    : plan.saleEnabled
                      ? "开放开通"
                      : "已停售"}
                </Badge>
              </div>
              <h2>{plan.name}</h2>
              <p>
                {plan.userCount === undefined
                  ? `规则版本 ${plan.revision}`
                  : `${plan.userCount} 位用户使用中`}
              </p>
              <dl>
                <div>
                  <dt>共享带宽</dt>
                  <dd>
                    {plan.bandwidthMbps === null
                      ? "不限速"
                      : `${plan.bandwidthMbps} Mbps`}
                  </dd>
                </div>
                <div>
                  <dt>一次性流量</dt>
                  <dd>
                    {plan.quotaBytes === null
                      ? "不限量"
                      : bytes(plan.quotaBytes)}
                  </dd>
                </div>
                <div>
                  <dt>VPN 连接上限</dt>
                  <dd>{plan.maxConnections}</dd>
                </div>
                <div>
                  <dt>可用节点</dt>
                  <dd>{plan.nodeIds.length} 个</dd>
                </div>
                <div>
                  <dt>默认节点</dt>
                  <dd>
                    {nodes.data?.find((node) => node.id === plan.defaultNodeId)
                      ?.name || "未设置"}
                  </dd>
                </div>
              </dl>
              <Button
                variant="secondary"
                className="full-width"
                icon={Settings2}
                onClick={() => setEditor(plan)}
              >
                编辑套餐
              </Button>
            </section>
          ))}
        </div>
      ) : (
        !error && (
          <Panel title="套餐列表">
            <Empty
              icon={Package}
              title="创建第一个套餐"
              description="每位用户拥有独立流量额度；套餐修改会同步到已分配用户。"
              action={
                <Button icon={Plus} onClick={() => setEditor("new")}>
                  新建套餐
                </Button>
              }
            />
          </Panel>
        )
      )}
      <Notice>
        到期时间在开通用户时单独设置。一次性流量不按月重置；降低额度后，已用流量超过新额度的用户会停止服务。
      </Notice>
      {editor && nodes.data && (
        <PlanEditor
          plan={editor === "new" ? undefined : editor}
          nodes={nodes.data}
          onClose={() => setEditor(null)}
          onDone={async () => {
            await refresh();
            toast("套餐已保存");
          }}
        />
      )}
      {editor && !nodes.data && (
        <Modal title="加载可用节点" onClose={() => setEditor(null)}>
          {nodes.loading ? (
            <Loading />
          ) : (
            <ErrorNotice
              message={nodes.error || "无法读取节点"}
              retry={() => void nodes.refresh()}
            />
          )}
        </Modal>
      )}
    </>
  );
}
export function TurnstileSettingsPage() {
  const settings = usePolling<TurnstileSettings>("/settings/turnstile", 60000),
    portal = usePolling<{
      registrationEnabled: boolean;
      portalEnabled?: boolean;
    }>("/settings/portal", 60000);
  const [siteKey, setSiteKey] = useState(""),
    [hostnames, setHostnames] = useState(""),
    [revision, setRevision] = useState(0),
    [secret, setSecret] = useState(""),
    [initialized, setInitialized] = useState(false),
    [busy, setBusy] = useState(false),
    [registrationBusy, setRegistrationBusy] = useState(false),
    [error, setError] = useState("");
  const toast = useToast();
  useEffect(() => {
    if (settings.data && !initialized) {
      setSiteKey(settings.data.siteKey);
      setHostnames((settings.data.allowedHostnames || []).join(", "));
      setRevision(settings.data.revision || 0);
      setInitialized(true);
    }
  }, [settings.data, initialized]);
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const saved = await api<TurnstileSettings>("/settings/turnstile", "PUT", {
        siteKey: siteKey.trim(),
        allowedHostnames: hostnames
          .split(/[\s,，]+/)
          .map((value) => value.trim())
          .filter(Boolean),
        expectedRevision: revision,
        ...(secret ? { secretKey: secret } : {}),
      });
      setSecret("");
      setRevision(saved.revision);
      await settings.refresh();
      toast("Turnstile 设置已保存");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <PageHeader
        eyebrow="REGISTRATION & SECURITY"
        title="注册与安全验证"
        description="保护公开注册与用户登录，管理端始终可以进入此页面。"
      />
      <ErrorNotice
        message={settings.error || portal.error}
        retry={() => {
          void settings.refresh();
          void portal.refresh();
        }}
      />
      <Panel
        title="Cloudflare Turnstile"
        subtitle="此处凭据独立于 DNS 与证书设置"
        action={
          <Badge
            tone={
              settings.data?.secretConfigured && settings.data.siteKey
                ? "success"
                : "warning"
            }
          >
            {settings.data?.secretConfigured && settings.data.siteKey
              ? "已配置"
              : "需要配置"}
          </Badge>
        }
      >
        {settings.loading ? (
          <Loading />
        ) : (
          <form className="form-stack settings-form" onSubmit={save}>
            <Field
              label="Site Key"
              hint="在 Cloudflare Turnstile 添加当前公开网站域名。"
            >
              <input
                required
                value={siteKey}
                onChange={(e) => setSiteKey(e.target.value)}
                placeholder="Turnstile Site Key"
                autoComplete="off"
              />
            </Field>
            <Field
              label="允许的公开域名"
              hint="填写完整域名，以逗号或空格分隔；不含协议、端口和通配符。"
            >
              <input
                required
                value={hostnames}
                onChange={(e) => setHostnames(e.target.value)}
                placeholder="network.example.com"
              />
            </Field>
            <Field
              label={
                settings.data?.secretConfigured
                  ? "Secret Key（留空保留）"
                  : "Secret Key"
              }
              hint="加密保存，不会回显或发送到浏览器。"
            >
              <input
                type="password"
                autoComplete="new-password"
                required={!settings.data?.secretConfigured}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={
                  settings.data?.secretConfigured
                    ? "已保存；输入可替换"
                    : "Turnstile Secret Key"
                }
              />
            </Field>
            <Notice>
              未配置成功时，公开注册和用户的新登录会被拒绝。管理员仍可登录管理端修复配置。
            </Notice>
            <ErrorNotice message={error} />
            <div className="modal-actions">
              <Button
                type="button"
                variant="secondary"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    const latest = await api<TurnstileSettings>(
                      "/settings/turnstile",
                    );
                    setSiteKey(latest.siteKey);
                    setHostnames((latest.allowedHostnames || []).join(", "));
                    setRevision(latest.revision);
                    setSecret("");
                    await settings.refresh();
                  } catch (e) {
                    setError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                重新读取设置
              </Button>
              <Button icon={Save} type="submit" busy={busy}>
                保存安全验证设置
              </Button>
            </div>
          </form>
        )}
      </Panel>
      <Panel title="公开注册" subtitle="用户注册后由管理员分配套餐">
        <div className="account-control">
          <div className="account-registration">
            <div>
              <strong>
                {portal.data?.registrationEnabled
                  ? "允许新用户注册"
                  : "新用户注册已关闭"}
              </strong>
              <p>关闭注册不影响已有用户登录。新账户默认没有套餐权限。</p>
            </div>
            <Button
              variant="secondary"
              busy={registrationBusy}
              disabled={!portal.data}
              onClick={async () => {
                setRegistrationBusy(true);
                setError("");
                try {
                  await api("/settings/portal", "PUT", {
                    registrationEnabled: !portal.data?.registrationEnabled,
                  });
                  await portal.refresh();
                  toast("注册设置已更新");
                } catch (e) {
                  setError((e as Error).message);
                } finally {
                  setRegistrationBusy(false);
                }
              }}
            >
              {portal.data?.registrationEnabled ? "关闭注册" : "开放注册"}
            </Button>
          </div>
          <div className="account-divider" />
          <p>
            <ShieldCheck size={15} className="inline-icon" />{" "}
            用户邮箱仅作登录名，不发送验证邮件。忘记密码由管理员重置。
          </p>
          <a href="/" target="_blank" rel="noreferrer" className="text-link">
            打开用户门户 <ArrowRight size={15} />
          </a>
        </div>
      </Panel>
    </>
  );
}
