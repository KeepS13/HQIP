// Exercise the built admin UI with intercepted requests only; no live accounts.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.HQIP_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '../..');
const dist = path.join(root, 'admin-web/dist');
const output = path.join(root, '.local/subscription-apply-ui');
const CMS = 'https://cms.example.test', API = 'https://hqip.sharevps.online';
const template = { id: 'template', name: '美国 · 原生 IP', enabled: true, listed: true, validityDays: 30, priceCents: 1000, quotaBytes: 300e9, bandwidthMbps: 100, maxConnections: 3, nodesPerSubscription: 2, nodeIds: ['a','b'], revision: 7 };
const nodes = ['a','b'].map(id => ({ id, name:'节点 '+id, publicIp:'203.0.113.1', domain:id+'.example.test', status:'ready', lifecycle:'active', capabilities:['admission-v1'] }));
const writes = [], errors = [], unexpected = [];
let mode = 'success', release = null;
(async () => {
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.HQIP_CHROMIUM || undefined });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', e => errors.push(e.message));
  await context.route('**/*', async route => {
    const req = route.request(), u = new URL(req.url()), p = u.pathname;
    if (u.origin === API) {
      const headers = { 'access-control-allow-origin': CMS, 'access-control-allow-credentials': 'true', 'access-control-allow-headers': 'Content-Type,X-CSRF-Token', 'access-control-allow-methods': 'GET,POST,OPTIONS' };
      if (req.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      let body, status = 200;
      if (p === '/api/v1/session') body = { authenticated: true, username: 'fixture-admin', csrfToken: 'fixture-csrf' };
      else if (p === '/api/v1/alerts/unread') body = { unread: 0 };
      else if (p === '/api/v1/host-resources') body = { sample: { sampledAt: new Date().toISOString(), cpuPercent: 1, memory: { used: 1e9, total: 4e9 }, disk: { used: 1e10, total: 2e10 } }, refreshing: false };
      else if (p === '/api/v1/subscription-templates') body = [template];
      else if (p === '/api/v1/nodes') body = nodes;
      else if (p === '/api/v1/subscription-templates/template/preview') body = { affectedUsers: 12, affectedSubscriptions: 12, activeConnections: 0, connectionsToClose: [], allocationShortfalls: [] };
      else if (p === '/api/v1/subscription-templates/template' && req.method() === 'PUT') {
        const input = req.postDataJSON();
        assert.equal(input.expectedRevision, template.revision);
        Object.assign(template, input, { revision:template.revision+1 }); body = template;
      }
      else if (p === '/api/v1/subscription-templates/template/apply' && req.method() === 'POST') {
        writes.push({ body: req.postDataJSON(), csrf: req.headers()['x-csrf-token'] });
        if (mode === 'hold') await new Promise(resolve => { release = resolve; });
        if (mode === 'error') { status = 409; body = { error: '模板版本已变化' }; }
        else {
          template.appliedRevision = template.revision;
          body = { affectedSubscriptions: 12, updatedSubscriptions: 9, allocationShortfalls: mode === 'shortfall' ? [{ userId: 'user', subscriptionId: 'sub-missing', assigned: 1, required: 2 }] : [] };
        }
      } else { unexpected.push(req.method()+' '+p); status = 404; body = { error: 'Unexpected route' }; }
      return route.fulfill({ status, headers, contentType: 'application/json', body: JSON.stringify(body) });
    }
    if (u.origin === CMS) {
      let file = path.resolve(dist, '.'+p);
      if (file !== dist && !file.startsWith(dist+path.sep)) return route.abort();
      if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(dist,'index.html');
      return route.fulfill({ body: fs.readFileSync(file), contentType: { '.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml' }[path.extname(file)] || 'text/html' });
    }
    unexpected.push(req.url()); return route.abort();
  });
  try {
    await page.goto(CMS+'/subscriptions');
    const apply = page.getByRole('button',{ name:/^立刻应用/ });
    const card = page.locator('.account-plan-card');
    const confirm = page.getByRole('button',{ name:'确认应用', exact:true });
    assert.match(await apply.getAttribute('class'), /button-apply-pending/);
    assert.match(await apply.innerText(), /待应用/);
    assert.equal(await card.locator('small, .notice, [role="status"]').count(), 0);
    for (const [name,width,height] of [['desktop',1440,1000],['mobile',375,812],['landscape',812,375]]) {
      await page.setViewportSize({ width,height });
      await apply.click();
      await page.getByText('当前版本尚无立刻应用记录。',{exact:false}).waitFor();
      assert.equal(writes.length,0,'opening confirmation must not apply');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, name+' overflow');
      await page.screenshot({ path:path.join(output,name+'-confirm.png'), fullPage:true, animations:'disabled' });
      await page.keyboard.press('Escape');
      assert.equal(await apply.evaluate(el => el === document.activeElement), true);
      await page.screenshot({ path:path.join(output,name+'.png'), fullPage:true, animations:'disabled' });
    }
    await page.setViewportSize({width:1440,height:1000});
    const initialHeight = (await card.boundingBox()).height;
    mode = 'hold';
    await apply.click();
    await confirm.click();
    for (let i=0; !release && i<100; i++) await page.waitForTimeout(20);
    assert.ok(release, 'apply request not sent');
    assert.equal(await confirm.isDisabled(), true, 'in-flight request must disable duplicate submission');
    await page.keyboard.press('Escape');
    assert.equal(await page.getByRole('dialog').count(),1,'busy dialog must remain open');
    mode = 'success'; release(); release = null;
    await page.getByText('已应用到 12 份订阅', { exact:false }).waitFor();
    await page.getByRole('dialog').waitFor({state:'hidden'});
    assert.deepEqual(writes[0], { body:{ expectedRevision:7 }, csrf:'fixture-csrf' });
    assert.doesNotMatch(await apply.getAttribute('class'), /button-apply-pending/);
    assert.equal((await card.boundingBox()).height, initialHeight, 'success must not grow the card');
    assert.equal(await card.locator('small, .notice, [role="status"]').count(), 0);
    await page.reload();
    await apply.waitFor();
    assert.doesNotMatch(await apply.getAttribute('class'), /button-apply-pending/);
    template.revision++;
    await page.reload();
    await apply.waitFor();
    assert.match(await apply.getAttribute('class'), /button-apply-pending/);
    mode = 'error'; await apply.click(); await confirm.click();
    await page.getByText('模板版本已变化', { exact:false }).waitFor();
    assert.match(await apply.getAttribute('class'), /button-apply-pending/);
    await page.getByRole('button',{name:'取消',exact:true}).click();
    mode = 'shortfall'; await apply.click(); await confirm.click();
    await page.getByText('其中 1 份节点不足', { exact:false }).waitFor();
    await page.getByRole('dialog').waitFor({state:'hidden'});
    assert.equal(await card.locator('.notice, [role="status"]').count(), 0);
    assert.equal(writes.length,3);
    // The split editor retains its draft through card preview, node filtering,
    // impact preview and save. Apply must subsequently use the new revision.
    await page.getByRole('button',{ name:'编辑订阅',exact:true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('每份分配节点数',{exact:false}).fill('10');
    await dialog.getByLabel('搜索节点',{exact:true}).fill('b');
    await dialog.locator('.subscription-node-options label').filter({hasText:'节点 b'}).getByRole('checkbox').uncheck();
    await dialog.getByText('配置数量大于已选节点数，每份将分配全部 1 个已选节点。',{exact:true}).waitFor();
    assert.equal(await dialog.getByText('保存后将显示无货。',{exact:false}).count(),0);
    await dialog.getByRole('button',{ name:'预览订阅',exact:true }).click();
    await dialog.getByRole('heading',{ name:'订阅预览',exact:true }).waitFor();
    assert.equal(await dialog.locator('.subscription-facts div').filter({hasText:'每份分配节点'}).locator('dd').innerText(),'1 个');
    await dialog.getByRole('button',{ name:'返回编辑',exact:true }).click();
    assert.equal(await dialog.getByLabel('每份分配节点数',{exact:false}).inputValue(),'10');
    await dialog.getByRole('button',{ name:'预览影响',exact:true }).click();
    await dialog.getByRole('button',{ name:'确认保存',exact:true }).click();
    await dialog.waitFor({state:'hidden'});
    assert.match(await apply.getAttribute('class'), /button-apply-pending/);
    mode='success'; await apply.click(); await confirm.click();
    await page.getByText('已应用到 12 份订阅',{exact:false}).waitFor();
    assert.deepEqual(writes.at(-1).body,{expectedRevision:9});
    assert.deepEqual(template.nodeIds,['a']);
    assert.equal(template.nodesPerSubscription,10);
    await page.getByText('1 个（配置上限 10 个） / 候选池 1 个',{exact:true}).waitFor();
    assert.deepEqual(errors,[]); assert.deepEqual(unexpected,[]);
    console.log(JSON.stringify({ passed:true, checks:['pending-color','persistent-acknowledgement','edit-rehighlights','confirmation-cancel','focus-return','saved-revision','csrf','busy','toast-without-layout-change','conflict','shortfall-toast','responsive'], screenshots:output }));
  } finally { if (release) release(); await browser.close(); }
})().catch(e => { console.error(e); process.exitCode=1; });
