import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

export interface PaymentSettings {
  enabled: boolean;
  apiBaseUrl: string;
  merchantNum: string;
  payType: "aloop" | "alipay";
  payDurationMinutes: number;
  queryEnabled: boolean;
  secretConfigured: boolean;
  revision: number;
  notifyUrl: string;
}

export interface PaymentOrder {
	orderType: "purchase" | "renewal";
	renewalSubscriptionId?: string;
	periodStartsAt?: string;
	periodEndsAt?: string;
  id: string;
  orderNo: string;
  userId: string;
  email?: string;
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
  subscriptionId: string;
  platformOrderNo?: string;
  status:
    | "creating"
    | "pending"
    | "unknown"
    | "failed"
    | "expired"
    | "closed"
    | "paid_pending"
    | "fulfilled";
  creationState: "creating" | "ready" | "unknown" | "failed";
  paymentState: "unpaid" | "paid";
  fulfillmentState: "none" | "pending" | "blocked" | "fulfilled";
  lastError: string;
  revision: number;
  activities?: PaymentActivity[];
}

export interface PaymentActivity {
  id: string;
  at: string;
  kind: string;
  message: string;
  actualAmountCents?: number;
}

export interface PaymentOrderPage {
  items: PaymentOrder[];
  total: number;
  page: number;
  pageSize: number;
}

export const orderStates: Record<
  PaymentOrder["status"],
  { label: string; tone: string }
> = {
  creating: { label: "创建中", tone: "info" },
  pending: { label: "待付款", tone: "info" },
  unknown: { label: "待核实", tone: "warning" },
  failed: { label: "创建失败", tone: "danger" },
  expired: { label: "已超时", tone: "muted" },
  closed: { label: "已关闭", tone: "muted" },
  paid_pending: { label: "已付款 · 待开通", tone: "warning" },
  fulfilled: { label: "订阅已生成", tone: "success" },
};

export function priceText(cents: number | null | undefined) {
  if (cents == null) return "未设置价格";
  return new Intl.NumberFormat("zh-CN", {
    style: "currency",
    currency: "CNY",
  }).format(cents / 100);
}

export function parsePriceInput(value: string): number | null {
  const input = value.trim();
  if (!input) return null;
  if (!/^(0|[1-9]\d*)(?:\.\d{1,2})?$/.test(input))
    throw new Error("价格请填写最多两位小数的人民币金额");
  const [whole, decimal = ""] = input.split(".");
  const cents = Number(whole) * 100 + Number(decimal.padEnd(2, "0"));
  if (!Number.isSafeInteger(cents) || cents < 100 || cents > 100000000)
    throw new Error("价格须为 ¥1.00 至 ¥1,000,000.00；留空表示暂不销售");
  return cents;
}

// Read-only polling keeps the previous response on failure and never lets a
// slow response for an earlier route/filter replace the current selection.
export function usePaymentData<T>(path: string, interval = 5000) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<string | null>(null);
  const generation = useRef(0);
  const sequence = useRef(0);
  const pending = useRef(false);
  const refresh = useCallback(
    async (force = false) => {
      if (pending.current && !force) return;
      pending.current = true;
      const current = generation.current;
      const request = ++sequence.current;
      try {
        const value = await api<T>(path);
        if (current === generation.current && request === sequence.current) {
          setData(value);
          setError("");
          setUpdatedAt(new Date().toISOString());
        }
      } catch (e) {
        if (current === generation.current && request === sequence.current)
          setError((e as Error).message);
      } finally {
        if (current === generation.current && request === sequence.current) {
          pending.current = false;
          setLoading(false);
        }
      }
    },
    [path],
  );
  useEffect(() => {
    generation.current++;
    pending.current = false;
    setData(null);
    setError("");
    setUpdatedAt(null);
    setLoading(true);
    void refresh();
    const timer =
      interval > 0
        ? setInterval(() => {
            if (!document.hidden) void refresh();
          }, interval)
        : undefined;
    const visible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      generation.current++;
      if (timer !== undefined) clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh, interval]);
  return { data, error, loading, updatedAt, refresh };
}
