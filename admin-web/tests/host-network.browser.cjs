// Build admin-web first. All requests are intercepted; no production service is contacted.
const { chromium } = require('playwright');
const { expect } = require('playwright/test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..'), output = path.join(root, '.local/host-network-ui');
const CMS = 'https://cms-hqip.sharevps.online';
const API = process.env.HQIP_TEST_API_URL || 'https://hqip.sharevps.online';
const at = new Date();
const host = {serverId:'server-a',receive:1e9,send:2e9,firstObserved:at.toISOString(),fresh:true,discontinuity:false,receiveRate:123,sendRate:456,sample:{bootId:'11111111-1111-1111-1111-111111111111',bootAt:new Date(at-3600000).toISOString(),at:at.toISOString(),interfaces:[{name:'eth0',index:2,receive:1e9,send:2e9}],excluded:['lo']}};
const node = {id:'node-a',name:'美国节点 A',serverId:'server-a',domain:'a.example.invalid',publicIp:'203.0.113.11',port:443,lifecycle:'active',managed:true,status:'ready',coreHealthy:true,lastSeen:at.toISOString(),createdAt:at.toISOString(),monthUpload:1e8,monthDownload:2e8,totalUpload:2e8,totalDownload:4e8,metrics:{uploadRate:0,downloadRate:0,cpuPercent:1,memoryUsed:1e8,memoryTotal:1e9,diskUsed:1e9,diskTotal:1e10},onlineDevices:0,instances:0,sshHost:'example.invalid',sshPort:22,sshUser:'root',desiredRevision:1,appliedRevision:1,hostNetwork:host,audience:{subscribedUsers:2,activeSubscriptions:2,onlineUsers:0,onlineConnections:0,onlineReportedAt:at.toISOString()}};
const nodes = [node,{...node,id:'node-b',name:'美国节点 B'},{...node,id:'node-c',name:'日本节点 C',serverId:'server-b',hostNetwork:undefined}];
let failPoll = false;
const errors=[], unexpected=[], checks=[];
(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const browser=await chromium.launch({headless:true,...(process.env.HQIP_TEST_CHROMIUM_PATH ? {executablePath:process.env.HQIP_TEST_CHROMIUM_PATH} : {})});
  const context=await browser.newContext({viewport:{width:1440,height:1000}}), page=await context.newPage();
  page.on('pageerror',e=>errors.push(e.message));
  await page.clock.install({time:at});
  await context.route('**/*', async route=>{
    const req=route.request(),u=new URL(req.url()),p=u.pathname;
    if(u.origin===API){
      const headers={'access-control-allow-origin':CMS,'access-control-allow-credentials':'true','access-control-allow-headers':'Content-Type,X-CSRF-Token'};
      if(req.method()==='OPTIONS')return route.fulfill({status:204,headers});
      let body, status=200;
      if(p==='/api/v1/session')body={authenticated:true,username:'fixture-admin',csrfToken:'fixture'};
      else if(p==='/api/v1/alerts/unread')body={unread:0};
      else if(p==='/api/v1/host-resources')body={sample:{sampledAt:at.toISOString(),cpuPercent:1,memory:{used:1e9,total:4e9},disk:{used:1e10,total:2e10}},refreshing:false};
      else if(p==='/api/v1/nodes')body=nodes;
      else if(p==='/api/v1/nodes/node-a')body={node,traffic:[],devices:[],events:[]};
      else if(p==='/api/v1/nodes/node-a/events')body=[];
      else if(p==='/api/v1/nodes/node-a/user-traffic')body={nodeId:'node-a',items:[],totalUsers:0,offset:0,limit:20,generatedAt:at.toISOString()};
      else if(p.startsWith('/api/v1/nodes/node-a/network-quality'))body={supported:false,enabled:false,status:'disabled',targets:[],samples:[],runs:[]};
      else{unexpected.push(p);status=404;body={error:'Unexpected fixture route'};}
      if(failPoll && p.startsWith('/api/v1/nodes')){status=503;body={error:'测试：暂时无法刷新'};}
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
  try{
    for(const [width,height] of [[1440,1000],[390,844],[320,700]]){
      await page.setViewportSize({width,height});await page.goto(CMS+'/nodes');
      await expect(page.getByText('网卡已观测累计 · 按服务器去重',{exact:true})).toBeVisible();
      const totals=page.locator('.panel').filter({has:page.getByText('流量总览',{exact:true})});
      await expect(totals).toContainText('3 GB');await expect(totals).toContainText('1 / 2 台服务器');
      await expect(page.getByText('尚未采集',{exact:true})).toHaveCount(1);
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`${width}: overflow`);
      await page.screenshot({path:path.join(output,`nodes-${width}.png`),fullPage:true,animations:'disabled'});
    }
    checks.push('desktop/mobile node totals, same-host deduplication, explicit unknown carrier');
    await page.setViewportSize({width:1440,height:1000});await page.goto(CMS+'/nodes/node-a');
    const panel=page.locator('.panel').filter({has:page.getByText('服务器网卡总流量',{exact:true})});
    await expect(panel).toContainText('123 B/s');await expect(panel).toContainText('独立分项未分类');
    await page.screenshot({path:path.join(output,'detail-desktop.png'),fullPage:true,animations:'disabled'});
    failPoll=true;await page.clock.fastForward(31000);
    await expect(panel).toContainText('当前速率未知');await expect(panel).not.toContainText('123 B/s');await expect(panel).toContainText('3 GB');
    checks.push('cached snapshot expires automatically during polling failure, cumulative total retained');
    await page.setViewportSize({width:390,height:844});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'detail overflow');
    await page.screenshot({path:path.join(output,'detail-stale-mobile.png'),fullPage:true,animations:'disabled'});
    assert.deepEqual(errors,[]);assert.deepEqual(unexpected,[]);
    fs.writeFileSync(path.join(output,'receipt.json'),JSON.stringify({checks,errors,unexpected},null,2));
    console.log(JSON.stringify({passed:true,checks}));
  }catch(e){await page.screenshot({path:path.join(output,'failure.png'),fullPage:true});console.error({errors,unexpected});throw e;}
  finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
