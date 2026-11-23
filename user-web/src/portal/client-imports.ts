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
  { id: "clash", name: "Clash / Mihomo", platforms: "Windows · Android · macOS", format: "clash", help: "添加并更新订阅，选中完整配置后使用「规则」模式。已包含大陆域名、IP 和 DNS 直连；全局模式或本地覆写会改变分流。节点需要支持 Hysteria 2 和 VLESS + Reality 的 Meta/Mihomo 内核。" },
  { id: "verge", name: "Clash Verge Rev", platforms: "Windows · macOS", format: "clash", help: "一键导入或在「订阅」中粘贴链接，更新并启用此配置，选择「规则」模式。大陆平台默认直连；检查本地覆写是否替换了订阅的规则或 DNS。" },
  { id: "cmfa", name: "Clash Meta Android", platforms: "Android", format: "clash", help: "一键导入或新建 URL 配置，更新并选中后以「规则」模式启动。配置包含大陆平台直连；仅导入节点、开启全局或覆写规则时不会应用完整分流。" },
  { id: "v2rayn", name: "v2rayN", platforms: "Windows", format: "v2ray", help: "添加并更新订阅后选择节点，并在客户端路由设置中启用「绕过局域网和大陆」，不要只选「绕过局域网」。此订阅只包含节点，不能修改客户端路由；请使用支持对应协议的内核。" },
  { id: "v2rayng", name: "v2rayNG", platforms: "Android", format: "v2ray", help: "添加并更新订阅后，启用客户端的「绕过局域网和大陆」路由并更新 Geo 资源，再选择节点启动。节点订阅不携带路由；网络不能使用 UDP 时可选 VLESS + Reality。" },
  { id: "shadowrocket", name: "Shadowrocket", platforms: "iOS / iPadOS · Apple Silicon macOS", format: "v2ray", help: "添加 Subscribe 订阅并更新节点。此链接只提供节点；需另选包含大陆域名和 GEOIP CN 直连规则的配置，并使用「配置」路由，不能仅靠节点订阅实现大陆直连。" },
  { id: "singbox", name: "Sing Box", platforms: "Android · iOS / iPadOS · macOS", format: "singbox", help: "配置按 sing-box 1.14.4 验证。添加远程配置并更新后启动，已内置大陆域名、IPv4/IPv6 和 DNS 直连。进程名分流仅在支持进程识别的平台生效；节点暂未就绪时请稍后重试。" },
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
