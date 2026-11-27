// Built user UI regression. All network requests are intercepted; no live orders.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.HQIP_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '..'), dist = path.join(root, 'user-web/dist');
const output = path.join(root, '.local/subscription-node-cap-ui');
const origin = 'https://hqip.example.test', api = 'https://hqip.sharevps.online';
const user = { id: 'fixture', email: 'fixture@example.test', enabled: true, revision: 1 };
const base = { enabled: true, listed: true, revision: 1, validityDays: 30, priceCents: 1000, quotaBytes: null, bandwidthMbps: null, maxConnections: 3, description: '', availabilityReason: '', availableCopies: 1 };
const plans = [
  { ...base, id: 'capped', name: '全部选中节点', nodesPerSubscription: 10, nodeIds: ['a','b','c'] },
  { ...base, id: 'subset', name: '按数量分配', nodesPerSubscription: 2, nodeIds: ['a','b','c'] },
  { ...base, id: 'manual', name: '人工开通', nodesPerSubscription: 10, nodeIds: [], manualProvisioning: true },
];
(async () => {
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.HQIP_CHROMIUM || undefined });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage(), errors = [], unexpected = [];
  page.setDefaultTimeout(10000);
  page.on('pageerror', e => errors.push(e.message));
  await context.route('**/*', async route => {
    const req = route.request(), url = new URL(req.url());
    if (url.origin === api) {
      const headers = { 'access-control-allow-origin': origin, 'access-control-allow-credentials': 'true', 'access-control-allow-headers': 'Content-Type,X-CSRF-Token', 'access-control-allow-methods': 'GET,OPTIONS' };
      if (req.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      const bodies = {
        '/api/v1/user/session': { authenticated: true, user, csrfToken: 'fixture-csrf' },
        '/api/v1/user/config': { portalEnabled: true, registrationEnabled: true },
        '/api/v1/user/profile': { user, subscriptions: [], sessions: [] },
        '/api/v1/user/catalog': plans,
        '/api/v1/user/payment-info': { enabled: true, payType: 'aloop', payDurationMinutes: 10 },
      };
      if (req.method() !== 'GET' || !bodies[url.pathname]) { unexpected.push(req.method()+' '+url.pathname); return route.abort(); }
      return route.fulfill({ headers, contentType: 'application/json', body: JSON.stringify(bodies[url.pathname]) });
    }
    if (url.origin === origin) {
      let file = path.resolve(dist, '.'+url.pathname);
      if (file !== dist && !file.startsWith(dist+path.sep)) return route.abort();
      if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(dist,'index.html');
      return route.fulfill({ body: fs.readFileSync(file), contentType: { '.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml' }[path.extname(file)] || 'text/html' });
    }
    unexpected.push(req.url()); return route.abort();
  });
  try {
    await page.goto(origin+'/catalog');
    for (const [name, count] of [['全部选中节点',3],['按数量分配',2],['人工开通',10]]) {
      const card = page.locator('article').filter({ has: page.getByRole('heading',{name,exact:true}) });
      const fact = card.locator('.user-sub-facts div').filter({hasText:'每份分配节点'}).locator('dd');
      await fact.waitFor();
      assert.equal(await fact.innerText(), count+' 个');
      await card.getByRole('button',{name:'购买订阅',exact:true}).click();
      const dialog = page.getByRole('dialog');
      assert.equal(await dialog.locator('.user-sub-facts div').filter({hasText:'每份分配节点'}).locator('dd').innerText(), count+' 个');
      await dialog.getByRole('button',{name:'返回商城',exact:true}).click();
    }
    for (const [name,width,height] of [['desktop',1440,1000],['mobile',375,812]]) {
      await page.setViewportSize({width,height});
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),false);
      await page.screenshot({path:path.join(output,name+'.png'),fullPage:true});
    }
    assert.deepEqual(errors,[]); assert.deepEqual(unexpected,[]);
    console.log(JSON.stringify({passed:true,checks:['capped-catalog','purchase-confirmation','subset','manual','responsive'],screenshots:output}));
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode=1; });
