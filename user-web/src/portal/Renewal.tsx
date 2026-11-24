import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { CreditCard, RefreshCw } from "lucide-react";
import { Button, ErrorNotice, Loading, Modal, Notice, date, bytes } from "../components";
import { userAPI, useUserData } from "./api";
import { money, type PaymentInfo, type PaymentOrder } from "./payment-data";
import type { SubscriptionDetail, UserSubscription } from "./types";
import { clearRenewalDraft, renewalDraft, renewalExpiry, renewalOrderInput, saveRenewalDraft, type RenewalDraft } from "./renewal-data";

function RenewalDialog({ value, onClose }: { value: UserSubscription; onClose: () => void }) {
  const detail = useUserData<SubscriptionDetail>(`/subscriptions/${encodeURIComponent(value.id)}`, 5000);
  const payment = useUserData<PaymentInfo>("/payment-info", 15000);
  const [draft, setDraft] = useState<RenewalDraft | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [discard, setDiscard] = useState(false);
  const submitting = useRef(false);
  const navigate = useNavigate();
  const current = detail.data?.subscription;
  // Renewal uses the owned subscription's terms, including hidden shop plans.
  const template = detail.data?.renewalTemplate;
  useEffect(() => {
    if (!draft && current && template) setDraft(renewalDraft(value.userId, template, current));
  }, [draft, current, template, value.userId]);
  const dataError = detail.error || payment.error;
  const available = current?.renewable && payment.data?.enabled && Date.parse(current.expiresAt) > Date.now();

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!draft || submitting.current || dataError || (!draft.attempted && !available)) return;
    submitting.current = true;
    setBusy(true); setError("");
    try {
      const next = { ...draft, attempted: true };
      try { saveRenewalDraft(value.userId, next); }
      catch { throw new Error("浏览器无法保存续费请求，请允许本站使用存储后重试。尚未发起支付订单。"); }
      setDraft(next);
      const order = await userAPI<PaymentOrder>("/orders", "POST", renewalOrderInput(next));
      clearRenewalDraft(value.userId, value.id);
      navigate(`/orders/${encodeURIComponent(order.id)}`);
    } catch (e) {
      setError((e as Error).message);
      await Promise.all([detail.refresh(), payment.refresh()]);
    } finally { submitting.current = false; setBusy(false); }
  }

  return <Modal title="确认续费" description={`${value.templateName} · #${value.id.slice(-8)}`} onClose={() => { if (!busy) onClose(); }}>
    {detail.loading || payment.loading ? <Loading /> : <form className="form-stack" onSubmit={submit}>
      <ErrorNotice message={dataError} retry={() => { void Promise.all([detail.refresh(), payment.refresh()]); }} />
      {!available && <Notice tone="warning">当前暂不可新建续费订单。仅支持本人持有、已开通、未到期且启用、有定价的套餐，并需开放在线支付。若此前已提交，请到<Link to="/orders">我的订单</Link>核对结果。</Notice>}
      {draft && <>
        {draft.attempted && <Notice>将继续确认上一次续费请求，沿用原订单参数。请先核对<Link to="/orders">我的订单</Link>，避免重复付款。</Notice>}
        <div className="commerce-checkout-total"><span>续费一期</span><strong>{money(draft.template.priceCents!)}</strong></div>
        <dl className="user-sub-facts">
          <div><dt>续费天数</dt><dd>{draft.template.validityDays} 天</dd></div>
          <div><dt>每期流量</dt><dd>{draft.template.quotaBytes === null ? "不限量" : bytes(draft.template.quotaBytes)}</dd></div>
          {/* Renewal targets the instance, never the template's candidate node pool. */}
          <div><dt>保留原节点</dt><dd>{detail.data?.nodes.map(node => node.name).join("、") || "暂无分配节点"}</dd></div>
          <div><dt>同时在线</dt><dd>{current?.occupied} / {current?.maxConnections}</dd></div>
          <div><dt>本期结束</dt><dd>{date(draft.currentPeriodEndsAt)}</dd></div>
          <div><dt>目前总到期</dt><dd>{date(draft.expiresAt)}</dd></div>
          <div><dt>预计续费后到期</dt><dd>{date(renewalExpiry(draft.expiresAt, draft.template.validityDays))}</dd></div>
        </dl>
        <Notice>付款核验成功后延长原订阅期限，保留原节点和导入链接，不重新分配节点。续费不增加设备名额，也不清空当前在线数。本期流量不变，下一期开始时重置；剩余流量不结转，也不能提前使用下一期额度。</Notice>
        <p className="commerce-hint">价格与天数在下单时锁定，使用限额跟随套餐设置。请在付款期限内完成支付；若核验通知到达时订阅已过期，收款将转人工处理，不会恢复过期订阅。</p>
        <ErrorNotice message={error} />
        {draft.attempted && !discard && <Button type="button" variant="ghost" disabled={busy} onClick={() => setDiscard(true)}>需要另建一笔续费订单？</Button>}
        {discard && <Notice tone="warning">另建订单不会取消已有支付；两笔均付款时将分别追加一期。
          <Button type="button" variant="secondary" disabled={busy || !available || !!dataError} onClick={() => {
            try { clearRenewalDraft(value.userId, value.id); setDraft(renewalDraft(value.userId, template!, current!)); setDiscard(false); setError(""); }
            catch { setError("浏览器无法清理续费请求，请稍后重试。"); }
          }}>我已核对，另建订单</Button>
        </Notice>}
      </>}
      <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>返回</Button>
        <Button type="submit" icon={CreditCard} busy={busy} disabled={!draft || !!dataError || (!draft.attempted && !available)}>{draft?.attempted ? "重试确认续费订单" : "创建续费订单"}</Button></div>
    </form>}
  </Modal>;
}

export function RenewalButton({ value }: { value: UserSubscription }) {
  const [open, setOpen] = useState(false);
  // Eligibility excludes shop visibility; both listed and hidden plans use
  // the same primary action, with no visibility-dependent styling.
  if (!value.renewable && !open) return null;
  return <><Button variant="primary" icon={RefreshCw} onClick={() => setOpen(true)}>续费订阅</Button>
    {open && <RenewalDialog value={value} onClose={() => setOpen(false)} />}</>;
}
