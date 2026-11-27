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
  { id: "clash", name: "Clash Meta / Mihomo", platforms: "Windows · Android · macOS", format: "clash", help: "添加并更新订阅，选中完整配置后使用「规则」模式。已包含大陆域名、IP 和 DNS 直连；全局模式或本地覆写会改变分流。节点需要支持 Hysteria 2 和 VLESS + Reality 的 Meta/Mihomo 内核。" },
  { id: "verge", name: "Clash Verge Rev", platforms: "Windows · macOS", format: "clash", help: "一键导入或在「订阅」中粘贴链接，更新并启用此配置，选择「规则」模式。大陆平台默认直连；检查本地覆写是否替换了订阅的规则或 DNS。" },
  { id: "cmfa", name: "Clash Meta Android", platforms: "Android", format: "clash", help: "一键导入或新建 URL 配置，更新并选中后以「规则」模式启动。配置包含大陆平台直连；仅导入节点、开启全局或覆写规则时不会应用完整分流。" },
  { id: "v2rayn", name: "v2rayN", platforms: "Windows · Linux · macOS", format: "v2ray", help: "添加订阅分组并更新订阅，选择节点后启用系统代理或 TUN。使用包含配套内核的版本，并启用「绕过局域网和大陆」路由。此链接只提供节点；官网直连还需按下方说明添加。" },
  { id: "v2rayng", name: "v2rayNG", platforms: "Android", format: "v2ray", help: "添加并更新订阅，选择节点后启动 VPN。请升级支持 Hysteria 2 的版本；启用「绕过局域网和大陆」并更新 Geo 资源。此链接不携带路由；官网直连还需按下方说明添加。" },
  { id: "shadowrocket", name: "Shadowrocket", platforms: "iOS / iPadOS · Apple Silicon macOS", format: "v2ray", help: "添加 Subscribe 订阅并更新节点。此链接只提供节点；需另选包含大陆域名和 GEOIP CN 直连规则的配置，并使用「配置」路由，不能仅靠节点订阅实现大陆直连。" },
  { id: "singbox", name: "Sing Box", platforms: "Android · iOS / iPadOS · macOS", format: "singbox", help: "使用官方 sing-box 客户端，配置已按 1.14.4 验证。以「远程配置」添加并更新后启动；旧版提示未知字段时先升级。已包含大陆与官网的 DNS 和直连规则，节点暂未就绪时请稍后重试。" },
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
      // Verge 2.5.10 reads everything after `url=` as the subscription URL.
      // Keep it last so the display name cannot become part of a secret token.
      // The dedicated scheme also avoids another installed Clash app claiming
      // a link explicitly intended for Verge.
      return `${client === "verge" ? "clash-verge" : "clash"}://install-config?name=${encodedName}&url=${encodedURL}`;
    case "cmfa":
      return `clashmeta://install-config?name=${encodedName}&url=${encodedURL}`;
    case "v2rayng":
      return `v2rayng://install-sub?url=${encodedURL}#${encodedName}`;
    case "singbox":
      return `sing-box://import-remote-profile?url=${encodedURL}#${encodedName}`;
    default:
      return "";
  }
}

export function selectedImportSubscription(subscriptions: readonly UserSubscription[], requested: string | null) {
  // Only usable subscriptions may be selected. An expired explicit URL never
  // silently falls back to another subscription or the account-wide scope.
  return requested === null ? null : subscriptions.find((item) => item.id === requested && item.status === "active") || null;
}

export function availableImportSubscriptions(subscriptions: readonly UserSubscription[]) {
  return subscriptions.filter((item) => item.status === "active");
}
