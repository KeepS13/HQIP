import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const { outputFiles } = await build({ entryPoints: [fileURLToPath(new URL("../src/subscription-grant.ts", import.meta.url))], bundle: true, write: false, platform: "node", format: "esm", define: { "import.meta.env": "{}" } });
const grant = await import(`data:text/javascript;base64,${Buffer.from(outputFiles[0].text).toString("base64")}`);
const operation = { userId: "user", template: { id: "template", name: "Plan", revision: 2, nodeIds: ["node"] }, body: { templateId: "template", templateRevision: 2, expectedRevision: 3, requestId: "fixed-request-key" } };
const receipt = { id: "subscription", userId: "user", templateId: "template" };
const response = (status, data) => new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });

test("pending identity survives reopening and is isolated per user; storage failures never silently discard it", () => {
  const entries = new Map();
  globalThis.sessionStorage = { getItem: key => entries.get(key) ?? null, setItem: (key, value) => entries.set(key, value), removeItem: key => entries.delete(key) };
  grant.savePendingGrant(operation);
  assert.deepEqual(grant.readPendingGrant("user"), operation);
  assert.equal(grant.readPendingGrant("other"), null);
  const key = [...entries.keys()][0];
  entries.set(key, "corrupt");
  assert.throws(() => grant.readPendingGrant("user"), /无法读取/);
  assert.equal(entries.get(key), "corrupt");
  grant.clearPendingGrant("user");
  assert.equal(grant.readPendingGrant("user"), null);
  globalThis.sessionStorage.setItem = () => { throw new Error("storage blocked"); };
  assert.throws(() => grant.savePendingGrant(operation), /尚未提交/);
});

test("lost network response and 503 retry the same immutable body and accept only a matching receipt", async () => {
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, body: options.body });
    if (calls.length === 1) throw new TypeError("connection reset after commit");
    if (calls.length === 2) return response(503, { code: "SUBSCRIPTION_ASSIGNMENT_UNAVAILABLE", error: "temporary" });
    return response(200, receipt);
  };
  assert.deepEqual(await grant.sendSubscriptionGrant(operation), receipt);
  assert.equal(calls.length, 3);
  assert(calls.every(call => call.body === JSON.stringify(operation.body)));
});

test("malformed success is uncertain and automatic recovery stops after three attempts", async () => {
  let count = 0;
  globalThis.fetch = async () => { count++; return response(200, { ...receipt, userId: "other" }); };
  await assert.rejects(grant.sendSubscriptionGrant(operation), /未收到有效/);
  assert.equal(count, 3);
});

test("only explicit business rejection releases identity, never HTTP status or an auth failure alone", async () => {
  for (const [status, code, terminal] of [[409, "SUBSCRIPTION_ASSIGNMENT_REJECTED", true], [403, "SUBSCRIPTION_ASSIGNMENT_REJECTED", true], [403, "", false], [409, "", false]]) {
    let count = 0;
    globalThis.fetch = async () => { count++; return response(status, { code, error: "rejected" }); };
    try { await grant.sendSubscriptionGrant(operation); assert.fail("must fail"); }
    catch (error) { assert.equal(grant.grantWasRejected(error), terminal); }
    assert.equal(count, 1);
  }
});
