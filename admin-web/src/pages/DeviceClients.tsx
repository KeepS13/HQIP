import { useState, type FormEvent } from "react";
import {
  KeyRound,
  Monitor,
  Network,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { api, usePolling } from "../api";
import {
  Badge,
  Button,
  CopyButton,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Modal,
  NodeBadge,
  Notice,
  Panel,
  date,
  useToast,
} from "../components";
import type {
  Assignment,
  ClientAccount,
  ClientSession,
  DeviceDetail,
  Node,
  Removal,
} from "../types";

function AccountModal({
  deviceId,
  account,
  serverURL,
  onClose,
  refresh,
}: {
  deviceId: string;
  account: ClientAccount;
  serverURL: string;
  onClose: () => void;
  refresh: () => Promise<void>;
}) {
  const [username, setUsername] = useState(account.username),
    [password, setPassword] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [result, setResult] = useState<{
      username: string;
      password: string;
    } | null>(null);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const credentials = await api<{ username: string; password: string }>(
        `/devices/${deviceId}/client-account`,
        "POST",
        { username: username.trim(), ...(password ? { password } : {}) },
      );
      setPassword("");
      setResult(credentials);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={
        result
          ? "设备账号已就绪"
          : account.initialized
            ? "重置设备登录账号"
            : "初始化设备登录账号"
      }
      description="仅用于 HQIP Windows 客户端，与管理员账号相互独立。"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      {result ? (
        <div className="form-stack">
          <Notice tone="success">
            请现在保存登录信息。关闭后无法再次查看密码，需要重新重置。
          </Notice>
          <Field label="客户端服务地址">
            <input
              readOnly
              value={serverURL || "请从设备订阅地址确认客户端服务地址"}
              onFocus={(e) => e.currentTarget.select()}
            />
          </Field>
          <Field label="设备账号">
            <input
              readOnly
              value={result.username}
              onFocus={(e) => e.currentTarget.select()}
            />
          </Field>
          <Field label="设备密码">
            <input
              readOnly
              value={result.password}
              onFocus={(e) => e.currentTarget.select()}
            />
          </Field>
          <div className="modal-actions">
            <CopyButton
              label="复制登录信息"
              value={`客户端服务：${serverURL}\n账号：${result.username}\n密码：${result.password}`}
            />
            <Button onClick={onClose}>已保存，关闭</Button>
          </div>
        </div>
      ) : (
        <form className="form-stack" onSubmit={submit}>
          {account.initialized && (
            <Notice tone="warning">
              保存后此设备的现有客户端登录会话全部失效，需要使用新密码重新登录。其他设备与管理员会话不受影响。
            </Notice>
          )}
          <Field
            label="设备账号"
            hint="3–64 位，以字母或数字开头，可包含 . _ @ -。"
          >
            <input
              autoComplete="off"
              required
              minLength={3}
              maxLength={64}
              pattern="[A-Za-z0-9][A-Za-z0-9_.@\-]{2,63}"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="例如：my-laptop"
            />
          </Field>
          <Field
            label="设备密码（可留空）"
            hint="留空自动生成高强度密码；自定义至少 12 个字符。"
          >
            <input
              type="password"
              autoComplete="new-password"
              minLength={12}
              maxLength={128}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="自动生成安全密码"
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
            <Button type="submit" busy={busy} icon={KeyRound}>
              {account.initialized ? "确认重置" : "创建登录账号"}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}

function AssignmentModal({
  deviceId,
  assignment,
  onClose,
  refresh,
}: {
  deviceId: string;
  assignment: Assignment;
  onClose: () => void;
  refresh: () => Promise<void>;
}) {
  const {
      data: nodes,
      error: loadError,
      loading,
      refresh: reloadNodes,
    } = usePolling<Node[]>("/nodes"),
    [selected, setSelected] = useState(assignment.nodeIds),
    [defaultId, setDefaultId] = useState(assignment.defaultNodeId),
    [revision, setRevision] = useState(assignment.revision),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const servers = usePolling<import("../types").ManagedServer[]>("/servers");
  const toast = useToast();
  function toggle(id: string) {
    const next = selected.includes(id)
      ? selected.filter((n) => n !== id)
      : [...selected, id];
    setSelected(next);
    if (!next.includes(defaultId)) setDefaultId(next[0] || "");
  }
  async function reload() {
    setBusy(true);
    try {
      const current = await api<DeviceDetail>(`/devices/${deviceId}`);
      if (current.assignment) {
        setSelected(current.assignment.nodeIds);
        setDefaultId(current.assignment.defaultNodeId);
        setRevision(current.assignment.revision);
        setError("");
      }
      await reloadNodes();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api(`/devices/${deviceId}/assignment`, "PATCH", {
        nodeIds: selected,
        defaultNodeId: defaultId,
        expectedRevision: revision,
      });
      await refresh();
      toast("节点分配已更新，等待客户端与节点确认");
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title="分配节点与默认连接"
      description={`正在编辑配置版本 v${revision}。保存时检查版本，避免覆盖其他操作。`}
      wide
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={submit}>
        <Notice>
          按 IP 选择该设备可使用的节点，再设置默认节点。同一服务器上的其他 IP 不会自动获得授权。客户端手动断开后不会因配置更新自动连接；已连接时会按照新的默认节点切换。
        </Notice>
        <ErrorNotice message={loadError} retry={() => void reloadNodes()} />
        {loading ? (
          <Loading />
        ) : nodes?.length ? (
          <div className="assignment-options">
            {[...new Set(nodes.filter(n => n.lifecycle === "active").map(n => n.serverId))].map(serverId => <section key={serverId} className="assignment-server"><h3>{servers.data?.find(s => s.id === serverId)?.name || "服务器"}</h3>{nodes.filter(n => n.serverId === serverId && n.lifecycle === "active").map(node => <label className={`assignment-option ${selected.includes(node.id) ? "selected" : ""}`} key={node.id}><input type="checkbox" checked={selected.includes(node.id)} onChange={() => toggle(node.id)} /><span><strong>{node.name} · {node.publicIp}</strong><small>{node.domain}</small></span><NodeBadge node={node} /></label>)}</section>)}
          </div>
        ) : (
          <Empty
            title="还没有可分配的节点"
            description="请先添加服务器并完成部署。"
          />
        )}
        <Field label="默认连接节点">
          <select
            value={defaultId}
            onChange={(e) => setDefaultId(e.target.value)}
            disabled={!selected.length}
            required={selected.length > 0}
          >
            {!selected.length && <option value="">未分配节点</option>}
            {selected.map((id) => {
              const node = nodes?.find((n) => n.id === id);
              return (
                <option key={id} value={id}>
                  {node ? `${node.name} · ${node.publicIp}` : id}
                  {node && node.status !== "ready" ? " · 尚未就绪" : ""}
                </option>
              );
            })}
          </select>
        </Field>
        {assignment.nodeIds.some((id) => !selected.includes(id)) && (
          <Notice tone="warning">
            移除节点将撤销此设备在原节点的凭据。系统先等待客户端切换，再由节点执行撤销；失联节点在确认之前显示「待同步」。
          </Notice>
        )}
        {!selected.length && (
          <Notice tone="warning">
            保存空节点集合后，此设备将没有可连接节点。
          </Notice>
        )}
        <ErrorNotice message={error} />
        {error && (
          <Button
            type="button"
            variant="secondary"
            icon={RefreshCw}
            disabled={busy}
            onClick={() => void reload()}
          >
            丢弃本次编辑，加载最新分配
          </Button>
        )}
        <div className="modal-actions">
          <Button
            type="button"
            variant="secondary"
            onClick={onClose}
            disabled={busy}
          >
            取消
          </Button>
          <Button type="submit" icon={Network} busy={busy} disabled={loading}>
            保存节点分配
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function sessionStatus(session: ClientSession) {
  if (!session.lastSeen || Date.now() - Date.parse(session.lastSeen) > 30000)
    return { label: "心跳过期", tone: "warning" };
  const states: Record<string, { label: string; tone: string }> = {
    connected: { label: "已连接", tone: "success" },
    recovering: { label: "恢复中", tone: "info" },
    connecting: { label: "连接中", tone: "info" },
    disconnected: { label: "已断开", tone: "muted" },
    failed: { label: "连接失败", tone: "danger" },
    cached: { label: "使用缓存配置", tone: "warning" },
  };
  return states[session.runState] || { label: "等待状态上报", tone: "muted" };
}

function ClientSessions({
  sessions,
  nodes,
}: {
  sessions: ClientSession[];
  nodes: Node[];
}) {
  const nodeName = (id: string) =>
    nodes.find((n) => n.id === id)?.name || (id ? `节点 ${id}` : "未连接");
  return (
    <Panel
      title="Windows 客户端会话"
      subtitle="展示客户端实际运行状态；心跳超过 30 秒后不再视为当前状态"
    >
      {sessions.length ? (
        <div className="client-session-list">
          {sessions.map((session) => {
            const state = sessionStatus(session);
            return (
              <article className="client-session" key={session.id}>
                <div className="client-session-heading">
                  <span className="device-icon">
                    <Monitor size={18} />
                  </span>
                  <div>
                    <strong>{session.clientName || "Windows 客户端"}</strong>
                    <small>
                      {session.coreVersion
                        ? `sing-box ${session.coreVersion}`
                        : "核心版本尚未上报"}
                    </small>
                  </div>
                  <Badge tone={state.tone}>{state.label}</Badge>
                </div>
                <dl className="client-session-data">
                  <div>
                    <dt>实际节点</dt>
                    <dd>{nodeName(session.actualNodeId)}</dd>
                  </div>
                  <div>
                    <dt>客户端目标节点</dt>
                    <dd>{nodeName(session.desiredNodeId)}</dd>
                  </div>
                  <div>
                    <dt>代理模式</dt>
                    <dd>
                      {session.mode === "rule"
                        ? "规则分流"
                        : session.mode === "global"
                          ? "全局代理"
                          : session.mode === "direct"
                            ? "直连"
                            : "尚未上报"}
                      {session.tunEnabled ? " · TUN" : ""}
                    </dd>
                  </div>
                  <div>
                    <dt>连接意愿</dt>
                    <dd>
                      {session.connectionIntent ? "保持连接" : "保持断开"}
                    </dd>
                  </div>
                  <div>
                    <dt>配置版本</dt>
                    <dd>
                      v{session.appliedConfigRevision}{" "}
                      <span className="muted">
                        / 目标 v{session.desiredConfigRevision}
                      </span>
                      <span className="table-small">
                        已处理 v{session.processedConfigRevision || 0}
                      </span>
                    </dd>
                  </div>
                  <div>
                    <dt>最近心跳</dt>
                    <dd>{date(session.lastSeen)}</dd>
                  </div>
                </dl>
                {session.revocationPending && (
                  <div className="client-session-sync">
                    <Badge tone="warning">配置待同步</Badge>
                    <span>尚未确认目标配置，实际连接以上报为准。</span>
                  </div>
                )}
                {session.lastError && (
                  <ErrorNotice message={session.lastError} />
                )}
              </article>
            );
          })}
        </div>
      ) : (
        <Empty
          icon={Monitor}
          title="尚无客户端登录会话"
          description="初始化设备账号后，在 HQIP Windows 客户端中登录即可查看运行状态。"
        />
      )}
    </Panel>
  );
}

function RemovalStatus({
  removals,
  nodes,
}: {
  removals: Removal[];
  nodes: Node[];
}) {
  if (!removals.length) return null;
  return (
    <Panel
      title="节点撤销进度"
      subtitle="以节点的执行确认作为完成依据，不以截止时间推断成功"
    >
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>原节点</th>
              <th>撤销状态</th>
              <th>原连接最晚保留至</th>
              <th>配置版本</th>
            </tr>
          </thead>
          <tbody>
            {removals.slice(0, 30).map((removal) => (
              <tr key={removal.id}>
                <td>
                  {nodes.find((n) => n.id === removal.nodeId)?.name ||
                    removal.nodeId}
                </td>
                <td>
                  <Badge
                    tone={removal.status === "revoked" ? "success" : "warning"}
                  >
                    {removal.status === "revoked"
                      ? "已撤销"
                      : removal.status === "scheduled"
                        ? "节点已接收，等待撤销"
                        : "待节点同步"}
                  </Badge>
                </td>
                <td className="muted">
                  {removal.status === "revoked"
                    ? "已完成"
                    : removal.deadline
                      ? date(removal.deadline)
                      : "节点接收后开始计时"}
                </td>
                <td>v{removal.revision}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Panel>
  );
}

export function DeviceClients({
  detail,
  refresh,
}: {
  detail: DeviceDetail;
  refresh: () => Promise<void>;
}) {
  const [accountOpen, setAccountOpen] = useState(false),
    [assignmentOpen, setAssignmentOpen] = useState(false);
  const account = detail.clientAccount || { username: "", initialized: false },
    assignment = detail.assignment;
  const assigned = (assignment?.nodeIds || []).map(
      (id) => detail.nodes.find((n) => n.id === id)?.name || id,
    ),
    defaultNode = detail.nodes.find((n) => n.id === assignment?.defaultNodeId);
  let serverURL = "";
  try {
    if (detail.device.subscriptionUrl)
      serverURL = new URL(detail.device.subscriptionUrl).origin;
  } catch {
    /* Keep an unknown origin explicit. */
  }
  return (
    <>
      <div className="two-column">
        <Panel
          title="Windows 客户端账号"
          subtitle="通过设备账号登录，无需手动复制节点密码"
          action={
            <Button
              variant="secondary"
              icon={KeyRound}
              onClick={() => setAccountOpen(true)}
            >
              {account.initialized ? "重置账号" : "初始化账号"}
            </Button>
          }
        >
          <div className="client-account-summary">
            <span className="client-account-icon">
              <ShieldCheck size={26} />
            </span>
            <div>
              <strong>
                {account.initialized ? account.username : "尚未初始化"}
              </strong>
              <p>
                {account.initialized
                  ? "登录密码不可回读，重置后需要重新登录。"
                  : "为这台设备创建独立的客户端登录账号。"}
              </p>
            </div>
          </div>
        </Panel>
        <Panel
          title="允许连接的节点"
          subtitle={
            assignment ? `配置版本 v${assignment.revision}` : "正在读取节点分配"
          }
          action={
            <Button
              variant="secondary"
              icon={Network}
              disabled={!assignment}
              onClick={() => setAssignmentOpen(true)}
            >
              分配节点
            </Button>
          }
        >
          <div className="assignment-summary">
            <div>
              <span>默认节点</span>
              <strong>
                {defaultNode?.name || assignment?.defaultNodeId || "未设置"}
              </strong>
            </div>
            <div>
              <span>节点集合</span>
              <p>{assigned.length ? assigned.join("、") : "尚未分配节点"}</p>
            </div>
            <small>客户端只会收到已分配且完成节点配置同步的连接凭据。</small>
          </div>
        </Panel>
      </div>
      <ClientSessions sessions={detail.sessions || []} nodes={detail.nodes} />
      <RemovalStatus removals={detail.removals || []} nodes={detail.nodes} />
      {accountOpen && (
        <AccountModal
          deviceId={detail.device.id}
          account={account}
          serverURL={serverURL}
          onClose={() => setAccountOpen(false)}
          refresh={refresh}
        />
      )}{" "}
      {assignmentOpen && assignment && (
        <AssignmentModal
          deviceId={detail.device.id}
          assignment={assignment}
          onClose={() => setAssignmentOpen(false)}
          refresh={refresh}
        />
      )}
    </>
  );
}
