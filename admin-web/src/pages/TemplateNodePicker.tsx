import { useState } from "react";
import { Network, Search } from "lucide-react";
import { Badge, Button } from "../components";
import type { Node } from "../types";
import { compareResourceNames } from "../resource-order";

// Controlled candidate selection; filtering is local and never changes saved allocations.
export function TemplateNodePicker({
  nodes,
  eligible,
  invalidSelected,
  selected,
  onChange,
}: {
  nodes: Node[];
  eligible: Node[];
  invalidSelected: string[];
  selected: string[];
  onChange: (ids: string[]) => void;
}) {
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLowerCase();
  const available = eligible
    .filter((node) => `${node.name} ${node.publicIp} ${node.domain}`.toLowerCase().includes(query))
    .sort(compareResourceNames);
  return (
    <section className="subscription-node-picker" aria-labelledby="subscription-picker-title">
      <div className="subscription-picker-heading">
        <h3 id="subscription-picker-title">
          <Network size={17} aria-hidden="true" />
          可用节点（可留空）
        </h3>
        <span className="subscription-picker-count">{selected.length} 个已选</span>
      </div>
      <p className="subscription-picker-hint">
        勾选节点组成候选池；全部留空则需付款后手动分配节点并开通，有效期从正式开通时起算。
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
              <p>这些节点已不在可选列表中或尚不支持订阅准入。请移除引用后再保存。</p>
              {invalidSelected.map((id) => {
                const node = nodes.find((n) => n.id === id);
                return (
                  <div className="subscription-invalid-node" key={id}>
                    <div>
                      <strong>{node?.name || "节点已删除或不可见"}</strong>
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
                      onClick={() => onChange(selected.filter((value) => value !== id))}
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
              <label className={selected.includes(node.id) ? "selected" : ""} key={node.id}>
                <input
                  type="checkbox"
                  checked={selected.includes(node.id)}
                  onChange={(e) => {
                    if (e.target.checked) onChange([...selected, node.id]);
                    else onChange(selected.filter((value) => value !== node.id));
                  }}
                />
                <span>
                  <strong>{node.name}</strong>
                  <small>{node.publicIp}</small>
                  <small>{node.domain}</small>
                </span>
                <Badge tone={node.status === "ready" ? "success" : "warning"}>
                  {node.status === "ready" ? "已就绪" : "待就绪"}
                </Badge>
              </label>
            ))}
          </div>
          {!available.length && (
            <p className="subscription-picker-empty">
              {query ? "没有匹配的节点，试试其他名称、IP 或域名。" : "当前没有可选活动节点，可添加并升级节点后再分配。"}
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
              onChange([]);
            }}
          >
            清空所选节点
          </Button>
        </div>
      </div>
    </section>
  );
}
