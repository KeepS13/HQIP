// Build with VITE_API_URL=https://api-hqip.sharevps.online. All requests below
// are fulfilled locally; the fixture never contacts production or grants users.
const { chromium } = require('playwright');
const { expect } = require('playwright/test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const output = path.join(root, '.local/subscription-grant-ui');
const CMS = 'https://cms-hqip.sharevps.online', API = 'https://api-hqip.sharevps.online';
const template = {id:'template',name:'测试订阅',description:'独立流量',enabled:true,listed:true,validityDays:30,priceCents:1000,quotaBytes:300e9,bandwidthMbps:null,maxConnections:3,nodesPerSubscription:1,nodeIds:['node-a'],revision:1};
const user = {id:'user',email:'grant@example.invalid',enabled:true,createdAt:'2026-11-15T09:03:00Z',revision:1};
const grants = new Map(), writes = [], errors = [], unexpected = [], checks = [];
let mode = 'lost', failProfile = false;

(async () => {
  fs.mkdirSync(output, {recursive:true});
  const browser = await chromium.launch({headless:true,...(process.env.HQIP_TEST_CHROMIUM_PATH ? {executablePath:process.env.HQIP_TEST_CHROMIUM_PATH} : {})});
  const context = await browser.newContext({viewport:{width:1440,height:1000}});
  const page = await context.newPage();
  page.on('pageerror', error => errors.push(error.message));
  await context.route('**/*', async route => {
    const req = route.request(), url = new URL(req.url()), p = url.pathname;
    if (url.origin === API) {
      const headers = {'access-control-allow-origin':CMS,'access-control-allow-credentials':'true','access-control-allow-headers':'Content-Type,X-CSRF-Token','access-control-allow-methods':'GET,POST,OPTIONS'};
      if (req.method() === 'OPTIONS') return route.fulfill({status:204,headers});
      let status = 200, body;
      if (p === '/api/v1/session') body = {authenticated:true,username:'fixture-admin',csrfToken:'fixture-csrf'};
      else if (p === '/api/v1/alerts/unread') body = {unread:0};
      else if (p === '/api/v1/host-resources') body = {sample:{sampledAt:new Date().toISOString(),cpuPercent:1,memory:{used:1e9,total:4e9},disk:{used:1e10,total:2e10}},refreshing:false};
      else if (p === '/api/v1/subscription-templates') body = [template];
      else if (p === '/api/v1/users/user') {
        if (failProfile) { status=503; body={error:'详情刷新暂不可用'}; }
        else body = {user,subscriptions:[...grants.values()],sessions:[],connections:[]};
      } else if (p === '/api/v1/users/user/subscriptions' && req.method() === 'POST') {
        const input=req.postDataJSON(); writes.push({input,csrf:req.headers()['x-csrf-token']});
        if (mode==='auth') {status=403;body={error:'会话校验未通过'};}
        else {
          body = grants.get(input.requestId);
          if (!body && (input.templateRevision!==template.revision || input.expectedRevision!==user.revision)) {
            status=409;body={code:'SUBSCRIPTION_ASSIGNMENT_REJECTED',error:'资料已变化'};
          } else {
            if (!body) {
              body={id:'sub-'+grants.size,userId:user.id,templateId:template.id,templateName:template.name,createdAt:new Date().toISOString(),expiresAt:'2099-01-01T00:00:00Z',status:'active',usedBytes:0,reservedBytes:0,quotaBytes:template.quotaBytes,bandwidthMbps:null,maxConnections:3,occupied:0,nodeIds:['node-a'],nodesPerSubscription:1,revision:1};
              grants.set(input.requestId,body);user.revision++;
            }
            if (mode==='lost') {status=503;body={code:'SUBSCRIPTION_ASSIGNMENT_UNAVAILABLE',error:'开通结果暂未确认'};}
          }
        }
      } else {unexpected.push(req.method()+' '+p);status=404;body={error:'unexpected fixture request'};}
      return route.fulfill({status,headers,contentType:'application/json',body:JSON.stringify(body)});
    }
    if (url.origin===CMS) {
      const dist=path.join(root,'admin-web/dist');let file=path.resolve(dist,'.'+p);
      assert(file.startsWith(dist+path.sep));
      if (!fs.existsSync(file)||fs.statSync(file).isDirectory()) file=path.join(dist,'index.html');
      return route.fulfill({body:fs.readFileSync(file),contentType:{'.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml'}[path.extname(file)]||'text/html'});
    }
    unexpected.push(req.url());return route.abort();
  });
  const dialog=page.getByRole('dialog'), select=()=>dialog.getByRole('combobox',{name:'订阅模板'});
  const open=async()=>{await page.getByRole('button',{name:'添加订阅',exact:true}).first().click();await select().waitFor();};
  const confirm=()=>dialog.getByRole('button',{name:'确认开通',exact:true});
  const retry=()=>dialog.getByRole('button',{name:'重试本次开通',exact:true});
  const pending=()=>page.evaluate(()=>Object.keys(sessionStorage).filter(key=>key.startsWith('hqip:subscription-grant:')));
  try {
    await page.goto(CMS+'/users/user');await open();await select().selectOption('template');await confirm().click();
    await expect(dialog.getByText(/结果尚未确认/)).toBeVisible();
    assert.equal(writes.length,3);assert.equal(grants.size,1);assert(await select().isDisabled());
    const original=writes[0].input;assert(writes.every(value=>JSON.stringify(value.input)===JSON.stringify(original)));
    await dialog.getByRole('button',{name:'稍后确认'}).click();await open();assert(await select().isDisabled());
    template.revision=2;await page.reload();await open();assert(await select().isDisabled());
    mode='ok';await retry().click();await dialog.waitFor({state:'detached'});
    assert.equal(grants.size,1);assert.deepEqual(writes.at(-1).input,original);assert.deepEqual(await pending(),[]);
    checks.push('committed lost response: bounded retries, close/reopen, page reload, unchanged revisions and exactly one grant');

    await open();await select().selectOption('template');template.revision=3;
    await page.waitForResponse(res=>res.url()===API+'/api/v1/subscription-templates');
    await confirm().click();await dialog.getByRole('button',{name:'重新加载',exact:true}).waitFor();
    assert.equal(writes.at(-1).input.templateRevision,2);assert.equal(grants.size,1);assert.deepEqual(await pending(),[]);
    await dialog.getByRole('button',{name:'重新加载',exact:true}).click();await confirm().waitFor();
    failProfile=true;await confirm().click();await dialog.waitFor({state:'detached'});
    await page.getByText('新的订阅已开通',{exact:true}).waitFor();assert.equal(grants.size,2);assert.deepEqual(await pending(),[]);
    await page.getByText(/详情刷新暂不可用；/).waitFor();failProfile=false;
    checks.push('polling preserves reviewed terms; explicit conflict requires reload and confirmation; refresh failure cannot fail a completed gift');

    await page.reload();await open();await select().selectOption('template');mode='auth';const beforeAuth=writes.length;
    await confirm().click();await expect(dialog.getByText(/会话校验未通过/)).toBeVisible();
    assert.equal(writes.length,beforeAuth+1);assert(await select().isDisabled());assert.equal((await pending()).length,1);
    mode='ok';await retry().click();await dialog.waitFor({state:'detached'});assert.equal(grants.size,3);
    assert.deepEqual(writes.at(-1).input,writes.at(-2).input);checks.push('authentication/CSRF-shaped failure retains the original identity without automatic retries');

    await open();await select().selectOption('template');
    await page.evaluate(()=>{window.originalStorageSet=Storage.prototype.setItem;Storage.prototype.setItem=function(){throw new Error('fixture storage blocked');};});
    const beforeStorage=writes.length;await confirm().click();await expect(dialog.getByText(/尚未提交开通/)).toBeVisible();assert.equal(writes.length,beforeStorage);
    await page.evaluate(()=>{Storage.prototype.setItem=window.originalStorageSet;});checks.push('unavailable durable tab storage blocks sending a new grant');
    for (const [width,height] of [[1440,1000],[390,844],[320,700]]) {
      await page.setViewportSize({width,height});
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      assert.equal(await dialog.evaluate(el=>el.scrollWidth>el.clientWidth+1),false);
      await confirm().scrollIntoViewIfNeeded();await page.screenshot({path:path.join(output,`grant-${width}.png`),animations:'disabled'});
    }
    checks.push('desktop and narrow layout; visible actionable recovery and reachable submit');
    assert(writes.every(write=>write.csrf==='fixture-csrf'));assert.deepEqual(errors,[]);assert.deepEqual(unexpected,[]);
    fs.writeFileSync(path.join(output,'receipt.json'),JSON.stringify({checks,writes,grants:grants.size,errors,unexpected},null,2));
    console.log(JSON.stringify({passed:true,checks}));
  } catch (error) {await page.screenshot({path:path.join(output,'failure.png'),animations:'disabled'});console.error({errors,unexpected,writes});throw error;}
  finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
