import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ArrowRight, CreditCard, Save, ShieldCheck } from "lucide-react";
import { api } from "../api";
import {
  Badge,
  Button,
  CopyButton,
  ErrorNotice,
  Field,
  Loading,
  Notice,
  PageHeader,
  Panel,
  useToast,
} from "../components";
import { type PaymentSettings, usePaymentData } from "../payments";
import "./payments.css";

function PaymentSettingsForm({
  initial,
  onSaved,
}: {
  initial: PaymentSettings;
  onSaved: () => Promise<void>;
}) {
  const [revision, setRevision] = useState(initial.revision);
  const [secretConfigured, setSecretConfigured] = useState(
    initial.secretConfigured,
  );
  const [enabled, setEnabled] = useState(initial.enabled);
  const [apiBaseUrl, setApiBaseUrl] = useState(initial.apiBaseUrl || "");
  const [merchantNum, setMerchantNum] = useState(initial.merchantNum || "");
  const [payType, setPayType] = useState(initial.payType || "aloop");
  const [duration, setDuration] = useState(
    String(initial.payDurationMinutes || 10),
  );
  const [queryEnabled, setQueryEnabled] = useState(initial.queryEnabled);
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setError("");
    try {
      const minutes = Number(duration);
      if (!Number.isInteger(minutes) || minutes < 1 || minutes > 15)
        throw new Error("付款期限须为 1 至 15 分钟的整数");
      if (apiBaseUrl.trim()) {
        const url = new URL(apiBaseUrl.trim());
        if (
          url.protocol !== "https:" ||
          url.username ||
          url.password ||
          url.search ||
          url.hash
        )
          throw new Error("请填写不含账号、查询参数或片段的 HTTPS 网关地址");
      }
      if (
        enabled &&
        (!apiBaseUrl.trim() ||
          !merchantNum.trim() ||
          (!secretConfigured && !secret))
      )
        throw new Error("启用支付前，请填写网关地址、商户号和支付密钥");
      setBusy(true);
      const saved = await api<PaymentSettings>("/settings/payment", "PUT", {
        enabled,
        apiBaseUrl: apiBaseUrl.trim(),
        merchantNum: merchantNum.trim(),
        payType,
        payDurationMinutes: minutes,
        queryEnabled,
        expectedRevision: revision,
        ...(secret ? { secret } : {}),
      });
      setRevision(saved.revision);
      setSecretConfigured(saved.secretConfigured);
      setSecret("");
      toast("支付配置已保存");
      await onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="form-stack" onSubmit={submit}>
      <div className="payment-setting-state">
        <div>
          <strong>在线支付</strong>
          <p>用户付款确认后，自动获得一份独立订阅。</p>
        </div>
        <label className="payment-switch">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            disabled={busy}
          />
          启用支付
        </label>
      </div>
      <div className="form-grid">
        <Field
          label="支付网关地址"
          hint="填写该支付服务提供的 HTTPS 接口根地址。"
        >
          <input
            type="url"
            value={apiBaseUrl}
            onChange={(e) => setApiBaseUrl(e.target.value)}
            placeholder="https://payment.example.com"
            maxLength={500}
            disabled={busy}
            autoComplete="off"
          />
        </Field>
        <Field label="商户号">
          <input
            value={merchantNum}
            onChange={(e) => setMerchantNum(e.target.value)}
            maxLength={200}
            disabled={busy}
            autoComplete="off"
          />
        </Field>
        <Field label="支付通道">
          <select
            value={payType}
            onChange={(e) =>
              setPayType(e.target.value as PaymentSettings["payType"])
            }
            disabled={busy}
          >
            <option value="aloop">支付宝通道 · aloop</option>
            <option value="alipay">支付宝通道 · alipay</option>
          </select>
        </Field>
        <Field
          label="付款期限（分钟）"
          hint="新订单可在 1 至 15 分钟内付款，默认 10 分钟。"
        >
          <input
            type="number"
            min={1}
            max={15}
            step={1}
            required
            value={duration}
            onChange={(e) => setDuration(e.target.value)}
            disabled={busy}
          />
        </Field>
      </div>
      <Field
        label="支付密钥"
        hint={
          secretConfigured
            ? "已加密保存。留空保持原密钥，填写后替换；已有订单仍按创建时的配置核验。"
            : "仅在保存时提交，保存后不回显。"
        }
      >
        <input
          type="password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          placeholder={
            secretConfigured
              ? "已配置 · 留空保持不变"
              : "请输入支付服务商分配的密钥"
          }
          autoComplete="new-password"
          maxLength={2000}
          disabled={busy}
        />
      </Field>
      <label className="payment-option">
        <input
          type="checkbox"
          checked={queryEnabled}
          onChange={(e) => setQueryEnabled(e.target.checked)}
          disabled={busy}
        />
        <span>
          <strong>启用主动查单</strong>
          <small>
            支付服务商支持查单接口时开启；关闭时通过经过校验的支付通知确认到账。
          </small>
        </span>
      </label>
      <Notice>
        停用支付后不再创建新付款订单，已有订单仍会核对付款结果。这里保存配置不会发起扣款。
      </Notice>
      <ErrorNotice message={error} />
      <div className="payment-form-actions">
        <Button type="submit" icon={Save} busy={busy}>
          保存支付配置
        </Button>
      </div>
    </form>
  );
}

export function Payments() {
  const settings = usePaymentData<PaymentSettings>("/settings/payment", 0);
  return (
    <>
      <PageHeader
        eyebrow="PAYMENT SETTINGS"
        title="支付设置"
        description="配置收款网关与商户密钥，让订阅购买自动完成开通。"
        actions={
          <Link to="/orders" className="button button-secondary">
            订单管理 <ArrowRight size={15} />
          </Link>
        }
      />
      <ErrorNotice
        message={settings.error}
        retry={() => void settings.refresh()}
      />
      {settings.loading ? (
        <Loading />
      ) : (
        settings.data && (
          <div className="payment-settings-layout">
            <Panel
              className="payment-panel"
              title="商户配置"
              subtitle="仅管理员可管理，支付密钥加密保存。"
              action={
                <Badge tone={settings.data.enabled ? "success" : "muted"}>
                  {settings.data.enabled ? "支付已启用" : "支付未启用"}
                </Badge>
              }
            >
              <PaymentSettingsForm
                initial={settings.data}
                onSaved={() => settings.refresh(true)}
              />
            </Panel>
            <div className="payment-side-panels">
              <Panel
                className="payment-panel"
                title="支付通知地址"
                subtitle="在支付服务商后台配置的异步通知地址。"
              >
                <span className="payment-panel-icon">
                  <CreditCard size={23} />
                </span>
                <code className="payment-url">
                  {settings.data.notifyUrl || "暂未提供"}
                </code>
                {settings.data.notifyUrl && (
                  <CopyButton
                    value={settings.data.notifyUrl}
                    label="复制通知地址"
                  />
                )}
                <p className="muted">
                  订单以服务端核验结果为准。用户跳转回来不会直接标记付款成功。
                </p>
              </Panel>
              <Panel className="payment-panel" title="配置状态">
                <div className="payment-secret-state">
                  <ShieldCheck size={20} />
                  <span>
                    {settings.data.secretConfigured
                      ? "支付密钥已配置"
                      : "尚未配置支付密钥"}
                  </span>
                </div>
                <p className="muted">
                  密钥更换后不会回显。修改订阅价格只影响之后创建的订单。
                </p>
                <Link to="/subscriptions" className="text-link">
                  设置订阅售价 <ArrowRight size={14} />
                </Link>
              </Panel>
            </div>
          </div>
        )
      )}
    </>
  );
}
