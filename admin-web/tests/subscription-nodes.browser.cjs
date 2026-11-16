// Build admin-web first. Requires Playwright via node resolution (or NODE_PATH).
// Every request is fulfilled locally; this never contacts a deployed service.
const { chromium } = require('playwright');
const { expect } = require('playwright/test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const output = path.join(root, '.local/subscription-nodes-ui');
const CMS = 'https://cms-hqip.sharevps.online';
const API = 'https://api-hqip.sharevps.online';
const template = {id:'template',name:'美国-原生IP',enabled:true,listed:true,validityDays:30,priceCents:1000,quotaBytes:300e9,bandwidthMbps:null,maxConnections:3,nodesPerSubscription:1,nodeIds:['node-a'],revision:1};
let sub = {id:'sub-example',userId:'user',templateId:'template',templateName:template.name,createdAt:'2026-11-15T09:03:00Z',expiresAt:'2099-01-15T09:03:00Z',status:'active',usedBytes:2200000,reservedBytes:0,quotaBytes:300e9,bandwidthMbps:null,maxConnections:3,occupied:0,nodeIds:['node-a'],nodesPerSubscription:1,revision:1};
const options = [
  {id:'node-a',name:'美国-原生IP-A',publicIp:'203.0.113.1',domain:'a.example.invalid',inTemplate:true,selectable:true,reason:''},
  {id:'node-b',name:'日本-备用-B',publicIp:'203.0.113.2',domain:'b.example.invalid',inTemplate:false,selectable:true,reason:''},
  {id:'node-c',name:'已占用节点',publicIp:'203.0.113.3',domain:'c.example.invalid',inTemplate:true,selectable:false,reason:'已被该用户的另一份同套餐订阅占用'},
  ...Array.from({length:15},(_,i)=>({id:`node-${i}`,name:`候选节点-${i}`,publicIp:`203.0.113.${i+10}`,domain:`long-candidate-domain-${i}.example.invalid`,inTemplate:false,selectable:true,reason:''})),
];
let failLoad = false;
const writes=[],errors=[],unexpected=[],checks=[];
(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const browser = await chromium.launch({headless:true,...(process.env.HQIP_TEST_CHROMIUM_PATH ? {executablePath:process.env.HQIP_TEST_CHROMIUM_PATH} : {})});
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  const page=await context.newPage();
  page.on('pageerror',e=>errors.push(e.message));
  await context.route('**/*',async route=>{
    const req=route.request(),u=new URL(req.url()),method=req.method(),p=u.pathname;
    if(u.origin===API){
      const headers={'access-control-allow-origin':CMS,'access-control-allow-credentials':'true','access-control-allow-headers':'Content-Type,X-CSRF-Token','access-control-allow-methods':'GET,PATCH,OPTIONS'};
      if(method==='OPTIONS')return route.fulfill({status:204,headers});
      let body,status=200;
      if(p==='/api/v1/session')body={authenticated:true,username:'fixture-admin',csrfToken:'fixture-csrf'};
      else if(p==='/api/v1/alerts/unread')body={unread:0};
      else if(p==='/api/v1/host-resources')body={sample:{sampledAt:new Date().toISOString(),cpuPercent:1,memory:{used:1e9,total:4e9},disk:{used:1e10,total:2e10}},refreshing:false};
      else if(p==='/api/v1/subscription-templates')body=[template];
      else if(p==='/api/v1/users/user')body={user:{id:'user',email:'user@example.invalid',enabled:true,createdAt:sub.createdAt,revision:sub.revision},subscriptions:[sub],sessions:[],connections:[]};
      else if(p==='/api/v1/users/user/subscriptions/sub-example/nodes'){
        if(method==='GET'){
          if(failLoad){status=503;body={error:'测试：暂时无法读取节点'};}
          else body={subscription:sub,nodes:options.map(n=>({...n,selected:sub.nodeIds.includes(n.id)}))};
        }else{
          const input=req.postDataJSON();writes.push({input,csrf:req.headers()['x-csrf-token']});
          if(input.expectedRevision!==sub.revision){status=409;body={error:'订阅已发生变化，请重新加载后重试'};}
          else {sub={...sub,nodeIds:input.nodeIds,nodesPerSubscription:input.nodeIds.length,revision:sub.revision+1};body=sub;}
        }
      }else{unexpected.push(method+' '+p);status=404;body={error:'Unexpected fixture route'};}
      return route.fulfill({status,headers,contentType:'application/json',body:JSON.stringify(body)});
    }
    if(u.origin===CMS){
      const dist=path.join(root,'admin-web/dist');let file=path.resolve(dist,'.'+p);
      assert(file.startsWith(dist+path.sep));
      if(!fs.existsSync(file)||fs.statSync(file).isDirectory())file=path.join(dist,'index.html');
      return route.fulfill({body:fs.readFileSync(file),contentType:{'.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml'}[path.extname(file)]||'text/html'});
    }
    unexpected.push(req.url());return route.abort();
  });
  const dialog=page.getByRole('dialog');
  const option=name=>dialog.locator('.subscription-assignment-option').filter({has:page.getByText(name,{exact:true})}).getByRole('checkbox');
  const open=async()=>{await page.goto(CMS+'/users/user');await page.getByRole('button',{name:'修改节点',exact:true}).click();await dialog.getByPlaceholder('搜索全站节点').waitFor();};
  const save=()=>dialog.getByRole('button',{name:'保存节点',exact:true});
  try{
    await open();
    assert(await save().isDisabled());assert(await option('美国-原生IP-A').isChecked());assert(await option('已占用节点').isDisabled());
    await dialog.getByPlaceholder('搜索全站节点').fill('b.example.invalid');
    assert.equal(await dialog.locator('.subscription-assignment-option').count(),1);
    await option('日本-备用-B').check();await dialog.getByPlaceholder('搜索全站节点').fill('');await option('美国-原生IP-A').uncheck();
    assert.match(await dialog.locator('.subscription-node-preview').innerText(),/新增节点[\s\S]*日本-备用-B[\s\S]*移除节点[\s\S]*美国-原生IP-A/);
    await save().click();await dialog.waitFor({state:'detached'});
    assert.deepEqual(sub.nodeIds,['node-b']);assert.equal(writes.at(-1).csrf,'fixture-csrf');checks.push('search, blocked candidates, external replacement, preview, CSRF and refresh');
    await open();await option('美国-原生IP-A').check();const frozen=sub.revision;sub={...sub,revision:sub.revision+1};
    await page.waitForResponse(r=>r.url()===API+'/api/v1/users/user'); // the real background poll
    assert(await option('美国-原生IP-A').isChecked());
    await save().click();await dialog.getByText('订阅已发生变化，请重新加载后重试',{exact:true}).waitFor();assert.equal(writes.at(-1).input.expectedRevision,frozen);
    await dialog.getByRole('button',{name:'重新加载',exact:true}).click();await expect(option('美国-原生IP-A')).not.toBeChecked();checks.push('polling preserves draft/revision; conflict reload resets draft');
    await option('日本-备用-B').uncheck();await dialog.getByText('保存后此订阅将没有可用节点，现有连接会被撤销。',{exact:true}).waitFor();await save().click();await dialog.waitFor({state:'detached'});assert.deepEqual(sub.nodeIds,[]);checks.push('explicit empty selection warning and save');
    await page.goto(CMS+'/users/user?subscription=sub-example');await dialog.getByPlaceholder('搜索全站节点').waitFor();checks.push('node deletion link opens matching subscription editor');
    await page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
    failLoad=true;await page.getByRole('button',{name:'修改节点',exact:true}).click();await dialog.getByText('测试：暂时无法读取节点',{exact:true}).waitFor();assert(await save().isDisabled());failLoad=false;await dialog.getByRole('button',{name:'重新加载',exact:true}).click();await dialog.getByPlaceholder('搜索全站节点').waitFor();checks.push('load failure and retry');
    await page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
    sub={...sub,nodeIds:['node-a'],nodesPerSubscription:1};
    for(const [width,height] of [[1440,1000],[1024,768],[390,844],[320,700]]){
      await page.setViewportSize({width,height});await open();
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`${width}: page overflow`);
      assert.equal(await dialog.evaluate(e=>e.scrollWidth>e.clientWidth+1),false,`${width}: dialog overflow`);
      await page.screenshot({path:path.join(output,`nodes-${width}.png`),animations:'disabled'});
      await option('日本-备用-B').check();await save().scrollIntoViewIfNeeded();
      const rect=await save().boundingBox();assert(rect.y>=0 && rect.y+rect.height<=height,`${width}: save unreachable`);
      await page.keyboard.press('Escape');await dialog.waitFor({state:'detached'});
    }
    checks.push('desktop and narrow layouts; scrollable picker and reachable save');
    sub={...sub,expiresAt:'2000-01-01T00:00:00Z',status:'expired'};await page.goto(CMS+'/users/user?subscription=sub-example');await dialog.getByText('订阅已到期或已移除，不能修改节点。',{exact:true}).waitFor();assert(await save().isDisabled());checks.push('expired deep link is read-only');
    assert.deepEqual(errors,[]);assert.deepEqual(unexpected,[]);
    fs.writeFileSync(path.join(output,'receipt.json'),JSON.stringify({checks,writes,errors,unexpected},null,2));console.log(JSON.stringify({passed:true,checks}));
  }catch(e){await page.screenshot({path:path.join(output,'failure.png'),animations:'disabled'});console.error({errors,unexpected});throw e;}
  finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
