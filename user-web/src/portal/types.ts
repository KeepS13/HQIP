export interface PortalUser {
  id: string;
  email: string;
  enabled: boolean;
  createdAt: string;
  revision: number;
}
export interface SubscriptionTemplate {
  id: string;
  name: string;
  enabled: boolean;
  listed: boolean;
  validityDays: number;
  nodeIds: string[];
  defaultNodeId: string;
  bandwidthMbps: number | null;
  quotaBytes: number | null;
  maxConnections: number;
  revision: number;
}
export interface UserSubscription {
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
  status: "pending" | "approved" | "rejected" | "cancelled";
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
  expiresAt?: string;
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
};

export interface UserProfile {
  user: PortalUser;
  subscriptions: UserSubscription[];
  sessions: UserSession[];
}
export interface SubscriptionDetail {
  subscription: UserSubscription;
  nodes: PortalNode[];
  subscriptionUrl: string;
}
