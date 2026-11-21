import type { SubscriptionTemplate, UserSubscription } from "./types";

export interface RenewalDraft {
  requestId: string;
  template: SubscriptionTemplate;
  subscriptionId: string;
  subscriptionRevision: number;
  expiresAt: string;
  currentPeriodEndsAt: string;
  attempted: boolean;
}

export function renewalDraftKey(userId: string, subscriptionId: string) {
  return `hqip:renewal:${userId}:${subscriptionId}`;
}

export function renewalDraft(userId: string, template: SubscriptionTemplate, subscription: UserSubscription): RenewalDraft {
  try {
    const cached = JSON.parse(sessionStorage.getItem(renewalDraftKey(userId, subscription.id)) || "null") as RenewalDraft | null;
    if (cached?.attempted === true && /^[a-f0-9-]{36}$/.test(cached.requestId) &&
        cached.subscriptionId === subscription.id && cached.template?.id === template.id &&
        Number.isSafeInteger(cached.subscriptionRevision) && cached.subscriptionRevision > 0 &&
        Number.isSafeInteger(cached.template.revision) && cached.template.revision > 0 &&
        Number.isSafeInteger(cached.template.priceCents) && cached.template.priceCents !== null && cached.template.priceCents >= 100 &&
        Number.isSafeInteger(cached.template.validityDays) && cached.template.validityDays > 0 && cached.template.validityDays <= 36500 &&
        Number.isFinite(Date.parse(cached.expiresAt)) && Number.isFinite(Date.parse(cached.currentPeriodEndsAt))) return cached;
  } catch { /* Invalid or unavailable storage never authorizes a new order. */ }
  return { requestId: crypto.randomUUID(), template, subscriptionId: subscription.id,
    subscriptionRevision: subscription.revision, expiresAt: subscription.expiresAt,
    currentPeriodEndsAt: subscription.currentPeriodEndsAt, attempted: false };
}

export function saveRenewalDraft(userId: string, draft: RenewalDraft) {
  sessionStorage.setItem(renewalDraftKey(userId, draft.subscriptionId), JSON.stringify(draft));
}

export function clearRenewalDraft(userId: string, subscriptionId: string) {
  sessionStorage.removeItem(renewalDraftKey(userId, subscriptionId));
}

export function renewalOrderInput(draft: RenewalDraft) {
  return { requestId: draft.requestId, templateId: draft.template.id, templateRevision: draft.template.revision,
    renewalSubscriptionId: draft.subscriptionId, expectedSubscriptionRevision: draft.subscriptionRevision };
}

export function renewalExpiry(expiresAt: string, days: number) {
  return new Date(Date.parse(expiresAt) + days * 86400000).toISOString();
}
