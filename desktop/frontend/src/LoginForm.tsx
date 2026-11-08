import {useId, useState} from 'react';
import {ArrowUpRight, ChevronDown} from 'lucide-react';
import './login-form.css';

export const defaultServerURL='https://api-hqip.sharevps.online';

export function LoginForm({serverUrl,pending,busy,onLogin}:{serverUrl:string;pending:boolean;busy:boolean;onLogin:(address:string)=>void}) {
  // Keep edits separate from the native state, which is refreshed while typing.
  const [draft,setDraft]=useState<string|null>(null);
  const address=draft??serverUrl,disabled=busy||pending,helpId=useId();
  return <form className="login-form" onSubmit={event=>{event.preventDefault();if(!disabled)onLogin(address.trim())}}>
    <button type="submit" className="primary" disabled={disabled}>{pending?'等待浏览器授权…':'登录 / 注册'}<ArrowUpRight size={17}/></button>
    <details className="login-service">
      <summary><span>服务地址</span><span className="login-service-current" title={address}>{address||'未填写'}</span><ChevronDown size={14} aria-hidden="true"/></summary>
      <div className="login-service-fields">
        <label>账户服务地址<input type="text" inputMode="url" value={address} disabled={disabled} onChange={event=>setDraft(event.target.value)} placeholder={defaultServerURL} autoComplete="off" autoCapitalize="none" spellCheck={false} aria-describedby={helpId}/></label>
        <p id={helpId}>填写 HTTPS 根地址，登录成功后保存。</p>
        <button type="button" className="text-button login-service-reset" disabled={disabled||address===defaultServerURL} onClick={()=>setDraft(defaultServerURL)}>恢复默认地址</button>
      </div>
    </details>
  </form>;
}
