export interface PortalUser {
  id: string;
  email: string;
  enabled: boolean;
  createdAt: string;
  revision: number;
}
export interface SubscriptionTemplate {
  description: string;
  nodesPerSubscription: number;
  allocationShortfalls?: { userId: string; subscriptionId?: string; orderId?: string; required: number; assigned: number }[];
  id: string;
  name: string;
  enabled: boolean;
  listed: boolean;
  validityDays: number;
  priceCents: number | null;
  nodeIds: string[];
  defaultNodeId: string;
  bandwidthMbps: number | null;
  quotaBytes: number | null;
  maxConnections: number;
  revision: number;
}
export interface UserSubscription {
  activationPending?: boolean;
  activatedAt?: string;
  validityDays?: number;
  nodesPerSubscription: number;
  id: string;
  userId: string;
  templateId: string;
  templateName: string;
  createdAt: string;
  expiresAt: string;
  status: string;
  quotaBytes: number | null;
  usedBytes: number;
  reservedBytes: number;
  emergencyUsedBytes?: number;
  emergencyReservedBytes?: number;
  offlineReservations?: number;
  bandwidthMbps: number | null;
  maxConnections: number;
  occupied: number;
  nodeIds: string[];
  defaultNodeId: string;
  revision: number;
}
export interface SubscriptionRequest {
  id: string;
  userId: string;
  email: string;
  templateId: string;
  templateName: string;
  status: "pending" | "approved" | "rejected" | "cancelled" | "closed";
  requestedAt: string;
  decidedAt?: string | null;
  note: string;
  subscriptionId: string;
  revision: number;
}
export interface PortalNode {
  id: string;
  name: string;
  ready?: boolean;
  status?: string;
  domain?: string;
  server?: string;
  publicIp?: string;
  sni?: string;
}
export interface UserSession {
  id: string;
  clientName?: string;
  installationId?: string;
  createdAt?: string;
  lastSeen?: string;
  current?: boolean;
  kind?: string;
  revoked?: boolean;
}
export interface UserSessionResponse {
  authenticated: boolean;
  user?: PortalUser;
  csrfToken?: string;
}
export interface PortalConfig {
  registrationEnabled: boolean;
  portalEnabled: boolean;
  turnstileSiteKey: string;
  turnstileConfigured: boolean;
  recoveryHold?: boolean;
}
export interface Release {
  version: string;
  platform: string;
  url: string;
  sha256?: string;
  name?: string;
  size?: number;
  sourceSHA?: string;
}
export interface TurnstileSettings {
  siteKey: string;
  secretConfigured: boolean;
  allowedHostnames: string[];
  revision: number;
}
export const subscriptionLabels: Record<string, string> = {
  pending: "待开通",
  active: "可使用",
  expired: "已到期",
  exhausted: "流量用尽",
  disabled: "已停用",
  revoked: "已移除",
  removed: "已移除",
};
export const requestLabels: Record<string, string> = {
  pending: "待审核",
  approved: "已开通",
  rejected: "未通过",
  cancelled: "已取消",
  closed: "已关闭",
};

export interface Connection {
  clientIP?: string;
  id: string;
  subscriptionId: string;
  nodeId: string;
  nodeName?: string;
  clientName?: string;
  createdAt?: string;
  lastSeen?: string;
  state?: string;
  ratePending?: boolean;
  offlineReservation?: boolean;
  lastForwardingReport?: string;
  closed?: boolean;
}
export interface UserProfile {
  user: PortalUser;
  subscriptions: UserSubscription[];
  sessions: UserSession[];
  connections: Connection[];
}
export interface UserRow extends PortalUser {
  subscriptions: UserSubscription[];
}
export interface SubscriptionImpact {
  allocationShortfalls: NonNullable<SubscriptionTemplate["allocationShortfalls"]>;
  affectedUsers: number;
  affectedSubscriptions: number;
  activeConnections: number;
  connectionsToClose: {
    id: string;
    userId: string;
    subscriptionId?: string;
    nodeId: string;
    reason: string;
  }[];
}
export function connectionState(value: Connection) {
  return value.state === "closed" || value.closed
    ? "已断开"
    : value.state === "expired"
      ? "已过期"
      : value.state === "closing"
        ? "断开中"
        : value.state === "reserved"
          ? "离线许可预留（非实时在线）"
        : value.ratePending
          ? "等待带宽确认"
          : "已连接";
}
export function connectionActive(value: Connection) {
  return (
    !value.closed &&
    !["closed", "expired", "closing"].includes(value.state || "")
  );
}
