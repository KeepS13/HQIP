import {useId} from 'react';

export type AutoStartState={enabled:boolean;error:string};

export function AutoStartSetting({state,busy,onChange}:{state?:AutoStartState;busy:boolean;onChange:(enabled:boolean)=>void}) {
  const descriptionId=useId();
  return <div className="mode-row">
    <div><strong>开机自启</strong><p id={descriptionId}>登录 Windows 后自动打开 HQIP，VPN 连接仍需手动开启。</p>{state?.error&&<p role="alert">{state.error}</p>}</div>
    <button type="button" className={'switch '+(state?.enabled?'enabled':'')} role="switch" aria-label="开机自启" aria-describedby={descriptionId} aria-checked={!!state?.enabled} disabled={busy||!state} onClick={()=>onChange(!state?.enabled)}><span/></button>
  </div>;
}
