// Build both web apps first. All network requests are intercepted with local
// fixtures: this test cannot create purchases or mutate a deployed server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.HQIP_PLAYWRIGHT || 'playwright');
const {expect} = require(process.env.HQIP_PLAYWRIGHT ? path.join(process.env.HQIP_PLAYWRIGHT, 'test') : 'playwright/test');
const root = path.resolve(__dirname, '..');
const output = path.join(root, '.local/custom-activation-ui');
const user = {id:'custom-user',email:'custom@example.test',enabled:true,revision:1};
let template = {id:'custom-template',name:'定制专用订阅',description:'等待分配节点',enabled:true,listed:true,validityDays:30,priceCents:1000,quotaBytes:null,bandwidthMbps:null,maxConnections:2,nodeIds:[],nodesPerSubscription:1,revision:1,manualProvisioning:true,availableCopies:0};
let sub = {id:'custom-sub',userId:user.id,templateId:template.id,templateName:template.name,status:'pending',activationPending:true,validityDays:30,renewable:false,revision:2,createdAt:'2026-11-01T00:00:00Z',expiresAt:'0001-01-01T00:00:00Z',currentPeriodEndsAt:'0001-01-01T00:00:00Z',periods:[],nodeIds:[],nodesPerSubscription:1,maxConnections:2,occupied:0,usedBytes:0,reservedBytes:0,quotaBytes:null,bandwidthMbps:null};
let order = {id:'custom-order',orderNo:'CUSTOM-TEST',orderType:'purchase',templateId:template.id,templateName:template.name,amountCents:1000,currency:'CNY',validityDays:30,createdAt:new Date().toISOString(),expiresAt:'2099-01-01T00:00:00Z',status:'pending',paymentState:'unpaid',creationState:'ready',fulfillmentState:'pending',revision:1,lastError:''};
const writes=[],unexpected=[],errors=[];
const importURL='https://api.example.test/usub/custom-test-capability';
let link='', hostStarted=0, hostCalls=0, hostMode='normal';

async function routeFixtures(context, app, origin) {
  await context.route('**/*', async route => {
    const request=route.request(), u=new URL(request.url()), p=u.pathname, method=request.method();
    if(p.startsWith('/api/v1/')) {
      const headers={'access-control-allow-origin':origin,'access-control-allow-credentials':'true','access-control-allow-headers':'Content-Type,X-CSRF-Token','access-control-allow-methods':'GET,POST,PATCH,OPTIONS'};
      if(method==='OPTIONS') return route.fulfill({status:204,headers});
      let body,status=200;
      if(p==='/api/v1/user/config') body={portalEnabled:true,registrationEnabled:true};
      else if(p==='/api/v1/user/session') body={authenticated:true,user,csrfToken:'fixture-csrf'};
      else if(p==='/api/v1/user/profile') body={user,subscriptions:[sub],sessions:[]};
      else if(p==='/api/v1/user/subscriptions') body=[sub];
      else if(p==='/api/v1/user/subscriptions/custom-sub') body={subscription:sub,nodes:[],subscriptionUrl:link};
      else if(p.endsWith('/import-link')) { if(method==='POST') link=importURL; body={url:link}; }
      else if(p==='/api/v1/user/catalog') body=[template];
      else if(p==='/api/v1/user/payment-info') body={enabled:true,payDurationMinutes:10,payType:'aloop'};
      else if(p==='/api/v1/user/orders' && method==='POST') {writes.push(request.postDataJSON());body=order;}
      else if(p==='/api/v1/user/orders/custom-order') body=order;
      else if(p==='/api/v1/user/orders') body={items:[order],total:1,page:1,pageSize:20};
      else if(p==='/api/v1/session') body={authenticated:true,username:'fixture-admin',csrfToken:'fixture-csrf'};
      else if(p==='/api/v1/alerts/unread') body={unread:0};
      else if(p==='/api/v1/host-resources') {
        hostCalls++;hostStarted ||= Date.now();
        const warming=Date.now()-hostStarted<900;
        if(hostMode==='failure') {status=503;body={error:'fixture unavailable'};}
        else body={sample:{sampledAt:new Date().toISOString(),cpuPercent:warming?null:hostMode==='idle'?0:42,memory:{used:1e9,total:4e9},disk:{used:1e10,total:2e10}},refreshing:warming};
      }
      else if(p==='/api/v1/nodes') body=[];
      else if(p==='/api/v1/subscription-templates') {
        if(method==='POST') {const input=request.postDataJSON();writes.push(input);assert.deepEqual(input.nodeIds,[]);template={...template,...input};body=template;} else body=[template];
      }
      else if(p==='/api/v1/users/custom-user') body={user,subscriptions:[sub],sessions:[],connections:[]};
      else if(p==='/api/v1/users/custom-user/subscriptions/custom-sub/nodes') {
        if(method==='GET') body={subscription:sub,nodes:[{id:'custom-node',name:'定制原节点',publicIp:'203.0.113.1',domain:'node.example.test',inTemplate:false,selectable:true,selected:false,reason:''}]};
        else {
          const input=request.postDataJSON();writes.push(input);assert.equal(request.headers()['x-csrf-token'],'fixture-csrf');
          assert.deepEqual(input.nodeIds,['custom-node']);assert.equal(input.expectedRevision,sub.revision);
          sub={...sub,activationPending:false,status:'active',nodeIds:input.nodeIds,activatedAt:new Date().toISOString(),expiresAt:'2099-01-01T00:00:00Z',currentPeriodEndsAt:'2099-01-01T00:00:00Z',revision:sub.revision+1,renewable:true};body=sub;
        }
      } else {unexpected.push(method+' '+p);status=404;body={error:'unexpected fixture request'};}
      return route.fulfill({status,headers,contentType:'application/json',body:JSON.stringify(body)});
    }
    if(u.origin===origin) {
      const dist=path.join(root,app,'dist');let file=path.resolve(dist,'.'+p);
      assert(file===dist||file.startsWith(dist+path.sep));
      if(!fs.existsSync(file)||fs.statSync(file).isDirectory()) file=path.join(dist,'index.html');
      return route.fulfill({body:fs.readFileSync(file),contentType:{'.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml'}[path.extname(file)]||'text/html'});
    }
    unexpected.push(request.url());return route.abort();
  });
}

async function main() {
  fs.mkdirSync(output,{recursive:true});
  const browser=await chromium.launch({headless:true,executablePath:process.env.HQIP_CHROMIUM||undefined});
  try {
    const userContext=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'});
    const userOrigin='https://user.example.test';await routeFixtures(userContext,'user-web',userOrigin);
    const page=await userContext.newPage();page.on('pageerror',e=>errors.push(e.message));
    await page.goto(userOrigin+'/');await page.getByRole('heading',{name:'待开通的订阅'}).waitFor();
    assert.equal(await page.getByRole('button',{name:'续费订阅',exact:true}).count(),0);
    await expect(page.locator('.user-sub-card')).toContainText('开通后 30 天');
    await page.goto(userOrigin+'/imports?subscription=custom-sub');
    await expect(page.getByLabel('此订阅的导入链接')).toHaveValue(importURL);
    await expect(page.getByRole('button',{name:'复制链接',exact:true})).toBeEnabled();
    await expect(page.getByText('订阅链接已可使用，当前配置不含节点。管理员分配节点并正式开通后开始计时，届时刷新同一链接即可获取节点。')).toBeVisible();
    await page.goto(userOrigin+'/catalog');await page.getByRole('button',{name:'购买订阅',exact:true}).click();
    await expect(page.getByRole('dialog')).toContainText('等待期间不扣天数');
    await page.getByRole('button',{name:'创建订单',exact:true}).click();await page.waitForURL('**/orders/custom-order');
    assert.equal(writes.length,1);assert.equal(writes[0].templateId,template.id);
    order={...order,status:'fulfilled',paymentState:'paid',fulfillmentState:'fulfilled',subscriptionId:sub.id};
    await page.reload();await page.getByRole('heading',{name:'你的新订阅已生成'}).waitFor();
    for(const width of [1440,375]) {
      await page.setViewportSize({width,height:900});await page.goto(userOrigin+'/');
      await page.getByRole('heading',{name:'待开通的订阅'}).waitFor();
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await page.screenshot({path:path.join(output,`pending-${width}.png`),fullPage:true,animations:'disabled'});
    }
    const adminContext=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'});
    const adminOrigin='https://admin.example.test';await routeFixtures(adminContext,'admin-web',adminOrigin);
    const admin=await adminContext.newPage();admin.on('pageerror',e=>errors.push(e.message));
    await admin.goto(adminOrigin+'/users/custom-user');
    const host=admin.getByRole('region',{name:'控制台主机资源'});
    await expect(host.getByText('42.0%',{exact:true})).toBeVisible({timeout:3000});
    assert(hostCalls>=2 && hostCalls<=8,'warmup must follow result promptly with bounded requests');
    await admin.getByRole('button',{name:'分配节点并开通',exact:true}).click();
    const dialog=admin.getByRole('dialog'),save=dialog.getByRole('button',{name:'保存节点并开通',exact:true});
    await expect(save).toBeDisabled();await dialog.getByRole('checkbox').check();await expect(save).toBeEnabled();
    await expect(dialog).toContainText('从保存成功时开始计算 30 天有效期');
    await admin.screenshot({path:path.join(output,'activate-preview.png'),fullPage:true});
    await save.click();await dialog.waitFor({state:'detached'});assert.equal(sub.status,'active');
    hostMode='idle';await host.getByRole('button',{name:'刷新主机资源'}).click();await expect(host.getByText('0.0%',{exact:true})).toBeVisible();
    hostMode='failure';await host.getByRole('button',{name:'刷新主机资源'}).click();await expect(host.getByText('数据已过期',{exact:true})).toBeVisible();
    hostMode='normal';hostStarted=Date.now();await host.getByRole('button',{name:'刷新主机资源'}).click();await expect(host.getByText('42.0%',{exact:true})).toBeVisible({timeout:3000});
    await admin.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
    const hiddenCalls=hostCalls;await admin.waitForTimeout(5200);assert.equal(hostCalls,hiddenCalls,'hidden page kept polling');
    hostStarted=Date.now();await admin.evaluate(()=>{delete document.hidden;document.dispatchEvent(new Event('visibilitychange'));});
    await expect.poll(()=>hostCalls).toBeGreaterThan(hiddenCalls);
    await expect(host.getByText('42.0%',{exact:true})).toBeVisible({timeout:3000});
    await admin.goto(adminOrigin+'/subscriptions');await admin.getByRole('button',{name:'新建订阅',exact:true}).click();
    await dialog.getByLabel('订阅名称',{exact:true}).fill('空节点新套餐');
    await expect(dialog.getByText('可用节点（可留空）',{exact:true})).toBeVisible();
    await dialog.getByRole('button',{name:'创建订阅模板',exact:true}).click();await dialog.waitFor({state:'detached'});
    assert(writes.some(w=>w.name==='空节点新套餐'&&w.nodeIds.length===0));
    assert.deepEqual(unexpected,[]);assert.deepEqual(errors,[]);
    console.log('PASS: custom checkout, pending overview/link, original count activation, mobile layout, empty template, CPU first load/manual refresh/real zero/failure recovery/visibility pause-resume');
  } finally {await browser.close();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});
