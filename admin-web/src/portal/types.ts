export interface PortalUser {
  id: string;
  email: string;
  enabled: boolean;
  createdAt: string;
  revision: number;
}
export interface Entitlement {
  planId: string;
  planName: string;
  status: "active" | "expired" | "exhausted" | "unassigned" | "disabled";
  expiresAt: string | null;
  quotaBytes: number | null;
  usedBytes: number;
  reservedBytes: number;
  bandwidthMbps: number | null;
  maxConnections: number;
  occupied: number;
  nodeIds: string[];
  defaultNodeId?: string;
  epoch: number;
  revision: number;
}
export interface Plan {
  id: string;
  name: string;
  enabled: boolean;
  saleEnabled: boolean;
  nodeIds: string[];
  defaultNodeId: string;
  bandwidthMbps: number | null;
  quotaBytes: number | null;
  maxConnections: number;
  revision: number;
  userCount?: number;
}
export interface PortalNode {
  id: string;
  name: string;
  ready?: boolean;
  status?: string;
  domain?: string;
  server?: string;
  default?: boolean;
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
export interface Connection {
  id: string;
  nodeId: string;
  nodeName?: string;
  clientName?: string;
  createdAt?: string;
  lastSeen?: string;
  state?: string;
  ratePending?: boolean;
  closed?: boolean;
}
export interface UserProfile {
  user: PortalUser;
  entitlement: Entitlement;
  subscriptionUrl?: string;
  nodes: PortalNode[];
  sessions: UserSession[];
  connections: Connection[];
}
export type UserProfileResponse = Omit<UserProfile, "entitlement"> & {
  entitlement: Entitlement | null;
};
export const emptyEntitlement: Entitlement = {
  planId: "",
  planName: "",
  status: "unassigned",
  expiresAt: null,
  quotaBytes: null,
  usedBytes: 0,
  reservedBytes: 0,
  bandwidthMbps: null,
  maxConnections: 0,
  occupied: 0,
  nodeIds: [],
  epoch: 0,
  revision: 0,
};
export function normalizeProfile(value: UserProfileResponse): UserProfile {
  return {
    ...value,
    entitlement: value.entitlement || emptyEntitlement,
    nodes: value.nodes || [],
    sessions: value.sessions || [],
    connections: value.connections || [],
  };
}
export function connectionState(value: Connection) {
  return value.state === "closed" || value.closed
    ? "已断开"
    : value.state === "expired"
      ? "已过期"
      : value.state === "closing"
        ? "断开中"
        : value.ratePending
          ? "等待带宽确认"
          : "已连接";
}
export function connectionActive(value: Connection) {
  return (
    !value.closed &&
    value.state !== "closed" &&
    value.state !== "expired" &&
    value.state !== "closing"
  );
}
export interface UserRow extends PortalUser {
  entitlement?: Entitlement;
  connections?: number;
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
