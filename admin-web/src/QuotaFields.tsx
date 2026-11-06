import { useRef, useState } from "react";
import { Toggle } from "./components";

export default function QuotaFields({ bandwidth, setBandwidth, quota, setQuota, unit, setUnit, quotaLabel = "月流量额度" }: {
  bandwidth: string; setBandwidth: (value: string) => void;
  quota: string; setQuota: (value: string) => void;
  unit: string; setUnit: (value: string) => void;
  quotaLabel?: string;
}) {
  const [unlimitedBandwidth, setUnlimitedBandwidth] = useState(bandwidth === "");
  const [unlimitedQuota, setUnlimitedQuota] = useState(quota === "");
  const savedBandwidth = useRef(bandwidth || "100");
  const savedQuota = useRef(quota || "1000");
  return <div className="form-grid">
    <div className="quota-field">
      <div className="quota-field-heading"><span>带宽容量</span><Toggle label="无限" checked={unlimitedBandwidth} onChange={value => {
        if (bandwidth !== "") savedBandwidth.current = bandwidth;
        setUnlimitedBandwidth(value); setBandwidth(value ? "" : savedBandwidth.current);
      }} /></div>
      <div className="input-addon"><input aria-label="带宽容量 Mbps" type="number" min="0.1" step="any" max="10000000" disabled={unlimitedBandwidth} required={!unlimitedBandwidth} value={bandwidth} onChange={e => setBandwidth(e.target.value)} placeholder={unlimitedBandwidth ? "无限制" : "100"} /><span>Mbps</span></div>
    </div>
    <div className="quota-field">
      <div className="quota-field-heading"><span>{quotaLabel}</span><Toggle label="无限" checked={unlimitedQuota} onChange={value => {
        if (quota !== "") savedQuota.current = quota;
        setUnlimitedQuota(value); setQuota(value ? "" : savedQuota.current);
      }} /></div>
      <div className="input-addon with-select"><input aria-label={quotaLabel} type="number" min="0.001" step="any" max="1000000000" disabled={unlimitedQuota} required={!unlimitedQuota} value={quota} onChange={e => setQuota(e.target.value)} placeholder={unlimitedQuota ? "不限量" : "1000"} /><select aria-label="流量单位" disabled={unlimitedQuota} value={unit} onChange={e => setUnit(e.target.value)}><option value="GB">GB</option><option value="TB">TB</option></select></div>
    </div>
  </div>;
}
