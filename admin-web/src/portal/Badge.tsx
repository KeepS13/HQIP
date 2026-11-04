import { Badge } from "../components";
import type { Entitlement } from "./types";
const entitlementLabels: Record<string, string> = {
  active: "使用中",
  expired: "已到期",
  exhausted: "流量已用尽",
  unassigned: "待开通",
  disabled: "已停用",
};
export function EntitlementBadge({ value }: { value?: Entitlement }) {
  return (
    <Badge
      tone={
        value?.status === "active"
          ? "success"
          : value?.status === "unassigned"
            ? "muted"
            : "warning"
      }
    >
      {entitlementLabels[value?.status || "unassigned"] || "待同步"}
    </Badge>
  );
}
