import { useEffect, useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { DatabaseBackup, Eye, RefreshCw, Search, ShieldCheck } from "lucide-react";
import { api } from "../api";
import { Badge, Button, Empty, ErrorNotice, Loading, Modal, Panel, bytes } from "../components";
import "./operation-logs.css";

type Entry = {
  id: string; at: string; lastAt: string; category: string; action: string;
  actorType: string; actorId: string; actorName: string; authType: string;
  resourceType: string; resourceId: string; method: string; route: string; ip: string;
  status: number; result: string; durationMs: number; requestId: string; jobId: string;
  details: Record<string, unknown>; count: number;
};
type Page = { items: Entry[]; total: number; page: number; pageSize: number };
type Backup = { enabled: boolean; intervalHours: number; retentionDays: number; nextRunAt?: string; lastSuccessAt?: string; lastRun?: { status: string; sizeBytes?: number; retainedCount: number; errorCode?: string } };
const results: Record<string, [string, string]> = { success: ["成功", "success"], failure: ["失败", "danger"], denied: ["已拒绝", "warning"], accepted: ["已提交", "info"], partial: ["部分成功", "warning"] };
const actors: Record<string, string> = { admin: "管理员", user: "用户", system: "系统", anonymous: "未认证" };
const authTypes: Record<string, string> = { password: "账号密码", session: "管理会话", browser: "用户会话", native: "客户端", system: "系统任务", none: "未认证" };
const detailLabels: Record<string, string> = { fields: "涉及字段", reason: "原因", failureCount: "失败次数", banLevel: "封禁级别", banStarted: "开始封禁", retryAfter: "建议等待秒数", acceptedCount: "已提交数量", failedCount: "失败数量", totalCount: "总数量", jobIds: "关联任务", attempt: "执行次数", errorCode: "错误代码", sizeBytes: "归档字节数", deletedCount: "清理份数", message: "结果说明" };
const textFilters = [["search", "关键词", "动作、接口或对象编号"], ["actor", "操作者", "管理员名称或用户邮箱"], ["action", "动作", "例如 admin.login"], ["ip", "客户端 IP", "IPv4 或 IPv6"]] as const;
function stamp(value?: string) { return value ? new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) : "尚无记录"; }
function localInput(value: string) { const d = new Date(value); return Number.isNaN(d.getTime()) ? "" : new Date(d.getTime() + 8 * 3600000).toISOString().slice(0, 16); }
function defaults() { const now = new Date(); return new URLSearchParams({ from: new Date(now.getTime() - 7 * 86400000).toISOString(), to: now.toISOString(), page: "1", pageSize: "20" }); }
function initialFilters(params: URLSearchParams) { const out: Record<string,string> = {}; for (const key of ["search","actor","action","ip","method","authType","result"]) out[key] = params.get(key) || ""; out.from = localInput(params.get("from") || defaults().get("from")!); out.to = localInput(params.get("to") || new Date().toISOString()); return out; }
function Outcome({entry}: {entry: Entry}) { const [label,tone] = results[entry.result] || [entry.result || "未知", "muted"]; return <Badge tone={tone}>{label}{entry.status ? ` · ${entry.status}` : ""}</Badge>; }

function Detail({id,onClose}: {id: string;onClose:()=>void}) {
  const [entry,setEntry]=useState<Entry|null>(null),[error,setError]=useState("");
  useEffect(()=>{let alive=true;api<Entry>(`/operation-logs/${encodeURIComponent(id)}`).then(v=>{if(alive)setEntry(v)}).catch(e=>{if(alive)setError(e.message)});return()=>{alive=false}},[id]);
  return <Modal title="操作详情" onClose={onClose} wide>
    <ErrorNotice message={error}/>{!entry&&!error?<Loading/>:entry&&<div className="operation-detail">
      <div className="operation-detail-title"><div><strong>{entry.action}</strong><p>{stamp(entry.at)}</p></div><Outcome entry={entry}/></div>
      <dl>{[["操作者",entry.actorName||actors[entry.actorType]], ["身份",actors[entry.actorType]], ["认证方式",authTypes[entry.authType]||entry.authType], ["客户端 IP",entry.ip||"不适用"], ["请求",[entry.method,entry.route].filter(Boolean).join(" ")||"系统任务"], ["操作对象",[entry.resourceType,entry.resourceId].filter(Boolean).join(" / ")||"—"], ["耗时",`${entry.durationMs||0} ms`], ["请求编号",entry.requestId||"—"], ["任务编号",entry.jobId||"—"], ["记录编号",entry.id]].map(([label,value])=><div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      {entry.count>1&&<p className="operation-muted">已聚合 {entry.count} 次同类事件，最近发生于 {stamp(entry.lastAt)}。</p>}
      {entry.details&&Object.keys(entry.details).length>0&&<div className="operation-summary"><h3>操作摘要</h3><dl>{Object.entries(entry.details).map(([key,value])=><div key={key}><dt>{detailLabels[key]||key}</dt><dd>{Array.isArray(value)?value.join("、"):typeof value==="boolean"?(value?"是":"否"):typeof value==="object"?JSON.stringify(value):String(value)}</dd></div>)}</dl></div>}
    </div>}
  </Modal>;
}

export function OperationLogs() {
  const [params,setParams]=useSearchParams();
  const initial=useRef(defaults());
  const effective=new URLSearchParams(params);
  for(const [key,value] of initial.current) if(!effective.has(key)) effective.set(key,value);
  const query=effective.toString();
  const [draft,setDraft]=useState(()=>initialFilters(effective));
  const [data,setData]=useState<Page|null>(null),[backup,setBackup]=useState<Backup|null>(null),[error,setError]=useState(""),[backupError,setBackupError]=useState(""),[loading,setLoading]=useState(true),[revision,setRevision]=useState(0),[selected,setSelected]=useState("");
  useEffect(()=>{setDraft(initialFilters(new URLSearchParams(query)))},[query]);
  useEffect(()=>{
    let alive=true;setLoading(true);setError("");
    api<Page>(`/operation-logs?${query}`).then(v=>{if(alive)setData(v)}).catch(e=>{if(alive)setError(e.message)}).finally(()=>{if(alive)setLoading(false)});
    api<Backup>("/backups/status").then(v=>{if(alive){setBackup(v);setBackupError("")}}).catch(e=>{if(alive)setBackupError(e.message)});
    return()=>{alive=false};
  },[query,revision]);
  function search(e:FormEvent) {
    e.preventDefault();const next=new URLSearchParams({page:"1",pageSize:effective.get("pageSize")||"20"});
    for(const [key,value] of Object.entries(draft)) {
      if(!value.trim()) continue;
      if(key==="from"||key==="to") {const d=new Date(value+":00+08:00");if(Number.isNaN(d.getTime())){setError("请输入有效的北京时间范围");return}next.set(key,d.toISOString())} else next.set(key,value.trim());
    }
    setParams(next);setRevision(v=>v+1);
  }
  function changePage(page:number,size?:number) {const next=new URLSearchParams(query);next.set("page",String(page));if(size)next.set("pageSize",String(size));setParams(next)}
  const page=Number(effective.get("page"))||1,size=Number(effective.get("pageSize"))||20,totalPages=Math.max(1,Math.ceil((data?.total||0)/size));
  const backupBad=backup?.lastRun?.status==="failed"||Boolean(backup?.enabled&&(!backup.lastSuccessAt||Date.now()-Date.parse(backup.lastSuccessAt)>7*3600000));
  return <>
    <section className="operation-heading"><div><h1>操作日志</h1><p>查看账号安全、业务变更与运维任务，记录保留 13 个月。</p></div><Button variant="secondary" icon={RefreshCw} busy={loading} onClick={()=>{const next=new URLSearchParams(query);next.set("to",new Date().toISOString());setParams(next);setRevision(v=>v+1)}}>刷新</Button></section>
    <section className={`operation-backup${backupBad||backupError?" is-warning":""}`} aria-label="自动备份状态"><DatabaseBackup size={22}/><div><strong>自动备份</strong><span>{backupError||(!backup?"正在读取备份状态":!backup.enabled?"等待启用":backup.lastRun?.status==="running"?"备份执行中":backupBad?"备份需要检查":"备份正常")}</span></div><div><small>计划</small><span>每 6 小时 · 本机保留 14 天</span></div><div><small>最近成功</small><span>{stamp(backup?.lastSuccessAt)}</span></div><div><small>下次计划</small><span>{stamp(backup?.nextRunAt)}</span></div>{backup?.lastRun&&<div><small>最近归档</small><span>{bytes(backup.lastRun.sizeBytes)} · {backup.lastRun.retainedCount||0} 份</span></div>}</section>
    <Panel title="筛选记录" subtitle="时间范围按北京时间填写。">
      <form className="operation-filters" onSubmit={search}>
        {textFilters.map(([key,label,placeholder])=><label key={key}>{label}<input value={draft[key]} maxLength={200} placeholder={placeholder} onChange={e=>setDraft({...draft,[key]:e.target.value})}/></label>)}
        <label>请求方法<select value={draft.method} onChange={e=>setDraft({...draft,method:e.target.value})}><option value="">全部</option>{["POST","PUT","PATCH","DELETE"].map(v=><option key={v}>{v}</option>)}</select></label>
        <label>认证方式<select value={draft.authType} onChange={e=>setDraft({...draft,authType:e.target.value})}><option value="">全部</option>{Object.entries(authTypes).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select></label>
        <label>结果<select value={draft.result} onChange={e=>setDraft({...draft,result:e.target.value})}><option value="">全部</option>{Object.entries(results).map(([k,[v]])=><option key={k} value={k}>{v}</option>)}</select></label>
        <label>开始时间<input type="datetime-local" value={draft.from} onChange={e=>setDraft({...draft,from:e.target.value})}/></label>
        <label>结束时间<input type="datetime-local" value={draft.to} onChange={e=>setDraft({...draft,to:e.target.value})}/></label>
        <div className="operation-filter-actions"><Button type="submit" icon={Search} busy={loading}>搜索</Button><Button type="button" variant="secondary" onClick={()=>{const next=defaults();initial.current=next;setParams(next);setDraft(initialFilters(next));setRevision(v=>v+1)}}>重置</Button></div>
      </form>
    </Panel>
    <ErrorNotice message={error} retry={()=>setRevision(v=>v+1)}/>
    <div className="operation-table-panel" aria-busy={loading}>
      {loading?<Loading/>:error? <Empty title="暂时无法读取日志" description="请稍后重试，筛选条件已保留。"/>:!data?.items.length?<Empty icon={ShieldCheck} title="暂无符合条件的操作" description="调整筛选条件或时间范围后重试。"/>:<div className="operation-table-scroll"><table className="operation-table"><thead><tr><th>时间</th><th>操作者</th><th>动作 / 接口</th><th>结果</th><th>耗时</th><th>客户端 IP</th><th>操作</th></tr></thead><tbody>{data.items.map(entry=><tr key={entry.id}><td className="operation-time">{stamp(entry.at)}{entry.count>1&&<small>聚合 {entry.count} 次</small>}</td><td><strong>{entry.actorName||actors[entry.actorType]||"未知"}</strong><small>{actors[entry.actorType]||entry.actorType} · {authTypes[entry.authType]||entry.authType}</small></td><td className="operation-action"><strong>{entry.action}</strong><small>{[entry.method,entry.route].filter(Boolean).join(" ")||entry.resourceType||"系统任务"}</small></td><td><Outcome entry={entry}/></td><td>{entry.durationMs||0} ms</td><td className="operation-ip">{entry.ip||"—"}</td><td><Button variant="ghost" icon={Eye} onClick={()=>setSelected(entry.id)}>详情</Button></td></tr>)}</tbody></table></div>}
      <div className="operation-pagination"><span>共 {data?.total||0} 条记录</span><label>每页 <select aria-label="每页数量" value={size} onChange={e=>changePage(1,Number(e.target.value))}>{[20,50,100].map(v=><option key={v} value={v}>{v}</option>)}</select></label><div><Button variant="secondary" disabled={loading||page<=1} onClick={()=>changePage(page-1)}>上一页</Button><span>{page} / {totalPages}</span><Button variant="secondary" disabled={loading||page>=totalPages} onClick={()=>changePage(page+1)}>下一页</Button></div></div>
    </div>
    {selected&&<Detail id={selected} onClose={()=>setSelected("")}/>}
  </>;
}
