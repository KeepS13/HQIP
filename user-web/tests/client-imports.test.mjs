import assert from "node:assert/strict";
import test from "node:test";
import { clientImportScheme, importClients, selectedImportSubscription, subscriptionImportURL } from "../src/portal/client-imports.ts";

test("format and download links retain subscription identity and unrelated query values", () => {
  const base = "https://example.test/usub/token_A-123?source=%E6%B5%8B%E8%AF%95&format=old&download=1";
  for (const format of ["clash", "v2ray", "singbox"]) {
    const link = new URL(subscriptionImportURL(base, format));
    assert.equal(link.pathname, "/usub/token_A-123");
    assert.equal(link.searchParams.get("source"), "测试");
    assert.equal(link.searchParams.get("format"), format === "clash" ? null : format);
    assert.equal(link.searchParams.has("download"), false);
    const download = new URL(subscriptionImportURL(base, format, true));
    assert.equal(download.searchParams.get("download"), "1");
    download.searchParams.delete("download");
    assert.equal(download.href, link.href);
  }
});

test("invalid or executable URLs never become import links", () => {
  for (const value of ["", "/usub/token", "not a url", "javascript:alert(1)", "data:text/plain,token"]) {
    assert.equal(subscriptionImportURL(value, "singbox"), "");
  }
  assert.equal(subscriptionImportURL("http://localhost:8080/usub/test", "clash"), "http://localhost:8080/usub/test");
});

test("client schemes preserve URL query characters and Unicode names without fragment confusion", () => {
  const subscription = "https://example.test/usub/token?format=singbox&source=a%26b";
  const name = "HQIP 香港 & 日本 #1";
  for (const id of ["clash", "verge", "cmfa", "v2rayng", "singbox"]) {
    const link = new URL(clientImportScheme(id, subscription, name));
    assert.equal(link.searchParams.get("url"), subscription);
    if (id === "clash" || id === "verge") assert.equal(link.searchParams.get("name"), name);
    if (id === "v2rayng" || id === "singbox") assert.equal(decodeURIComponent(link.hash.slice(1)), name);
  }
  assert.equal(new URL(clientImportScheme("v2rayng", subscription, name)).hostname, "install-sub");
  assert.equal(new URL(clientImportScheme("singbox", subscription, name)).hostname, "import-remote-profile");
  assert.equal(clientImportScheme("v2rayn", subscription, name), "");
  assert.equal(clientImportScheme("shadowrocket", subscription, name), "");
  assert.equal(clientImportScheme("clash", "", name), "");
});

test("each client receives its native subscription family", () => {
  assert.deepEqual(Object.fromEntries(importClients.map(({ id, format }) => [id, format])), {
    clash: "clash", verge: "clash", cmfa: "clash", v2rayn: "v2ray", v2rayng: "v2ray", shadowrocket: "v2ray", singbox: "singbox",
  });
});

test("subscription selection never substitutes another user's intended package", () => {
  const active = { id: "active", status: "active" };
  const expired = { id: "expired", status: "expired" };
  const other = { id: "other", status: "active" };
  assert.equal(selectedImportSubscription([active, expired], null), active);
  assert.equal(selectedImportSubscription([active, other], null), null);
  assert.equal(selectedImportSubscription([expired], null), null);
  assert.equal(selectedImportSubscription([], null), null);
  assert.equal(selectedImportSubscription([active, expired], "expired"), expired);
  assert.equal(selectedImportSubscription([active, expired], "missing"), null);
  assert.equal(selectedImportSubscription([active], ""), null);
});
