export const API_URL = (import.meta.env.VITE_API_URL || "https://api-hqip.5202345.xyz").replace(/\/$/, "");
export const ADMIN_URL = "https://cms-hqip.5202345.xyz";
export function passwordError(value: string): string {
  return Array.from(value).length < 6 ? "密码至少需要 6 个字符" : new TextEncoder().encode(value).length > 1024 ? "密码不能超过 1024 字节" : "";
}
