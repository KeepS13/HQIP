// Render the original Wails components unchanged, with an in-memory bridge.
// Every browser request is fulfilled locally. This harness never starts a VPN.
const fs = require('node:fs');
const path = require('node:path');
const {createRequire} = require('node:module');
const {chromium} = require('playwright');
const root = path.resolve(__dirname, '..');
const frontend = path.join(root, 'desktop/frontend');
const ui = createRequire(path.join(frontend, 'package.json'));
const output = path.join(root, '.diagnostics/style-replica');
const fixture = JSON.parse(fs.readFileSync(path.join(root, 'godot-client/tests/visual-fixture.json'), 'utf8'));
async function main() {
  fs.mkdirSync(path.join(output,'original'),{recursive:true});
  fs.writeFileSync(path.join(output,'fixture.json'),JSON.stringify(fixture,null,2));
  const build = await ui('esbuild').build({entryPoints:[path.join(frontend,'src/main.tsx')],bundle:true,write:false,outdir:'out',format:'esm',jsx:'automatic'});
  const js=build.outputFiles.find(f=>f.path.endsWith('.js')).text, css=build.outputFiles.find(f=>f.path.endsWith('.css')).text;
  const browser=await chromium.launch({headless:true,channel:'msedge'});
  const context=await browser.newContext({viewport:{width:1040,height:820},deviceScaleFactor:1,locale:'zh-CN',timezoneId:'America/Phoenix'});
  const page=await context.newPage();
  const errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await context.addInitScript(({fixture})=>{
    window.fixture=fixture;
    window.wails={Call:{ByName:async name=>name.endsWith('.State')?window.fixture:undefined},Window:{Minimise:async()=>{},ToggleMaximise:async()=>{},IsMaximised:async()=>false,Close:async()=>{}}};
  },{fixture});
  await context.route('**/*',async route=>{
    const p=new URL(route.request().url()).pathname;
    if(p==='/')return route.fulfill({contentType:'text/html',body:'<!doctype html><html lang="zh-CN"><meta charset="utf-8"><link rel="stylesheet" href="/app.css"><div id="root"></div><script type="module" src="/app.js"></script></html>'});
    if(p==='/app.js')return route.fulfill({contentType:'text/javascript',body:js});
    if(p==='/app.css')return route.fulfill({contentType:'text/css',body:css});
    if(p==='/wails/runtime.js')return route.fulfill({contentType:'text/javascript',body:'export {};'});
    if(/^\/hqip[-a-z]*\.svg$/.test(p))return route.fulfill({contentType:'image/svg+xml',body:fs.readFileSync(path.join(frontend,'public',p.slice(1)))});
    return route.abort();
  });
  await page.goto('http://hqip-reference.invalid/');
  await page.getByRole('heading',{name:'已连接',exact:true}).waitFor();
  const bounds={};
  async function capture(name){
    await page.screenshot({path:path.join(output,'original',name+'.png')});
    bounds[name]=await page.locator('aside,main,header,.login-card,.unified-node-panel,.connection-card,.traffic-panel,.subscription-summary,footer,.node-picker-dialog,.settings,.subscription-instance').evaluateAll(nodes=>nodes.map(e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e);return {selector:e.className||e.tagName,x:r.x,y:r.y,width:r.width,height:r.height,font:s.font,padding:s.padding,gap:s.gap};}));
  }
  await capture('dashboard');
  await page.locator('.node-picker-trigger').click(); await capture('nodes');
  await page.getByRole('button',{name:'关闭节点选择'}).click();
  await page.getByRole('button',{name:'我的订阅',exact:true}).click(); await capture('subscriptions');
  await page.locator('.subscription-toggle').click(); await capture('subscription-expanded');
  await page.getByRole('button',{name:'连接设置',exact:true}).click(); await capture('settings');
  await page.evaluate(()=>{window.fixture={...window.fixture,loggedIn:false,runState:'disconnected',connectionIcon:{name:'disconnected',label:'未连接'}};});
  await page.getByRole('button',{name:'网络仪表盘',exact:true}).click();
  await page.getByRole('heading',{name:'订阅随账户，连接由你选择'}).waitFor(); await capture('login');
  await page.locator('.login-service summary').click(); await capture('login-service');
  fs.writeFileSync(path.join(output,'reference-bounds.json'),JSON.stringify(bounds,null,2));
  await browser.close();
  if(errors.length)throw Error(errors.join('\n'));
  console.log('Captured seven original views with an isolated fixture.');
}
main().catch(e=>{console.error(e);process.exitCode=1;});
