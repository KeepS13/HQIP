import { ADMIN_URL, passwordError } from "../config";
import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { Link, NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  ArrowRight,
  ArrowUpRight,
  CircleHelp,
  Download,
  Globe2,
  Info,
  LayoutDashboard,
  Link2,
  LockKeyhole,
  LogOut,
  Menu,
  Monitor,
  ReceiptText,
  ShieldCheck,
  UserRound,
  Wifi,
  X,
} from "lucide-react";
import {
  Button,
  CopyButton,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Notice,
  PageHeader,
  Panel,
  ToastContext,
  bytes,
  date,
} from "../components";
import { setUserCSRF, userAPI, useUserData } from "./api";
import { Turnstile } from "./Turnstile";
import type {
  PortalConfig,
  Release,
  UserProfile,
  UserSessionResponse,
} from "./types";
import { AccountSettings } from "./AccountSettings";
import {
  OwnedSubscriptions,
  UserOverview,
  UserSubscriptionDetail,
} from "./SubscriptionPages";
import { SubscriptionCatalog } from "./Commerce";
import { UserOrderDetail, UserOrders } from "./Orders";
import "./portal.css";

export function PortalBrand() {
  return (
    <div className="brand">
      <span className="brand-symbol">
        <span>H</span>
        <i />
      </span>
      <div>
        <strong>
          HQIP<span> / </span>
        </strong>
        <small>YOUR CONNECTION</small>
      </div>
    </div>
  );
}
function Auth({
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
              {!ready && (
                <Notice tone="warning">
                  用户入口尚未准备好，请稍后重试或联系管理员。
                </Notice>
              )}
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
function Downloads() {
  const releases = useUserData<{ releases: Release[] }>("/releases", 60000);
  return (
    <>
      <PageHeader
        eyebrow="HQIP CLIENT"
        title="每一台设备，轻松连接"
        description="下载已发布的官方客户端，在系统浏览器登录你的账户。"
      />
      <div className="portal-download-hero">
        <div className="portal-download-symbol">H</div>
        <div>
          <span className="eyebrow">DESIGNED FOR YOUR EVERYDAY</span>
          <h2>HQIP 客户端</h2>
          <p>一键连接 · 规则分流 · 安全登录</p>
        </div>
        <ShieldCheck size={40} strokeWidth={1} />
      </div>
      <ErrorNotice
        message={releases.error}
        retry={() => void releases.refresh()}
      />
      <Panel title="已发布版本" subtitle="下载地址与校验值由管理员发布">
        {releases.loading ? (
          <Loading />
        ) : releases.data?.releases.length ? (
          <div className="portal-release-list">
            {releases.data.releases.map((release, index) => {
              const safe =
                /^https:\/\//.test(release.url) ||
                (release.url.startsWith("/") && !release.url.startsWith("//"));
              return (
                <div key={`${release.platform}-${index}`}>
                  <Monitor size={25} />
                  <section>
                    <h3>
                      {release.name ||
                        (release.platform === "windows-amd64"
                          ? "Windows 64 位"
                          : release.platform)}
                    </h3>
                    <p>
                      版本 {release.version}
                      {release.size ? ` · ${bytes(release.size)}` : ""}
                    </p>
                    {release.sha256 && (
                      <details>
                        <summary>SHA-256 校验值</summary>
                        <code>{release.sha256}</code>
                        <CopyButton value={release.sha256} label="复制校验值" />
                      </details>
                    )}
                  </section>
                  {safe ? (
                    <a
                      href={release.url}
                      className="button button-primary"
                      rel="noreferrer"
                    >
                      <Download size={16} /> 下载
                    </a>
                  ) : (
                    <span className="muted">下载地址暂不可用</span>
                  )}
                </div>
              );
            })}
          </div>
        ) : (
          !releases.error && (
            <Empty
              icon={Download}
              title="客户端版本即将发布"
              description="管理员发布安装包后，可在这里下载。你也可以使用兼容的第三方订阅客户端。"
              action={
                <Link className="button button-secondary" to="/subscriptions">
                  前往订阅 <ArrowRight size={15} />
                </Link>
              }
            />
          )
        )}
      </Panel>
      <Notice>
        官方客户端会打开系统浏览器完成登录。请核对网站地址，授权后会自动返回客户端。
      </Notice>
    </>
  );
}
function Authorize() {
  const request =
    new URLSearchParams(useLocation().search).get("request") || "";
  const data = useUserData<{ clientName: string; expiresAt: string }>(
    `/authorize/${encodeURIComponent(request)}`,
    30000,
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [complete, setComplete] = useState(false);
  async function approve() {
    setBusy(true);
    setError("");
    try {
      const result = await userAPI<{ redirectUrl: string }>(
        `/authorize/${encodeURIComponent(request)}`,
        "POST",
        {},
      );
      const target = new URL(result.redirectUrl);
      if (
        target.protocol !== "http:" ||
        !["127.0.0.1", "[::1]"].includes(target.hostname) ||
        target.username ||
        target.password
      )
        throw new Error("客户端返回地址无效，请重新发起登录");
      setComplete(true);
      window.location.assign(target.href);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="portal-authorize">
      <span className="portal-icon">
        <LockKeyhole size={25} />
      </span>
      <h1>{complete ? "授权已完成" : "登录 HQIP 客户端"}</h1>
      {complete ? (
        <p>正在返回客户端，可以关闭此页面。</p>
      ) : (
        <>
          <p>仅授权你刚刚打开的客户端。</p>
          {!request ? (
            <ErrorNotice message="缺少授权请求，请从客户端重新发起登录" />
          ) : data.loading ? (
            <Loading />
          ) : data.error ? (
            <ErrorNotice message={data.error} />
          ) : (
            <>
              <div className="portal-authorize-client">
                <Monitor size={28} />
                <strong>{data.data?.clientName || "HQIP 客户端"}</strong>
                <small>请求有效至 {date(data.data?.expiresAt)}</small>
              </div>
              <ErrorNotice message={error} />
              <Button
                className="full-width"
                busy={busy}
                icon={ArrowRight}
                onClick={() => void approve()}
              >
                授权并返回客户端
              </Button>
            </>
          )}
          <Link className="text-link" to="/">
            取消并返回账户
          </Link>
        </>
      )}
    </div>
  );
}
function PortalHome({ onLogout }: { onLogout: () => void }) {
  const profile = useUserData<UserProfile>("/profile", 5000),
    config = useUserData<PortalConfig>("/config", 30000);
  if (!profile.data)
    return profile.loading ? (
      <Loading />
    ) : (
      <ErrorNotice
        message={profile.error}
        retry={() => void profile.refresh()}
      />
    );
  const data = {
    ...profile.data,
    subscriptions: profile.data.subscriptions || [],
    sessions: profile.data.sessions || [],
  };
  return (
    <>
      {config.data?.recoveryHold && (
        <Notice tone="warning">备份恢复核对中，暂时不能建立新连接。</Notice>
      )}
      <ErrorNotice
        message={
          profile.error ? `${profile.error}；当前显示上次成功读取的数据。` : ""
        }
        retry={() => void profile.refresh()}
      />
      <Routes>
        <Route path="/" element={<UserOverview profile={data} />} />
        <Route
          path="/catalog"
          element={<SubscriptionCatalog userId={data.user.id} />}
        />
        <Route path="/orders" element={<UserOrders />} />
        <Route path="/orders/:id" element={<UserOrderDetail />} />
        <Route path="/subscriptions" element={<OwnedSubscriptions />} />
        <Route path="/subscriptions/:id" element={<UserSubscriptionDetail />} />
        <Route path="/downloads" element={<Downloads />} />
        <Route
          path="/account"
          element={
            <AccountSettings
              profile={data}
              refresh={profile.refresh}
              onLogout={onLogout}
            />
          }
        />
        <Route path="/authorize" element={<Authorize />} />
        <Route
          path="*"
          element={
            <Empty
              title="页面不存在"
              description="返回概览，继续你的连接。"
              action={
                <Link to="/" className="button button-primary">
                  返回概览
                </Link>
              }
            />
          }
        />
      </Routes>
    </>
  );
}
export default function Portal() {
  const [session, setSession] = useState<UserSessionResponse | null>(null),
    [error, setError] = useState(""),
    [menu, setMenu] = useState(false),
    [toast, setToast] = useState(""),
    [busy, setBusy] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined),
    location = useLocation();
  const authPage = ["/login", "/register", "/authorize"].includes(
    location.pathname,
  );
  const logout = () => {
    setUserCSRF("");
    setSession({ authenticated: false });
  };
  async function load() {
    setError("");
    try {
      const result = await userAPI<UserSessionResponse>("/session");
      setUserCSRF(result.csrfToken || "");
      setSession(result);
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    document.title = "HQIP · 我的连接";
    void load();
    window.addEventListener("hqip:user-unauthorized", logout);
    return () => {
      window.removeEventListener("hqip:user-unauthorized", logout);
      clearTimeout(timer.current);
    };
  }, []);
  useEffect(() => {
    setMenu(false);
    window.scrollTo(0, 0);
  }, [location.pathname]);
  useEffect(() => {
    if (session?.authenticated === false && !authPage) {
      // Reload the document so the login page receives its Turnstile CSP.
      const returnTo = location.pathname + location.search;
      window.location.replace(
        `/login?returnTo=${encodeURIComponent(returnTo)}`,
      );
    }
  }, [session, authPage, location.pathname, location.search]);
  const notify = (message: string) => {
    setToast(message);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setToast(""), 4500);
  };
  if (!session || (!session.authenticated && !authPage))
    return (
      <div className="boot-screen">
        <PortalBrand />
        {error ? (
          <ErrorNotice message={error} retry={() => void load()} />
        ) : (
          <Loading />
        )}
      </div>
    );
  if (!session.authenticated)
    return (
      <Auth
        initialRegister={location.pathname === "/register"}
        onSession={(value) => {
          setSession(value);
          if (["/login", "/register"].includes(location.pathname)) {
            const requested =
              new URLSearchParams(location.search).get("returnTo") || "/";
            let destination = "/";
            try {
              const target = new URL(requested, window.location.origin);
              if (
                target.origin === window.location.origin &&
                ([
                  "/",
                  "/catalog",
                  "/subscriptions",
                  "/orders",
                  "/downloads",
                  "/account",
                  "/authorize",
                ].includes(target.pathname) ||
                  /^\/(?:subscriptions|orders)\/[A-Za-z0-9_-]+$/.test(
                    target.pathname,
                  ))
              )
                destination = target.pathname + target.search;
            } catch {
              /* Invalid return URLs use the account overview. */
            }
            window.location.replace(destination);
          }
        }}
      />
    );
  const links: { to: string; name: string; icon: ReactNode }[] = [
    { to: "/", name: "账户概览", icon: <LayoutDashboard size={19} /> },
    { to: "/catalog", name: "订阅商城", icon: <Globe2 size={19} /> },
    { to: "/subscriptions", name: "我的订阅", icon: <Link2 size={19} /> },
    { to: "/orders", name: "我的订单", icon: <ReceiptText size={19} /> },
    { to: "/downloads", name: "客户端下载", icon: <Download size={19} /> },
    { to: "/account", name: "账户设置", icon: <UserRound size={19} /> },
  ];
  return (
    <ToastContext.Provider value={notify}>
      <div className="app-shell portal-shell">
        {menu && (
          <div className="sidebar-scrim" onClick={() => setMenu(false)} />
        )}
        <aside className={`sidebar ${menu ? "sidebar-open" : ""}`}>
          <Link to="/" className="brand-link">
            <PortalBrand />
          </Link>
          <button
            className="icon-button mobile-close"
            aria-label="关闭导航"
            onClick={() => setMenu(false)}
          >
            <X size={22} />
          </button>
          <div className="portal-workspace">
            <span className="portal-account-avatar">
              {session.user?.email[0]?.toUpperCase() || "U"}
            </span>
            <div>
              <strong>我的账户</strong>
              <small title={session.user?.email}>{session.user?.email}</small>
            </div>
          </div>
          <div className="nav-caption">我的空间</div>
          <nav>
            {links.map((link) => (
              <NavLink key={link.to} to={link.to} end={link.to === "/"}>
                {link.icon}
                <span>{link.name}</span>
              </NavLink>
            ))}
          </nav>
          <div className="sidebar-bottom">
            <div className="portal-support">
              <CircleHelp size={20} />
              <strong>需要帮助？</strong>
              <p>
                订阅开通与密码重置，
                <br />
                请联系你的管理员。
              </p>
            </div>
            <div className="sidebar-version">
              <span>
                <span className="live-dot" /> HQIP NETWORK
              </span>
              <ShieldCheck size={14} />
            </div>
          </div>
        </aside>
        <div className="app-main">
          <header className="topbar">
            <div className="topbar-title">
              <button
                className="icon-button mobile-menu"
                aria-label="打开导航"
                onClick={() => setMenu(true)}
              >
                <Menu size={21} />
              </button>
              <span>我的空间</span>
              <span className="topbar-slash">/</span>
              <strong>
                {links.find((link) => link.to === location.pathname)?.name ||
                  (location.pathname.startsWith("/subscriptions/")
                    ? "订阅详情"
                    : location.pathname.startsWith("/orders/")
                      ? "订单详情"
                      : "客户端授权")}
              </strong>
            </div>
            <div className="topbar-right">
              <span className="portal-secure">
                <ShieldCheck size={14} /> 安全连接
              </span>
              <span className="topbar-divider" />
              <button
                className="icon-button"
                aria-label="退出账户"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await userAPI("/logout", "POST", {});
                    logout();
                  } catch (e) {
                    notify((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                <LogOut size={18} />
              </button>
            </div>
          </header>
          <main className="page-content portal-content">
            <PortalHome onLogout={logout} />
            <footer className="page-footer">
              <span>HQIP · 你的世界，自由连接</span>
              <span>流量统计：上传 + 下载 · UTC+8</span>
            </footer>
          </main>
        </div>
      </div>
      {toast && (
        <div className="toast" role="status">
          <Info size={18} />
          {toast}
        </div>
      )}
    </ToastContext.Provider>
  );
}
