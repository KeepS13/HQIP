import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";

async function compile(entry) {
  const result = await build({ entryPoints: [fileURLToPath(new URL(entry, import.meta.url))], bundle: true, write: false, platform: "node", format: "cjs", packages: "external", loader: { ".css": "empty" }, define: { "import.meta.env": "{}" } });
  const module = { exports: {} };
  new Function("require", "module", "exports", result.outputFiles[0].text)(createRequire(import.meta.url), module, module.exports);
  return module.exports;
}
const { hostNetworkFresh, uniqueHostNetworks } = await compile("../src/host-network.ts");
const { HostNetworkPanel, NodeTrafficTotals } = await compile("../src/HostNetwork.tsx");
const at = Date.now();
function host(serverId = "server-a", offset = 0) {
  return { serverId, receive: 1000, send: 2000, firstObserved: new Date(at).toISOString(), fresh: true, discontinuity: false, receiveRate: 123, sendRate: 456, sample: { bootId: "boot", bootAt: new Date(at - 3600000).toISOString(), at: new Date(at + offset).toISOString(), interfaces: [{name:"eth0",index:2,receive:1000,send:2000}], excluded: ["lo"] } };
}
function node(serverId, hostNetwork) { return { serverId, hostNetwork, monthUpload: 10, monthDownload: 20 }; }
const render = element => renderToStaticMarkup(React.createElement(MemoryRouter, {}, element));

test("same-server nodes count host once, keep missing hosts, and choose chronological latest", () => {
  const first = host(), next = host("server-a", 1);
  const values = uniqueHostNetworks([node("server-a", first), node("server-a", next), node("server-a"), node("server-b"), node("server-b"), node("")]);
  assert.deepEqual(values, [next, undefined]);
  const html = render(React.createElement(NodeTrafficTotals, {nodes:[node("server-a", first),node("server-a", first),node("server-b")]}));
  assert.ok(html.includes("3 KB")); assert.ok(html.includes("1 / 2 台服务器")); assert.ok(!html.includes("6 KB"));
});
test("unknown, expired and discontinuous counters retain explicit states", () => {
  const unknown = render(React.createElement(HostNetworkPanel, {serverId:"server-a"}));
  assert.ok(unknown.includes("未知用量不等于 0")); assert.ok(unknown.includes("独立分项未分类"));
  const value = {...host("server-a", -31000), discontinuity:true};
  const stale = render(React.createElement(HostNetworkPanel, {value,serverId:"server-a"}));
  assert.ok(stale.includes("当前速率未知")); assert.ok(stale.includes("3 KB")); assert.ok(!stale.includes("123 B/s")); assert.ok(stale.includes("断点前最后采样"));
});
test("freshness expires after thirty seconds even if the cached API flag is true", () => {
  const value = host();
  assert.equal(hostNetworkFresh(value, at),true);
  assert.equal(hostNetworkFresh(value, at+30000),true);
  assert.equal(hostNetworkFresh(value, at+30001),false);
  assert.equal(hostNetworkFresh(value, at-1),false);
  assert.equal(hostNetworkFresh({...value,fresh:false}, at),false);
  assert.equal(hostNetworkFresh(undefined, at),false);
});

test("fractional host rates stay visible and measured zero stays zero", () => {
  const value = {...host(), receiveRate:0.2,sendRate:0};
  const html = render(React.createElement(HostNetworkPanel, {value,serverId:"server-a"}));
  assert.ok(html.includes("&lt;1 B/s")); assert.ok(html.includes("0 B/s")); assert.ok(!html.includes("undefined"));
});
