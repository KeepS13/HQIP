import { useState } from "react";
import { RefreshCw, TriangleAlert } from "lucide-react";
import { api } from "../api";
import { Button, Confirm, useToast } from "../components";
import type { SubscriptionTemplate } from "../portal/types";
import "./subscription-apply.css";

interface ApplyResult {
  affectedSubscriptions: number;
  updatedSubscriptions: number;
  allocationShortfalls: NonNullable<SubscriptionTemplate["allocationShortfalls"]>;
}

// The server owns application state; refreshing or changing administrator must
// not clear an unapplied edit. Pin the confirmed version across polling updates.
export function ApplySubscriptionTemplate({ template, onApplied }: {
  template: SubscriptionTemplate;
  onApplied: () => Promise<unknown>;
}) {
  const [confirmation, setConfirmation] = useState<SubscriptionTemplate | null>(null);
  const toast = useToast();
  const pending = template.revision > 1 && (template.appliedRevision ?? 0) < template.revision;
  async function apply() {
    if (!confirmation) return;
    let result: ApplyResult;
    try {
      result = await api<ApplyResult>(`/subscription-templates/${confirmation.id}/apply`, "POST", { expectedRevision: confirmation.revision });
    } catch (e) {
      throw new Error(`${(e as Error).message}；请关闭弹窗并刷新套餐后重试，重复应用不会重置用量或有效期。`);
    }
    const shortfall = result.allocationShortfalls.length;
    toast(`已应用到 ${result.affectedSubscriptions} 份订阅，更新了 ${result.updatedSubscriptions} 份节点分配。${shortfall ? `其中 ${shortfall} 份节点不足，目标已保存，请补充节点后再次应用。` : ""}`);
    await onApplied();
  }
  return <>
    <Button className={`full-width${pending ? " button-apply-pending" : ""}`} variant="secondary"
      icon={pending ? TriangleAlert : RefreshCw} onClick={() => setConfirmation(template)}>
      {pending ? "立刻应用 · 待应用" : "立刻应用"}
    </Button>
    {confirmation && <Confirm title="确认立刻应用" confirm="确认应用"
      description={`将「${confirmation.name}」已保存的配置应用到所有尚未到期或移除的已有订阅，包含待开通订阅。${confirmation.revision > 1 && (confirmation.appliedRevision ?? 0) < confirmation.revision ? "当前版本尚无立刻应用记录。" : "当前版本可重复应用，以补齐可分配的节点。"}每份分配节点数会按当前配置更新；同时在线、限速和流量限制在保存时已生效。新节点需客户端更新订阅后获取。不会重置用量、已购有效期或订阅链接；节点不足时会提示。`}
      onConfirm={apply} onClose={() => setConfirmation(null)} />}
  </>;
}
