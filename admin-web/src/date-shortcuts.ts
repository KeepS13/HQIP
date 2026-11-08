import { BILLING_HOUR, beijingHourParts } from "./billing";

const CALENDAR_DAY = 24 * BILLING_HOUR;

export type DateRange = { from: string; to: string };
export type DateRangePreset = "today" | "yesterday" | "last7" | "last30" | "month";

function shiftDay(day: string, days: number) {
  return new Date(Date.parse(`${day}T00:00:00Z`) + days * CALENDAR_DAY)
    .toISOString().slice(0, 10);
}

// Calendar dates are always Beijing dates, regardless of the browser timezone.
export function dateRangeShortcut(preset: DateRangePreset, now = Date.now()): DateRange {
  const today = beijingHourParts(now).day;
  switch (preset) {
    case "yesterday": {
      const yesterday = shiftDay(today, -1);
      return { from: yesterday, to: yesterday };
    }
    case "last7": return { from: shiftDay(today, -6), to: today };
    case "last30": return { from: shiftDay(today, -29), to: today };
    case "month": return { from: `${today.slice(0, 7)}-01`, to: today };
    default: return { from: today, to: today };
  }
}

export function expiryDateShortcut(
  days: 0 | 1 | 7 | 30,
  hour: string,
  now = Date.now(),
): { day: string; hour: string } | null {
  const today = beijingHourParts(now).day;
  const day = shiftDay(today, days);
  const nextHour = beijingHourParts((Math.floor(now / BILLING_HOUR) + 1) * BILLING_HOUR);
  const chosenHour = /^(0\d|1\d|2[0-3])$/.test(hour) ? hour : nextHour.hour;
  const choice = { day, hour: chosenHour };
  if (Date.parse(`${day}T${chosenHour}:00:00+08:00`) > now) return choice;
  // Today may have no remaining whole hour. Do not silently select tomorrow.
  return days === 0 && nextHour.day === today ? nextHour : null;
}
