import type { RuntimeOverview } from "./traffic-types";

export interface Session {
  authenticated: boolean;
  username?: string;
  csrfToken?: string;
  mustChangePassword?: boolean;
}
export interface Metrics {
  cpuPercent: number;
  memoryUsed: number;
  memoryTotal: number;
  diskUsed: number;
  diskTotal: number;
  uploadRate: number;
  downloadRate: number;
}
export interface BillingCycle {
  status: "unconfigured" | "active" | "expired";
  expiresAt: string | null;
  cycleId: string;
  cycleStartedAt: string | null;
  nextResetAt: string | null;
  cycleUpload: number;
  cycleDownload: number;
  cycleComplete: boolean;
}
export interface ServerRenewal {
  id: string;
  serverId: string;
  requestId: string;
  kind: "initial" | "advance" | "overdue" | "correction";
  months: number;
  previousExpiresAt: string | null;
  expiresAt: string;
  confirmedAt: string;
  actor: string;
  revision: number;
}
export interface Node {
  id: string;
  serverId: string;
  addressId: string;
  revision: number;
  lifecycle: string;
  capabilities?: string[];
  managed: boolean;
  retirement?: { stopped: boolean };
  dnsMode: string;
  certificateMode: string;
  installationVersion: number;
  certificate?: { version: number; notAfter: string; error: string };
  name: string;
  sshHost: string;
  sshPort: number;
  sshUser: string;
  publicIp: string;
  ipSwitch?: { fromIp: string; fromAddressId: string } | null;
  domain: string;
  port: number;
  bandwidthMbps: number | null;
  monthlyQuotaBytes: number | null;
  billing: BillingCycle;
  status: string;
  message: string;
  hostKey: string;
  sharedHost: boolean;
  createdAt: string;
  lastSeen: string | null;
  metrics: Metrics;
  monthUpload: number;
  monthDownload: number;
  totalUpload: number;
  totalDownload: number;
  onlineDevices: number;
  instances: number;
  desiredRevision: number;
  appliedRevision: number;
  coreHealthy: boolean;
}
export interface Device {
  id: string;
  deletedAt?: string | null;
  name: string;
  enabled: boolean;
  createdAt: string;
  subscriptionUrl?: string;
  monthUpload: number;
  monthDownload: number;
  totalUpload: number;
  totalDownload: number;
  online: boolean;
  instances: number;
  pendingSync: boolean;
}

export interface ManagedServer {
  id: string;
  name: string;
  sshHost: string;
  sshPort: number;
  sshUser: string;
  hostKey: string;
  machineId: string;
  platform: string;
  architecture: string;
  revision: number;
  createdAt: string;
  lastSeen?: string | null;
  metrics: Metrics;
  bandwidthMbps: number | null;
  monthlyQuotaBytes: number | null;
  billing: BillingCycle;
  monthUpload: number;
  monthDownload: number;
  totalUpload: number;
  totalDownload: number;
  nodeCount: number;
  ipCount: number;
  expectedNodes: number;
  reportingNodes: number;
  ratesStatus: "none" | "complete" | "partial" | "unknown";
  metricsFresh: boolean;
  metricsSourceNodeId: string;
  metricsReportedAt?: string | null;
  addresses?: ServerAddress[];
}
export interface ServerAddress {
  id: string;
  serverId: string;
  ip: string;
  nodeIds: string[];
}
export interface CloudflareZone { id: string; name: string; status: string }
export interface CloudflareSettings {
  configured: boolean;
  defaultZoneId: string;
  defaultZoneName: string;
  email: string;
  version: number;
  updatedAt: string;
}
export interface CertificateInfo {
  nodeId: string;
  domain: string;
  issuedVersion: number;
  appliedVersion: number;
  notAfter: string;
  nextAttempt: string;
  error: string;
  appliedError: string;
  expiresInDays: number;
  warning: string;
}
export interface NodePreflight {
  fingerprint: string;
  machineId: string;
  platform: string;
  architecture: string;
  boundIps: string[];
  conflicts: string[];
  takeoverCandidates: { unit: string; configPath: string; owner?: string }[];
  canInstall: boolean;
  canTakeover: boolean;
}
export interface TrafficPoint {
  at: number;
  upload: number;
  download: number;
}
export interface TaskEvent {
  id: number;
  nodeId: string;
  stage: string;
  message: string;
  createdAt: string;
}
export interface Overview {
  runtime?: RuntimeOverview;
  nodes: Node[];
  devices: Device[];
  traffic: TrafficPoint[];
  alerts: { level: string; message: string; nodeId?: string; serverId?: string }[];
  usage: { monthUpload: number; monthDownload: number; totalUpload: number; totalDownload: number };
  serverTime: string;
}
export interface NodeDetail {
  node: Node;
  events: TaskEvent[];
  traffic: TrafficPoint[];
  devices: Device[];
}
export interface DeviceDetail {
  device: Device;
  traffic: TrafficPoint[];
  nodes: Node[];
  clientAccount?: ClientAccount;
  assignment?: Assignment;
  sessions?: ClientSession[];
  removals?: Removal[];
}

export interface ClientAccount {
  username: string;
  initialized: boolean;
}
export interface Assignment {
  nodeIds: string[];
  defaultNodeId: string;
  revision: number;
}
export interface ClientSession {
  id: string;
  clientName: string;
  installationId: string;
  coreVersion: string;
  desiredNodeId: string;
  actualNodeId: string;
  runState: string;
  mode: string;
  tunEnabled: boolean;
  connectionIntent: boolean;
  desiredConfigRevision: number;
  appliedConfigRevision: number;
  processedConfigRevision: number;
  lastSeen: string | null;
  lastError: string;
  revocationPending: boolean;
}
export interface Removal {
  id: string;
  nodeId: string;
  deviceId: string;
  revision: number;
  status: string;
  deadline: string | null;
  createdAt: string;
  immediate: boolean;
}
