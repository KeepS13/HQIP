import { useCallback, useEffect, useRef, useState } from "react";
import { userAPI } from "./api";
import type { SubscriptionTemplate } from "./types";

export interface CheckoutDraft {
  requestId: string;
  template: SubscriptionTemplate;
  attempted: boolean;
}

function draftKey(userId: string, templateId: string) {
  return `hqip:checkout:${userId}:${templateId}`;
}

export function checkoutDraft(
  userId: string,
  template: SubscriptionTemplate,
): CheckoutDraft {
  try {
    const cached = JSON.parse(
      sessionStorage.getItem(draftKey(userId, template.id)) || "null",
    ) as CheckoutDraft | null;
    const value = cached?.template;
    if (
      cached &&
      typeof cached.requestId === "string" &&
      /^[a-f0-9-]{36}$/.test(cached.requestId) &&
      cached.attempted === true &&
      value?.id === template.id &&
      typeof value.name === "string" &&
      Number.isSafeInteger(value.revision) &&
      Number.isSafeInteger(value.priceCents) &&
      value.priceCents !== null &&
      value.priceCents >= 100 &&
      Number.isSafeInteger(value.validityDays) &&
      Array.isArray(value.nodeIds) &&
      Number.isSafeInteger(value.maxConnections)
    )
      return cached;
  } catch {
    /* Unavailable or invalid tab storage does not authorize a purchase. */
  }
  return { requestId: crypto.randomUUID(), template, attempted: false };
}

export function saveCheckoutDraft(userId: string, draft: CheckoutDraft) {
  // Persist before any HTTP request. If tab storage is blocked, refuse the
  // request so reloads cannot silently forget an uncertain payment attempt.
  sessionStorage.setItem(
    draftKey(userId, draft.template.id),
    JSON.stringify(draft),
  );
}

export function clearCheckoutDraft(userId: string, templateId: string) {
  sessionStorage.removeItem(draftKey(userId, templateId));
}

export interface PaymentInfo {
  enabled: boolean;
  payType: string;
  payDurationMinutes: number;
}

export interface PaymentOrder {
  id: string;
  orderNo: string;
  templateId: string;
  templateName: string;
  amountCents: number;
  currency: "CNY";
  validityDays: number;
  createdAt: string;
  expiresAt: string;
  paidAt?: string | null;
  fulfilledAt?: string | null;
  lastNotifyAt?: string | null;
  subscriptionId?: string;
  payUrl?: string;
  platformOrderNo?: string;
  status: string;
  creationState: string;
  paymentState: string;
  fulfillmentState: string;
  lastError: string;
  revision: number;
}

export interface PaymentOrderList {
  items: PaymentOrder[];
  total: number;
  page: number;
  pageSize: number;
}

export const orderLabels: Record<string, string> = {
  creating: "正在创建",
  pending: "待付款",
  unknown: "支付创建结果待核实",
  failed: "创建失败",
  expired: "付款入口已过期",
  closed: "付款入口已关闭",
  paid_pending: "已付款，等待开通",
  fulfilled: "已开通",
};

export function money(cents: number) {
  return new Intl.NumberFormat("zh-CN", {
    style: "currency",
    currency: "CNY",
    minimumFractionDigits: 2,
  }).format(cents / 100);
}

export function paymentURL(value?: string): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    if (url.protocol !== "https:" || url.username || url.password) return null;
    return url.href;
  } catch {
    return null;
  }
}

// Order polling must not let an earlier response overwrite a newer payment
// result. Failed refreshes preserve the last successful response and its time.
export function usePaymentData<T>(path: string) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const active = useRef(false),
    sequence = useRef(0);
  const refresh = useCallback(async () => {
    const request = ++sequence.current;
    setRefreshing(true);
    try {
      const result = await userAPI<T>(path);
      if (active.current && request === sequence.current) {
        setData(result);
        setUpdatedAt(Date.now());
        setError("");
      }
    } catch (e) {
      if (active.current && request === sequence.current)
        setError((e as Error).message);
    } finally {
      if (active.current && request === sequence.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [path]);
  useEffect(() => {
    active.current = true;
    setData(null);
    setUpdatedAt(null);
    setError("");
    setLoading(true);
    void refresh();
    const timer = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, 5000);
    const visible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      active.current = false;
      sequence.current++;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh]);
  return { data, error, loading, updatedAt, refreshing, refresh };
}
