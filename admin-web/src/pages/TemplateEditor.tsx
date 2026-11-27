// Own the unsaved template draft and preview/save sequence. The supplied saved
// revision guards concurrent edits; errors retain the draft and never apply it
// to holders. onDone refreshes the list only after a confirmed save.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ArrowRight, ChevronDown, Gauge, Store, Eye, Package, Save } from "lucide-react";
import { api } from "../api";
import { Button, ErrorNotice, Field, Modal, Notice } from "../components";
import type { Node } from "../types";
import type { SubscriptionImpact, SubscriptionTemplate } from "../portal/types";
import { parsePriceInput } from "../payments";
import { SubscriptionCardPreview } from "./SubscriptionCardPreview";
import { TemplateNodePicker } from "./TemplateNodePicker";

export function TemplateEditor({
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
    String(template?.nodesPerSubscription ?? (template ? Math.max(1, template.nodeIds.length) : 1)),
  );
  const [savedShortfalls, setSavedShortfalls] = useState<SubscriptionTemplate["allocationShortfalls"] | null>(null);
  const [enabled, setEnabled] = useState(template?.enabled ?? true);
  const [listed, setListed] = useState(template?.listed ?? false);
  const [days, setDays] = useState(template ? String(template.validityDays) : "30");
  const [price, setPrice] = useState(template?.priceCents == null ? "" : (template.priceCents / 100).toFixed(2));
  const [bandwidth, setBandwidth] = useState(template?.bandwidthMbps?.toString() || "");
  const [quota, setQuota] = useState(template?.quotaBytes == null ? "" : String(template.quotaBytes / 1e9));
  const [maxConnections, setMaxConnections] = useState(String(template?.maxConnections || 1));
  const [selected, setSelected] = useState(template?.nodeIds || []);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [preview, setPreview] = useState<SubscriptionImpact | null>(null);
  const previewDetails = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (preview) previewDetails.current?.scrollIntoView({ block: "nearest" });
  }, [preview]);
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
  const eligible = nodes.filter((n) => n.lifecycle === "active" && n.capabilities?.includes("admission-v1"));
  const invalidSelected = selected.filter((id) => !eligible.some((node) => node.id === id));
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
      if (Array.from(description).length > 2000) throw new Error("订阅介绍最多2000字");
      if (invalidSelected.length) throw new Error("请先移除不可用节点的引用，再保存订阅模板");
      if (template && !preview) {
        setPreview(await api<SubscriptionImpact>(`/subscription-templates/${template.id}/preview`, "POST", payload));
        return;
      }
      const saved = await api<SubscriptionTemplate>(
        template ? `/subscription-templates/${template.id}` : "/subscription-templates",
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
      title={showProductPreview ? "订阅预览" : template ? "编辑订阅模板" : "新建订阅模板"}
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
            <Button type="button" variant="secondary" onClick={() => setShowProductPreview(false)}>
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
                {item.subscriptionId ? "订阅" : "订单"} {item.subscriptionId || item.orderId}：{item.assigned} /{" "}
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
        <form className="form-stack subscription-editor" onSubmit={save} onChange={() => setPreview(null)}>
          <div className="subscription-editor-layout">
            <div className="subscription-editor-settings">
              <section className="subscription-editor-section" aria-label="基本信息">
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
                  <Field label="有效期（天）" hint="开通时起算，不延长已有订阅。">
                    <input
                      required
                      type="number"
                      min={1}
                      step={1}
                      value={days}
                      onChange={(e) => setDays(e.target.value)}
                    />
                  </Field>
                  <Field label="售价（人民币元）" hint="¥1 起；留空仅支持管理员赠送。">
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
              <section className="subscription-editor-section" aria-label="额度与连接">
                <h3>
                  <Gauge size={16} aria-hidden="true" />
                  额度与连接
                </h3>
                <div className="form-grid">
                  <Field label="一次性总流量（GB）" hint="上传＋下载，不重置；留空不限量。">
                    <input
                      type="number"
                      min={0.000001}
                      step="any"
                      value={quota}
                      onChange={(e) => setQuota(e.target.value)}
                      placeholder="不限量"
                    />
                  </Field>
                  <Field label="每份共享带宽（Mbps）" hint="同一份订阅共享；留空不限速。">
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
                  <Field label="每份分配节点数" hint="大于已选数量时分配全部已选节点；保存后影响新购，点击「立刻应用」更新已有订阅。">
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
              <section className="subscription-editor-section" aria-label="商城与服务">
                <h3>
                  <Store size={16} aria-hidden="true" />
                  商城与服务
                </h3>
                <div className="subscription-setting-toggles">
                  <label className="account-checkbox subscription-setting-toggle">
                    <input type="checkbox" checked={listed} onChange={(e) => setListed(e.target.checked)} />
                    <span>
                      <strong>展示在订阅商城</strong>
                      <small>设置价格并启用支付后可购买。</small>
                    </span>
                  </label>
                  <label className="account-checkbox subscription-setting-toggle">
                    <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
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
                    <label htmlFor="subscription-description-input">订阅介绍</label>
                    <textarea
                      id="subscription-description-input"
                      rows={4}
                      value={description}
                      onChange={(e) => setDescription(e.target.value)}
                      aria-describedby="subscription-description-help"
                      aria-invalid={Array.from(description).length > 2000}
                      placeholder={"例如：适合日常浏览与影音\n支持多设备使用"}
                    />
                    <p id="subscription-description-help">每行一条特点，商城将按列表展示。可在下方预览订阅。</p>
                  </div>
                </details>
              </section>
              <details className="subscription-editor-help">
                <summary>
                  分配与生效说明 <ChevronDown size={14} aria-hidden="true" />
                </summary>
                <p>
                  已选节点时，从候选池自动分配；留空时作为定制订阅出售，付款后提供订阅链接，等待你分配节点并正式开通才开始计时。同一用户在同一套餐内不会重复分配节点。
                </p>
                <p>
                  修改有效期不延长已有订阅；保存后点击「立刻应用」可将每份分配节点数同步到已有订阅。商城隐藏后，管理员仍可主动分配。
                </p>
              </details>
              {!selected.length && (
                <Notice tone="warning">
                  当前未选择节点。保存后，该模板下的订阅将没有可连接节点，现有连接会停止；流量和有效期保留。
                </Notice>
              )}
              {selected.length > 0 && selected.length < Number(nodeCount) && (
                <Notice>配置数量大于已选节点数，每份将分配全部 {selected.length} 个已选节点。</Notice>
              )}
              {template && !preview && (
                <Notice>
                  保存前会预览影响范围。节点池变化会尝试补齐自动分配的订阅；定制订阅仍由管理员开通。保存后可在列表点击「立刻应用」同步已有订阅的节点数量，已有用量和期限保留。
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
                      <Link to={`/users/${c.userId}`}>用户 {c.userId.slice(0, 8)}</Link> ·{" "}
                      {nodes.find((n) => n.id === c.nodeId)?.name || c.nodeId} · {c.reason}
                    </p>
                  ))}
                  {!!preview.allocationShortfalls?.length && (
                    <Notice tone="warning">
                      本次修改后，{preview.allocationShortfalls.length} 份订阅或订单无法补足节点。
                      <ul>
                        {preview.allocationShortfalls.map((item) => (
                          <li key={item.subscriptionId || item.orderId}>
                            {item.subscriptionId || item.orderId}：{item.assigned} / {item.required} 个节点
                          </li>
                        ))}
                      </ul>
                    </Notice>
                  )}
                </div>
              )}
            </div>
            <TemplateNodePicker
              nodes={nodes}
              eligible={eligible}
              invalidSelected={invalidSelected}
              selected={selected}
              onChange={(ids) => {
                setSelected(ids);
                setPreview(null);
                setError("");
              }}
            />
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
            <Button type="button" variant="secondary" disabled={busy} onClick={onClose}>
              取消
            </Button>
            <Button type="submit" busy={busy} icon={template && !preview ? ArrowRight : Save}>
              {template && !preview ? "预览影响" : template ? "确认保存" : "创建订阅模板"}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
