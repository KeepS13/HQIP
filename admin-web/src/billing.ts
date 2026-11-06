import type { BillingCycle } from "./types";

export const BILLING_HOUR = 60 * 60 * 1000;
export const BILLING_MONTH = 30 * 24 * BILLING_HOUR;
const BEIJING_OFFSET = 8 * BILLING_HOUR;

export type BillingResource = {
  billing?: BillingCycle;
  monthUpload: number;
  monthDownload: number;
};

export function billingFor(resource: BillingResource): BillingCycle {
  if (resource.billing && resource.billing.status !== "unconfigured")
    return resource.billing;
  return {
    status: "unconfigured",
    expiresAt: null,
    cycleId: "",
    cycleStartedAt: null,
    nextResetAt: null,
    cycleUpload: resource.monthUpload,
    cycleDownload: resource.monthDownload,
    cycleComplete: true,
  };
}

export function billingTotal(resource: BillingResource): number {
  const billing = billingFor(resource);
  return billing.cycleUpload + billing.cycleDownload;
}

export function billingUsageLabel(resource: BillingResource): string {
  return billingFor(resource).status === "unconfigured"
    ? "本月代理流量（待设置到期）"
    : "本周期代理流量";
}

export function billingQuotaLabel(resource: BillingResource): string {
  return billingFor(resource).status === "unconfigured"
    ? "月流量额度"
    : "每周期流量额度";
}

export function billingExpiry(billing: BillingCycle, now = Date.now()) {
  if (billing.status === "unconfigured" || !billing.expiresAt) {
    return {
      state: "unconfigured",
      label: "未设置到期时间",
      tone: "muted",
      abnormal: false,
    } as const;
  }
  const hours = Math.ceil((Date.parse(billing.expiresAt) - now) / BILLING_HOUR);
  if (billing.status === "expired" || hours <= 0) {
    return {
      state: "expired",
      label: "已到期 · 待确认续期",
      tone: "danger",
      abnormal: true,
    } as const;
  }
  if (hours <= 48) {
    return {
      state: "expiring",
      label: `${hours} 小时内到期`,
      tone: "warning",
      abnormal: true,
    } as const;
  }
  return {
    state: "active",
    label: `剩余约 ${Math.ceil(hours / 24)} 天`,
    tone: "success",
    abnormal: false,
  } as const;
}

export function beijingHourParts(value: string | number): {
  day: string;
  hour: string;
} {
  const timestamp = typeof value === "number" ? value : Date.parse(value);
  if (!Number.isFinite(timestamp)) return { day: "", hour: "" };
  const shifted = new Date(timestamp + BEIJING_OFFSET).toISOString();
  return { day: shifted.slice(0, 10), hour: shifted.slice(11, 13) };
}

export function defaultServerExpiry(now = Date.now()): {
  day: string;
  hour: string;
} {
  return beijingHourParts(
    Math.ceil(now / BILLING_HOUR) * BILLING_HOUR + BILLING_MONTH,
  );
}

export function parseBeijingExpiry(
  day: string,
  hour: string,
  now = Date.now(),
): string {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || !/^(0\d|1\d|2[0-3])$/.test(hour)) {
    throw new Error("请选择到期日期和小时（北京时间）");
  }
  const timestamp = Date.parse(`${day}T${hour}:00:00+08:00`);
  const checked = beijingHourParts(timestamp);
  if (
    !Number.isFinite(timestamp) ||
    checked.day !== day ||
    checked.hour !== hour
  ) {
    throw new Error("到期时间无效，请重新选择");
  }
  if (timestamp <= now) throw new Error("到期时间必须晚于当前时间");
  return new Date(timestamp).toISOString();
}

export function billingDate(value: string | null | undefined): string {
  if (!value) return "—";
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return "—";
  const shifted = new Date(parsed + BEIJING_OFFSET).toISOString();
  return `${shifted.slice(0, 10).replaceAll("-", "/")} ${shifted.slice(11, 16)}`;
}
