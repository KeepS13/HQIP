// Browser regression against the built user-web bundle. All network requests are
// intercepted; neither credentials nor requests reach a live HQIP service.
// Run after npm --prefix user-web run build. Install Playwright separately or set
// HQIP_PLAYWRIGHT to its module path; HQIP_CHROMIUM may select a local browser.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.HQIP_PLAYWRIGHT || 'playwright');

const root = path.resolve(__dirname, '..');
const assets = path.join(root, 'user-web', 'dist');
const output = path.join(root, '.local', 'client-imports-qa');
const origin = 'https://hqip.example.test';
const apiOrigin = 'https://hqip.sharevps.online';
const user = { id: 'fixture-user', email: 'demo@example.test', enabled: true, revision: 1 };
const nodes = [{ id: 'fixture-node', name: '测试节点', server: 'node.example.test', ready: true }];
const createSubscription = (id, status = 'active') => ({
  id, userId: user.id, templateId: 'fixture-template', templateName: '同名套餐 · 香港 + #2',
  createdAt: '2026-11-01T00:00:00Z', expiresAt: '2099-01-01T00:00:00Z', status,
  quotaBytes: 50e9, usedBytes: 2e9, reservedBytes: 0, bandwidthMbps: 100,
  maxConnections: 2, occupied: 1, nodeIds: ['fixture-node'], defaultNodeId: 'fixture-node', revision: 1,
});
const subscriptions = [createSubscription('fixture-sub-a'), createSubscription('fixture-sub-b'), createSubscription('fixture-expired', 'expired')];
const tokens = new Map(subscriptions.map((s) => [s.id, `${apiOrigin}/usub/${s.id}-1`]));
const combinedURL = () => {
  const current = visibleSubscriptions.filter((s) => s.status === 'active');
  return !current.length || current.some((s) => !tokens.get(s.id)) ? '' : `${apiOrigin}/usub/${current.map((s) => tokens.get(s.id).split('/').pop()).join('.')}`;
};
let visibleSubscriptions = subscriptions;
let authenticated = true;
let tokenVersion = 1;
let readToHold = null;
let heldRead = null;
let holdWrite = false;
let heldWrite = null;
let failNextRead = false;
let failNextWrite = false;
let detailDelayMs = 0;
const writes = [], unexpected = [], errors = [];

async function main() {
  assert.ok(fs.existsSync(path.join(assets, 'index.html')), 'Build user-web before browser tests');
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.HQIP_CHROMIUM || undefined });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1100 }, acceptDownloads: true });
  await context.addInitScript(() => {
    window.fixtureClipboard = '';
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (value) => { window.fixtureClipboard = value; } } });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (error) => errors.push(error.message));
  await context.route('**/*', async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.origin === apiOrigin) {
      const headers = { 'access-control-allow-origin': req.headers().origin || origin, 'access-control-allow-credentials': 'true', 'access-control-allow-headers': 'Content-Type,X-CSRF-Token', 'access-control-allow-methods': 'GET,POST,OPTIONS' };
      if (req.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      const p = url.pathname;
      const combined = /^\/api\/v1\/user\/import-link(?:\/(rotate|revoke))?$/.exec(p);
      const match = /^\/api\/v1\/user\/subscriptions\/([^/]+)(\/import-link(?:\/(rotate|revoke))?)?$/.exec(p) || (combined && [p, 'all', '/import-link', combined[1]]);
      let body, status = 200;
      if (p === '/api/v1/user/session') body = { authenticated, user, csrfToken: 'fixture-csrf' };
      else if (p === '/api/v1/user/config') body = { portalEnabled: true, registrationEnabled: true, turnstileConfigured: true, turnstileSiteKey: 'fixture-site-key' };
      else if (p === '/api/v1/user/profile') body = { user, subscriptions: visibleSubscriptions, sessions: [] };
      else if (p === '/api/v1/user/login' && req.method() === 'POST') {
        authenticated = true;
        body = { authenticated, user, csrfToken: 'fixture-csrf' };
      } else if (match && req.method() === 'GET') {
        const id = decodeURIComponent(match[1]);
        const subscription = visibleSubscriptions.find((s) => s.id === id);
        if (id === 'all') body = { url: combinedURL() };
        else if (!subscription) { status = 404; body = { error: '订阅不存在' }; }
        else if (match[2]) body = { url: subscription.status === 'active' ? tokens.get(id) || '' : '' };
        else body = structuredClone({ subscription, nodes, subscriptionUrl: tokens.get(id) || '' });
        if (failNextRead) { failNextRead = false; status = 503; body = { error: '测试读取失败' }; }
        if (readToHold === id) {
          readToHold = null;
          await new Promise((resolve) => { heldRead = resolve; });
        }
        if (detailDelayMs) await new Promise((resolve) => setTimeout(resolve, detailDelayMs));
      } else if (match && req.method() === 'POST' && match[2]) {
        const id = decodeURIComponent(match[1]);
        writes.push({ id, action: match[3] || 'create', csrf: req.headers()['x-csrf-token'] });
        if (holdWrite) {
          holdWrite = false;
          await new Promise((resolve) => { heldWrite = resolve; });
        }
        if (failNextWrite) { failNextWrite = false; status = 500; body = { error: '测试写入失败' }; }
        else {
          const ids = id === 'all' ? visibleSubscriptions.filter((s) => s.status === 'active').map((s) => s.id) : [id];
          for (const item of ids) {
            if (match[3] === 'revoke') tokens.set(item, '');
            else if (match[3] === 'rotate' || !tokens.get(item)) tokens.set(item, `${apiOrigin}/usub/${item}-${++tokenVersion}`);
          }
          body = { url: id === 'all' ? combinedURL() : tokens.get(id) };
        }
      } else if (p.startsWith('/usub/') && url.searchParams.get('download') === '1') {
        const format = url.searchParams.get('format') || 'clash';
        const ext = { clash: 'yaml', v2ray: 'txt', singbox: 'json' }[format];
        const content = { clash: 'proxies: []\n', v2ray: 'aHlzdGVyaWEyOi8vZml4dHVyZQ==', singbox: '{"outbounds":[]}' }[format];
        return route.fulfill({ status: 200, headers: { ...headers, 'content-disposition': `attachment; filename="hqip-${format}.${ext}"` }, contentType: { clash: 'text/yaml', v2ray: 'text/plain', singbox: 'application/json' }[format], body: content });
      } else { unexpected.push(`${req.method()} ${p}`); status = 404; body = { error: 'Unexpected fixture route' }; }
      return route.fulfill({ status, headers, contentType: 'application/json', body: JSON.stringify(body) });
    }
    if (url.origin === origin) {
      let file = path.resolve(assets, '.' + url.pathname);
      if (file !== assets && !file.startsWith(assets + path.sep)) return route.abort();
      if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(assets, 'index.html');
      const mime = { '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' }[path.extname(file)] || 'text/html';
      return route.fulfill({ contentType: mime, body: fs.readFileSync(file) });
    }
    if (url.hostname === 'challenges.cloudflare.com') {
      return route.fulfill({ contentType: 'text/javascript', body: 'window.turnstile={render:(el,opts)=>{queueMicrotask(()=>opts.callback("fixture-token"));return "fixture-widget"},remove:()=>{}};' });
    }
    unexpected.push(req.url());
    return route.abort();
  });
  const masked = page.getByLabel('此订阅的导入链接', { exact: true });
  const select = page.getByLabel('选择订阅', { exact: true });
  const refresh = page.getByRole('button', { name: '刷新链接状态', exact: true });
  const client = (name) => page.getByRole('button', { name, exact: true });
  async function value(expected) {
    await page.waitForFunction((expected) => document.querySelector('[aria-label="此订阅的导入链接"]')?.value === expected, expected);
    assert.equal(await masked.getAttribute('type'), 'password');
  }
  async function waitHeld(kind) {
    for (let i = 0; i < 100; i++) {
      if (kind === 'read' ? heldRead : heldWrite) return;
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    throw new Error(`No held ${kind} request`);
  }
  function releaseRead() { const release = heldRead; heldRead = null; release(); }
  async function snapshot(name) {
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${name}: horizontal overflow`);
    await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true, animations: 'disabled' });
  }
  try {
    tokens.delete('fixture-sub-b');
    await page.goto(`${origin}/imports`);
    await select.waitFor();
    assert.equal(await select.inputValue(), 'all', 'default must be all subscriptions');
    await page.waitForFunction(() => !!document.querySelector('[aria-label="此订阅的导入链接"]')?.value);
    await value(combinedURL());
    assert.equal(writes.length, 1, 'entry must automatically prepare missing links once');
    assert.equal(writes[0].id, 'all');
    assert.equal(await page.getByRole('button', { name: '创建导入链接', exact: true }).count(), 0);
    await page.getByRole('button', { name: '复制链接', exact: true }).click();
    assert.equal(await page.evaluate(() => window.fixtureClipboard), combinedURL());
    const oldCombined = combinedURL();
    await page.getByRole('button', { name: '轮换链接', exact: true }).click();
    await page.getByRole('dialog').getByText('正在使用的相关连接会被断开', { exact: false }).waitFor();
    await page.getByRole('button', { name: '确认轮换', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    await value(combinedURL());
    assert.notEqual(combinedURL(), oldCombined);
    writes.length = 0;
    const nav = await page.locator('aside nav a').allTextContents();
    assert.equal(nav.indexOf('复制订阅链接'), nav.indexOf('我的订阅') + 1);
    await select.selectOption('fixture-sub-a');
    await value(tokens.get('fixture-sub-a'));
    const cases = [
      ['Clash / Mihomo', 'clash', 'clash:'], ['Clash Verge Rev', 'clash', 'clash:'],
      ['Clash Meta Android', 'clash', 'clashmeta:'], ['v2rayN', 'v2ray', null],
      ['v2rayNG', 'v2ray', 'v2rayng:'], ['Shadowrocket', 'v2ray', null], ['Sing Box', 'singbox', 'sing-box:'],
    ];
    for (const [label, format, protocol] of cases) {
      await client(label).click();
      const expected = tokens.get('fixture-sub-a') + (format === 'clash' ? '' : `?format=${format}`);
      await value(expected);
      await page.getByRole('button', { name: '复制链接', exact: true }).click();
      assert.equal(await page.evaluate(() => window.fixtureClipboard), expected);
      const link = page.getByRole('link', { name: '一键导入', exact: true });
      if (protocol) {
        const scheme = new URL(await link.getAttribute('href'));
        assert.equal(scheme.protocol, protocol);
        assert.equal(scheme.searchParams.get('url'), expected);
        if (['v2rayng:', 'sing-box:'].includes(protocol)) assert.equal(decodeURIComponent(scheme.hash.slice(1)), 'HQIP 同名套餐 · 香港 + #2 -sub-a');
      } else assert.equal(await link.count(), 0);
      const exported = new URL(await page.getByRole('link', { name: '导出配置', exact: true }).getAttribute('href'));
      assert.equal(exported.searchParams.get('download'), '1');
      assert.equal(exported.searchParams.get('format') || 'clash', format);
    }
    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: '导出配置', exact: true }).click()]);
    assert.equal(download.suggestedFilename(), 'hqip-singbox.json');
    assert.equal(fs.readFileSync(await download.path(), 'utf8'), '{"outbounds":[]}');
    await snapshot('desktop');

    await select.selectOption('fixture-sub-b');
    await value(tokens.get('fixture-sub-b') + '?format=singbox');
    await page.goBack();
    await value(tokens.get('fixture-sub-a') + '?format=singbox');
    await page.goForward();
    await value(tokens.get('fixture-sub-b') + '?format=singbox');
    await page.reload();
    await value(tokens.get('fixture-sub-b'));

    // A late read for A must never populate B's controls.
    readToHold = 'fixture-sub-a';
    await select.selectOption('fixture-sub-a');
    await waitHeld('read');
    await select.selectOption('fixture-sub-b');
    await value(tokens.get('fixture-sub-b'));
    releaseRead();
    await page.waitForTimeout(80);
    await value(tokens.get('fixture-sub-b'));

    // A poll started before rotation must not restore the previous token.
    readToHold = 'fixture-sub-b';
    await refresh.click();
    await waitHeld('read');
    await page.getByRole('button', { name: '轮换链接', exact: true }).click();
    holdWrite = true;
    await page.getByRole('button', { name: '确认轮换', exact: true }).click();
    await waitHeld('write');
    assert.equal(await select.isDisabled(), true);
    assert.equal(await masked.inputValue(), '');
    heldWrite(); heldWrite = null;
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    await value(tokens.get('fixture-sub-b'));
    releaseRead();
    await page.waitForTimeout(80);
    await value(tokens.get('fixture-sub-b'));

    readToHold = 'fixture-sub-b';
    await refresh.click();
    await waitHeld('read');
    await page.getByRole('button', { name: '撤销导入链接', exact: true }).click();
    await page.getByRole('button', { name: '确认撤销', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    await page.getByRole('button', { name: '复制链接', exact: true }).waitFor();
    releaseRead();
    await page.waitForTimeout(80);
    assert.equal(await masked.count(), 0);
    await refresh.click();
    await page.waitForTimeout(80);
    assert.equal(await masked.count(), 0, 'poll must not resurrect a revoked link');
    await page.getByRole('button', { name: '复制链接', exact: true }).click();
    await page.waitForFunction(() => !!document.querySelector('[aria-label="此订阅的导入链接"]')?.value);
    await value(tokens.get('fixture-sub-b'));
    assert.ok(writes.every((write) => write.csrf === 'fixture-csrf'));
    assert.ok(writes.every((write) => write.id === 'fixture-sub-b'));

    failNextRead = true;
    await refresh.click();
    await page.getByText('测试读取失败', { exact: false }).waitFor();
    assert.equal(await page.getByRole('link', { name: '导出配置', exact: true }).count(), 0);
    await refresh.click();
    await page.getByRole('link', { name: '导出配置', exact: true }).waitFor();
    failNextWrite = true;
    await page.getByRole('button', { name: '轮换链接', exact: true }).click();
    await page.getByRole('button', { name: '确认轮换', exact: true }).click();
    await page.getByText('测试写入失败', { exact: false }).waitFor();
    await page.getByRole('button', { name: '取消', exact: true }).click();
    await refresh.click();
    await value(tokens.get('fixture-sub-b'));
    assert.equal(await select.isDisabled(), false);

    await select.selectOption('fixture-expired');
    await page.getByText('这份订阅已到期', { exact: false }).waitFor();
    assert.equal(await masked.count(), 0);
    assert.equal(await page.getByRole('button', { name: '复制链接', exact: true }).isDisabled(), true);
    assert.equal(await page.getByRole('button', { name: '轮换链接', exact: true }).isDisabled(), true);
    assert.equal(await page.getByRole('link', { name: '导出配置', exact: true }).count(), 0);
    assert.equal(await page.getByRole('button', { name: '撤销导入链接', exact: true }).isEnabled(), true);

    await page.goto(`${origin}/imports?subscription=missing`);
    await page.getByText('指定的订阅不存在或已不可访问', { exact: false }).waitFor();
    assert.equal(await select.inputValue(), '');
    assert.equal(await masked.count(), 0);
    // Slow link reads still render while the separate profile poll continues.
    detailDelayMs = 6000;
    await page.goto(`${origin}/imports?subscription=fixture-sub-a`);
    await value(tokens.get('fixture-sub-a'));
    detailDelayMs = 0;
    await select.selectOption('fixture-sub-a');
    await value(tokens.get('fixture-sub-a'));
    await page.setViewportSize({ width: 390, height: 844 });
    await client('Sing Box').click();
    await snapshot('mobile');
    await page.setViewportSize({ width: 320, height: 720 });
    await snapshot('narrow');
    await page.getByRole('button', { name: '打开导航', exact: true }).click();
    await page.locator('.sidebar-open').getByRole('link', { name: '我的订阅', exact: true }).waitFor();
    await page.getByRole('button', { name: '关闭导航', exact: true }).click();

    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.goto(`${origin}/subscriptions/fixture-sub-a`);
    assert.equal(await masked.count(), 0);
    const jump = page.locator('a[href="/imports?subscription=fixture-sub-a"]');
    await jump.click();
    await value(tokens.get('fixture-sub-a'));

    visibleSubscriptions = [subscriptions[0]];
    await page.goto(`${origin}/imports`);
    await value(tokens.get('fixture-sub-a'));
    assert.equal(new URL(page.url()).searchParams.get('subscription'), null);
    assert.equal(await select.inputValue(), 'all');
    await page.goto(`${origin}/`);
    await page.getByRole('button', { name: '复制群号', exact: true }).click();
    assert.equal(await page.evaluate(() => window.fixtureClipboard), '383590149');
    await snapshot('overview');
    await page.setViewportSize({ width: 375, height: 812 });
    await snapshot('overview-mobile');
    await page.setViewportSize({ width: 1440, height: 1100 });
    visibleSubscriptions = [];
    await page.goto(`${origin}/imports`);
    await page.getByText('还没有开通订阅', { exact: true }).waitFor();
    assert.equal(await select.count(), 0);

    visibleSubscriptions = subscriptions;
    authenticated = false;
    await page.goto(`${origin}/imports?subscription=fixture-sub-b`);
    await page.waitForURL('**/login?returnTo=*');
    await page.getByLabel('邮箱', { exact: true }).fill(user.email);
    await page.getByLabel('密码', { exact: true }).fill('fixture-password');
    await page.getByRole('button', { name: '登录账户', exact: true }).click();
    await page.waitForURL(`${origin}/imports?subscription=fixture-sub-b`);
    await value(tokens.get('fixture-sub-b'));
    assert.deepEqual(errors, []);
    assert.deepEqual(unexpected, []);
    console.log(JSON.stringify({ passed: true, clients: cases.length, checks: ['default-combined', 'automatic-prepare', 'combined-rotation', 'qq-copy', 'navigation', 'formats', 'schemes', 'download', 'subscription-selection', 'history', 'late-read', 'rotation-race', 'revocation-race', 'slow-response', 'request-errors', 'inactive', 'invalid-selection', 'responsive', 'empty', 'login-return'], screenshots: output }));
  } catch (error) {
    await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true }).catch(() => {});
    throw error;
  } finally {
    if (heldRead) heldRead();
    if (heldWrite) heldWrite();
    await browser.close();
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
