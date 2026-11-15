import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const { outputFiles } = await build({ entryPoints: [fileURLToPath(new URL("../src/subscription-expiry.ts", import.meta.url))], bundle: true, write: false, platform: "node", format: "esm" });
const { subscriptionExpiryFields, parseSubscriptionExpiry, canAdjustSubscriptionExpiry } = await import(`data:text/javascript;base64,${Buffer.from(outputFiles[0].text).toString("base64")}`);

test("expiry uses Beijing minutes across browser zones and year boundaries", () => {
  const original = process.env.TZ;
  try {
    for (const zone of ["UTC", "America/Los_Angeles", "Asia/Tokyo"]) {
      process.env.TZ = zone;
      assert.deepEqual(subscriptionExpiryFields("2026-12-31T16:37:42Z"), { day: "2027-01-01", time: "00:37" });
      assert.equal(parseSubscriptionExpiry("2027-01-01", "00:37"), "2026-12-31T16:37:00.000Z");
      assert.equal(parseSubscriptionExpiry("2028-02-29", "16:37"), "2028-02-29T08:37:00.000Z");
    }
  } finally { if (original === undefined) delete process.env.TZ; else process.env.TZ = original; }
});
test("rejects impossible dates, hours and missing fields", () => {
  for (const [day, time] of [["2026-02-29", "12:00"], ["2026-04-31", "12:00"], ["2026-11-15", "24:00"], ["2026-11-15", "12:60"], ["", "12:00"], ["2026-11-15", ""]]) assert.equal(parseSubscriptionExpiry(day, time), null);
});
test("serializes only minute precision", () => {
  assert.equal(parseSubscriptionExpiry("2026-11-15", "12:34"), "2026-11-15T04:34:00.000Z");
});
test("only unexpired, unrevoked grants can be edited, regardless of other restrictions", () => {
  const now = Date.parse("2026-11-15T12:00:00Z");
  for (const status of ["active", "disabled", "exhausted"]) {
    assert.equal(canAdjustSubscriptionExpiry(status,"2026-11-15T12:01:00Z",now),true);
    assert.equal(canAdjustSubscriptionExpiry(status,"2026-11-15T12:00:00Z",now),false);
  }
  for (const status of ["expired", "revoked", "removed"]) assert.equal(canAdjustSubscriptionExpiry(status,"2026-12-15T12:00:00Z",now),false);
});
