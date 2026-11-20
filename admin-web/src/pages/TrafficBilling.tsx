import { useEffect, useState, type FormEvent } from "react";
import { Save, RefreshCw } from "lucide-react";
import { api } from "../api";
import { Button, ErrorNotice, Field, Loading, Notice, PageHeader, Panel, date, useToast } from "../components";

type Configuration = { enabled: boolean; hysteria2: number; vless: number };
type Change = { revision: number; actor: string; at: string; before: Configuration; after: Configuration };
type Settings = Configuration & { revision: number; activeLeases: number; pendingLeases: number; history: Change[] };

function multiplier(value: string): number {
  if (!/^\d+(\.\d{1,2})?$/.test(value)) throw new Error("倍率最多支持两位小数");
  const [whole, fraction = ""] = value.split(".");
  const result = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  if (!Number.isInteger(result) || result < 1 || result > 1000) throw new Error("倍率必须在 0.01～10.00 之间");
  return result;
}

export function TrafficBilling() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [hy2, setHy2] = useState("1.50"), [vless, setVless] = useState("1.50");
  const [enabled, setEnabled] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const toast = useToast();
  function apply(value: Settings) {
    setSettings(value); setEnabled(value.enabled); setHy2((value.hysteria2 / 100).toFixed(2)); setVless((value.vless / 100).toFixed(2));
  }
  async function load() {
    setBusy(true); setError("");
    try { apply(await api<Settings>("/settings/traffic-billing")); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); }, []);
  async function save(event: FormEvent) {
    event.preventDefault(); if (!settings || busy) return;
    setError(""); setBusy(true);
    try {
      apply(await api<Settings>("/settings/traffic-billing", "PUT", { enabled, expectedRevision: settings.revision, hysteria2: multiplier(hy2), vless: multiplier(vless) }));
      toast("流量计费设置已保存");
    } catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  return <>
    <PageHeader eyebrow="系统设置" title="流量计费" description="全局按入口协议设置订阅扣量倍率" actions={<Button icon={RefreshCw} busy={busy} onClick={() => void load()}>刷新</Button>} />
    <ErrorNotice message={error} />
    {!settings ? (busy ? <Loading /> : null) : <>
      <Panel title="协议倍率" subtitle={`当前版本 ${settings.revision} · ${settings.activeLeases} 份有效授权 · ${settings.pendingLeases} 份待生效`}>
        <form className="form-stack" onSubmit={save}>
          <label><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.target.checked)} /> 启用协议倍率</label>
          <Field label="Hysteria 2" hint="范围 0.01～10.00"><input aria-label="Hysteria 2 倍率" type="number" min="0.01" max="10" step="0.01" required value={hy2} onChange={event => setHy2(event.target.value)} /></Field>
          <Field label="VLESS + Reality" hint="范围 0.01～10.00"><input aria-label="VLESS + Reality 倍率" type="number" min="0.01" max="10" step="0.01" required value={vless} onChange={event => setVless(event.target.value)} /></Field>
          <Notice>每实际传输 1 GB：Hysteria 2 扣除 {enabled ? hy2 : "1.00"} GB，VLESS + Reality 扣除 {enabled ? vless : "1.00"} GB。新连接及后续成功续租的计费段采用新倍率，离线授权恢复同步后生效；历史用量保持原值。</Notice>
          <p className="footnote">首次启用前须完成 Agent、转发内核和客户端升级。管理员监控保留原始代理流量；用户客户端按计费口径显示流量和速度。</p>
          <Button type="submit" icon={Save} busy={busy}>保存计费设置</Button>
        </form>
      </Panel>
      <Panel title="修改记录" subtitle="最近 100 次修改">
        <div className="table-wrap"><table><thead><tr><th>时间 / 操作者</th><th>版本</th><th>Hysteria 2</th><th>VLESS + Reality</th><th>状态</th></tr></thead><tbody>
          {settings.history.map(change => <tr key={change.revision}><td>{date(change.at)} · {change.actor}</td><td>{change.revision}</td><td>{(change.before.hysteria2 / 100).toFixed(2)} → {(change.after.hysteria2 / 100).toFixed(2)}</td><td>{(change.before.vless / 100).toFixed(2)} → {(change.after.vless / 100).toFixed(2)}</td><td>{change.after.enabled ? "已启用" : "1.00 倍"}</td></tr>)}
          {!settings.history.length && <tr><td colSpan={5}>尚无修改记录</td></tr>}
        </tbody></table></div>
      </Panel>
    </>}
  </>;
}
