import { useRef, useState } from 'react';
import { Globe2, LoaderCircle, Route } from 'lucide-react';
import './connection-controls.css';

type RouteMode = 'rule' | 'global';

export function ConnectionControls({ mode, tunEnabled, busy, disabled, onChange }: {
  mode: RouteMode;
  tunEnabled: boolean;
  busy: boolean;
  disabled: boolean;
  onChange: (mode: RouteMode, tunEnabled: boolean) => Promise<void>;
}) {
  const changing = useRef(false);
  const [pending, setPending] = useState(false);
  const locked = busy || disabled || pending;

  async function change(nextMode: RouteMode, nextTUN: boolean) {
    if (locked || changing.current || (nextMode === mode && nextTUN === tunEnabled)) return;
    changing.current = true;
    setPending(true);
    try {
      await onChange(nextMode, nextTUN);
    } finally {
      changing.current = false;
      setPending(false);
    }
  }

  return <div className="connection-controls" aria-busy={pending}>
    <span className="connection-controls-label">路由模式</span>
    <div className="connection-mode-buttons" role="group" aria-label="路由模式">
      <button type="button" aria-pressed={mode === 'rule'} disabled={locked} onClick={() => void change('rule', tunEnabled)}><Route size={14} aria-hidden="true"/>规则</button>
      <button type="button" aria-pressed={mode === 'global'} disabled={locked} onClick={() => void change('global', tunEnabled)}><Globe2 size={14} aria-hidden="true"/>全局</button>
    </div>
    <span className="connection-controls-divider" aria-hidden="true"/>
    <button type="button" className="connection-tun" role="switch" aria-label="TUN 虚拟网卡" aria-checked={tunEnabled} disabled={locked} title="TUN 虚拟网卡；首次开启可能需要管理员授权" onClick={() => void change(mode, !tunEnabled)}>
      <span>TUN</span><span className="connection-tun-track" aria-hidden="true"><span/></span>
    </button>
    {pending && <span className="connection-controls-pending" role="status"><LoaderCircle size={14} aria-hidden="true"/>正在切换</span>}
  </div>;
}
