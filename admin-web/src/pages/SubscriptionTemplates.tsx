import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  ArrowRight,
  ChevronDown,
  Gauge,
  Network,
  Search,
  Store,
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
import { parsePriceInput, priceText } from "../payments";
import { compareResourceNames } from "../resource-order";
import "./accounts.css";
import "./subscriptions.css";

export function TemplateFacts({ value }: { value: SubscriptionTemplate }) {
  return (
    <dl className="subscription-facts">
      <div>
        <dt>商城售价</dt>
        <dd>{priceText(value.priceCents)}</dd>
      </div>
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
        <dt>每份同时在线上限</dt>
        <dd>{value.maxConnections} 个</dd>
      </div>
      <div>
        <dt>每份分配节点</dt>
        <dd>
          {value.nodesPerSubscription ?? value.nodeIds.length} 个 / 候选池{" "}
          {value.nodeIds.length} 个
        </dd>
      </div>
    </dl>
  );
}

function SubscriptionCardPreview({ value, price }: {
  value: Pick<SubscriptionTemplate, "name" | "description" | "validityDays" | "quotaBytes" | "bandwidthMbps" | "maxConnections" | "nodesPerSubscription">;
  price: string;
}) {
  let priceCents: number | null = null;
  try { priceCents = parsePriceInput(price); } catch { /* An incomplete draft can still be previewed. */ }
  const quantity = (value: number) => Number.isFinite(value) && value > 0 ? value : "—";
  const lines = value.description.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  return (
    <article className="subscription-product-preview">
      <div className="subscription-product-heading">
        <span className="subscription-icon"><Package size={23} /></span>
        <Badge tone="info">一次性流量包</Badge>
      </div>
      <h3>{value.name || "订阅名称"}</h3>
      <p className="subscription-product-subtitle">每份独立生效，同一套餐的节点不重复分配。</p>
      <div className="subscription-product-price">
        <strong>{priceCents === null ? "暂未定价" : priceText(priceCents)}</strong>
        {priceCents !== null && <span>/ 份</span>}
      </div>
      <dl className="subscription-facts">
        <div><dt>一次性总流量</dt><dd>{value.quotaBytes === null ? "不限量" : Number.isFinite(value.quotaBytes) && value.quotaBytes > 0 ? bytes(value.quotaBytes) : "—"}</dd></div>
        <div><dt>有效期</dt><dd>开通后 {quantity(value.validityDays)} 天</dd></div>
        <div><dt>共享带宽</dt><dd>{value.bandwidthMbps === null ? "不限速" : `${quantity(value.bandwidthMbps)} Mbps`}</dd></div>
        <div><dt>同时在线</dt><dd>{quantity(value.maxConnections)} 个</dd></div>
        <div><dt>每份分配节点</dt><dd>{quantity(value.nodesPerSubscription)} 个</dd></div>
      </dl>
      {!!lines.length && <ul className="subscription-product-description">{lines.map((line, index) => <li key={index}>{line}</li>)}</ul>}
      <Button type="button" icon={ArrowRight} disabled className="subscription-product-buy">{priceCents === null ? "暂不可购买" : "购买订阅"}</Button>
    </article>
  );
}

function TemplateEditor({
  template,
  nodes,
  onClose,
  onDone,
}: {
  template?: SubscriptionTemplate;
  nodes: Node[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [name, setName] = useState(template?.name || "");
  const [description, setDescription] = useState(template?.description || "");
  const [showProductPreview, setShowProductPreview] = useState(false);
  const [nodeCount, setNodeCount] = useState(
    String(
      template?.nodesPerSubscription ??
        (template ? Math.max(1, template.nodeIds.length) : 1),
    ),
  );
  const [savedShortfalls, setSavedShortfalls] = useState<
    SubscriptionTemplate["allocationShortfalls"] | null
  >(null);
  const [enabled, setEnabled] = useState(template?.enabled ?? true);
  const [listed, setListed] = useState(template?.listed ?? false);
  const [days, setDays] = useState(
    template ? String(template.validityDays) : "30",
  );
  const [price, setPrice] = useState(
    template?.priceCents == null ? "" : (template.priceCents / 100).toFixed(2),
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
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [preview, setPreview] = useState<SubscriptionImpact | null>(null);
  const previewDetails = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (preview) previewDetails.current?.scrollIntoView({ block: "nearest" });
  }, [preview]);
  const [filter, setFilter] = useState("");
  const body = {
    name: name.trim(),
    description,
    nodesPerSubscription: Number(nodeCount),
    enabled,
    listed,
    validityDays: Number(days),
    bandwidthMbps: bandwidth === "" ? null : Number(bandwidth),
    quotaBytes: quota === "" ? null : Math.round(Number(quota) * 1e9),
    maxConnections: Number(maxConnections),
    nodeIds: selected,
    ...(template ? { expectedRevision: template.revision } : {}),
  };
  const eligible = nodes.filter(
    (n) => n.lifecycle === "active" && n.capabilities?.includes("admission-v1"),
  );
  const invalidSelected = selected.filter(
    (id) => !eligible.some((node) => node.id === id),
  );
  const query = filter.trim().toLowerCase();
  const available = eligible
    .filter((node) =>
      `${node.name} ${node.publicIp} ${node.domain}`
        .toLowerCase()
        .includes(query),
    )
    .sort(compareResourceNames);
  function removeSelection(id: string) {
    setSelected((value) => value.filter((selectedId) => selectedId !== id));
    setPreview(null);
    setError("");
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const payload = { ...body, priceCents: parsePriceInput(price) };
      if (!Number.isInteger(body.validityDays) || body.validityDays < 1)
        throw new Error("有效期至少为 1 天，请填写整数");
      if (
        !Number.isInteger(body.nodesPerSubscription) ||
        body.nodesPerSubscription < 1 ||
        body.nodesPerSubscription > 1000
      )
        throw new Error("每份分配节点数须为1至1000的整数");
      if (Array.from(description).length > 2000)
        throw new Error("订阅介绍最多2000字");
      if (invalidSelected.length)
        throw new Error("请先移除不可用节点的引用，再保存订阅模板");
      if (template && !preview) {
        setPreview(
          await api<SubscriptionImpact>(
            `/subscription-templates/${template.id}/preview`,
            "POST",
            payload,
          ),
        );
        return;
      }
      const saved = await api<SubscriptionTemplate>(
        template
          ? `/subscription-templates/${template.id}`
          : "/subscription-templates",
        template ? "PUT" : "POST",
        payload,
      );
      if (saved.allocationShortfalls?.length) {
        setSavedShortfalls(saved.allocationShortfalls);
        return;
      }
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
      key={showProductPreview ? "product-preview" : "editor"}
      wide={!showProductPreview}
      title={
        showProductPreview
          ? "订阅预览"
          : template
            ? "编辑订阅模板"
            : "新建订阅模板"
      }
      description={
        showProductPreview
          ? "预览当前编辑内容；实际库存与购买状态因用户而异。"
          : "每次开通都会创建独立流量包，开通时开始计算有效期。"
      }
      onClose={() => {
        if (showProductPreview) setShowProductPreview(false);
        else if (!busy) onClose();
      }}
    >
      {showProductPreview ? (
        <div className="subscription-storefront-preview">
          <SubscriptionCardPreview value={body} price={price} />
          <div className="modal-actions">
            <Button
              type="button"
              variant="secondary"
              onClick={() => setShowProductPreview(false)}
            >
              返回编辑
            </Button>
          </div>
        </div>
      ) : savedShortfalls ? (
        <div className="form-stack">
          <Notice tone="warning">已保存。以下订阅或订单暂未补足节点：</Notice>
          <ul>
            {savedShortfalls.map((item) => (
              <li key={item.subscriptionId || item.orderId}>
                {item.subscriptionId ? "订阅" : "订单"}{" "}
                {item.subscriptionId || item.orderId}：{item.assigned} /{" "}
                {item.required} 个节点
              </li>
            ))}
          </ul>
          <Button
            onClick={async () => {
              await onDone();
              onClose();
            }}
          >
            完成
          </Button>
        </div>
      ) : (
        <form
          className="form-stack subscription-editor"
          onSubmit={save}
          onChange={() => setPreview(null)}
        >
          <div className="subscription-editor-layout">
            <div className="subscription-editor-settings">
              <section
                className="subscription-editor-section"
                aria-label="基本信息"
              >
                <h3>
                  <Package size={16} aria-hidden="true" />
                  基本信息
                </h3>
                <div className="subscription-editor-name">
                  <Field label="订阅名称">
                    <input
                      required
                      maxLength={80}
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="例如：轻享 100 GB"
                    />
                  </Field>
                </div>
                <div className="form-grid">
                  <Field
                    label="有效期（天）"
                    hint="开通时起算，不延长已有订阅。"
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
                    label="售价（人民币元）"
                    hint="¥1 起；留空仅支持管理员赠送。"
                  >
                    <input
                      inputMode="decimal"
                      value={price}
                      maxLength={12}
                      onChange={(e) => setPrice(e.target.value)}
                      placeholder="未设置价格"
                    />
                  </Field>
                </div>
              </section>
              <section
                className="subscription-editor-section"
                aria-label="额度与连接"
              >
                <h3>
                  <Gauge size={16} aria-hidden="true" />
                  额度与连接
                </h3>
                <div className="form-grid">
                  <Field
                    label="一次性总流量（GB）"
                    hint="上传＋下载，不重置；留空不限量。"
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
                    label="每份共享带宽（Mbps）"
                    hint="同一份订阅共享；留空不限速。"
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
                    label="每份同时在线上限"
                    hint="HY2 每个在线客户端实例占一个名额；VLESS 按来源 IP 去重。两者合计，跨节点共享上限；分配节点数独立设置。"
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
                  <Field label="每份分配节点数" hint="仅影响新订单及新开通。">
                    <input
                      required
                      type="number"
                      min={1}
                      max={1000}
                      step={1}
                      value={nodeCount}
                      onChange={(e) => setNodeCount(e.target.value)}
                    />
                  </Field>
                </div>
              </section>
              <section
                className="subscription-editor-section"
                aria-label="商城与服务"
              >
                <h3>
                  <Store size={16} aria-hidden="true" />
                  商城与服务
                </h3>
                <div className="subscription-setting-toggles">
                  <label className="account-checkbox subscription-setting-toggle">
                    <input
                      type="checkbox"
                      checked={listed}
                      onChange={(e) => setListed(e.target.checked)}
                    />
                    <span>
                      <strong>展示在订阅商城</strong>
                      <small>设置价格并启用支付后可购买。</small>
                    </span>
                  </label>
                  <label className="account-checkbox subscription-setting-toggle">
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
                </div>
                <details className="subscription-description">
                  <summary>
                    <span>
                      订阅介绍 <small>选填 · 商城展示</small>
                    </span>
                    <span
                      className="subscription-description-count"
                      data-over-limit={Array.from(description).length > 2000}
                    >
                      {Array.from(description).length}
                      <span> / 2000</span>
                    </span>
                    <ChevronDown size={15} aria-hidden="true" />
                  </summary>
                  <div className="subscription-description-editor">
                    <label htmlFor="subscription-description-input">
                      订阅介绍
                    </label>
                    <textarea
                      id="subscription-description-input"
                      rows={4}
                      value={description}
                      onChange={(e) => setDescription(e.target.value)}
                      aria-describedby="subscription-description-help"
                      aria-invalid={Array.from(description).length > 2000}
                      placeholder={"例如：适合日常浏览与影音\n支持多设备使用"}
                    />
                    <p id="subscription-description-help">
                      每行一条特点，商城将按列表展示。可在下方预览订阅。
                    </p>
                  </div>
                </details>
              </section>
              <details className="subscription-editor-help">
                <summary>
                  分配与生效说明 <ChevronDown size={14} aria-hidden="true" />
                </summary>
                <p>
                  每次开通从候选池分配指定数量的节点，优先选择已分配用户较少、剩余流量较多和较早到期的服务器。同一用户在同一套餐内不会重复分配节点。
                </p>
                <p>
                  修改有效期不延长已有订阅；每份分配节点数仅影响新订单和新开通。商城隐藏后，管理员仍可主动分配。
                </p>
              </details>
              {!selected.length && (
                <Notice tone="warning">
                  当前未选择节点。保存后，该模板下的订阅将没有可连接节点，现有连接会停止；流量和有效期保留。
                </Notice>
              )}
              {selected.length < Number(nodeCount) && (
                <Notice tone="warning">
                  候选节点不足每份分配数量，保存后将显示无货。
                </Notice>
              )}
              {template && !preview && (
                <Notice>
                  保存前会预览影响范围。节点池变化会尝试补齐已有分配；分配数量只影响新开通，已用流量与到期时间保留。
                </Notice>
              )}
              {preview && (
                <div className="subscription-impact" ref={previewDetails}>
                  <strong>
                    将影响 {preview.affectedUsers} 位用户的 {preview.affectedSubscriptions} 份订阅
                  </strong>
                  <p>当前 {preview.activeConnections} 个在线连接。</p>
                  <p>
                    {preview.connectionsToClose?.length
                      ? `将停止 ${preview.connectionsToClose.length} 个连接`
                      : "当前没有需要立即停止的连接"}
                  </p>
                  {preview.connectionsToClose?.map((c) => (
                    <p key={c.id}>
                      <Link to={`/users/${c.userId}`}>
                        用户 {c.userId.slice(0, 8)}
                      </Link>{" "}
                      · {nodes.find((n) => n.id === c.nodeId)?.name || c.nodeId}{" "}
                      · {c.reason}
                    </p>
                  ))}
                  {!!preview.allocationShortfalls?.length && (
                  <Notice tone="warning">
                  本次修改后，{preview.allocationShortfalls.length}{" "}
                  份订阅或订单无法补足节点。
                  <ul>
                    {preview.allocationShortfalls.map((item) => (
                      <li key={item.subscriptionId || item.orderId}>
                        {item.subscriptionId || item.orderId}：{item.assigned} /{" "}
                        {item.required} 个节点
                      </li>
                    ))}
                  </ul>
                  </Notice>
                  )}
                </div>
              )}
            </div>
            <section
              className="subscription-node-picker"
              aria-labelledby="subscription-picker-title"
            >
              <div className="subscription-picker-heading">
                <h3 id="subscription-picker-title">
                  <Network size={17} aria-hidden="true" />
                  可用节点
                </h3>
                <span className="subscription-picker-count">
                  {selected.length} 个已选
                </span>
              </div>
              <p className="subscription-picker-hint">
                勾选节点组成候选池，按名称排序。
              </p>
              <div className="subscription-picker-body">
                <div className="subscription-picker-searchbox">
                  <Search size={16} aria-hidden="true" />
                  <input
                    className="subscription-picker-search"
                    aria-label="搜索节点"
                    placeholder="搜索名称、IP 或域名"
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                  />
                </div>
                <div className="subscription-picker-list">
                  {!!invalidSelected.length && (
                    <div className="subscription-invalid-nodes">
                      <h4>不可用节点引用 · {invalidSelected.length} 个</h4>
                      <p>
                        这些节点已不在可选列表中或尚不支持订阅准入。请移除引用后再保存。
                      </p>
                      {invalidSelected.map((id) => {
                        const node = nodes.find((n) => n.id === id);
                        return (
                          <div className="subscription-invalid-node" key={id}>
                            <div>
                              <strong>
                                {node?.name || "节点已删除或不可见"}
                              </strong>
                              <small>{id}</small>
                              <span>
                                {!node
                                  ? "无法从节点列表读取"
                                  : node.lifecycle !== "active"
                                    ? "节点已停止或退役"
                                    : "节点内核需升级"}
                              </span>
                            </div>
                            <Button
                              type="button"
                              variant="secondary"
                              onClick={() => removeSelection(id)}
                            >
                              移除引用
                            </Button>
                          </div>
                        );
                      })}
                    </div>
                  )}
                  <div className="subscription-node-options">
                    {available.map((node) => (
                      <label
                        className={selected.includes(node.id) ? "selected" : ""}
                        key={node.id}
                      >
                        <input
                          type="checkbox"
                          checked={selected.includes(node.id)}
                          onChange={(e) => {
                            if (e.target.checked)
                              setSelected((value) => [...value, node.id]);
                            else removeSelection(node.id);
                          }}
                        />
                        <span>
                          <strong>{node.name}</strong>
                          <small>{node.publicIp}</small>
                          <small>{node.domain}</small>
                        </span>
                        <Badge
                          tone={node.status === "ready" ? "success" : "warning"}
                        >
                          {node.status === "ready" ? "已就绪" : "待就绪"}
                        </Badge>
                      </label>
                    ))}
                  </div>
                  {!available.length && (
                    <p className="subscription-picker-empty">
                      {query
                        ? "没有匹配的节点，试试其他名称、IP 或域名。"
                        : "当前没有可选活动节点，可添加并升级节点后再分配。"}
                    </p>
                  )}
                </div>
                <div className="subscription-picker-actions">
                  <span>共 {eligible.length} 个可选节点</span>
                  <Button
                    type="button"
                    variant="ghost"
                    disabled={!selected.length}
                    onClick={() => {
                      setSelected([]);
                      setPreview(null);
                      setError("");
                    }}
                  >
                    清空所选节点
                  </Button>
                </div>
              </div>
            </section>
          </div>
          <ErrorNotice message={error} />
          <div className="modal-actions">
            <Button
              type="button"
              variant="secondary"
              icon={Eye}
              className="subscription-preview-trigger"
              disabled={busy}
              onClick={() => setShowProductPreview(true)}
            >
              预览订阅
            </Button>
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
      )}
    </Modal>
  );
}

export function SubscriptionTemplates() {
  const [searchParams, setSearchParams] = useSearchParams();
  const editId = searchParams.get("edit") || "";
  const list = usePolling<SubscriptionTemplate[]>("/subscription-templates");
  const nodes = usePolling<Node[]>("/nodes", 15000);
  const [editing, setEditing] = useState<SubscriptionTemplate | "new" | null>(
    null,
  );
  const [filter, setFilter] = useState("all"),
    [search, setSearch] = useState("");
  const toast = useToast();
  useEffect(() => {
    if (!editId) {
      setEditing((current) => (current === "new" ? current : null));
      return;
    }
    const target = list.data?.find((item) => item.id === editId);
    if (target)
      setEditing((current) =>
        current && current !== "new" && current.id === target.id
          ? current
          : target,
      );
  }, [editId, list.data]);
  function closeEditor() {
    setEditing(null);
    const next = new URLSearchParams(searchParams);
    next.delete("edit");
    setSearchParams(next, { replace: true });
  }
  function openEditor(value: SubscriptionTemplate | "new") {
    setEditing(value);
    const next = new URLSearchParams(searchParams);
    if (value === "new") next.delete("edit");
    else next.set("edit", value.id);
    setSearchParams(next);
  }
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
            <Button icon={Plus} onClick={() => openEditor("new")}>
              新建订阅
            </Button>
          </>
        }
      />
      <ErrorNotice
        message={list.error ? `${list.error}；当前保留最近一次数据。` : ""}
        retry={() => void list.refresh()}
      />
      <ErrorNotice
        message={
          editId && list.data && !list.data.some((item) => item.id === editId)
            ? "未找到指定订阅模板，请刷新列表后重试。"
            : ""
        }
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
          旧申请历史 <ArrowRight size={15} />
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
                {t.listed
                  ? t.priceCents == null
                    ? "商城展示 · 未设售价"
                    : "商城展示"
                  : "仅后台分配"}
              </p>
              <TemplateFacts value={t} />
              <Button
                className="full-width"
                variant="secondary"
                icon={Settings2}
                onClick={() => openEditor(t)}
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
            description="设置流量、有效期、节点和售价后，用户即可购买，管理员也可直接赠送。"
            action={
              <Button icon={Plus} onClick={() => openEditor("new")}>
                新建订阅
              </Button>
            }
          />
        </Panel>
      )}
      {editing &&
        (nodes.data ? (
          <TemplateEditor
            key={editing === "new" ? "new" : editing.id}
            template={editing === "new" ? undefined : editing}
            nodes={nodes.data}
            onClose={closeEditor}
            onDone={async () => {
              await list.refresh();
              toast("订阅模板已保存");
            }}
          />
        ) : (
          <Modal title="读取节点" onClose={closeEditor}>
            {nodes.error ? (
              <ErrorNotice
                message={nodes.error}
                retry={() => {
                  void nodes.refresh();
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
