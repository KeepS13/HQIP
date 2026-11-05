import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  ArrowRight,
  Eye,
  EyeOff,
  Package,
  Plus,
  Save,
  Settings2,
} from "lucide-react";
import { api, usePolling } from "../api";
import {
  Badge,
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
  bytes,
  useToast,
} from "../components";
import type { Node } from "../types";
import type { SubscriptionImpact, SubscriptionTemplate } from "../portal/types";
import "./accounts.css";
import "./subscriptions.css";

export function TemplateFacts({ value }: { value: SubscriptionTemplate }) {
  return (
    <dl className="subscription-facts">
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
        <dt>每份共享带宽</dt>
        <dd>
          {value.bandwidthMbps === null
            ? "不限速"
            : `${value.bandwidthMbps} Mbps`}
        </dd>
      </div>
      <div>
        <dt>每份连接上限</dt>
        <dd>{value.maxConnections} 个 VPN 连接</dd>
      </div>
      <div>
        <dt>可用节点</dt>
        <dd>{value.nodeIds.length} 个</dd>
      </div>
    </dl>
  );
}

function TemplateEditor({
  template,
  nodes,
  servers,
  onClose,
  onDone,
}: {
  template?: SubscriptionTemplate;
  nodes: Node[];
  servers: { id: string; name: string }[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [name, setName] = useState(template?.name || "");
  const [enabled, setEnabled] = useState(template?.enabled ?? true);
  const [listed, setListed] = useState(template?.listed ?? false);
  const [days, setDays] = useState(
    template ? String(template.validityDays) : "30",
  );
  const [bandwidth, setBandwidth] = useState(
    template?.bandwidthMbps?.toString() || "",
  );
  const [quota, setQuota] = useState(
    template?.quotaBytes == null ? "" : String(template.quotaBytes / 1e9),
  );
  const [maxConnections, setMaxConnections] = useState(
    String(template?.maxConnections || 1),
  );
  const [selected, setSelected] = useState(template?.nodeIds || []);
  const [defaultNodeId, setDefault] = useState(template?.defaultNodeId || "");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [preview, setPreview] = useState<SubscriptionImpact | null>(null);
  const [filter, setFilter] = useState("");
  const body = {
    name: name.trim(),
    enabled,
    listed,
    validityDays: Number(days),
    bandwidthMbps: bandwidth === "" ? null : Number(bandwidth),
    quotaBytes: quota === "" ? null : Math.round(Number(quota) * 1e9),
    maxConnections: Number(maxConnections),
    nodeIds: selected,
    defaultNodeId,
    ...(template ? { expectedRevision: template.revision } : {}),
  };
  const available = nodes.filter(
    (n) => n.lifecycle === "active" || selected.includes(n.id),
  );
  const groups = Array.from(new Set(available.map((n) => n.serverId || "")));
  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (!Number.isInteger(body.validityDays) || body.validityDays < 1)
        throw new Error("有效期至少为 1 天，请填写整数");
      if (!selected.length || !selected.includes(defaultNodeId))
        throw new Error("请选择可用节点，并指定其中一个默认节点");
      if (
        selected.some(
          (id) =>
            !nodes.some(
              (n) =>
                n.id === id &&
                n.lifecycle === "active" &&
                n.capabilities?.includes("admission-v1"),
            ),
        )
      )
        throw new Error("请选择已支持订阅准入的活动节点");
      if (template && !preview) {
        setPreview(
          await api<SubscriptionImpact>(
            `/subscription-templates/${template.id}/preview`,
            "POST",
            body,
          ),
        );
        return;
      }
      await api(
        template
          ? `/subscription-templates/${template.id}`
          : "/subscription-templates",
        template ? "PUT" : "POST",
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
      wide
      title={template ? "编辑订阅模板" : "新建订阅模板"}
      description="每次开通都会创建独立流量包，开通时开始计算有效期。"
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
          <Field label="订阅名称">
            <input
              required
              maxLength={80}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如：轻享 100 GB"
            />
          </Field>
          <Field
            label="有效期（天）"
            hint="每次开通时起算，修改不延长已开通订阅。"
          >
            <input
              required
              type="number"
              min={1}
              step={1}
              value={days}
              onChange={(e) => setDays(e.target.value)}
            />
          </Field>
          <Field
            label="每份共享带宽（Mbps）"
            hint="留空不限速；同一份订阅的连接共享。"
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
            hint="上传＋下载；不按月重置。留空不限量。"
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
          <Field
            label="每份同时在线连接数"
            hint="实际 VPN 连接计数，登录不会占用。"
          >
            <input
              required
              type="number"
              min={1}
              max={1000}
              step={1}
              value={maxConnections}
              onChange={(e) => setMaxConnections(e.target.value)}
            />
          </Field>
        </div>
        <fieldset className="account-node-picker subscription-node-picker">
          <legend>
            可用节点 <span>{selected.length} 个已选</span>
          </legend>
          <input
            aria-label="搜索节点"
            placeholder="搜索名称、IP 或域名"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
          {groups.map((group) => {
            const items = available.filter(
              (n) =>
                (n.serverId || "") === group &&
                `${n.name} ${n.publicIp} ${n.domain}`
                  .toLowerCase()
                  .includes(filter.toLowerCase()),
            );
            return items.length ? (
              <div className="subscription-node-group" key={group}>
                <h4>
                  {servers.find((s) => s.id === group)?.name || "服务器节点"}
                </h4>
                {items.map((node) => (
                  <label key={node.id}>
                    <input
                      type="checkbox"
                      checked={selected.includes(node.id)}
                      disabled={
                        !selected.includes(node.id) &&
                        !node.capabilities?.includes("admission-v1")
                      }
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
                      <small>
                        {node.publicIp} · {node.domain}
                      </small>
                    </span>
                    <Badge
                      tone={
                        node.capabilities?.includes("admission-v1") &&
                        node.status === "ready"
                          ? "success"
                          : "warning"
                      }
                    >
                      {!node.capabilities?.includes("admission-v1")
                        ? "需升级"
                        : node.lifecycle !== "active"
                          ? "已退役"
                          : node.status === "ready"
                            ? "已就绪"
                            : "待就绪"}
                    </Badge>
                  </label>
                ))}
              </div>
            ) : null;
          })}
          {!available.length && <p className="muted">请先添加并升级节点。</p>}
        </fieldset>
        <Field
          label="默认节点"
          hint="首次选择订阅或当前节点被移除时使用；默认节点不可用则停止连接。"
        >
          <select
            required
            value={defaultNodeId}
            onChange={(e) => setDefault(e.target.value)}
          >
            <option value="">选择默认节点</option>
            {nodes
              .filter((n) => selected.includes(n.id))
              .map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name} · {n.publicIp}
                </option>
              ))}
          </select>
        </Field>
        <label className="account-checkbox">
          <input
            type="checkbox"
            checked={listed}
            onChange={(e) => setListed(e.target.checked)}
          />
          <span>
            <strong>展示给用户申请</strong>
            <small>显示在订阅商城；隐藏后管理员仍可主动分配。</small>
          </span>
        </label>
        <label className="account-checkbox">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>
            <strong>启用订阅服务</strong>
            <small>停用会停止该模板已开通订阅的使用权限。</small>
          </span>
        </label>
        {template && (
          <Notice tone={preview ? "warning" : "info"}>
            {preview
              ? `将影响 ${preview.affectedUsers} 位用户的 ${preview.affectedSubscriptions} 份订阅，当前 ${preview.activeConnections} 个在线连接。`
              : "保存前会预览影响范围。节点、带宽、额度和连接上限会同步；各份已用流量与到期时间保留。"}
          </Notice>
        )}
        {preview && (
          <div className="subscription-impact">
            <strong>
              {preview.connectionsToClose?.length
                ? `将停止 ${preview.connectionsToClose.length} 个连接`
                : "当前没有需要立即停止的连接"}
            </strong>
            {preview.connectionsToClose?.map((c) => (
              <p key={c.id}>
                <Link to={`/users/${c.userId}`}>
                  用户 {c.userId.slice(0, 8)}
                </Link>{" "}
                · {nodes.find((n) => n.id === c.nodeId)?.name || c.nodeId} ·{" "}
                {c.reason}
              </p>
            ))}
          </div>
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
            busy={busy}
            icon={template && !preview ? ArrowRight : Save}
          >
            {template && !preview
              ? "预览影响"
              : template
                ? "确认保存"
                : "创建订阅模板"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function SubscriptionTemplates() {
  const list = usePolling<SubscriptionTemplate[]>("/subscription-templates");
  const nodes = usePolling<Node[]>("/nodes", 15000),
    servers = usePolling<{ id: string; name: string }[]>("/servers", 15000);
  const [editing, setEditing] = useState<SubscriptionTemplate | "new" | null>(
    null,
  );
  const [filter, setFilter] = useState("all"),
    [search, setSearch] = useState("");
  const toast = useToast();
  const visible = (list.data || []).filter(
    (t) =>
      t.name.toLowerCase().includes(search.toLowerCase()) &&
      (filter === "all" ||
        (filter === "listed" && t.listed) ||
        (filter === "hidden" && !t.listed) ||
        (filter === "disabled" && !t.enabled)),
  );
  return (
    <>
      <PageHeader
        eyebrow="SUBSCRIPTIONS"
        title="订阅管理"
        description="一次开通，一份流量包。用户可同时持有多份相同订阅。"
        actions={
          <>
            <RefreshButton refresh={list.refresh} />
            <Button icon={Plus} onClick={() => setEditing("new")}>
              新建订阅
            </Button>
          </>
        }
      />
      <ErrorNotice
        message={list.error ? `${list.error}；当前保留最近一次数据。` : ""}
        retry={() => void list.refresh()}
      />
      <div className="subscription-toolbar">
        <input
          aria-label="搜索订阅模板"
          placeholder="搜索订阅名称"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <select
          aria-label="筛选订阅模板"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        >
          <option value="all">全部模板</option>
          <option value="listed">商城展示</option>
          <option value="hidden">仅后台分配</option>
          <option value="disabled">已停用</option>
        </select>
        <Link to="/subscription-requests" className="button button-secondary">
          开通申请 <ArrowRight size={15} />
        </Link>
      </div>
      {list.loading ? (
        <Loading />
      ) : visible.length ? (
        <div className="account-plans-grid">
          {visible.map((t) => (
            <article className="account-plan-card" key={t.id}>
              <div>
                <span className="subscription-icon">
                  <Package size={23} />
                </span>
                <Badge tone={t.enabled ? "success" : "warning"}>
                  {t.enabled ? "服务启用" : "已停用"}
                </Badge>
              </div>
              <h2>{t.name}</h2>
              <p className="subscription-visibility">
                {t.listed ? <Eye size={14} /> : <EyeOff size={14} />}
                {t.listed ? "用户可申请" : "仅后台分配"}
              </p>
              <TemplateFacts value={t} />
              <Button
                className="full-width"
                variant="secondary"
                icon={Settings2}
                onClick={() => setEditing(t)}
              >
                编辑订阅
              </Button>
            </article>
          ))}
        </div>
      ) : (
        <Panel title="订阅模板">
          <Empty
            icon={Package}
            title={
              search || filter !== "all"
                ? "没有匹配的订阅"
                : "创建第一份订阅模板"
            }
            description="设置流量、有效期和节点后，管理员即可分配，或开放用户申请。"
            action={
              <Button icon={Plus} onClick={() => setEditing("new")}>
                新建订阅
              </Button>
            }
          />
        </Panel>
      )}
      {editing &&
        (nodes.data && servers.data ? (
          <TemplateEditor
            template={editing === "new" ? undefined : editing}
            nodes={nodes.data}
            servers={servers.data}
            onClose={() => setEditing(null)}
            onDone={async () => {
              await list.refresh();
              toast("订阅模板已保存");
            }}
          />
        ) : (
          <Modal title="读取节点" onClose={() => setEditing(null)}>
            {nodes.error || servers.error ? (
              <ErrorNotice
                message={nodes.error || servers.error}
                retry={() => {
                  void nodes.refresh();
                  void servers.refresh();
                }}
              />
            ) : (
              <Loading />
            )}
          </Modal>
        ))}
    </>
  );
}
