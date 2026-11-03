import type { Node } from "./types";

export function expectsNodeReport(node: Node) {
  return node.managed && node.lifecycle !== "deleted" && !node.retirement?.stopped;
}

export function hasNodeReport(node: Node) {
  const age = node.lastSeen ? Date.now() - Date.parse(node.lastSeen) : Number.NaN;
  const validRate = (value: number) => Number.isFinite(value) && value >= 0 && value <= 2 ** 60;
  return expectsNodeReport(node) && node.coreHealthy && age >= 0 && age <= 30_000
    && validRate(node.metrics.uploadRate) && validRate(node.metrics.downloadRate)
    && Number.isFinite(node.metrics.cpuPercent);
}
