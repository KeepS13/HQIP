import {useEffect,useRef} from 'react';
import {Download,RefreshCw} from 'lucide-react';
import './updates.css';

export type UpdateState={currentVersion:string;latestVersion:string;available:boolean;prompt:boolean;checking:boolean;checkedAt:string|null;error:string};
type Actions={state?:UpdateState;busy:boolean;onCheck:()=>void;onDownload:()=>void;onSnooze:()=>void};

export function UpdateSettings({state,busy,onCheck,onDownload}:Omit<Actions,'onSnooze'>) {
  return <section className="update-settings" aria-label="客户端更新"><div><strong>HQIP 客户端 <code>v{state?.currentVersion||'—'}</code></strong><p role="status">{state?.checking?'正在检查更新…':state?.error|| (state?.available?`发现新版本 v${state.latestVersion}`:state?.checkedAt?'已是最新版本':'启动后自动检查，每小时检查一次')}</p>{state?.checkedAt&&<small>上次检查 {new Date(state.checkedAt).toLocaleString('zh-CN',{hour12:false})}</small>}</div><div className="update-settings-actions"><button type="button" className="text-button" disabled={busy||state?.checking} onClick={onCheck}><RefreshCw size={13}/>检查更新</button>{state?.available&&<button type="button" className="text-button" disabled={busy} onClick={onDownload}><Download size={13}/>下载更新</button>}</div></section>;
}

export function UpdatePrompt({state,busy,onDownload,onSnooze}:Omit<Actions,'onCheck'>) {
  const dialog=useRef<HTMLDialogElement>(null);
  const show=!!state?.available&&!!state?.prompt;
  useEffect(()=>{const element=dialog.current;if(!element)return;if(show&&!element.open)element.showModal();else if(!show&&element.open)element.close()},[show]);
  return <dialog ref={dialog} className="update-dialog" aria-labelledby="update-title" aria-describedby="update-description" onCancel={event=>event.preventDefault()}><div className="update-dialog-icon"><Download size={24}/></div><h2 id="update-title">HQIP 有新版本</h2><p className="update-versions">当前 v{state?.currentVersion} <span>→</span> 新版 v{state?.latestVersion}</p><p id="update-description">当前连接继续运行。下载后，在托盘退出旧客户端，再解压并运行新版 <strong>HQIP.exe</strong>。</p><div className="update-dialog-actions"><button type="button" disabled={busy} className="update-later" onClick={onSnooze}>稍后提醒（24 小时）</button><button type="button" disabled={busy} className="primary" onClick={onDownload}><Download size={15}/>下载更新</button></div></dialog>;
}
