// These fields always represent Beijing time, independent of the browser zone.
export function subscriptionExpiryFields(value: string) {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return { day: "", time: "" };
  const local = new Date(timestamp + 8 * 60 * 60 * 1000).toISOString();
  return { day: local.slice(0, 10), time: local.slice(11, 16) };
}

export function parseSubscriptionExpiry(day: string, time: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || !/^\d{2}:\d{2}$/.test(time)) return null;
  const input = `${day}T${time}:00+08:00`;
  const value = Date.parse(input);
  if (!Number.isFinite(value)) return null;
  const roundTrip = subscriptionExpiryFields(input);
  if (roundTrip.day !== day || roundTrip.time !== time) return null;
  return new Date(value).toISOString();
}

export function canAdjustSubscriptionExpiry(status: string, expiresAt: string, now = Date.now()) {
  return !["revoked", "removed", "expired"].includes(status) && Date.parse(expiresAt) > now;
}
