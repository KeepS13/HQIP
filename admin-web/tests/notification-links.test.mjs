import assert from "node:assert/strict";
import { test } from "node:test";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const { outputFiles } = await build({
  entryPoints: [fileURLToPath(new URL("../src/pages/Notifications.tsx", import.meta.url))],
  bundle: true, write: false, platform: "node", format: "cjs",
  packages: "external", loader: { ".css": "empty" },
  define: { "import.meta.env": "{}" },
});
const compiled = { exports: {} };
new Function("require", "module", "exports", outputFiles[0].text)(createRequire(import.meta.url), compiled, compiled.exports);
const { detailLink } = compiled.exports;

test("backup and logging alerts can open the operation log page", () => {
  assert.equal(detailLink("/operation-logs"), "/operation-logs");
  assert.equal(detailLink("/nodes/node_1"), "/nodes/node_1");
  assert.equal(detailLink("/servers/server-1"), "/servers/server-1");
  assert.equal(detailLink("/domains"), "/domains");
});

test("alert links never accept arbitrary URLs or path suffixes", () => {
  for (const path of ["https://example.com/operation-logs", "//example.com/operation-logs", "javascript:alert(1)", "/operation-logs?next=https://example.com", "/operation-logs/../users", "/operation-logs#external", "/nodes/node_1/extra", ""]) {
    assert.equal(detailLink(path), "/notifications", path);
  }
});
