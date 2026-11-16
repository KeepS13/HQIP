export type MonitoringStatus = "none" | "unknown" | "partial" | "complete";

export interface RuntimeOverview {
  onlineStatus: MonitoringStatus;
  ratesStatus: MonitoringStatus;
  onlineUsers: number;
  activeSubscriptions: number;
  instances: number;
  unmappedInstances: number;
  expectedNodes: number;
  onlineReportingNodes: number;
  reportingNodes: number;
  uploadRate: number;
  downloadRate: number;
  lastReportedAt: string | null;
}

export interface TrafficDay {
  date: string;
  upload: number;
  download: number;
  total: number;
  hasData: boolean;
}

export interface TrafficContribution {
  id: string;
  name: string;
  deleted: boolean;
  upload: number;
  download: number;
  total: number;
}

export interface TrafficAnalytics {
  from: string;
  to: string;
  minDate: string;
  today: string;
  timeZone: "Asia/Shanghai";
  includesToday: boolean;
  dayCount: number;
  summary: {
    upload: number;
    download: number;
    total: number;
    dailyAverage: number;
    recordedDays: number;
    peakDate?: string;
    hasData: boolean;
  };
  days: TrafficDay[];
  contributorKind: "server" | "node";
  contributors: TrafficContribution[];
  generatedAt: string;
}
