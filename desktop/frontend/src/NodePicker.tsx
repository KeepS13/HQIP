import {useCallback, useEffect, useId, useRef, useState} from 'react';
import {createPortal} from 'react-dom';
import {Check, ChevronRight, Globe2, Network, Search, X} from 'lucide-react';
import {NodeLatencyButton, nodeLatency, type NodeTests} from './NodeLatency';
import './node-picker.css';

export type NodeChoice = {subscriptionId:string;nodeId:string;name:string;ready:boolean};
type Subscription = {id:string;templateName:string;status:string;expiresAt:string};
type Props = {choices:NodeChoice[];subscriptions:Subscription[];subscriptionId:string;nodeId:string;pending:boolean;busy:boolean;tests?:NodeTests;onSelect:(subscriptionId:string,nodeId:string)=>void;onTest:()=>void};

function NodePickerDialog({choices,subscriptions,subscriptionId,nodeId,pending,busy,tests,onSelect,onTest,onClose}:Props&{onClose:()=>void}) {
  const [search,setSearch]=useState('');
  const dialog=useRef<HTMLDivElement>(null),input=useRef<HTMLInputElement>(null),titleId=useId();
  useEffect(()=>{
    const previous=document.activeElement as HTMLElement|null;
    input.current?.focus();
    const keydown=(event:KeyboardEvent)=>{
      if(event.key==='Escape'){event.preventDefault();onClose();return}
      if(event.key!=='Tab')return;
      const elements=Array.from(dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),[tabindex="0"]')||[]);
      const first=elements[0],last=elements[elements.length-1];
      if(event.shiftKey&&document.activeElement===first){event.preventDefault();last?.focus()}
      else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus()}
    };
    document.addEventListener('keydown',keydown);
    return()=>{document.removeEventListener('keydown',keydown);previous?.focus()};
  },[onClose]);
  const query=search.trim().toLocaleLowerCase();
  const visible=choices.filter(choice=>{
    const subscription=subscriptions.find(item=>item.id===choice.subscriptionId);
    return `${choice.name} ${subscription?.templateName||''} ${choice.subscriptionId}`.toLocaleLowerCase().includes(query);
  });
  return createPortal(<div className="node-picker-backdrop" onMouseDown={event=>{if(event.target===event.currentTarget)onClose()}}>
    <div className="node-picker-dialog" ref={dialog} role="dialog" aria-modal="true" aria-labelledby={titleId}>
      <div className="node-picker-heading"><div><h2 id={titleId}>选择节点</h2><p>{choices.length} 个可选节点 · 按订阅分别计量</p></div><button className="node-picker-close" type="button" aria-label="关闭节点选择" onClick={onClose}><X size={19}/></button></div>
      <div className="node-picker-toolbar"><label className="node-picker-search"><Search size={16}/><input ref={input} value={search} onChange={event=>setSearch(event.target.value)} placeholder="搜索节点或订阅" aria-label="搜索节点或订阅"/></label><NodeLatencyButton pending={!!tests?.pending} disabled={busy||pending||!choices.some(choice=>choice.ready)} onClick={onTest}/></div>
      <div className="node-picker-options" aria-label="可选节点">{visible.map(choice=>{
        const subscription=subscriptions.find(item=>item.id===choice.subscriptionId);
        const selected=choice.subscriptionId===subscriptionId&&choice.nodeId===nodeId;
        const latency=nodeLatency(tests,choice.nodeId);
        return <button type="button" key={`${choice.subscriptionId}:${choice.nodeId}`} className={`node-picker-option${selected?' selected':''}`} disabled={busy||pending||!choice.ready} aria-pressed={selected} onClick={()=>{onSelect(choice.subscriptionId,choice.nodeId);onClose()}}>
          <span className="node-picker-option-name" title={choice.name}>{choice.name}</span>
          <span className="node-picker-option-subscription" title={`${subscription?.templateName||'订阅'} · #${choice.subscriptionId.slice(-6)}`}>{subscription?.templateName||'订阅'} · #{choice.subscriptionId.slice(-6)}</span>
          <span className="node-picker-option-footer"><span className={`node-latency ${latency.statusKind||''}`}>{choice.ready?(latency.status||'待测速'):'暂不可用'}</span>{selected&&<Check size={18} aria-label="当前选择"/>}</span>
        </button>;
      })}</div>
      {!visible.length&&<p className="node-picker-empty">{query?'没有匹配的节点':'暂无可用订阅节点，请刷新或前往订阅中心查看。'}</p>}
      <p className="node-picker-note">选择节点后使用对应订阅；相同节点共享测速结果。</p>
    </div>
  </div>,document.body);
}

export function NodePicker(props:Props) {
  const [open,setOpen]=useState(false);
  const close=useCallback(()=>setOpen(false),[]);
  const active=new Set(props.subscriptions.filter(item=>item.status==='active'&&Date.parse(item.expiresAt)>Date.now()).map(item=>item.id));
  const choices=props.choices.filter(choice=>active.has(choice.subscriptionId));
  const selected=choices.find(choice=>choice.subscriptionId===props.subscriptionId&&choice.nodeId===props.nodeId);
  const subscription=props.subscriptions.find(item=>item.id===selected?.subscriptionId);
  const latency=selected?nodeLatency(props.tests,selected.nodeId):{};
  return <section className="panel unified-node-panel">
    <div className="panel-title"><h3><Globe2 size={17}/>选择节点</h3><NodeLatencyButton pending={!!props.tests?.pending} disabled={props.busy||props.pending||!choices.some(choice=>choice.ready)} onClick={props.onTest}/></div>
    <button className="node-picker-trigger" type="button" aria-haspopup="dialog" aria-expanded={open} disabled={props.pending||props.busy} onClick={()=>setOpen(true)}>
      <span className="node-picker-icon"><Network size={20}/></span><span className="node-picker-current"><strong>{props.pending?'正在切换节点…':selected?.name||'请选择节点'}</strong><small>{subscription?`${subscription.templateName} · #${subscription.id.slice(-6)}`:'从有效订阅中选择一个节点'}</small></span>{latency.status&&<span className={`node-latency ${latency.statusKind||''}`}>{latency.status}</span>}<ChevronRight size={17}/>
    </button>
    <div className="node-meta"><span>{choices.length} 个节点选项 · {active.size} 份有效订阅</span><span className={selected?.ready?'ready':''}>{selected?.ready?'就绪':'等待选择'}</span></div>
    {open&&<NodePickerDialog {...props} choices={choices} onClose={close}/>}
  </section>;
}
