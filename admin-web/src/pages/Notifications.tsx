import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ArrowUpRight, BellRing, CheckCheck, Clock3, MessageSquare, RefreshCw, Search, ShieldCheck, TriangleAlert } from "lucide-react";
import { api, usePolling } from "../api";
import { Badge, Button, Empty, ErrorNotice, Loading, Modal, Notice, PageHeader, Panel, RefreshButton, StatCard, date, useToast } from "../components";
import "./notifications.css";

type Condition = { category:string;kind:string;title:string;message:string;detailPath:string;level:string;resourceId:string;stage:number };
type Alert = { id:string;condition:Condition;status:string;acknowledged:boolean;acknowledgedBy:string;acknowledgedAt:string|null;firstSeen:string;lastSeen:string;endedAt:string|null;revision:number;activities?:{id:number;kind:string;message:string;actor:string;at:string}[] };
type Summary = { active:number;unacknowledged:number;unknown:number;critical:number;lastEvaluation:string|null };
type Page = { items:Alert[];total:number;page:number;pageSize:number };
const categories:Record<string,string>={system:"系统与部署",capacity:"容量与流量",certificate:"域名与证书",package:"用户套餐",recovery:"历史恢复"};
const statuses:Record<string,string>={active:"进行中",unknown:"待核实",resolved:"已恢复",ended:"已结束"};
function tone(a:Alert){return a.status==="resolved"?"success":a.status==="ended"?"muted":a.status==="unknown"?"info":a.condition.level}
function detailLink(path:string){return /^\/(nodes|servers|users)\/[A-Za-z0-9_-]+$/.test(path)||["/domains","/notifications"].includes(path)?path:"/notifications"}

function AlertDetail({id,onClose,onChange}:{id:string;onClose:()=>void;onChange:()=>void}){
 const {data,error,refresh,setData}=usePolling<Alert>(`/alerts/${id}`);
 const [note,setNote]=useState("");const [busy,setBusy]=useState(false);const [failure,setFailure]=useState("");const toast=useToast();
 const [draftRevision,setDraftRevision]=useState<number|null>(null);
 async function save(acknowledge:boolean){if(!data)return;setBusy(true);setFailure("");try{
  const value=await api<Alert>(`/alerts/${id}`,"PATCH",{expectedRevision:draftRevision??data.revision,acknowledge,note});setData(value);setNote("");setDraftRevision(null);onChange();toast(acknowledge?"已确认知晓，系统继续跟踪异常":"处理备注已保存");
 }catch(e){setFailure((e as Error).message);await refresh()}finally{setBusy(false)}}
 return <Modal title="预警详情" onClose={onClose} description="管理员确认与系统检测独立记录。">
  <ErrorNotice message={failure||error}/>{!data?<Loading/>:<div className="center-detail">
   <div className="center-detail-title"><Badge tone={tone(data)}>{statuses[data.status]}</Badge><Badge tone={data.acknowledged?"muted":"warning"}>{data.acknowledged?"已确认":"未确认"}</Badge></div>
   <h3>{data.condition.title}</h3><p className="center-message">{data.condition.message}</p>
   <dl className="center-facts"><div><dt>首次发生</dt><dd>{date(data.firstSeen)}</dd></div><div><dt>最近检查</dt><dd>{date(data.lastSeen)}</dd></div><div><dt>{data.status==="resolved"?"恢复时间":"结束时间"}</dt><dd>{data.endedAt?date(data.endedAt):"持续跟踪中"}</dd></div><div><dt>确认人</dt><dd>{data.acknowledged?`${data.acknowledgedBy} · ${date(data.acknowledgedAt)}`:"尚未确认"}</dd></div><div><dt>事件编号</dt><dd><code>{data.id}</code></dd></div></dl>
   <Link className="button secondary" to={detailLink(data.condition.detailPath)} onClick={onClose}>查看相关对象 <ArrowUpRight size={15}/></Link>
   <form className="form-stack" onSubmit={(e:FormEvent)=>{e.preventDefault();void save(false)}}>
    <label className="field"><span>处理备注</span><textarea rows={3} value={note} onChange={e=>{if(draftRevision===null)setDraftRevision(data.revision);setNote(e.target.value)}} placeholder="记录排查结果或后续处理安排"/></label>
    {draftRevision!==null&&draftRevision!==data.revision&&<Notice>状态已更新。请核对详情后<button type="button" className="text-button" onClick={()=>{setDraftRevision(data.revision);setFailure("")}}>使用最新版本继续</button>；已填写备注会保留。</Notice>}
    <div className="center-actions"><Button type="submit" icon={MessageSquare} busy={busy} disabled={!note.trim()||Array.from(note).length>2000}>保存备注</Button><Button type="button" variant="secondary" icon={CheckCheck} busy={busy} disabled={data.acknowledged} onClick={()=>void save(true)}>确认已知晓</Button></div>
   </form>
   <h4>处理时间线</h4><ol className="center-timeline">{data.activities?.map(x=><li key={x.id}><span className="center-timeline-dot"/><div><p>{x.message}</p><small>{date(x.at)}{x.actor?` · ${x.actor}`:" · 系统检测"}</small></div></li>)}</ol>
  </div>}
 </Modal>
}

export function Notifications(){
 const summary=usePolling<Summary>("/alerts/summary");
 const [status,setStatus]=useState("current"),[category,setCategory]=useState(""),[level,setLevel]=useState(""),[search,setSearch]=useState(""),[unack,setUnack]=useState(false),[page,setPage]=useState(1),[selected,setSelected]=useState("");
 const params=new URLSearchParams({status,category,level,search,unacknowledged:String(unack),page:String(page),pageSize:"20"});
 const list=usePolling<Page>(`/alerts?${params}`);
 const stale=Boolean(summary.error||list.error)||Boolean(summary.data?.lastEvaluation&&Date.now()-Date.parse(summary.data.lastEvaluation)>90000);
 const refresh=async()=>{await Promise.all([summary.refresh(),list.refresh()])};
 useEffect(()=>{setPage(1)},[status,category,level,search,unack]);
 return <>
  <PageHeader eyebrow="NETWORK WATCH" title="预警中心" description="系统异常、容量变化与到期提醒，在这里持续跟踪。" actions={<RefreshButton refresh={refresh}/>}/>
  <section className="center-banner"><div className="center-banner-icon"><ShieldCheck size={28}/></div><div><span className="eyebrow">STAY INFORMED</span><h2>每一次变化，都有迹可循。</h2><p>确认已知晓，记录处理过程；恢复状态由系统核验。</p></div><div className="center-monitor"><span className={stale?"":"live-dot"}/>{stale?"检测数据待更新":"站内监控运行中"}<small>最近检查 · {summary.data?.lastEvaluation?date(summary.data.lastEvaluation):"等待首次检查"}</small></div></section>
  <div className="center-stats"><StatCard hint="持续跟踪 · 北京时间" label="当前异常与提醒" value={summary.data?.active??"—"} icon={BellRing}/><StatCard hint="持续跟踪 · 北京时间" label="尚未确认" value={summary.data?.unacknowledged??"—"} icon={Clock3}/><StatCard hint="持续跟踪 · 北京时间" label="严重预警" value={summary.data?.critical??"—"} icon={TriangleAlert}/><StatCard hint="持续跟踪 · 北京时间" label="等待核实" value={summary.data?.unknown??"—"} icon={RefreshCw}/></div>
  <ErrorNotice message={summary.error||list.error}/>{stale&&<Notice>当前显示最近一次有效数据，连接恢复后会自动更新；缺失报告不代表异常已恢复。</Notice>}
  <Panel className="center-panel" title="预警记录" subtitle="容量提醒不改变限速策略；套餐待结算预留不计入已消耗流量。">
   <div className="center-tabs">{[["current","当前预警"],["history","历史记录"],["","全部记录"]].map(([key,label])=><button key={key} className={status===key?"active":""} onClick={()=>setStatus(key)}>{label}</button>)}</div>
   <div className="center-filters"><label className="center-search"><Search size={17}/><input aria-label="搜索预警" placeholder="搜索对象、原因或事件编号" value={search} onChange={e=>setSearch(e.target.value)}/></label><select aria-label="预警类别" value={category} onChange={e=>setCategory(e.target.value)}><option value="">所有类别</option>{Object.entries(categories).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select><select aria-label="预警等级" value={level} onChange={e=>setLevel(e.target.value)}><option value="">所有等级</option><option value="danger">严重</option><option value="warning">提醒</option><option value="info">信息</option></select><label className="center-checkbox"><input type="checkbox" checked={unack} onChange={e=>setUnack(e.target.checked)}/>仅未确认</label></div>
   {!list.data?list.loading?<Loading/>:<Empty title="暂时无法读取预警" description="请稍后刷新重试。"/>:list.data.items.length===0?<Empty icon={ShieldCheck} title={status==="current"?"暂无符合条件的预警":"暂无历史记录"} description="系统每 30 秒检查一次；新发现的异常和提前提醒会显示在这里。"/>:<div className="center-list">{list.data.items.map(a=><button key={a.id} className={`center-row ${a.condition.level}`} onClick={()=>setSelected(a.id)}><span className="center-row-icon">{a.status==="resolved"?<CheckCheck size={21}/>:<BellRing size={21}/>}</span><div className="center-row-body"><div className="center-row-heading"><strong>{a.condition.title}</strong><Badge tone={tone(a)}>{statuses[a.status]}</Badge>{!a.acknowledged&&["active","unknown"].includes(a.status)&&<span className="center-new">未确认</span>}</div><p>{a.condition.message}</p><small>{categories[a.condition.category]||"系统"} · {date(a.firstSeen)} · {a.id}</small></div><ArrowUpRight size={18}/></button>)}</div>}
   <div className="center-pagination"><span>共 {list.data?.total??0} 条 · 第 {page} 页</span><div><Button variant="secondary" disabled={page===1} onClick={()=>setPage(p=>p-1)}>上一页</Button><Button variant="secondary" disabled={page*20>=(list.data?.total??0)} onClick={()=>setPage(p=>p+1)}>下一页</Button></div></div>
  </Panel>{selected&&<AlertDetail id={selected} onClose={()=>setSelected("")} onChange={refresh}/>}
 </>
}
