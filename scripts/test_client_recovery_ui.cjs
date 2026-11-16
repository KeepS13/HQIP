// Render the shipped components with backend state fixtures. No browser/network
// bridge is needed; hooks and JSX use the real React server renderer.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const Module = require('node:module');
const root = path.resolve(__dirname, '..');
const frontend = path.join(root, 'desktop/frontend');
const requireUI = Module.createRequire(path.join(frontend, 'package.json'));
const esbuild = requireUI('esbuild');
const React = requireUI('react');
const { renderToStaticMarkup } = requireUI('react-dom/server');

async function load(source, directory, name) {
  const result = await esbuild.build({
    stdin: { contents: source, loader: 'tsx', resolveDir: directory, sourcefile: name },
    bundle: true, platform: 'node', format: 'cjs', write: false,
    external: ['react', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  });
  const m = new Module(path.join(frontend, name + '.cjs'));
  m.filename = path.join(frontend, name + '.cjs');
  m.paths = Module._nodeModulePaths(frontend);
  m._compile(result.outputFiles[0].text, m.filename);
  return m.exports;
}

(async () => {
  let source = fs.readFileSync(path.join(frontend, 'src/main.tsx'), 'utf8');
  source = source.replace(/createRoot\(document[\s\S]*$/, 'export {App};');
  source = source.replace('useState<State>(empty)', 'useState<State>(normalize(globalThis.__recoveryFixture))');
  const { App } = await load(source, path.join(frontend, 'src'), 'recovery-fixture');
  const subscription = { id: 'sub-a', templateName: '当前订阅', status: 'active', expiresAt: '2099-01-01', quotaBytes: null, usedBytes: 0, reservedBytes: 0, maxConnections: 2, occupied: 0 };
  const fixture = { loggedIn: true, subscription, subscriptions: [subscription], desiredNodeId: 'A', desiredSubscriptionId: 'sub-a', actualNodeId: '', actualSubscriptionId: '', nodes: [{ id: 'A', name: '日本-001', ready: true }], nodeChoices: [], runState: 'recovering', connectionIntent: true, lastError: '', syncError: '', metrics: { connectedAt: new Date().toISOString(), uploadBytes: 12345, downloadBytes: 67890, stale: true } };
  globalThis.__recoveryFixture = fixture;
  const recovering = renderToStaticMarkup(React.createElement(App));
  assert.match(recovering, /<h2>恢复中<\/h2>/);
  assert.match(recovering, /is-recovering/);
  assert.match(recovering, /status-dot amber/);
  assert.match(recovering, /Hysteria 2/);
  assert.match(recovering, /断开连接/);
  assert.doesNotMatch(recovering, /role="alert"|重新确认|power[^\"]*loading/);
  globalThis.__recoveryFixture = {...fixture,runState:'connected',actualNodeId:'A'};
  const restored = renderToStaticMarkup(React.createElement(App));
  assert.match(restored, /<h2>已连接<\/h2>/);
  assert.match(restored, /TLS 验证已开启/);
  assert.doesNotMatch(restored, /is-recovering|<h2>连接恢复中<\/h2>/);
  globalThis.__recoveryFixture = { ...fixture, runState: 'failed', connectionIntent: false, lastError: '首次连接失败' };
  const failed = renderToStaticMarkup(React.createElement(App));
  assert.match(failed, /role="alert"/);
  assert.match(failed, /首次连接失败/);
  assert.match(failed, /<h2>连接失败<\/h2>/);
  const admin = path.join(root, 'admin-web/src/pages/DeviceClients.tsx');
  const text = fs.readFileSync(admin, 'utf8');
  const start = text.indexOf('function sessionStatus('), end = text.indexOf('function ClientSessions(', start);
  const { sessionStatus } = await load(text.slice(start, end) + '\nexport {sessionStatus};', path.dirname(admin), 'admin-status-fixture');
  assert.equal(sessionStatus({ runState: 'recovering', lastSeen: new Date().toISOString() }).label, '恢复中');
  assert.equal(sessionStatus({ runState: 'recovering', lastSeen: '2000-01-01' }).label, '心跳过期');
  delete globalThis.__recoveryFixture;
  console.log('PASS desktop recovery/restored states, first failure, admin recovering/stale status');
})().catch(error => { console.error(error); process.exitCode = 1; });
