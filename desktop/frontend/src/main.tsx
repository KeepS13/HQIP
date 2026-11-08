/// <reference types="vite/client" />
import React, {useEffect, useId, useRef, useState} from 'react';
import {createRoot} from 'react-dom/client';
import {ArrowDown, ArrowUp, ArrowUpRight, Check, ChevronDown, CircleHelp, Clock, Gauge, Globe2, Layers, Laptop, LogOut, Network, Power, RefreshCw, ShieldCheck, SlidersHorizontal, X} from 'lucide-react';
import {NodePicker,type NodeChoice} from './NodePicker';
import {UpdateSettings,UpdatePrompt,type UpdateState} from './Updates';
import {type NodeTests} from './NodeLatency';
import {MiniTraffic} from './MiniTraffic';
import {WindowChrome, type NativeWindow} from './WindowChrome';
import {ConnectionControls} from './ConnectionControls';
import './style.css';
import './scrollbar.css';

type Subscription = {id:string;templateName:string;createdAt:string;expiresAt:string;status:string;quotaBytes:number|null;usedBytes:number;reservedBytes:number;bandwidthMbps:number|null;maxConnections:number;occupied:number};
type Point = {at:string;uploadRate:number;downloadRate:number};
type Metrics = {available:boolean;stale:boolean;uploadRate:number;downloadRate:number;uploadBytes:number;downloadBytes:number;applicationConnections:number;memoryBytes:number;connectedAt:string|null;updatedAt:string|null;delayMs:number|null;delayAt:string|null;delayPending:boolean;history:Point[]};
type State = {updates?:UpdateState;nodeTests?:NodeTests;nodeChoices:NodeChoice[];subscription:Subscription|null;subscriptions:Subscription[];selectionRevision:number;selectionPending:boolean;metrics:Metrics;loginPending:boolean;connections:{occupied:number;maxConnections:number;message:string};loggedIn:boolean;serverUrl:string;deviceName:string;nodes:{id:string;name:string;ready:boolean}[];defaultNodeId:string;revision:number;rulesVersion:number;lastSync:string|null;syncError:string;desiredSubscriptionId:string;actualSubscriptionId:string;desiredNodeId:string;actualNodeId:string;runState:string;mode:string;tunEnabled:boolean;connectionIntent:boolean;coreVersion:string;lastError:string;appliedConfigRevision:number};
declare global {interface Window {wails?:{Call:{ByName:(name:string,...args:unknown[])=>Promise<unknown>};Window?:NativeWindow}}}

const runtimePath='/wails/runtime.js';
const runtimeReady=import(/* @vite-ignore */ runtimePath).then(()=>{
  if(!window.wails?.Call?.ByName)throw new Error('Wails runtime did not initialize the bridge');
}).then(()=>null).catch(()=>new Error('客户端运行组件加载失败，请退出并重新启动 HQIP。'));
const emptyMetrics:Metrics={available:false,stale:false,uploadRate:0,downloadRate:0,uploadBytes:0,downloadBytes:0,applicationConnections:0,memoryBytes:0,connectedAt:null,updatedAt:null,delayMs:null,delayAt:null,delayPending:false,history:[]};
const empty:State={subscription:null,subscriptions:[],nodeChoices:[],selectionRevision:0,selectionPending:false,metrics:emptyMetrics,loginPending:false,connections:{occupied:0,maxConnections:0,message:''},loggedIn:false,serverUrl:'https://api-hqip.sharevps.online',deviceName:'',nodes:[],defaultNodeId:'',revision:0,rulesVersion:0,lastSync:null,syncError:'',desiredSubscriptionId:'',actualSubscriptionId:'',desiredNodeId:'',actualNodeId:'',runState:'disconnected',mode:'rule',tunEnabled:false,connectionIntent:false,coreVersion:'1.14.4',lastError:'',appliedConfigRevision:0};
async function call(method:string,...args:unknown[]) {
  const err=await runtimeReady;if(err)throw err;
  if(!window.wails?.Call?.ByName)throw new Error('请在 HQIP Windows 客户端中操作');
  return window.wails.Call.ByName('main.Desktop.'+method,...args);
}
function normalize(value:unknown):State {const s=value as State;return {...empty,...s,nodes:s.nodes||[],nodeChoices:s.nodeChoices||[],subscriptions:s.subscriptions||[],metrics:{...emptyMetrics,...s.metrics,history:s.metrics?.history||[]}}}
function bytes(n:number) {if(n>=1e12)return (n/1e12).toFixed(2)+' TB';if(n>=1e9)return (n/1e9).toFixed(2)+' GB';if(n>=1e6)return (n/1e6).toFixed(1)+' MB';if(n>=1e3)return (n/1e3).toFixed(1)+' KB';return Math.max(0,n).toFixed(0)+' B'}
function date(value:string) {return new Date(value).toLocaleString('zh-CN',{hour12:false})}
function duration(at:string|null, on:boolean) {if(!at||!on)return '—';const n=Math.max(0,Math.floor((Date.now()-new Date(at).getTime())/1000));return `${Math.floor(n/3600).toString().padStart(2,'0')}:${Math.floor(n/60%60).toString().padStart(2,'0')}:${(n%60).toString().padStart(2,'0')}`}
const labels:Record<string,string>={active:'有效',expired:'已到期',exhausted:'流量用尽',disabled:'已停用',removed:'已移除',revoked:'已移除',pending:'待生效'};
function subscriptionIsActive(value:Subscription) {return value.status==='active'&&Date.parse(value.expiresAt)>Date.now()}
function Usage({value}:{value:Subscription}) {
  const active=subscriptionIsActive(value),available=value.quotaBytes===null?null:Math.max(0,value.quotaBytes-value.usedBytes);
  return <>{active?<><div className="subscription-figures"><div><small>可用流量</small><strong>{available===null?'无限':bytes(available)}</strong></div><div><small>本包共享带宽</small><strong>{value.bandwidthMbps===null?'无限':value.bandwidthMbps+' Mbps'}</strong></div><div><small>VPN 名额</small><strong>{value.occupied} / {value.maxConnections}</strong></div></div>{value.quotaBytes!==null&&<div className="usage-track"><span style={{width:Math.min(100,value.usedBytes/Math.max(1,value.quotaBytes)*100)+'%'}}/></div>}</>:<p className="muted-note">{value.status==='pending'?'待生效':'已失效'}</p>}<p className="subscription-meta">已确认使用 {bytes(value.usedBytes)} · 待结算 {bytes(value.reservedBytes)}<br/>到期 {date(value.expiresAt)} · 一次性额度，不按月重置</p></>;
}
function SubscriptionCard({value,selected,pending,onSelect}:{value:Subscription;selected:boolean;pending:boolean;onSelect:()=>void}) {
  const [expanded,setExpanded]=useState(false),detailsId=useId();
  const active=subscriptionIsActive(value),available=value.quotaBytes===null?null:Math.max(0,value.quotaBytes-value.usedBytes);
  return <section className={'panel subscription-instance '+(selected?'selected':'')}>
    <button type="button" className="subscription-toggle" aria-expanded={expanded} aria-controls={detailsId} onClick={()=>setExpanded(open=>!open)}>
      <span className="subscription-name" title={value.templateName}>{value.templateName}</span>
      <span className="subscription-badges"><span className="badge">{!active&&value.status==='active'?'已到期':labels[value.status]||value.status}</span>{selected&&<span className="badge subscription-current">{pending?'正在切换…':'本机当前选择'}</span>}</span>
      <span className="subscription-available">{active?<><span>可用流量</span><strong>{available===null?'无限':bytes(available)}</strong></>:<strong>{value.status==='pending'?'待生效':'已失效'}</strong>}</span>
      <ChevronDown size={16} className={'subscription-chevron '+(expanded?'expanded':'')} aria-hidden="true"/>
    </button>
    <div id={detailsId} className="subscription-details" hidden={!expanded}>
      <p className="subscription-id">#{value.id.slice(-6)} · 开通于 {date(value.createdAt)}</p>
      <Usage value={value}/>
      <button type="button" className="choose-subscription" disabled={!active||pending||selected} onClick={onSelect}>{selected?(pending?'正在切换…':'本机当前选择'):'在本机使用此订阅'}</button>
    </div>
  </section>;
}
function TrafficChart({points}:{points:Point[]}) {const max=Math.max(1024,...points.flatMap(p=>[p.uploadRate,p.downloadRate]));const line=(key:'uploadRate'|'downloadRate')=>points.map((p,i)=>`${points.length===1?600:i/(points.length-1)*600},${95-Math.max(0,p[key])/max*85}`).join(' ');return <div className="traffic-chart"><svg viewBox="0 0 600 105" preserveAspectRatio="none" role="img" aria-label="最近 60 秒上下行速率"><path d="M0 10H600 M0 52H600 M0 95H600" className="chart-grid"/>{points.length>0&&<><polyline className="chart-upload" points={line('uploadRate')}/><polyline className="chart-download" points={line('downloadRate')}/></>}</svg><div className="chart-labels"><span>60 秒前</span><span>现在</span></div></div>}

function App() {
  const actionSequence=useRef(0);
  const [s,setS]=useState<State>(empty),[error,setError]=useState(''),[runtimeError,setRuntimeError]=useState(''),[busy,setBusy]=useState(''),[help,setHelp]=useState(false),[section,setSection]=useState('network');
  useEffect(()=>{let mounted=true;const update=()=>{call('State').then(value=>{if(!mounted)return;setS(normalize(value));setRuntimeError('')}).catch(e=>{if(mounted)setRuntimeError(String(e).replace(/^Error: /,''))})};update();const timer=setInterval(update,700);return()=>{mounted=false;clearInterval(timer)}},[]);
  async function act(method:string,...args:unknown[]) {const sequence=++actionSequence.current;setBusy(method);setError('');try{await call(method,...args);const latest=normalize(await call('State'));if(sequence===actionSequence.current)setS(latest)}catch(e){if(sequence===actionSequence.current)setError(String(e).replace(/^Error: /,''))}finally{if(sequence===actionSequence.current)setBusy('')}}
  const on=s.runState==='connected',connecting=s.runState==='connecting',canStop=s.connectionIntent||on||connecting;
  const selected=s.nodes.find(n=>n.id===s.desiredNodeId),actual=s.nodes.find(n=>n.id===s.actualNodeId),pack=s.subscription,m=s.metrics;
  const nodeTests=!s.nodeTests?.subscriptionId||s.nodeTests.subscriptionId===s.desiredSubscriptionId?s.nodeTests:undefined;
  const connectionDetail=s.selectionPending?'正在确认旧连接关闭并同步新订阅…':on?`${pack?.templateName||'当前订阅'} · ${actual?.name||'当前节点'}`:s.runState==='failed'?'代理已停止。处理提示后可以手动重连。':'先选订阅，再选节点，开启这台设备的连接。';
  const rate=(n:number)=>on&&m.available&&!m.stale?bytes(n)+'/s':'—';
  return <div className="shell"><WindowChrome runtimeReady={runtimeReady} onError={setError}/><aside><div className="brand"><img className="brand-mark" src="/hqip.svg" alt="HQIP"/><strong>HQIP<span>YOUR CONNECTION</span></strong></div><div className="workspace-label">我的空间</div><nav><button className={section==='network'?'nav active':'nav'} onClick={()=>setSection('network')}><Gauge size={18}/>网络仪表盘<span className={'tiny-dot '+(on?'green':'')}/></button><button className={section==='subscriptions'?'nav active':'nav'} onClick={()=>setSection('subscriptions')}><Layers size={18}/>我的订阅</button><button className={section==='settings'?'nav active':'nav'} onClick={()=>setSection('settings')}><SlidersHorizontal size={18}/>连接设置</button></nav><div className="aside-bottom">{s.loggedIn&&<MiniTraffic points={m.history} upload={rate(m.uploadRate)} download={rate(m.downloadRate)} memory={on&&m.available&&!m.stale&&m.memoryBytes>0?bytes(m.memoryBytes):'—'} live={on&&m.available&&!m.stale} status={!on?(connecting?'正在连接':'未连接'):m.stale?'数据已暂停':'等待数据'}/>}<div className="core"><span className="tiny-dot green"/>sing-box <code>v{s.coreVersion}</code></div><button className="help-button" onClick={()=>setHelp(!help)}><CircleHelp size={17}/>使用帮助<ArrowUpRight size={15}/></button></div></aside>
  <main><header><div><span className="eyebrow">HQIP / {section==='network'?'OVERVIEW':section==='subscriptions'?'SUBSCRIPTIONS':'PREFERENCES'}</span><h1>{section==='network'?'网络仪表盘':section==='subscriptions'?'我的订阅':'连接设置'}</h1></div><div className="device-chip"><Laptop size={16}/><span>{s.loggedIn?s.deviceName:'账户未登录'}</span></div></header>
  {(error||runtimeError||s.lastError||s.syncError)&&<div className="alert" role="alert"><span>{error||runtimeError||s.lastError||s.syncError}</span>{error&&<button onClick={()=>setError('')} aria-label="关闭"><X size={15}/></button>}</div>}
  {!s.loggedIn?<section className="login-card"><img className="login-icon" src="/hqip.svg" alt="HQIP"/><h2>订阅随账户，连接由你选择</h2><form onSubmit={e=>{e.preventDefault();void act('LoginBrowser',s.serverUrl)}}><button className="primary" disabled={!!busy||s.loginPending}>{s.loginPending?'等待浏览器授权…':'登录 / 注册'}<ArrowUpRight size={17}/></button></form><p className="login-hint" role="status">{s.loginPending?'请在浏览器中完成登录，完成后返回此处。':'使用 HQIP 账户，在浏览器中安全登录。'}</p>{s.loginPending&&<button className="text-button cancel-login" onClick={()=>void act('CancelLogin')}>取消本次登录</button>}<small><ShieldCheck size={16}/><span>登录凭据由 Windows 加密保护<br/>客户端不保存密码</span></small></section>:section==='network'?<>
    <NodePicker choices={s.nodeChoices} subscriptions={s.subscriptions} subscriptionId={s.desiredSubscriptionId} nodeId={s.desiredNodeId} pending={s.selectionPending} busy={!!busy} tests={nodeTests} onSelect={(subscriptionId,nodeId)=>void act('SelectSubscriptionNode',subscriptionId,nodeId)} onTest={()=>void act(nodeTests?.pending?'CancelNodeDelays':'TestNodeDelays')}/>
    <section className={'connection-card '+(on?'is-connected':'')}><div className="connection-info"><div className="connection-heading"><span className="section-label">本机连接</span><span className="connection-caption">{on?<><Check size={13}/>TLS 验证已开启</>:<><ShieldCheck size={13}/>Hysteria 2 · 加密连接</>}</span></div><div className="status-line"><span className={'status-dot '+(on?'green':connecting?'pulse':'')}/><h2>{s.selectionPending?'正在切换订阅':on?'已连接':connecting?'正在连接':s.runState==='failed'?'连接失败':'未连接'}</h2></div><p title={connectionDetail}>{connectionDetail}</p></div><button aria-label={canStop?'断开代理':'连接代理'} className={'power '+(on?'on':'')+(connecting?' loading':'')} onClick={()=>void act(canStop?'Disconnect':'Connect')} disabled={!canStop&&(!!busy||s.selectionPending||!pack||pack.status!=='active')}><Power size={20}/><span>{canStop?(connecting?'取消连接':'断开连接'):'连接'}</span></button><ConnectionControls mode={s.mode==='global'?'global':'rule'} tunEnabled={s.tunEnabled} busy={!!busy} disabled={connecting||s.selectionPending} onChange={(mode,tun)=>act('SetMode',mode,tun)}/></section>
    <section className="panel traffic-panel" aria-label="流量统计">
      <div className="panel-title"><h3>流量统计</h3><div className="traffic-legend"><span>↑ 上传</span><span>↓ 下载</span></div></div>
      <TrafficChart points={m.history}/>
      <div className="traffic-metrics">
        <dl className="traffic-upload"><dt><ArrowUp size={12}/>上传速度</dt><dd>{rate(m.uploadRate)}</dd></dl>
        <dl className="traffic-download"><dt><ArrowDown size={12}/>下载速度</dt><dd>{rate(m.downloadRate)}</dd></dl>
        <dl><dt>应用连接</dt><dd>{on&&m.available&&!m.stale?m.applicationConnections:'—'}</dd></dl>
        <dl><dt>累计上传</dt><dd>{bytes(m.uploadBytes)}</dd></dl>
        <dl><dt>累计下载</dt><dd>{bytes(m.downloadBytes)}</dd></dl>
        <dl><dt>内核内存</dt><dd>{on&&m.available&&!m.stale&&m.memoryBytes>0?bytes(m.memoryBytes):'—'}</dd></dl>
      </div>
      <div className="traffic-session"><span><Clock size={12}/>连接时长 <strong>{duration(m.connectedAt,on)}</strong></span><span><Gauge size={12}/>代理延迟 <strong>{on&&m.delayMs!==null?`${m.delayMs} ms`:'—'}</strong><button className="text-button" disabled={!on||s.selectionPending||m.delayPending} onClick={()=>void act('MeasureDelay')}>{m.delayPending?'检测中…':'检测延迟'}</button></span></div>
      <p className="muted-note">含直连流量，不用于订阅扣费 · {m.stale?'指标暂不可用，保留最近数据':'每秒更新 · 最近 60 秒'}</p>
    </section>
    {pack?<section className="panel subscription-summary"><div className="panel-title"><h3><ShieldCheck size={17}/>{pack.templateName}<small className="subtle">#{pack.id.slice(-6)}</small></h3><span className="badge">{labels[pack.status]||pack.status}</span></div><Usage value={pack}/></section>:<section className="panel empty-subscriptions"><Layers size={27}/><h3>选择一份订阅开始使用</h3><p>已购买的流量包会自动同步到这里。</p><button className="text-button" onClick={()=>void act('OpenAccount')}>前往网页订阅中心 <ArrowUpRight size={13}/></button></section>}
    <footer><span><span className={'tiny-dot '+(on?'green':'')}/>{on?`实际连接：${actual?.name||'—'}`:'HQIP 未接管系统网络'}</span><button className="text-button" disabled={busy==='Refresh'} onClick={()=>void act('Refresh')}><RefreshCw size={12}/> {s.lastSync?new Date(s.lastSync).toLocaleTimeString('zh-CN',{hour12:false}):'等待同步'} · 刷新</button></footer>
  </>:section==='subscriptions'?<><section className="subscriptions-heading"><p>同一个订阅可以拥有多份流量包，每份有独立额度和截止时间。</p><button className="text-button" onClick={()=>void act('OpenAccount')}>网页订阅中心 <ArrowUpRight size={14}/></button></section><div className="subscription-list">{s.subscriptions.map(item=><SubscriptionCard key={item.id} value={item} selected={item.id===s.desiredSubscriptionId} pending={s.selectionPending} onSelect={()=>void act('SelectSubscription',item.id)}/>)}</div>{s.subscriptions.length===0&&<section className="panel empty-subscriptions"><Layers size={30}/><h3>还没有订阅</h3><p>前往网页订阅中心查看可开通的流量包，或联系管理员。</p></section>}</>:<section className="panel settings"><h2>账户与连接设置</h2><div className="setting-detail"><span>用户账户</span><strong>{s.deviceName}</strong></div><div className="setting-detail"><span>控制服务</span><code>{s.serverUrl}</code></div><div className="setting-detail"><span>配置 / 规则版本</span><strong>v{s.appliedConfigRevision} / v{s.rulesVersion}</strong></div><div className="mode-row"><div><strong>路由模式</strong><p>规则模式保留大陆与指定服务直连。</p></div><div className="segmented"><button className={s.mode==='rule'?'chosen':''} onClick={()=>void act('SetMode','rule',s.tunEnabled)}>规则</button><button className={s.mode==='global'?'chosen':''} onClick={()=>void act('SetMode','global',s.tunEnabled)}>全局</button></div></div><div className="mode-row"><div><strong>TUN 虚拟网卡</strong><p>关闭时使用系统代理。首次启用需要管理员授权。</p></div><button className={'switch '+(s.tunEnabled?'enabled':'')} role="switch" aria-label="TUN 虚拟网卡" aria-checked={s.tunEnabled} onClick={()=>void act('SetMode',s.mode,!s.tunEnabled)}><span/></button></div><p>关闭窗口后继续在托盘运行；托盘中“退出并恢复网络”会完全退出。</p><UpdateSettings state={s.updates} busy={!!busy} onCheck={()=>void act('CheckUpdates')} onDownload={()=>void act('DownloadUpdate')}/><button className="danger-outline" onClick={()=>void act('Logout')}><LogOut size={16}/>退出用户账户</button></section>}
  {help&&<section className="help-panel"><h3><CircleHelp size={17}/>使用提示</h3><p>点击“选择节点”，在弹窗中选择节点及其对应订阅。同名节点用订阅名称和编号区分；切换会短暂断开连接，待旧连接关闭后再连接新目标。</p><p>订阅到期、流量耗尽或被移除时，VPN 会关闭，不自动使用另一份订阅。节点被移除时转向本订阅默认节点；默认节点失败则停止。</p><p>启动并同步账户后自动测试一次所有有效订阅的节点，相同节点只测一次；也可点击“延迟测试”重新测试。该结果是 QUIC/TLS 握手延迟，不切换连接，不代表代理网页访问延迟。流量区的“检测延迟”通过当前代理访问 HTTPS，消耗计入该订阅；应用连接数与 VPN 并发名额不同。账户服务失联超过 120 秒后连接停止，恢复后请手动重连。</p></section>}
  </main><UpdatePrompt state={s.updates} busy={!!busy} onDownload={()=>void act('DownloadUpdate')} onSnooze={()=>void act('SnoozeUpdate')}/></div>;
}
createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>);
