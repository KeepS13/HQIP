import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const { outputFiles } = await build({
  entryPoints: [fileURLToPath(new URL("../src/date-shortcuts.ts", import.meta.url))],
  bundle: true, write: false, platform: "node", format: "esm",
});
const { dateRangeShortcut, expiryDateShortcut } = await import(
  `data:text/javascript;base64,${Buffer.from(outputFiles[0].text).toString("base64")}`
);

test("order ranges use Beijing calendar days across browser timezones and year boundaries", () => {
  const original = process.env.TZ;
  try {
    for (const zone of ["UTC", "America/Los_Angeles", "Asia/Tokyo"]) {
      process.env.TZ = zone;
      const now = Date.parse("2026-12-31T16:30:00Z");
      assert.deepEqual(dateRangeShortcut("today", now), { from: "2027-01-01", to: "2027-01-01" });
      assert.deepEqual(dateRangeShortcut("yesterday", now), { from: "2026-12-31", to: "2026-12-31" });
      assert.deepEqual(dateRangeShortcut("last7", now), { from: "2026-12-26", to: "2027-01-01" });
      assert.deepEqual(dateRangeShortcut("last30", now), { from: "2026-12-03", to: "2027-01-01" });
      assert.deepEqual(dateRangeShortcut("month", now), { from: "2027-01-01", to: "2027-01-01" });
    }
  } finally {
    if (original === undefined) delete process.env.TZ;
    else process.env.TZ = original;
  }
});

test("ranges count inclusive days and keep leap day", () => {
  const now = Date.parse("2028-03-01T12:00:00+08:00");
  assert.deepEqual(dateRangeShortcut("last7", now), { from: "2028-02-24", to: "2028-03-01" });
  assert.deepEqual(dateRangeShortcut("last30", now), { from: "2028-02-01", to: "2028-03-01" });
  assert.deepEqual(dateRangeShortcut("yesterday", now), { from: "2028-02-29", to: "2028-02-29" });
  assert.deepEqual(dateRangeShortcut("month", Date.parse("2028-03-31T23:00:00+08:00")), { from: "2028-03-01", to: "2028-03-31" });
});

test("expiry shortcuts preserve an explicit future hour and choose a future whole hour otherwise", () => {
  const now = Date.parse("2026-11-08T12:30:00+08:00");
  assert.deepEqual(expiryDateShortcut(0, "16", now), { day: "2026-11-08", hour: "16" });
  assert.deepEqual(expiryDateShortcut(0, "09", now), { day: "2026-11-08", hour: "13" });
  assert.deepEqual(expiryDateShortcut(0, "", now), { day: "2026-11-08", hour: "13" });
  assert.deepEqual(expiryDateShortcut(1, "09", now), { day: "2026-11-09", hour: "09" });
  assert.deepEqual(expiryDateShortcut(7, "16", now), { day: "2026-11-15", hour: "16" });
  assert.deepEqual(expiryDateShortcut(0, "12", Date.parse("2026-11-08T12:00:00+08:00")), { day: "2026-11-08", hour: "13" });
});

test("today never silently rolls into tomorrow when no future whole hour remains", () => {
  const now = Date.parse("2026-12-31T23:30:00+08:00");
  assert.equal(expiryDateShortcut(0, "23", now), null);
  assert.equal(expiryDateShortcut(0, "", now), null);
  assert.deepEqual(expiryDateShortcut(1, "", now), { day: "2027-01-01", hour: "00" });
});

test("30-day shortcut uses 30 calendar days and does not change the billing-month rule", () => {
  assert.deepEqual(expiryDateShortcut(30, "18", Date.parse("2027-01-31T10:00:00+08:00")), { day: "2027-03-02", hour: "18" });
  assert.deepEqual(expiryDateShortcut(30, "18", Date.parse("2028-01-31T10:00:00+08:00")), { day: "2028-03-01", hour: "18" });
});
