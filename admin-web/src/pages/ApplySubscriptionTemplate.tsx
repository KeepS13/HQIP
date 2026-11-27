import { useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "../api";
import { Button, ErrorNotice, Notice } from "../components";
import type { SubscriptionTemplate } from "../portal/types";

interface ApplyResult {
  affectedSubscriptions: number;
  updatedSubscriptions: number;
  allocationShortfalls: NonNullable<SubscriptionTemplate["allocationShortfalls"]>;
}

// This action always names a saved revision. A stale card cannot apply an edit
// from another administrator; failures keep the message and permit a retry.
export function ApplySubscriptionTemplate({ template, onApplied }: {
  template: SubscriptionTemplate;
  onApplied: () => Promise<unknown>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<ApplyResult | null>(null);
  async function apply() {
    if (busy) return;
    setBusy(true);
    setError("");
    setResult(null);
    try {
      setResult(await api<ApplyResult>(`/subscription-templates/${template.id}/apply`, "POST", { expectedRevision: template.revision }));
      await onApplied();
    } catch (e) {
      setError(`${(e as Error).message}；请刷新套餐后重试，重复应用不会重置用量或有效期。`);
    } finally { setBusy(false); }
  }
  return <div className="form-stack" aria-busy={busy}>
    <Button className="full-width" variant="secondary" icon={RefreshCw} busy={busy} onClick={() => void apply()}>立刻应用</Button>
    <small>将已保存的配置及每份节点数应用到已有订阅；客户端更新订阅后获取新节点。</small>
    <ErrorNotice message={error} />
    {result && <div role="status"><Notice tone={result.allocationShortfalls.length ? "warning" : "success"}>
      已应用到 {result.affectedSubscriptions} 份订阅，更新了 {result.updatedSubscriptions} 份节点分配。
      {result.allocationShortfalls.length > 0 && <>以下订阅节点不足，目标数量已保存；补充节点后可再次应用：
        <ul>{result.allocationShortfalls.map((item) => <li key={item.subscriptionId}>订阅 #{item.subscriptionId}：{item.assigned} / {item.required} 个节点</li>)}</ul>
      </>}
    </Notice></div>}
  </div>;
}
