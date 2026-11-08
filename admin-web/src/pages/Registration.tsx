import { useEffect, useRef, useState, type FormEvent } from "react";
import { ArrowRight, Save, ShieldCheck } from "lucide-react";
import { api, usePolling } from "../api";
import { USER_URL } from "../config";
import {
  Badge,
  Button,
  Confirm,
  ErrorNotice,
  Field,
  Loading,
  Notice,
  PageHeader,
  Panel,
  useToast,
} from "../components";
import type { TurnstileSettings } from "../portal/types";
import "./accounts.css";
export function TurnstileSettingsPage() {
  const settings = usePolling<TurnstileSettings>("/settings/turnstile", 60000),
    portal = usePolling<{
      registrationEnabled: boolean;
      portalEnabled?: boolean;
    }>("/settings/portal", 60000);
  const [siteKey, setSiteKey] = useState(""),
    [hostnames, setHostnames] = useState(""),
    [revision, setRevision] = useState(0),
    [secret, setSecret] = useState(""),
    [initialized, setInitialized] = useState(false),
    [busy, setBusy] = useState(false),
    [registrationBusy, setRegistrationBusy] = useState(false),
    [activationBusy, setActivationBusy] = useState(false),
    [confirmActivation, setConfirmActivation] = useState(false),
    [error, setError] = useState("");
  const activationInFlight = useRef(false);
  const turnstileReady = !!(
    settings.data?.secretConfigured && settings.data.siteKey
  );
  const toast = useToast();
  useEffect(() => {
    if (settings.data && !initialized) {
      setSiteKey(settings.data.siteKey);
      setHostnames((settings.data.allowedHostnames || []).join(", "));
      setRevision(settings.data.revision || 0);
      setInitialized(true);
    }
  }, [settings.data, initialized]);
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const saved = await api<TurnstileSettings>("/settings/turnstile", "PUT", {
        siteKey: siteKey.trim(),
        allowedHostnames: hostnames
          .split(/[\s,，]+/)
          .map((value) => value.trim())
          .filter(Boolean),
        expectedRevision: revision,
        ...(secret ? { secretKey: secret } : {}),
      });
      setSecret("");
      setRevision(saved.revision);
      await settings.refresh();
      await portal.refresh();
      toast("Turnstile 设置已保存");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <PageHeader
        eyebrow="REGISTRATION & SECURITY"
        title="注册与安全验证"
        description="保护公开注册与用户登录，管理端始终可以进入此页面。"
      />
      <ErrorNotice
        message={settings.error || portal.error}
        retry={() => {
          void settings.refresh();
          void portal.refresh();
        }}
      />
      <Panel
        title="Cloudflare Turnstile"
        subtitle="此处凭据独立于 DNS 与证书设置"
        action={
          <Badge
            tone={
              settings.data?.secretConfigured && settings.data.siteKey
                ? "success"
                : "warning"
            }
          >
            {settings.data?.secretConfigured && settings.data.siteKey
              ? "已配置"
              : "需要配置"}
          </Badge>
        }
      >
        {settings.loading ? (
          <Loading />
        ) : (
          <form className="form-stack settings-form" onSubmit={save}>
            <Field
              label="Site Key"
              hint="在 Cloudflare Turnstile 添加当前公开网站域名。"
            >
              <input
                required
                value={siteKey}
                onChange={(e) => setSiteKey(e.target.value)}
                placeholder="Turnstile Site Key"
                autoComplete="off"
              />
            </Field>
            <Field
              label="允许的公开域名"
              hint="填写完整域名，以逗号或空格分隔；不含协议、端口和通配符。"
            >
              <input
                required
                value={hostnames}
                onChange={(e) => setHostnames(e.target.value)}
                placeholder="network.example.com"
              />
            </Field>
            <Field
              label={
                settings.data?.secretConfigured
                  ? "Secret Key（留空保留）"
                  : "Secret Key"
              }
              hint="加密保存，不会回显或发送到浏览器。"
            >
              <input
                type="password"
                autoComplete="new-password"
                required={!settings.data?.secretConfigured}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={
                  settings.data?.secretConfigured
                    ? "已保存；输入可替换"
                    : "Turnstile Secret Key"
                }
              />
            </Field>
            <Notice>
              未配置成功时，公开注册和用户的新登录会被拒绝。管理员仍可登录管理端修复配置。
            </Notice>
            <ErrorNotice message={error} />
            <div className="modal-actions">
              <Button
                type="button"
                variant="secondary"
                disabled={busy || activationBusy}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    const latest = await api<TurnstileSettings>(
                      "/settings/turnstile",
                    );
                    setSiteKey(latest.siteKey);
                    setHostnames((latest.allowedHostnames || []).join(", "));
                    setRevision(latest.revision);
                    setSecret("");
                    await settings.refresh();
                  } catch (e) {
                    setError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                重新读取设置
              </Button>
              <Button
                icon={Save}
                type="submit"
                busy={busy}
                disabled={activationBusy}
              >
                保存安全验证设置
              </Button>
            </div>
          </form>
        )}
      </Panel>
      <Panel
        title="用户门户"
        subtitle="保存安全验证设置后，需明确启用用户入口。公开注册由下方开关单独控制。"
        action={
          portal.data && (
            <Badge tone={portal.data.portalEnabled ? "success" : "warning"}>
              {portal.data.portalEnabled ? "已启用" : "未启用"}
            </Badge>
          )
        }
      >
        {portal.loading ? (
          <Loading />
        ) : (
          <div className="account-control account-registration">
            <div>
              <strong>
                {portal.data?.portalEnabled
                  ? "用户门户已启用"
                  : "用户门户尚未启用"}
              </strong>
              <p>
                {portal.data?.portalEnabled
                  ? "已有用户可完成安全验证后登录，关闭公开注册不会关闭登录入口。"
                  : turnstileReady
                    ? "安全验证已配置，启用后即可开放用户登录入口。"
                    : "请先保存完整的 Cloudflare Turnstile 设置，再启用用户门户。"}
              </p>
            </div>
            {!portal.data?.portalEnabled && (
              <Button
                busy={activationBusy}
                disabled={
                  !portal.data ||
                  !!portal.error ||
                  !!settings.error ||
                  !turnstileReady ||
                  busy ||
                  registrationBusy
                }
                onClick={() => setConfirmActivation(true)}
              >
                启用用户门户
              </Button>
            )}
          </div>
        )}
      </Panel>
      <Panel title="公开注册" subtitle="仅控制新用户注册，不会自动启用用户门户">
        <div className="account-control">
          <div className="account-registration">
            <div>
              <strong>
                {portal.data?.registrationEnabled
                  ? "允许新用户注册"
                  : "新用户注册已关闭"}
              </strong>
              <p>关闭注册不影响已有用户登录。新账户默认没有订阅权限。</p>
            </div>
            <Button
              variant="secondary"
              busy={registrationBusy}
              disabled={!portal.data || activationBusy}
              onClick={async () => {
                setRegistrationBusy(true);
                setError("");
                try {
                  await api("/settings/portal", "PUT", {
                    registrationEnabled: !portal.data?.registrationEnabled,
                  });
                  await portal.refresh();
                  toast("注册设置已更新");
                } catch (e) {
                  setError((e as Error).message);
                } finally {
                  setRegistrationBusy(false);
                }
              }}
            >
              {portal.data?.registrationEnabled ? "关闭注册" : "开放注册"}
            </Button>
          </div>
          <div className="account-divider" />
          <p>
            <ShieldCheck size={15} className="inline-icon" />{" "}
            用户邮箱仅作登录名，不发送验证邮件。忘记密码由管理员重置。
          </p>
          <a
            href={USER_URL}
            target="_blank"
            rel="noreferrer"
            className="text-link"
          >
            打开用户门户 <ArrowRight size={15} />
          </a>
        </div>
      </Panel>
      {confirmActivation && (
        <Confirm
          title="启用用户门户"
          confirm="确认启用"
          description={`启用后将停用旧设备鉴权、撤销旧客户端会话，并要求活动节点同步新配置。此操作不会修改公开注册开关；当前${portal.data?.registrationEnabled ? "允许新用户注册" : "不开放新用户注册"}。`}
          onClose={() => setConfirmActivation(false)}
          onConfirm={async () => {
            if (activationInFlight.current || portal.data?.portalEnabled) return;
            if (!turnstileReady || !portal.data || portal.error || settings.error)
              throw new Error("请先读取并保存完整的安全验证设置");
            activationInFlight.current = true;
            setActivationBusy(true);
            try {
              await api("/settings/portal/activate", "POST", { confirm: true });
              portal.setData((current) =>
                current ? { ...current, portalEnabled: true } : current,
              );
              await portal.refresh();
              toast("用户门户已启用");
            } finally {
              activationInFlight.current = false;
              setActivationBusy(false);
            }
          }}
        />
      )}
    </>
  );
}
