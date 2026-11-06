import {useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode} from 'react';
import {Check, ChevronDown} from 'lucide-react';

type SelectionOption = {value:string;label:string;description?:string;disabled?:boolean};

export function SelectionMenu({label,value,placeholder,icon,options,disabled=false,onChange}:{
  label:string;value:string;placeholder:string;icon:ReactNode;options:SelectionOption[];disabled?:boolean;onChange:(value:string)=>void;
}) {
  const id=useId(),container=useRef<HTMLDivElement>(null),trigger=useRef<HTMLButtonElement>(null),list=useRef<HTMLDivElement>(null);
  const [open,setOpen]=useState(false),[active,setActive]=useState('');
  const selected=options.find(option=>option.value===value);
  const activeIndex=options.findIndex(option=>option.value===active&&!option.disabled);
  const optionSignature=JSON.stringify(options);
  useEffect(()=>setOpen(false),[disabled,value,optionSignature]);
  useEffect(()=>{
    if(!open)return;
    const closeOutside=(event:PointerEvent)=>{if(!container.current?.contains(event.target as Node))setOpen(false)};
    document.addEventListener('pointerdown',closeOutside);
    return()=>document.removeEventListener('pointerdown',closeOutside);
  },[open]);
  useEffect(()=>{
    if(!open||!list.current)return;
    const option=list.current.querySelector<HTMLElement>('[data-active="true"]');
    if(!option)return;
    const top=option.offsetTop,bottom=top+option.offsetHeight;
    if(top<list.current.scrollTop)list.current.scrollTop=top;
    else if(bottom>list.current.scrollTop+list.current.clientHeight)list.current.scrollTop=bottom-list.current.clientHeight;
  },[open,active]);
  function expand(last=false) {
    const enabled=options.filter(option=>!option.disabled);
    setActive((selected&&!selected.disabled?selected:last?enabled.at(-1):enabled[0])?.value||'');
    setOpen(true);
  }
  function choose(option:SelectionOption) {
    if(disabled||option.disabled)return;
    setOpen(false);
    trigger.current?.focus();
    if(option.value!==value)onChange(option.value);
  }
  function keyDown(event:KeyboardEvent<HTMLButtonElement>) {
    if(['ArrowDown','ArrowUp','Home','End','Enter',' '].includes(event.key))event.preventDefault();
    if(event.key==='Escape'){event.preventDefault();setOpen(false);return}
    if(event.key==='Tab'){setOpen(false);return}
    if(event.key==='Enter'||event.key===' '){if(open&&activeIndex>=0)choose(options[activeIndex]);else expand();return}
    const enabled=options.filter(option=>!option.disabled);
    if(event.key==='Home'||event.key==='End'){
      setOpen(true);setActive((event.key==='Home'?enabled[0]:enabled.at(-1))?.value||'');return;
    }
    if(event.key==='ArrowDown'||event.key==='ArrowUp'){
      if(!open){expand(event.key==='ArrowUp');return}
      const current=enabled.findIndex(option=>option.value===active),step=event.key==='ArrowDown'?1:-1;
      setActive(enabled[(current+step+enabled.length)%enabled.length]?.value||'');
    }
  }
  return <div className="selection-menu" ref={container} onBlur={event=>{if(!event.currentTarget.contains(event.relatedTarget as Node|null))setOpen(false)}}>
    <button ref={trigger} type="button" role="combobox" aria-label={label} aria-haspopup="listbox" aria-expanded={open} aria-controls={id} aria-activedescendant={open&&activeIndex>=0?`${id}-${activeIndex}`:undefined} disabled={disabled} className={'node-select selection-trigger'+(open?' is-open':'')} onClick={()=>open?setOpen(false):expand()} onKeyDown={keyDown} title={selected?[selected.label,selected.description].filter(Boolean).join(' · '):placeholder}>
      <span className="node-symbol" aria-hidden="true">{icon}</span><span className="selection-value"><strong>{selected?.label||placeholder}</strong>{selected?.description&&<small>{selected.description}</small>}</span><ChevronDown size={16} aria-hidden="true"/>
    </button>
    {open&&<div ref={list} id={id} role="listbox" aria-label={label} className="selection-options">
      {options.length===0?<p className="selection-empty">暂无可选项</p>:options.map((option,index)=><div key={option.value} id={`${id}-${index}`} role="option" aria-selected={option.value===value} aria-disabled={!!option.disabled} data-active={option.value===active&&!option.disabled} className="selection-option" title={[option.label,option.description].filter(Boolean).join(' · ')} onMouseDown={event=>event.preventDefault()} onPointerMove={()=>{if(!option.disabled)setActive(option.value)}} onClick={()=>choose(option)}><span className="selection-option-copy"><strong>{option.label}</strong>{option.description&&<small>{option.description}</small>}</span>{option.value===value&&<Check size={16} aria-hidden="true"/>}</div>)}
    </div>}
  </div>;
}
