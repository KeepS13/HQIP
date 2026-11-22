import { api, ApiError } from "./api";
import type { SubscriptionTemplate, UserSubscription } from "./portal/types";

export interface PendingGrant {
  userId: string;
  template: SubscriptionTemplate;
  body: { templateId: string; templateRevision: number; expectedRevision: number; requestId: string };
}

const storageKey = (userId: string) => `hqip:subscription-grant:v1:${userId}`;

// The tab owns an unfinished grant until a receipt or explicit business
// rejection arrives. Persist before sending, preserving both reviewed terms
// and revisions across modal closes, reloads and a fresh administrator login.
export function readPendingGrant(userId: string): PendingGrant | null {
  try {
    const raw = sessionStorage.getItem(storageKey(userId));
    if (!raw) return null;
    const value = JSON.parse(raw) as PendingGrant;
    if (value.userId !== userId || !value.body || !value.template ||
      typeof value.body.requestId !== "string" || value.body.requestId.length < 8 ||
      value.body.requestId.length > 128 || /[\r\n\0]/.test(value.body.requestId) ||
      !Number.isSafeInteger(value.body.expectedRevision) || value.body.expectedRevision < 0 ||
      !Number.isSafeInteger(value.body.templateRevision) ||
      value.template.id !== value.body.templateId || !value.body.templateId ||
      value.template.revision !== value.body.templateRevision ||
      typeof value.template.name !== "string" || !Array.isArray(value.template.nodeIds)) throw new Error();
    return value;
  } catch {
    // Silently discarding a corrupt/inaccessible pending key could issue a
    // second gift. Keep the form blocked until the original result is checked.
    throw new Error("无法读取本次开通记录，请先核对用户已有订阅，勿重复赠送。");
  }
}

export function savePendingGrant(value: PendingGrant) {
  try {
    sessionStorage.setItem(storageKey(value.userId), JSON.stringify(value));
  } catch {
    throw new Error("浏览器无法保存开通记录，请允许此站点使用会话存储后重试；尚未提交开通。");
  }
}

export function clearPendingGrant(userId: string) {
  sessionStorage.removeItem(storageKey(userId));
}

export function grantWasRejected(error: unknown) {
  return error instanceof ApiError && error.status >= 400 && error.status < 500 &&
    error.code === "SUBSCRIPTION_ASSIGNMENT_REJECTED";
}

function canRetry(error: unknown) {
  return error instanceof TypeError || (error instanceof DOMException && error.name === "AbortError") ||
    (error instanceof ApiError && [500, 502, 503, 504].includes(error.status));
}

// Only this idempotent operation gets bounded retries. A timeout cancels the
// wait, not the server transaction; every attempt must reuse the exact body.
// A missing/mismatched receipt is uncertain, never proof that nothing happened.
export async function sendSubscriptionGrant(value: PendingGrant): Promise<UserSubscription> {
  for (let attempt = 0; ; attempt++) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const result = await api<UserSubscription>(`/users/${encodeURIComponent(value.userId)}/subscriptions`, "POST", value.body, controller.signal);
      if (!result?.id || result.userId !== value.userId || result.templateId !== value.body.templateId) {
        throw new ApiError("未收到有效的开通结果，请重试本次开通", 502);
      }
      return result;
    } catch (error) {
      if (attempt >= 2 || !canRetry(error)) throw error;
    } finally {
      clearTimeout(timeout);
    }
    // The assignment endpoint advertises Retry-After: 1; never retry sooner.
    await new Promise(resolve => setTimeout(resolve, attempt === 0 ? 1000 : 2000));
  }
}
