import type { UserSubscription } from "./types";

export type ImportFormat = "clash" | "v2ray" | "singbox";
export type ImportClientID = "clash" | "verge" | "cmfa" | "v2rayn" | "v2rayng" | "shadowrocket" | "singbox";
export interface ImportClient {
  id: ImportClientID;
  name: string;
  platforms: string;
  format: ImportFormat;
  help: string;
}

export const importClients: readonly ImportClient[] = [
  { id: "clash", name: "Clash / Mihomo", platforms: "Windows · Android · macOS", format: "clash", help: "在客户端的配置或订阅中添加链接并更新。请使用支持 Hysteria 2 和 VLESS + Reality 的 Mihomo 内核。" },
  { id: "verge", name: "Clash Verge Rev", platforms: "Windows · macOS", format: "clash", help: "可一键导入；也可在「订阅」中粘贴链接，导入后选择配置并启用代理。" },
  { id: "cmfa", name: "Clash Meta Android", platforms: "Android", format: "clash", help: "可一键导入；也可在「配置」中新建 URL 配置，更新后选中配置并启动。" },
  { id: "v2rayn", name: "v2rayN", platforms: "Windows", format: "v2ray", help: "复制链接，在「订阅分组」中添加并更新，再选择节点。同一服务器可选择 Hysteria 2 或 VLESS + Reality；请使用支持对应协议的内核。" },
  { id: "v2rayng", name: "v2rayNG", platforms: "Android", format: "v2ray", help: "可一键添加订阅；添加后更新订阅，选择节点并启动。网络无法使用 UDP 时可选 VLESS + Reality。" },
  { id: "shadowrocket", name: "Shadowrocket", platforms: "iOS / iPadOS · Apple Silicon macOS", format: "v2ray", help: "复制链接，在 Shadowrocket 中添加类型为 Subscribe 的订阅，更新后选择节点并开启连接。" },
  { id: "singbox", name: "Sing Box", platforms: "Android · iOS / iPadOS · macOS", format: "singbox", help: "需要 sing-box 1.14.4 或更新版本。使用官方图形客户端添加远程配置，可一键导入或粘贴链接，更新后启动。进程名分流仅在支持进程识别的平台生效；节点暂未就绪时请稍后重试。" },
];

export function subscriptionImportURL(value: string, format: ImportFormat, download = false) {
  if (!value) return "";
  try {
    const url = new URL(value);
    if (url.protocol !== "https:" && url.protocol !== "http:") return "";
    if (format === "clash") url.searchParams.delete("format");
    else url.searchParams.set("format", format);
    url.searchParams.delete("download");
    if (download) url.searchParams.set("download", "1");
    return url.toString();
  } catch {
    return "";
  }
}

export function clientImportScheme(client: ImportClientID, url: string, name: string) {
  if (!url) return "";
  const encodedURL = encodeURIComponent(url), encodedName = encodeURIComponent(name);
  switch (client) {
    case "clash":
    case "verge":
      return `clash://install-config?url=${encodedURL}&name=${encodedName}`;
    case "cmfa":
      return `clashmeta://install-config?url=${encodedURL}`;
    case "v2rayng":
      return `v2rayng://install-sub?url=${encodedURL}#${encodedName}`;
    case "singbox":
      return `sing-box://import-remote-profile?url=${encodedURL}#${encodedName}`;
    default:
      return "";
  }
}

export function selectedImportSubscription(subscriptions: readonly UserSubscription[], requested: string | null) {
  // Missing parameter means all; invalid explicit IDs never widen access.
  return requested === null ? null : subscriptions.find((item) => item.id === requested) || null;
}
