// Both built sites use intercepted API fixtures. This test never contacts or
// publishes to the live service; receipts survive reloads in the fixture server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.HQIP_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '../..'), output = path.join(root, '.local/announcements-ui');
const API = 'https://hqip.sharevps.online', CMS = 'https://cms.example.test', USER = 'https://user.example.test';
const users = ['one', 'two', 'three'].map(id => ({ id, email: `${id}@example.com`, enabled: true, revision: 1 }));
let messages = [], receipts = new Set(), failPublish = true, failAck = false, reads = 0;
const writes = [], acknowledgements = [], errors = [], unexpected = [];
const message = (id, mode) => ({ id, title: `公告 ${id}`, body: '公告正文\n第二行 <script>window.injected=true</script>', mode, active: true, createdAt: '2026-11-28T00:00:00Z', userIds: ['one', 'two'], recipientCount: 2 });
(async () => {
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.HQIP_CHROMIUM || undefined });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), p = url.pathname;
    if (url.origin === API) {
      const headers = { 'access-control-allow-origin': request.headers().origin || CMS, 'access-control-allow-credentials': 'true', 'access-control-allow-headers': 'Content-Type,X-CSRF-Token', 'access-control-allow-methods': 'GET,POST,DELETE,OPTIONS' };
      if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers });
      let body, status = 200;
      if (p === '/api/v1/session') body = { authenticated: true, username: 'fixture', csrfToken: 'admin-csrf' };
      else if (p === '/api/v1/alerts/unread') body = { unread: 0 };
      else if (p === '/api/v1/host-resources') body = { sample: { sampledAt: new Date().toISOString(), cpuPercent: 1, memory: { used: 1e9, total: 4e9 }, disk: { used: 1e10, total: 2e10 } }, refreshing: false };
      else if (p === '/api/v1/users') body = users;
      else if (p === '/api/v1/announcements' && request.method() === 'GET') body = { items: messages, nextCursor: '' };
      else if (p === '/api/v1/announcements' && request.method() === 'POST') {
        const input = request.postDataJSON(); writes.push(input);
        assert.equal(request.headers()['x-csrf-token'], 'admin-csrf');
        if (!messages.some(m => m.id === input.id)) messages.unshift({ ...input, active: true, recipientCount: input.userIds.length, createdAt: '2026-11-28T00:00:00Z' });
        if (failPublish) { status = 503; body = { error: '模拟回执丢失' }; failPublish = false; }
        else body = messages.find(m => m.id === input.id);
      } else if (p.startsWith('/api/v1/announcements/')) {
        body = messages.find(m => m.id === p.split('/').at(-1));
        if (request.method() === 'DELETE') { body.active = false; body = { ok: true }; }
      } else if (p === '/api/v1/user/session') body = { authenticated: true, user: users[0], csrfToken: 'user-csrf' };
      else if (p === '/api/v1/user/profile') body = { user: users[0], subscriptions: [], sessions: [] };
      else if (p === '/api/v1/user/config') body = { portalEnabled: true };
      else if (p === '/api/v1/user/installers') body = { releases: [] };
      else if (p === '/api/v1/user/orders') body = { items: [], nextCursor: '' };
      else if (p === '/api/v1/user/announcements') {
        reads++;
        const before = url.searchParams.get('before'), index = before ? messages.findIndex(m => m.id === before) + 1 : 0;
        const visible = messages.slice(index).filter(m => m.active && m.userIds.includes('one') && (m.mode === 'always' || !receipts.has(m.id)));
        body = { items: visible.slice(0, 2), nextCursor: visible.length > 2 ? visible[1].id : '' };
      } else if (p.endsWith('/acknowledge')) {
        assert.equal(request.headers()['x-csrf-token'], 'user-csrf');
        if (failAck) { status = 503; body = { error: '确认失败，请重试' }; }
        else { const id = p.split('/').at(-2); receipts.add(id); acknowledgements.push(id); body = { ok: true }; }
      } else { unexpected.push(request.method() + ' ' + p); body = { error: 'Unexpected route' }; status = 404; }
      return route.fulfill({ status, headers, contentType: 'application/json', body: JSON.stringify(body) });
    }
    if ([CMS, USER].includes(url.origin)) {
      const dist = path.join(root, url.origin === CMS ? 'admin-web/dist' : 'user-web/dist');
      let file = path.resolve(dist, '.' + p);
      if (file !== dist && !file.startsWith(dist + path.sep)) return route.abort();
      if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(dist, 'index.html');
      return route.fulfill({ body: fs.readFileSync(file), contentType: { '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' }[path.extname(file)] || 'text/html' });
    }
    unexpected.push(request.url()); return route.abort();
  });
  const admin = await context.newPage(), user = await context.newPage();
  for (const page of [admin, user]) { page.setDefaultTimeout(10000); page.on('pageerror', e => errors.push(e.message)); }
  try {
    await admin.goto(CMS + '/announcements');
    await admin.getByRole('button', { name: '发布公告', exact: true }).click();
    const form = admin.getByRole('dialog');
    await form.getByLabel('公告标题').fill('本周服务维护通知');
    await form.getByLabel('公告正文').fill('维护期间请留意服务状态。\n预计十分钟内完成。');
    await form.getByLabel('搜索用户').fill('one@');
    await form.getByRole('button', { name: /全选搜索结果/ }).click();
    await form.getByLabel('搜索用户').fill('two@');
    await form.getByRole('checkbox').check();
    await admin.screenshot({ path: path.join(output, 'admin-compose.png'), fullPage: true });
    await form.getByRole('button', { name: '发布给 2 位用户' }).click();
    await form.getByText('模拟回执丢失').waitFor();
    assert.equal(await form.getByLabel('公告标题').isDisabled(), true);
    await form.getByRole('button', { name: '取消', exact: true }).click();
    await admin.getByRole('button', { name: '发布公告', exact: true }).click();
    await form.getByRole('button', { name: '重试本次发布' }).click();
    await form.waitFor({ state: 'hidden' });
    assert.deepEqual(writes[0], writes[1]); assert.equal(messages.length, 1);
    assert.deepEqual(writes[0].userIds.sort(), ['one', 'two']);
    await admin.getByRole('button', { name: '停用', exact: true }).click();
    await admin.getByRole('button', { name: '确认停用' }).click();
    await admin.getByText('已停用', { exact: true }).waitFor();
    await admin.screenshot({ path: path.join(output, 'admin-list.png'), fullPage: true });

    messages = [message('first', 'until_acknowledged'), message('repeat', 'always'), message('last', 'until_acknowledged')];
    await user.goto(USER + '/downloads');
    const dialog = user.getByRole('dialog');
    await dialog.getByRole('heading', { name: '公告 first', exact: true }).waitFor();
    assert.equal(await user.evaluate(() => window.injected), undefined);
    for (const [name, width, height] of [['desktop', 1440, 1000], ['mobile', 375, 812], ['landscape', 812, 375]]) {
      await user.setViewportSize({ width, height });
      assert.equal(await user.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await user.screenshot({ path: path.join(output, 'user-' + name + '.png'), fullPage: true });
    }
    await dialog.getByRole('button', { name: '下一条', exact: true }).click();
    await dialog.getByRole('heading', { name: '公告 repeat', exact: true }).waitFor();
    assert.equal(await dialog.getByRole('button', { name: '我已知晓' }).count(), 0);
    await dialog.getByRole('button', { name: '下一条', exact: true }).click();
    await dialog.getByRole('heading', { name: '公告 last', exact: true }).waitFor();
    assert.deepEqual(acknowledgements, []);
    await dialog.getByRole('button', { name: '关闭对话框' }).click();
    await user.reload();
    await dialog.getByRole('heading', { name: '公告 first', exact: true }).waitFor();
    failAck = true; await dialog.getByRole('button', { name: '我已知晓' }).click();
    await dialog.getByText('确认失败，请重试').waitFor();
    assert.equal(await dialog.getByRole('heading', { name: '公告 first' }).count(), 1);
    failAck = false; await dialog.getByRole('button', { name: '我已知晓' }).click();
    await dialog.getByRole('heading', { name: '公告 repeat', exact: true }).waitFor();
    await user.keyboard.press('Escape');
    await user.reload();
    await dialog.getByRole('heading', { name: '公告 repeat', exact: true }).waitFor();
    await dialog.getByRole('button', { name: '下一条', exact: true }).click();
    await dialog.getByRole('button', { name: '我已知晓' }).click();
    await dialog.waitFor({ state: 'hidden' });
    const before = reads;
    await user.setViewportSize({ width: 1440, height: 1000 });
    await user.getByRole('link', { name: '我的订单', exact: true }).click();
    assert.equal(reads, before, 'route navigation must not reload announcements');
    await user.reload();
    await dialog.getByRole('heading', { name: '公告 repeat', exact: true }).waitFor();
    assert.deepEqual(acknowledgements, ['first', 'last']);
    await user.keyboard.press('Tab');
    assert.equal(await dialog.evaluate(el => el.contains(document.activeElement)), true, 'focus stays in dialog');
    assert.deepEqual(errors, []); assert.deepEqual(unexpected, []);
    console.log(JSON.stringify({ passed: true, writes: writes.length, acknowledgements, screenshots: output }));
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
