// Build admin-web first. All network traffic is fulfilled locally or blocked.
const { chromium } = require('playwright');
const { expect } = require('playwright/test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const output = path.join(root, '.local/node-deployment-ui');
const CMS = 'https://cms-hqip.sharevps.online', API = 'https://hqip.sharevps.online';
const zero = {cpuPercent:0,memoryUsed:0,memoryTotal:0,diskUsed:0,diskTotal:0,uploadRate:0,downloadRate:0};
const base = {sshPort:22,sshUser:'root',hostKey:'test-pin',machineId:'test-machine',platform:'Ubuntu',architecture:'x86_64',revision:1,createdAt:new Date().toISOString(),metrics:zero,bandwidthMbps:null,monthlyQuotaBytes:null,monthUpload:0,monthDownload:0,totalUpload:0,totalDownload:0,nodeCount:0,ipCount:0,expectedNodes:0,reportingNodes:0,ratesStatus:'none',metricsFresh:false,metricsSourceNodeId:'',addresses:[]};
let servers = [{...base,id:'server-a',name:'服务器 A',sshHost:'8.8.8.8',ipCount:1,addresses:[{id:'address-a',serverId:'server-a',ip:'8.8.4.4',nodeIds:[]}]},{...base,id:'server-b',name:'服务器 B',sshHost:'1.1.1.1'}];
let nodes = [], conflict = false, addressGate = null, settingsGate = null, cfConfigured = false, serverFailure = false, switchConflict = false;
const writes = [], errors = [], unexpected = [], checks = [];

(async () => {
  fs.mkdirSync(output, {recursive:true});
  const browser = await chromium.launch({headless:true,...(process.env.HQIP_TEST_CHROMIUM_PATH ? {executablePath:process.env.HQIP_TEST_CHROMIUM_PATH} : {})});
  const context = await browser.newContext({viewport:{width:1440,height:1000}});
  const page = await context.newPage();
  page.on('pageerror', e => errors.push(e.message));
  await context.route('**/*', async route => {
    const req = route.request(), u = new URL(req.url()), p = u.pathname, method = req.method();
    if (u.origin === API) {
      const headers = {'access-control-allow-origin':CMS,'access-control-allow-credentials':'true','access-control-allow-headers':'Content-Type,X-CSRF-Token','access-control-allow-methods':'GET,POST,PATCH,OPTIONS'};
      if (method === 'OPTIONS') return route.fulfill({status:204,headers});
      let body, status = 200;
      if (method !== 'GET') writes.push({p,method,body:req.postDataJSON(),csrf:req.headers()['x-csrf-token']});
      if (p === '/api/v1/session') body = {authenticated:true,username:'fixture',csrfToken:'fixture-csrf'};
      else if (p === '/api/v1/alerts/unread') body = {unread:0};
      else if (p === '/api/v1/host-resources') body = {sample:{sampledAt:new Date().toISOString(),cpuPercent:1,memory:{used:1e9,total:4e9},disk:{used:1e9,total:2e10}},refreshing:false};
      else if (p === '/api/v1/server-operations') body = [];
      else if (p === '/api/v1/settings/cloudflare') {if(settingsGate) await settingsGate;body = {configured:cfConfigured,defaultZoneId:cfConfigured?'zone':'',defaultZoneName:'example.com'};}
      else if (p === '/api/v1/settings/cloudflare/zones') body = {zones:[{id:'zone',name:'example.com',status:'active'}]};
      else if (p === '/api/v1/traffic/analytics') body = {from:u.searchParams.get('from'),to:u.searchParams.get('to'),minDate:'2020-01-01',today:u.searchParams.get('to'),timeZone:'Asia/Shanghai',includesToday:true,dayCount:7,summary:{upload:0,download:0,total:0,dailyAverage:0,recordedDays:0,hasData:false},days:[],contributorKind:'node',contributors:[],generatedAt:new Date().toISOString()};
      else if (p === '/api/v1/servers') {
        if (method === 'GET') body = servers;
        else if (serverFailure) {status=400;body={error:'测试：SSH 连接失败'};}
        else {const input=req.postDataJSON();const server={...base,id:'server-new',name:input.name,sshHost:input.sshHost};servers.push(server);body=server;status=201;}
      } else if (/^\/api\/v1\/servers\/[^/]+/.test(p)) {
        const id=p.split('/')[4], server=servers.find(s=>s.id===id);
        if (p.endsWith('/credential')) body={serverId:id,configured:true,status:'healthy',revision:1};
        else if (p.endsWith('/addresses')) {if(addressGate) await addressGate;body=server.addresses;}
        else if (method === 'PATCH' && serverFailure) {status=400;body={error:'测试：SSH 连接失败'};}
        else if (method === 'PATCH') {Object.assign(server,{name:req.postDataJSON().name,sshHost:req.postDataJSON().sshHost,revision:server.revision+1});body=server;}
        else body={server,addresses:server.addresses,nodes:nodes.filter(n=>n.serverId===id),nodeUsage:nodes.filter(n=>n.serverId===id)};
      } else if (p === '/api/v1/nodes') {
        if (method === 'GET') body=nodes;
        else if (conflict) {status=409;body={error:'该公网 IP 已有节点或待清理实例，请在原节点重试或完成删除'};}
        else {const input=req.postDataJSON(), server=servers.find(s=>s.id===input.serverId);body={...base,...input,id:'node-new',addressId:'new-address',sshHost:server.sshHost,status:'pending',message:'等待部署',lifecycle:'active',managed:false,desiredRevision:1,appliedRevision:0,coreHealthy:false,onlineDevices:0,instances:0};nodes=[body];status=202;}
      } else if (p === '/api/v1/nodes/node-new/public-ip') {
        if(switchConflict){status=409;body={error:'测试：新 IP 已有节点，请刷新后重试'};}
        else {const input=req.postDataJSON();nodes[0]={...nodes[0],publicIp:input.publicIp,ipSwitch:{fromIp:input.oldPublicIp,fromAddressId:nodes[0].addressId},status:'pending'};body=nodes[0];status=202;}
      } else if (p === '/api/v1/nodes/node-new') body={node:nodes[0],events:[],traffic:[]};
      else if (p.endsWith('/network-quality')) body={status:'not-run'};
      else if (p.endsWith('/events')) return route.fulfill({status:200,headers,contentType:'text/event-stream',body:': fixture\n\n'});
      else {unexpected.push(method+' '+p);status=404;body={error:'Unexpected fixture route'};}
      return route.fulfill({status,headers,contentType:'application/json',body:JSON.stringify(body)});
    }
    if (u.origin === CMS) {
      const dist=path.join(root,'admin-web/dist');let file=path.resolve(dist,'.'+p);
      assert(file.startsWith(dist+path.sep));
      if (!fs.existsSync(file)||fs.statSync(file).isDirectory()) file=path.join(dist,'index.html');
      return route.fulfill({body:fs.readFileSync(file),contentType:{'.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml'}[path.extname(file)]||'text/html'});
    }
    unexpected.push(req.url());return route.abort();
  });
  const dialog=page.getByRole('dialog'), ip=()=>dialog.getByLabel('公网 IPv4'), submit=()=>dialog.getByRole('button',{name:'添加并部署',exact:true});
  const cancel=async()=>{await dialog.getByRole('button',{name:'取消',exact:true}).click();await dialog.waitFor({state:'detached'});};
  const open=async()=>{await page.goto(CMS+'/servers/server-a');await page.getByRole('button',{name:'添加节点',exact:true}).click();await dialog.waitFor();};
  const nodeFields=async()=>{await dialog.getByLabel('节点名称',{exact:true}).fill('测试节点');await dialog.getByLabel('已有节点域名').fill('node.example.com');};
  try {
    await open();await expect(page).toHaveURL(CMS+'/servers/server-a');await expect(dialog.getByLabel('服务器',{exact:true})).toHaveAttribute('readonly','');await expect(ip()).toHaveValue('');await nodeFields();
    const before=writes.length;
    for(const bad of ['node.example.com','::1','192.168.1.1','1.2.3.256','01.2.3.4','203.0.113.1']){await ip().fill(bad);await submit().click();await expect(dialog.getByText('请填写有效的公网 IPv4 地址',{exact:true})).toBeVisible();}
    assert.equal(writes.length,before);checks.push('in-place locked server and client IP validation');
    await ip().fill('9.9.9.9');conflict=true;await submit().click();await expect(dialog.getByText(/该公网 IP 已有节点/)).toBeVisible();await expect(ip()).toHaveValue('9.9.9.9');assert.equal(writes.at(-1).body.publicIp,'9.9.9.9');assert.equal('addressId' in writes.at(-1).body,false);assert.equal(writes.at(-1).csrf,'fixture-csrf');conflict=false;await cancel();checks.push('single node request, conflict preserves draft, CSRF');
    await page.getByRole('button',{name:'部署节点',exact:true}).click();await expect(ip()).toHaveValue('8.8.4.4');await cancel();checks.push('registered address opens the same dialog in place');
    await page.getByRole('button',{name:'编辑连接',exact:true}).click();
    const editPassword=dialog.getByLabel('SSH 密码',{exact:false});
    assert.equal(await editPassword.evaluate(e=>e.required),false);await expect(editPassword).toHaveValue('');await expect(dialog.getByRole('checkbox')).toBeDisabled();
    await page.screenshot({path:path.join(output,'server-edit-optional-password.png'),animations:'disabled'});
    await dialog.getByRole('button',{name:'检查并保存'}).click();await dialog.waitFor({state:'detached'});assert.equal(writes.at(-1).body.password,'');await expect(page).toHaveURL(CMS+'/servers/server-a');
    await page.getByRole('button',{name:'编辑连接',exact:true}).click();await dialog.getByLabel('SSH 密码',{exact:false}).fill('edit-secret');await expect(dialog.getByRole('checkbox')).toBeEnabled();await dialog.getByRole('button',{name:'检查并保存'}).click();await dialog.waitFor({state:'detached'});assert.equal(writes.at(-1).body.password,'edit-secret');checks.push('editing allows blank password, explicit replacement remains available, and neither opens deployment');
    const switchNode={...base,id:'node-new',serverId:'server-a',addressId:'old-address',name:'原有节点',publicIp:'8.8.8.8',domain:'keep.example.com',port:443,status:'ready',lifecycle:'active',managed:true,installationVersion:2,dnsMode:'manual',certificateMode:'http',coreHealthy:true,desiredRevision:1,appliedRevision:1,onlineDevices:0,instances:0};
    nodes=[{...switchNode}];
    const changeSSH=async target=>{await page.goto(CMS+'/servers/server-a');await page.getByRole('button',{name:'编辑连接',exact:true}).click();await dialog.getByLabel('SSH 管理地址（公网 IPv4）').fill(target);await dialog.getByRole('button',{name:'检查并保存'}).click();};
    serverFailure=true;await changeSSH('1.1.1.1');await expect(dialog.getByText('测试：SSH 连接失败',{exact:true})).toBeVisible();await expect(dialog.getByRole('heading',{name:'是否切换节点公网 IP？'})).toHaveCount(0);await cancel();serverFailure=false;
    const switches=()=>writes.filter(w=>w.p.endsWith('/public-ip')).length;
    await changeSSH('1.1.1.1');await expect(dialog.getByRole('heading',{name:'是否切换节点公网 IP？'})).toBeVisible();await expect(dialog.getByRole('button',{name:'确认切换并部署'})).toBeDisabled();assert.equal(switches(),0);
    await page.screenshot({path:path.join(output,'node-ip-switch-manual.png'),animations:'disabled'});
    await dialog.getByRole('button',{name:'暂不切换'}).click();await dialog.waitFor({state:'detached'});assert.equal(switches(),0);assert.equal(servers[0].sshHost,'1.1.1.1');assert.equal(nodes[0].publicIp,'8.8.8.8');checks.push('SSH failure does not prompt; successful edit prompts; cancel preserves both saved SSH and old node IP');
    await changeSSH('9.9.9.9');await dialog.waitFor({state:'detached'});assert.equal(switches(),0);checks.push('no prompt when previous SSH IP has no node');
    servers[0].sshHost='8.8.8.8';await changeSSH('1.1.1.1');await dialog.getByRole('checkbox',{name:'我已将节点域名解析改为新 IP'}).check();await dialog.getByLabel('本次切换的 SSH 密码').fill('switch-secret');switchConflict=true;await dialog.getByRole('button',{name:'确认切换并部署'}).click();await expect(dialog.getByText('测试：新 IP 已有节点，请刷新后重试',{exact:true})).toBeVisible();await expect(dialog.getByLabel('本次切换的 SSH 密码')).toHaveValue('switch-secret');switchConflict=false;
    await dialog.getByRole('button',{name:'确认切换并部署'}).click();await expect(page).toHaveURL(CMS+'/nodes/node-new');await expect(page.getByText(/公网 IP 切换：8\.8\.8\.8/)).toBeVisible();assert.equal(writes.at(-1).body.oldPublicIp,'8.8.8.8');assert.equal(writes.at(-1).body.publicIp,'1.1.1.1');assert.equal(writes.at(-1).body.expectedServerRevision,servers[0].revision);assert.equal(writes.at(-1).body.manualDnsConfirmed,true);assert.equal(nodes[0].id,'node-new');checks.push('manual DNS confirmation, conflict preserves draft, confirmed switch retains node and shows progress');
    servers[0].sshHost='8.8.8.8';nodes=[{...switchNode,dnsMode:'cloudflare',certificateMode:'cloudflare'}];await page.setViewportSize({width:390,height:844});await changeSSH('1.1.1.1');await expect(dialog.getByText(/将自动把此节点的 Cloudflare A 记录更新为新 IP/)).toBeVisible();await expect(dialog.getByRole('checkbox')).toHaveCount(0);await expect(dialog.getByRole('button',{name:'确认切换并部署'})).toBeEnabled();assert.equal(await dialog.evaluate(e=>e.scrollWidth>e.clientWidth+1),false);await page.screenshot({path:path.join(output,'node-ip-switch-mobile.png'),animations:'disabled'});await dialog.getByRole('button',{name:'暂不切换'}).click();await page.setViewportSize({width:1440,height:1000});nodes=[];servers[0].sshHost='8.8.8.8';checks.push('Cloudflare switch explains automatic DNS and fits mobile');
    await page.goto(CMS+'/servers');await page.getByRole('button',{name:'添加服务器',exact:true}).click();await dialog.getByLabel('服务器名称').fill('新服务器');assert.equal(await dialog.getByLabel('SSH 密码',{exact:false}).evaluate(e=>e.required),true);await dialog.getByLabel('SSH 密码',{exact:false}).fill('new-secret');
    const ssh=dialog.getByLabel('SSH 管理地址（公网 IPv4）');const serverWrites=writes.length;
    for(const bad of ['vps.example.com','::1','10.0.0.1']){await ssh.fill(bad);await dialog.getByRole('button',{name:'检查并保存'}).click();await expect(dialog.getByText('SSH 管理地址必须为有效的公网 IPv4',{exact:true})).toBeVisible();}assert.equal(writes.length,serverWrites);
    await ssh.fill('1.0.0.1');serverFailure=true;await dialog.getByRole('button',{name:'检查并保存'}).click();await expect(dialog.getByText('测试：SSH 连接失败',{exact:true})).toBeVisible();await expect(page).toHaveURL(CMS+'/servers');serverFailure=false;
    await dialog.getByRole('button',{name:'检查并保存'}).click();await expect(dialog.getByRole('heading',{name:'添加节点',exact:true})).toBeVisible();await expect(ip()).toHaveValue('1.0.0.1');await expect(dialog.getByLabel('服务器',{exact:true})).toHaveValue('新服务器 · 1.0.0.1');await expect(page).toHaveURL(CMS+'/servers/server-new');await expect(dialog.getByLabel('本次部署的 SSH 密码')).toHaveValue('');await ip().fill('9.9.9.9');await cancel();await page.reload();await page.getByRole('heading',{name:'新服务器',exact:true}).waitFor();await expect(dialog).toHaveCount(0);checks.push('enrollment validates SSH, failure stays open, success prefills once, cancel preserves server');
    await page.goto(CMS+'/nodes?add=1&server=server-a&address=address-a');await expect(ip()).toHaveValue('8.8.4.4');await dialog.getByRole('combobox',{name:/^服务器/}).selectOption('server-b');await expect(ip()).toHaveValue('');await cancel();checks.push('legacy deep link and server switch');
    let release;addressGate=new Promise(resolve=>release=resolve);await page.goto(CMS+'/nodes?add=1&server=server-a&address=address-a');await ip().fill('9.9.9.9');release();addressGate=null;await expect(submit()).toBeEnabled();await expect(ip()).toHaveValue('9.9.9.9');await cancel();checks.push('late address loading cannot overwrite manual input');
    cfConfigured=true;await open();await expect(dialog.getByLabel('域名方式')).toHaveValue('cloudflare');await expect(dialog.getByLabel('Cloudflare 域名')).toHaveValue('zone');await cancel();cfConfigured=false;
    for(const [width,height] of [[1440,1000],[390,844],[320,700]]){await page.setViewportSize({width,height});await open();assert.equal(await dialog.evaluate(e=>e.scrollWidth>e.clientWidth+1),false);await fs.promises.mkdir(output,{recursive:true});await page.screenshot({path:path.join(output,`deployment-${width}.png`),animations:'disabled'});await submit().scrollIntoViewIfNeeded();const rect=await submit().boundingBox();assert(rect.y>=0&&rect.y+rect.height<=height);await cancel();}checks.push('Cloudflare defaults and desktop/mobile dialog layouts');
    await page.setViewportSize({width:1440,height:1000});let releaseSettings;settingsGate=new Promise(resolve=>releaseSettings=resolve);await open();await nodeFields();await ip().fill(' 9.9.9.9 ');await submit().click();await expect(page).toHaveURL(CMS+'/nodes/node-new');await page.getByRole('heading',{name:'测试节点',exact:true}).waitFor();assert.equal(writes.at(-1).body.publicIp,'9.9.9.9');assert.equal(writes.at(-1).body.dnsMode,'manual');assert.equal(writes.at(-1).body.domain,'node.example.com');releaseSettings();settingsGate=null;checks.push('successful deployment navigates to node progress even while Cloudflare settings are loading');
    assert.deepEqual(errors,[]);assert.deepEqual(unexpected,[]);assert.equal(writes.some(w=>w.p.endsWith('/addresses')),false);
    fs.writeFileSync(path.join(output,'receipt.json'),JSON.stringify({checks,writes: writes.map(({body,...rest})=>({...rest,body:{...body,password:body.password?'[redacted]':''}})),errors,unexpected},null,2));console.log(JSON.stringify({passed:true,checks}));
  } catch(e) {await page.screenshot({path:path.join(output,'failure.png')});console.error({errors,unexpected});throw e;}
  finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
