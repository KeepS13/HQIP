import type { UserSessionResponse, PortalConfig } from "./types";
import { ADMIN_URL, passwordError } from "../config";
import { useState, type FormEvent } from "react";
import { ArrowRight, ArrowUpRight, Globe2, Monitor, ShieldCheck, UserRound, Wifi } from "lucide-react";
import { Button, ErrorNotice, Field, Loading, Notice } from "../components";
import { setUserCSRF, userAPI, useUserData } from "./api";
import { Turnstile } from "./Turnstile";
import { CommunityContact } from "./CommunityContact";
import { PortalBrand } from "./PortalBrand";
export function Auth({
  onSession,
  initialRegister = false,
}: {
  onSession: (session: UserSessionResponse) => void;
  initialRegister?: boolean;
}) {
  const config = useUserData<PortalConfig>("/config", 60000);
  const [register, setRegister] = useState(initialRegister),
    [email, setEmail] = useState(""),
    [password, setPassword] = useState(""),
    [confirm, setConfirm] = useState("");
  const [token, setToken] = useState(""),
    [attempt, setAttempt] = useState(0),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const ready =
    config.data?.portalEnabled &&
    config.data?.turnstileConfigured &&
    !!config.data.turnstileSiteKey;
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!ready || !token || busy) return;
    if (register && passwordError(password)) {
      setError(passwordError(password));
      return;
    }
    if (register && password !== confirm) {
      setError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const session = await userAPI<UserSessionResponse>(
        register ? "/register" : "/login",
        "POST",
        { email: email.trim(), password, turnstileToken: token },
      );
      setUserCSRF(session.csrfToken || "");
      setPassword("");
      setConfirm("");
      onSession(session);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setToken("");
      setAttempt((v) => v + 1);
      setBusy(false);
    }
  }
  return (
    <div className="portal-auth">
      <section className="portal-auth-art">
        <PortalBrand />
        <div className="portal-auth-copy">
          <span className="eyebrow">A LITTLE CLOSER TO EVERYWHERE</span>
          <h1>
            你的世界，
            <br />
            <span>自由连接。</span>
          </h1>
          <p>
            从一个账户出发，连接属于你的网络。
            <br />
            订阅、节点、每一份用量，清晰可见。
          </p>
          <div className="portal-orbits" aria-hidden="true">
            <span />
            <span />
            <span />
            <div className="portal-orbit-center">H</div>
            <i className="portal-orbit-point one">
              <Globe2 />
            </i>
            <i className="portal-orbit-point two">
              <Wifi />
            </i>
            <i className="portal-orbit-point three">
              <Monitor />
            </i>
          </div>
        </div>
        <div className="portal-auth-bottom">
          <ShieldCheck size={16} /> 加密连接 <span>·</span> 一个账户，多端使用
        </div>
      </section>
      <main className="portal-auth-main">
        <div className="portal-mobile-brand">
          <PortalBrand />
        </div>
        <div className="portal-auth-card">
          <div className="portal-icon">
            <UserRound size={23} />
          </div>
          <h2>{register ? "创建你的账户" : "欢迎回来"}</h2>
          <p>
            {register
              ? "注册后选择订阅，付款成功后自动开通。"
              : "登录后，继续你的连接。"}
          </p>
          <CommunityContact compact />
          <div className="portal-auth-tabs">
            <button
              type="button"
              className={!register ? "active" : ""}
              onClick={() => {
                setRegister(false);
                setError("");
              }}
            >
              登录
            </button>
            <button
              type="button"
              className={register ? "active" : ""}
              onClick={() => {
                setRegister(true);
                setError("");
              }}
            >
              注册
            </button>
          </div>
          {config.loading ? (
            <Loading />
          ) : config.error ? (
            <ErrorNotice
              message={config.error}
              retry={() => void config.refresh()}
            />
          ) : (
            <form className="form-stack" onSubmit={submit}>
              {!config.data?.portalEnabled ? (
                <Notice tone="warning">
                  用户门户尚未启用，请联系管理员。
                </Notice>
              ) : !ready ? (
                <Notice tone="warning">
                  用户登录的安全验证尚未配置完成，请联系管理员。
                </Notice>
              ) : null}
              {config.data?.recoveryHold && (
                <Notice tone="warning">
                  备份恢复核对中，暂时不能建立新连接。
                </Notice>
              )}
              {register && !config.data?.registrationEnabled && (
                <Notice tone="warning">目前暂不开放新账户注册。</Notice>
              )}
              <Field label="邮箱">
                <input
                  type="email"
                  autoComplete="username"
                  required
                  maxLength={254}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                />
              </Field>
              <Field
                label="密码"
                hint={
                  register ? "至少 6 个字符，建议使用独立密码。" : undefined
                }
              >
                <input
                  type="password"
                  autoComplete={register ? "new-password" : "current-password"}
                  required
                  minLength={register ? 6 : undefined}
                  maxLength={1024}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder={register ? "设置账户密码" : "输入你的密码"}
                />
              </Field>
              {register && (
                <Field label="确认密码">
                  <input
                    type="password"
                    autoComplete="new-password"
                    required
                    minLength={6}
                    maxLength={1024}
                    value={confirm}
                    onChange={(e) => setConfirm(e.target.value)}
                    placeholder="再次输入密码"
                  />
                </Field>
              )}
              {ready && (
                <Turnstile
                  siteKey={config.data!.turnstileSiteKey}
                  action={register ? "register" : "login"}
                  attempt={attempt}
                  onToken={setToken}
                />
              )}
              <ErrorNotice message={error} />
              <Button
                className="full-width"
                type="submit"
                icon={ArrowRight}
                busy={busy}
                disabled={
                  !ready ||
                  !token ||
                  (register && !config.data?.registrationEnabled)
                }
              >
                {register ? "创建账户" : "登录账户"}
              </Button>
              <p className="portal-auth-note">
                {register
                  ? "邮箱用作登录名，当前不发送验证邮件。请妥善保存密码。"
                  : "忘记密码？请联系管理员重置。"}
              </p>
            </form>
          )}
          <div className="portal-auth-trust">
            <ShieldCheck size={14} /> Cloudflare 安全验证保护
          </div>
        </div>
        <footer>
          HQIP <span>连接，从这里开始。</span>
          <a href={ADMIN_URL}>
            管理入口 <ArrowUpRight size={12} />
          </a>
        </footer>
      </main>
    </div>
  );
}
