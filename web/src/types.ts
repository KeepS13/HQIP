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
export interface Node {
  id: string;
  name: string;
  sshHost: string;
  sshPort: number;
  sshUser: string;
  publicIp: string;
  domain: string;
  port: number;
  bandwidthMbps: number | null;
  monthlyQuotaBytes: number | null;
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
  nodes: Node[];
  devices: Device[];
  traffic: TrafficPoint[];
  alerts: { level: string; message: string; nodeId?: string }[];
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
}
