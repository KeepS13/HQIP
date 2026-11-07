import {useEffect, useRef, useState} from 'react';
import {Copy, Minus, Square, X} from 'lucide-react';

// The methods exposed by the bundled Wails v3 /wails/runtime.js Window API.
export type NativeWindow = {
  Minimise:()=>Promise<void>;
  ToggleMaximise:()=>Promise<void>;
  IsMaximised:()=>Promise<boolean>;
  Close:()=>Promise<void>;
};

export function WindowChrome({runtimeReady,onError}:{runtimeReady:Promise<Error|null>;onError:(message:string)=>void}) {
  const [ready,setReady]=useState(false),[maximized,setMaximized]=useState(false);
  const changingSize=useRef(false);
  useEffect(()=>{
    let mounted=true,timer:ReturnType<typeof setTimeout>|undefined;
    const sync=()=>{
      const native=window.wails?.Window;
      if(native)void native.IsMaximised().then(value=>{if(mounted)setMaximized(value)}).catch(()=>{});
    };
    // Native snapping, title-area double clicks and restore also resize the
    // WebView. Read the actual native state rather than assuming a toggle won.
    const resized=()=>{clearTimeout(timer);timer=setTimeout(sync,80)};
    void runtimeReady.then(error=>{
      if(!mounted||error||!window.wails?.Window)return;
      setReady(true);sync();
    });
    window.addEventListener('resize',resized);
    window.addEventListener('focus',sync);
    return()=>{mounted=false;clearTimeout(timer);window.removeEventListener('resize',resized);window.removeEventListener('focus',sync)};
  },[runtimeReady]);
  async function act(action:'Minimise'|'ToggleMaximise'|'Close') {
    const native=window.wails?.Window;
    if(!ready||!native||(action==='ToggleMaximise'&&changingSize.current))return;
    if(action==='ToggleMaximise')changingSize.current=true;
    try {
      await native[action]();
      if(action==='ToggleMaximise')setMaximized(await native.IsMaximised());
    } catch(error) {
      onError('窗口操作失败：'+String(error).replace(/^Error: /,''));
    } finally {
      if(action==='ToggleMaximise')changingSize.current=false;
    }
  }
  return <div className="window-chrome">
    <div className="window-drag-region" aria-hidden="true" onDoubleClick={()=>void act('ToggleMaximise')}/>
    <div className="window-controls" role="group" aria-label="窗口控制">
      <button type="button" disabled={!ready} aria-label="最小化窗口" title="最小化" onClick={()=>void act('Minimise')}><Minus size={15} aria-hidden="true"/></button>
      <button type="button" disabled={!ready} aria-label={maximized?'还原窗口':'最大化窗口'} title={maximized?'还原':'最大化'} onClick={()=>void act('ToggleMaximise')}>{maximized?<Copy size={13} aria-hidden="true"/>:<Square size={12} aria-hidden="true"/>}</button>
      <button type="button" disabled={!ready} className="window-close" aria-label="关闭窗口并最小化到托盘" title="关闭窗口，继续在托盘运行" onClick={()=>void act('Close')}><X size={16} aria-hidden="true"/></button>
    </div>
  </div>;
}
