// Run against the built administrator app. Every request is intercepted locally.
const { chromium } = require('playwright');
const { expect } = require('playwright/test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..'), output = path.join(root, '.local/node-user-traffic-ui');
const CMS = 'https://cms-hqip.sharevps.online';
const API = process.env.HQIP_TEST_API_URL || 'https://hqip.sharevps.online';
const at = new Date();
const node = {id:'node-a',name:'美国节点 A',serverId:'server-a',domain:'a.example.invalid',publicIp:'203.0.113.11',port:443,lifecycle:'active',managed:true,status:'ready',coreHealthy:true,lastSeen:at.toISOString(),createdAt:at.toISOString(),monthUpload:1e8,monthDownload:2e8,totalUpload:2e8,totalDownload:4e8,metrics:{uploadRate:0,downloadRate:0,cpuPercent:1,memoryUsed:1e8,memoryTotal:1e9,diskUsed:1e9,diskTotal:1e10},onlineDevices:0,instances:0,sshHost:'example.invalid',sshPort:22,sshUser:'root',desiredRevision:1,appliedRevision:1,audience:{subscribedUsers:2,activeSubscriptions:2,onlineUsers:0,onlineConnections:0}};
const other = {...node,id:'node-b',name:'日本节点 B'};
const users = Array.from({length:23},(_,i)=>({userId:`user-${i}`,email:i===0?'long.account.for.node.traffic.ranking@example.com':`person-${i}@example.com`,enabled:i!==1,upload:i<21?(23-i)*1e6:0,download:i<21?(23-i)*2e6:0,total:i<21?(23-i)*3e6:0,hasData:i!==22}));
let failTraffic=false, holdSecond=false, releaseSecond=null;
const errors=[], unexpected=[], trafficRequests=[], checks=[];
(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const browser=await chromium.launch({headless:true,...(process.env.HQIP_TEST_CHROMIUM_PATH?{executablePath:process.env.HQIP_TEST_CHROMIUM_PATH}:{})});
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
  page.on('pageerror',e=>errors.push(e.message));
  await page.clock.install({time:at});
  await context.route('**/*',async route=>{
    const req=route.request(),u=new URL(req.url()),p=u.pathname;
    if(u.origin===API){
      const headers={'access-control-allow-origin':CMS,'access-control-allow-credentials':'true','access-control-allow-headers':'Content-Type,X-CSRF-Token'};
      if(req.method()==='OPTIONS')return route.fulfill({status:204,headers});
      let body,status=200;
      if(p==='/api/v1/session')body={authenticated:true,username:'fixture-admin',csrfToken:'fixture'};
      else if(p==='/api/v1/alerts/unread')body={unread:0};
      else if(p==='/api/v1/host-resources')body={sample:{sampledAt:at.toISOString(),cpuPercent:1,memory:{used:1e9,total:4e9},disk:{used:1e10,total:2e10}},refreshing:false};
      else if(p==='/api/v1/nodes')body=[node,other];
      else if(/^\/api\/v1\/nodes\/node-[ab]$/.test(p))body={node:p.endsWith('node-a')?node:other,traffic:[],devices:[],events:[]};
      else if(p.endsWith('/events'))body=[];
      else if(p.includes('/network-quality'))body={supported:false,enabled:false,status:'disabled',targets:[],samples:[],runs:[]};
      else if(p.endsWith('/user-traffic')){
        trafficRequests.push(req.url());
        const nodeId=p.split('/')[4],offset=Number(u.searchParams.get('offset')),limit=Number(u.searchParams.get('limit'));
        if(holdSecond&&offset===20)await new Promise(resolve=>{releaseSecond=resolve;});
        const all=nodeId==='node-a'?users:[];
        body={nodeId,items:all.slice(offset,offset+limit),totalUsers:all.length,offset,limit,generatedAt:at.toISOString()};
        if(failTraffic){status=503;body={error:'测试：无法读取用户流量'};}
      }
      else if(p==='/api/v1/users/user-0')body={user:{id:'user-0',email:users[0].email,enabled:true,createdAt:at.toISOString(),revision:1},subscriptions:[],sessions:[],connections:[]};
      else if(p==='/api/v1/subscription-templates')body=[];
      else {unexpected.push(p);status=404;body={error:'Unexpected fixture route'};}
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
    await page.goto(CMS+'/nodes');
    await expect(page.getByRole('heading',{name:'美国节点 A',exact:true})).toBeVisible();
    await expect(page.getByText('用户流量排行',{exact:true})).toHaveCount(0);
    assert.equal(trafficRequests.length,0,'node cards must not request rankings');
    checks.push('ranking limited to node detail; node list makes no ranking request');
    const panel=page.locator('#user-traffic');
    await page.goto(CMS+'/nodes/node-a');
    await expect(panel.locator('tbody tr')).toHaveCount(20);
    await expect(panel).toContainText('不乘计费倍率');
    await expect(panel.locator('tbody tr').first()).toContainText('69 MB');
    await expect(panel.locator('tbody tr').nth(1)).toContainText('已停用');
    await expect(panel.getByRole('button',{name:'上一页'})).toBeDisabled();
    const emails=await panel.locator('tbody .node-user-traffic-user').allTextContents();
    assert.deepEqual(emails,users.slice(0,20).map(u=>u.email));
    for(const [width,height] of [[1440,1000],[768,1024],[390,844],[320,700]]){
      await page.setViewportSize({width,height});await panel.scrollIntoViewIfNeeded();
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`${width}: page overflow`);
      await panel.screenshot({path:path.join(output,`ranking-${width}.png`),animations:'disabled'});
      if(width===320){
        await expect(panel.getByText('左右滑动表格查看上传、下载和总流量')).toBeVisible();
        await panel.locator('.node-user-traffic-scroll').evaluate(el=>{el.scrollLeft=el.scrollWidth;});
        await expect(panel.locator('tbody tr').first().locator('td').last()).toBeInViewport();
        await panel.screenshot({path:path.join(output,'ranking-320-totals.png'),animations:'disabled'});
        await panel.locator('.node-user-traffic-scroll').evaluate(el=>{el.scrollLeft=0;});
      }
    }
    checks.push('descending raw bytes, disabled history, desktop/tablet/mobile table without page overflow');
    await page.setViewportSize({width:1440,height:1000});
    holdSecond=true;
    await panel.getByRole('button',{name:'下一页'}).click();
    await expect.poll(()=>releaseSecond!==null).toBe(true);
    await expect(panel.locator('tbody tr')).toHaveCount(0);
    releaseSecond();holdSecond=false;
    await expect(panel.locator('tbody tr')).toHaveCount(3);
    await expect(panel.locator('tbody tr').first().locator('td').first()).toHaveText('21');
    await expect(panel.locator('tbody tr').nth(1).locator('td').last()).toHaveText('0 B');
    await expect(panel.locator('tbody tr').nth(2).locator('td').last()).toHaveText('暂无记录');
    await expect(panel.getByRole('button',{name:'下一页'})).toBeDisabled();
    checks.push('pagination has global ranks; previous-page rows disappear during load; recorded zero differs from absent ledger');
    failTraffic=true;await page.clock.fastForward(31000);
    await expect(panel).toHaveCount(1);
    await expect(panel.getByRole('alert')).toContainText('当前显示上次成功读取的记录');
    await expect(panel.locator('tbody tr')).toHaveCount(3);
    failTraffic=false;await panel.getByRole('button',{name:'重试'}).click();
    await expect(panel.getByRole('alert')).toHaveCount(0);
    await panel.getByRole('button',{name:'上一页'}).click();
    await expect(panel.locator('tbody tr')).toHaveCount(20);
    await panel.getByRole('link',{name:users[0].email,exact:true}).click();
    await expect(page).toHaveURL(CMS+'/users/user-0');
    await expect(page.getByRole('heading',{name:users[0].email,exact:true})).toBeVisible();
    checks.push('failed refresh keeps same-page data and retries; clicking account opens matching user detail');
    await page.goto(CMS+'/nodes/node-b');
    await expect(panel).toContainText('暂无用户使用记录');
    await expect(panel.getByRole('link',{name:users[0].email,exact:true})).toHaveCount(0);
    await expect(panel.getByRole('button',{name:'下一页'})).toBeDisabled();
    failTraffic=true;await page.goto(CMS+'/nodes/node-a');
    await expect(panel.getByRole('alert')).toContainText('无法读取用户流量');
    await expect(panel).not.toContainText('暂无用户使用记录');
    failTraffic=false;await panel.getByRole('button',{name:'重试'}).click();
    await expect(panel.locator('tbody tr')).toHaveCount(20);
    checks.push('node isolation, empty page, initial failure distinguished from no users');
    assert.deepEqual(errors,[]);assert.deepEqual(unexpected,[]);
    fs.writeFileSync(path.join(output,'receipt.json'),JSON.stringify({checks,errors,unexpected},null,2));
    console.log(JSON.stringify({passed:true,checks}));
  }catch(e){await page.screenshot({path:path.join(output,'failure.png'),fullPage:true});console.error({errors,unexpected});throw e;}
  finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
