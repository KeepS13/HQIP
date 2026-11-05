import { Badge } from "../components";
import { subscriptionLabels, requestLabels } from "./types";
export function SubscriptionBadge({ status }: { status: string }) {
  return (
    <Badge
      tone={
        status === "active"
          ? "success"
          : ["removed", "revoked"].includes(status)
            ? "muted"
            : "warning"
      }
    >
      {subscriptionLabels[status] || "待同步"}
    </Badge>
  );
}
export function RequestBadge({ status }: { status: string }) {
  return (
    <Badge
      tone={
        status === "approved"
          ? "success"
          : status === "pending"
            ? "info"
            : "muted"
      }
    >
      {requestLabels[status] || status}
    </Badge>
  );
}
