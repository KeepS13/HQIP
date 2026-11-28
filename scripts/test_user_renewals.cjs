// Run after building user-web. All requests are intercepted; these fixtures
// cannot create a real order or reach production. Reuse a local Playwright via
// HQIP_PLAYWRIGHT and optionally choose a browser with HQIP_CHROMIUM.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.HQIP_PLAYWRIGHT || 'playwright');
const { expect } = require(process.env.HQIP_PLAYWRIGHT ? path.join(process.env.HQIP_PLAYWRIGHT, 'test') : 'playwright/test');

const root = path.resolve(__dirname, '..');
const assets = path.join(root, 'user-web', 'dist');
const output = path.join(root, '.local', 'renewal-entry-qa');
const origin = 'https://hqip.example.test';
const apiOrigin = 'https://hqip.sharevps.online';
const user = { id: 'renewal-user', email: 'renewal@example.test', enabled: true, revision: 1 };
const template = {
  id: 'shared-template', name: '同名多节点套餐', enabled: true, listed: true, priceCents: 1200,
  validityDays: 30, maxConnections: 3, quotaBytes: 50e9, bandwidthMbps: 100,
  nodeIds: ['original-a', 'original-b', 'candidate-only'], nodesPerSubscription: 2,
  revision: 5, availableCopies: 0, availabilityReason: '没有剩余新购库存', description: '',
};
const subscriptions = ['a', 'b', 'exhausted', 'unpriced', 'expired', 'removed'].map((name, i) => ({
  id: `subscription-${name}`, userId: user.id, templateId: template.id, templateName: template.name,
  status: name === 'exhausted' ? 'exhausted' : name === 'expired' ? 'expired' : name === 'removed' ? 'revoked' : 'active',
  renewable: !['unpriced', 'expired', 'removed'].includes(name), revision: 11 + i,
  createdAt: '2026-11-01T00:00:00Z', expiresAt: '2099-01-01T00:00:00Z',
  currentPeriodId: `period-${name}`, currentPeriodStartsAt: '2026-11-01T00:00:00Z',
  currentPeriodEndsAt: '2099-01-01T00:00:00Z', periods: [],
  nodeIds: [name === 'b' ? 'original-b' : 'original-a'], nodesPerSubscription: 1,
  quotaBytes: 50e9, usedBytes: name === 'exhausted' ? 50e9 : 2e9, reservedBytes: 0,
  maxConnections: 3, occupied: 3, bandwidthMbps: 100,
}));
const orders = new Map(), writes = [], unexpected = [], errors = [];
let holdWrite, releaseWrite, failWrite = false;
let paymentEnabled = true, catalogReads = 0;

async function main() {
  assert.ok(fs.existsSync(path.join(assets, 'index.html')), 'Build user-web first');
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.HQIP_CHROMIUM || undefined });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1100 }, reducedMotion: 'reduce' });
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    await context.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin === apiOrigin) {
        const headers = { 'access-control-allow-origin': origin, 'access-control-allow-credentials': 'true', 'access-control-allow-headers': 'Content-Type,X-CSRF-Token', 'access-control-allow-methods': 'GET,POST,OPTIONS' };
        if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
        const p = url.pathname;
        let body, status = 200;
        if (p === '/api/v1/user/session') body = { authenticated: true, user, csrfToken: 'fixture-csrf' };
        else if (p === '/api/v1/user/announcements' && request.method() === 'GET') body = { items: [], nextCursor: '' };
        else if (p === '/api/v1/user/config') body = { portalEnabled: true, registrationEnabled: true };
        else if (p === '/api/v1/user/profile') body = { user, subscriptions, sessions: [] };
        else if (p === '/api/v1/user/subscriptions') body = subscriptions;
        else if (p === '/api/v1/user/catalog') { catalogReads++; body = template.listed ? [template] : []; }
        else if (p === '/api/v1/user/payment-info') body = { enabled: paymentEnabled, payType: 'aloop', payDurationMinutes: 10 };
        else if (p.startsWith('/api/v1/user/subscriptions/')) {
          const subscription = subscriptions.find(s => s.id === decodeURIComponent(p.split('/').pop()));
          assert.ok(subscription, 'unknown subscription fixture');
          body = { subscription, renewalTemplate: template, nodes: subscription.nodeIds.map(id => ({ id, name: id === 'original-b' ? '原节点 B' : '原节点 A', ready: true })), subscriptionUrl: '' };
        } else if (p === '/api/v1/user/orders' && request.method() === 'POST') {
          const input = request.postDataJSON();
          writes.push(input);
          assert.equal(request.headers()['x-csrf-token'], 'fixture-csrf');
          if (holdWrite) {
            holdWrite = false;
            await new Promise(resolve => { releaseWrite = resolve; });
          }
          if (failWrite) { failWrite = false; status = 503; body = { error: '测试：订单结果待核实' }; }
          else {
            body = { id: `order-${input.renewalSubscriptionId}`, orderType: 'renewal', renewalSubscriptionId: input.renewalSubscriptionId,
              orderNo: 'fixture-order', templateId: template.id, templateName: template.name, amountCents: 1200,
              currency: 'CNY', validityDays: 30, createdAt: new Date().toISOString(), expiresAt: '2099-01-01T00:00:00Z',
              status: 'pending', creationState: 'created', paymentState: 'unpaid', fulfillmentState: 'pending', revision: 1, lastError: '' };
            orders.set(body.id, body);
          }
        } else if (p === '/api/v1/user/orders') body = { items: [...orders.values()], total: orders.size, page: 1, pageSize: 20 };
        else if (p.startsWith('/api/v1/user/orders/')) body = orders.get(p.split('/').pop());
        else { unexpected.push(`${request.method()} ${p}`); status = 404; body = { error: 'Unexpected fixture route' }; }
        return route.fulfill({ status, headers, contentType: 'application/json', body: JSON.stringify(body) });
      }
      if (url.origin === origin) {
        let file = path.resolve(assets, '.' + url.pathname);
        if (file !== assets && !file.startsWith(assets + path.sep)) return route.abort();
        if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(assets, 'index.html');
        const mime = { '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' }[path.extname(file)] || 'text/html';
        return route.fulfill({ contentType: mime, body: fs.readFileSync(file) });
      }
      unexpected.push(request.url());
      return route.abort();
    });
    const renew = page.getByRole('button', { name: '续费订阅', exact: true });
    const dialog = page.getByRole('dialog');
    const card = name => page.locator('.user-sub-card').filter({ has: page.locator(`a[href="/subscriptions/subscription-${name}"]`) });
    await page.goto(origin);
    await expect(renew).toHaveCount(3);
    await expect(card('exhausted')).toBeVisible();
    await expect(card('unpriced').getByRole('button', { name: '续费订阅' })).toHaveCount(0);
    await expect(card('expired')).toHaveCount(0);
    await expect(card('a')).toContainText('3 / 3');
    const listedButton = await card('b').getByRole('button', { name: '续费订阅' }).evaluate(button => {
      const style = getComputedStyle(button), rect = button.getBoundingClientRect();
      return { className: button.className, color: style.color, background: style.backgroundColor, width: rect.width, height: rect.height };
    });
    // The catalog really omits hidden plans. Renewal must use owned details,
    // retaining the same primary button and layout after a visibility change.
    template.listed = false;
    await page.reload();
    await expect(renew).toHaveCount(3);
    const hiddenButton = await card('b').getByRole('button', { name: '续费订阅' }).evaluate(button => {
      const style = getComputedStyle(button), rect = button.getBoundingClientRect();
      return { className: button.className, color: style.color, background: style.backgroundColor, width: rect.width, height: rect.height };
    });
    assert.deepEqual(hiddenButton, listedButton, 'shop visibility changed renewal button styling');
    await page.screenshot({ path: path.join(output, 'overview-desktop.png'), fullPage: true });
    await card('b').getByRole('button', { name: '续费订阅' }).click();
    await expect(dialog.getByRole('button', { name: '创建续费订单' })).toBeEnabled();
    assert.equal(catalogReads, 0, 'renewal depends on the public shop catalog');
    await expect(dialog).toContainText('原节点 B');
    await expect(dialog).toContainText('付款核验时仍未到期的原节点会保留');
    await expect(dialog).toContainText('定制套餐由管理员更换');
    await expect(dialog).not.toContainText('原节点 A');
    await expect(dialog).not.toContainText('candidate-only');
    await expect(dialog).toContainText('3 / 3');
    await page.screenshot({ path: path.join(output, 'confirm-desktop.png'), fullPage: true });
    // Two immediate clicks during a slow response must submit exactly once.
    holdWrite = true;
    await dialog.getByRole('button', { name: '创建续费订单' }).evaluate(button => { button.click(); button.click(); });
    await expect.poll(() => writes.length).toBe(1);
    await expect(dialog.getByRole('button', { name: /创建续费订单|重试确认续费订单/ })).toBeDisabled();
    assert.equal(writes[0].renewalSubscriptionId, 'subscription-b');
    assert.equal(writes[0].expectedSubscriptionRevision, 12);
    assert.equal(writes[0].templateRevision, 5);
    assert.deepEqual(Object.keys(writes[0]).sort(), ['expectedSubscriptionRevision', 'renewalSubscriptionId', 'requestId', 'templateId', 'templateRevision']);
    releaseWrite();
    await expect(page).toHaveURL(`${origin}/orders/order-subscription-b`);
    await expect(renew).toHaveCount(0);
    for (const route of ['/subscriptions', '/subscriptions/subscription-b', '/catalog', '/orders']) {
      await page.goto(origin + route);
      await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible();
      await expect(renew).toHaveCount(0);
    }
    // Exhausted but unexpired subscriptions retain the only entry. Retry an
    // uncertain submission with identical parameters even after a price edit.
    await page.goto(origin);
    await card('exhausted').getByRole('button', { name: '续费订阅' }).click();
    failWrite = true;
    await dialog.getByRole('button', { name: '创建续费订单' }).click();
    await expect(dialog).toContainText('测试：订单结果待核实');
    const original = structuredClone(writes.at(-1));
    template.revision = 6;
    template.priceCents = 1800;
    subscriptions[2].revision++;
    await dialog.getByRole('button', { name: '返回', exact: true }).click();
    await card('exhausted').getByRole('button', { name: '续费订阅' }).click();
    await expect(dialog).toContainText('12.00');
    await dialog.getByRole('button', { name: '重试确认续费订单' }).click();
    await expect(page).toHaveURL(`${origin}/orders/order-subscription-exhausted`);
    assert.deepEqual(writes.at(-1), original);
    // Mobile keyboard navigation and overflow are checked on real rendered UI.
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(origin);
    const mobileButton = card('a').getByRole('button', { name: '续费订阅' });
    await mobileButton.focus();
    await page.keyboard.press('Enter');
    await expect(dialog.getByRole('button', { name: '创建续费订单' })).toBeEnabled();
    await page.screenshot({ path: path.join(output, 'confirm-mobile.png') });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'mobile page overflow');
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await expect(mobileButton).toBeFocused();
    paymentEnabled = false;
    await mobileButton.click();
    await expect(dialog.getByRole('button', { name: '创建续费订单' })).toBeDisabled();
    await expect(dialog).toContainText('当前暂不可新建续费订单');
    assert.equal(writes.length, 3);
    assert.deepEqual(unexpected, []);
    assert.deepEqual(errors, []);
    console.log('PASS: overview-only renewal, correct instance/node/slots, exhausted eligibility, duplicate guard, immutable retry, mobile and payment-disabled state');
  } finally {
    if (releaseWrite) releaseWrite();
    await browser.close();
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
