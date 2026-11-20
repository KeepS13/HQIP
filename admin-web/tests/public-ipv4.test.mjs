import assert from "node:assert/strict";
import { test } from "node:test";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const { outputFiles } = await build({ entryPoints: [fileURLToPath(new URL("../src/public-ipv4.ts", import.meta.url))], bundle: true, write: false, platform: "node", format: "esm" });
const { isPublicIPv4 } = await import(`data:text/javascript;base64,${Buffer.from(outputFiles[0].text).toString("base64")}`);
const cases = JSON.parse(await readFile(new URL("../../internal/netutil/testdata/public-ipv4.json", import.meta.url), "utf8"));
test("frontend and backend share the same public IPv4 cases", () => {
  for (const { input, valid } of cases) assert.equal(isPublicIPv4(input), valid, input);
});
