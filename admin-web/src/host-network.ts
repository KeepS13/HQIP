// Host totals are shared by serverId. They must never be summed once per node,
// multiplied by subscription pricing, or subtracted from application payloads.
export interface HostNetworkUsage {
  serverId: string;
  receive: number;
  send: number;
  firstObserved: string;
  fresh: boolean;
  discontinuity: boolean;
  receiveRate: number | null;
  sendRate: number | null;
  sample: {
    bootId: string;
    bootAt: string;
    at: string;
    interfaces: { name: string; index: number; receive: number; send: number }[];
    excluded?: string[];
  };
}

export function uniqueHostNetworks(nodes: { serverId: string; hostNetwork?: HostNetworkUsage }[]) {
  const hosts = new Map<string, HostNetworkUsage | undefined>();
  for (const node of nodes) {
    if (!node.serverId) continue;
    const previous = hosts.get(node.serverId);
    if (!previous || node.hostNetwork && Date.parse(node.hostNetwork.sample.at) > Date.parse(previous.sample.at)) hosts.set(node.serverId, node.hostNetwork);
  }
  return [...hosts.values()];
}

/** A cached successful response cannot authorize live rates indefinitely. */
export function hostNetworkFresh(value: HostNetworkUsage | undefined, now = Date.now()) {
  if (!value?.fresh) return false;
  const age = now - Date.parse(value.sample.at);
  return Number.isFinite(age) && age >= 0 && age <= 30_000;
}
