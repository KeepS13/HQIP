import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ArrowRight, Eye, EyeOff, Package, Plus, Settings2 } from "lucide-react";
import { usePolling } from "../api";
import {
  Badge,
  Button,
  Empty,
  ErrorNotice,
  Loading,
  Modal,
  PageHeader,
  Panel,
  RefreshButton,
  bytes,
  useToast,
} from "../components";
import type { Node } from "../types";
import type { SubscriptionTemplate } from "../portal/types";
import { priceText } from "../payments";
import "./accounts.css";
import "./subscriptions.css";
import { ApplySubscriptionTemplate } from "./ApplySubscriptionTemplate";
import { TemplateEditor } from "./TemplateEditor";

export function TemplateFacts({ value }: { value: SubscriptionTemplate }) {
  const configured = value.nodesPerSubscription ?? value.nodeIds.length;
  const assigned = value.nodeIds.length ? Math.min(configured, value.nodeIds.length) : configured;
  return (
    <dl className="subscription-facts">
      <div>
        <dt>商城售价</dt>
        <dd>{priceText(value.priceCents)}</dd>
      </div>
      <div>
        <dt>一次性总流量</dt>
        <dd>{value.quotaBytes === null ? "不限量" : bytes(value.quotaBytes)}</dd>
      </div>
      <div>
        <dt>有效期</dt>
        <dd>开通后 {value.validityDays} 天</dd>
      </div>
      <div>
        <dt>每份共享带宽</dt>
        <dd>{value.bandwidthMbps === null ? "不限速" : `${value.bandwidthMbps} Mbps`}</dd>
      </div>
      <div>
        <dt>每份同时在线上限</dt>
        <dd>{value.maxConnections} 个</dd>
      </div>
      <div>
        <dt>每份分配节点</dt>
        <dd>
          {assigned} 个{assigned < configured ? `（配置上限 ${configured} 个）` : ""} / 候选池 {value.nodeIds.length} 个
        </dd>
      </div>
    </dl>
  );
}

export function SubscriptionTemplates() {
  const [searchParams, setSearchParams] = useSearchParams();
  const editId = searchParams.get("edit") || "";
  const list = usePolling<SubscriptionTemplate[]>("/subscription-templates");
  const nodes = usePolling<Node[]>("/nodes", 15000);
  const [editing, setEditing] = useState<SubscriptionTemplate | "new" | null>(null);
  const [filter, setFilter] = useState("all"),
    [search, setSearch] = useState("");
  const toast = useToast();
  useEffect(() => {
    if (!editId) {
      setEditing((current) => (current === "new" ? current : null));
      return;
    }
    const target = list.data?.find((item) => item.id === editId);
    if (target) setEditing((current) => (current && current !== "new" && current.id === target.id ? current : target));
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
        <select aria-label="筛选订阅模板" value={filter} onChange={(e) => setFilter(e.target.value)}>
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
                <Badge tone={t.enabled ? "success" : "warning"}>{t.enabled ? "服务启用" : "已停用"}</Badge>
              </div>
              <h2>{t.name}</h2>
              <p className="subscription-visibility">
                {t.listed ? <Eye size={14} /> : <EyeOff size={14} />}
                {t.listed ? (t.priceCents == null ? "商城展示 · 未设售价" : "商城展示") : "仅后台分配"}
              </p>
              <TemplateFacts value={t} />
              <div className="form-stack">
                <Button className="full-width" variant="secondary" icon={Settings2} onClick={() => openEditor(t)}>
                  编辑订阅
                </Button>
                <ApplySubscriptionTemplate template={t} onApplied={list.refresh} />
              </div>
            </article>
          ))}
        </div>
      ) : (
        <Panel title="订阅模板">
          <Empty
            icon={Package}
            title={search || filter !== "all" ? "没有匹配的订阅" : "创建第一份订阅模板"}
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
