// node-user-traffic v1: administrator-only lifetime raw bytes on one node.
// Same-user subscriptions are merged by the server; ordering is global across
// pages. hasData=false is an absent ledger record, not measured zero usage.
export type NodeUserTraffic = {
  userId: string;
  email: string;
  enabled: boolean;
  upload: number;
  download: number;
  total: number;
  hasData: boolean;
};

export type NodeUserTrafficPage = {
  nodeId: string;
  items: NodeUserTraffic[];
  totalUsers: number;
  offset: number;
  limit: number;
  generatedAt: string;
};
