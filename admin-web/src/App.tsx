import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  Activity,
  ArrowRight,
  BookOpen,
  Info,
  Eye,
  EyeOff,
  LayoutDashboard,
  LoaderCircle,
  LockKeyhole,
  LogOut,
  Menu,
  Monitor,
  Server,
  ShieldCheck,
  Wifi,
  Globe2,
  X,
  UsersRound,
  Package,
  BellRing,
  CreditCard,
  ReceiptText,
} from "lucide-react";
import { api, setCSRF } from "./api";
import {
  Button,
  ErrorNotice,
  Field,
  Loading,
  Modal,
  Notice,
  ToastContext,
} from "./components";
import type { Session } from "./types";
import { Dashboard, Guide, NotFound } from "./pages/Dashboard";
import { Nodes, NodePage } from "./pages/Nodes";
import { Users, UserPage } from "./pages/Accounts";
import { TurnstileSettingsPage } from "./pages/Registration";
import { SubscriptionTemplates } from "./pages/SubscriptionTemplates";
import { SubscriptionRequests } from "./pages/SubscriptionRequests";
import { Servers, ServerPage } from "./pages/Servers";
import { Domains } from "./pages/Domains";
import { Notifications } from "./pages/Notifications";
import { OperationLogs } from "./pages/OperationLogs";
import { Payments } from "./pages/Payments";
import { TrafficBilling } from "./pages/TrafficBilling";
import { Orders, OrderDetail } from "./pages/Orders";
import { HostResources } from "./HostResources";
import { AlertUnreadBadge } from "./AlertUnread";

function Brand() {
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
        <small>PRIVATE NETWORK</small>
      </div>
    </div>
  );
}
function PasswordForm({
  onDone,
  forced = false,
}: {
  onDone: () => Promise<void>;
  forced?: boolean;
}) {
  const [current, setCurrent] = useState(""),
    [password, setPassword] = useState(""),
    [confirm, setConfirm] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError("两次输入的新密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api("/password", "POST", {
        currentPassword: current,
        newPassword: password,
      });
      await onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="form-stack" onSubmit={submit}>
      {forced && (
        <Notice>首次登录需要设置新的管理密码，完成后即可进入控制台。</Notice>
      )}
      <Field label="当前密码">
        <input
          type="password"
          autoComplete="current-password"
          required
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          placeholder="输入当前管理密码"
        />
      </Field>
      <Field label="新密码" hint="至少 12 位，建议使用字母、数字与符号的组合。">
        <input
          type="password"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={128}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="设置新的管理密码"
        />
      </Field>
      <Field label="确认新密码">
        <input
          type="password"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={128}
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder="再次输入新密码"
        />
      </Field>
      <ErrorNotice message={error} />
      <Button
        type="submit"
        busy={busy}
        icon={LockKeyhole}
        className="full-width"
      >
        {forced ? "保存并进入控制台" : "保存新密码"}
      </Button>
    </form>
  );
}
function Login({
  session,
  onSession,
}: {
  session: Session;
  onSession: (session: Session) => void;
}) {
  const [username, setUsername] = useState("admin"),
    [password, setPassword] = useState(""),
    [visible, setVisible] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function login(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const value = await api<Session>("/login", "POST", {
        username,
        password,
      });
      setCSRF(value.csrfToken || "");
      onSession(value);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="login-page">
      <div className="login-art">
        <Brand />
        <div className="login-art-copy">
          <span className="eyebrow">YOUR NETWORK, IN YOUR HANDS</span>
          <h1>
            连接世界。
            <br />
            <span>从自己的网络开始。</span>
          </h1>
          <p>部署节点，连接设备，让每一份流量清晰可见。</p>
        </div>
        <div className="network-art" aria-hidden="true">
          <div className="orbit orbit-1" />
          <div className="orbit orbit-2" />
          <div className="orbit orbit-3" />
          <div className="network-center">
            <ShieldCheck size={45} strokeWidth={1.3} />
          </div>
          <span className="orbit-node node-a">
            <Server size={21} />
          </span>
          <span className="orbit-node node-b">
            <Monitor size={21} />
          </span>
          <span className="orbit-node node-c">
            <Wifi size={21} />
          </span>
          <span className="network-label">ENCRYPTED CONNECTION</span>
        </div>
        <div className="login-art-footer">
          <span className="live-dot" /> 为私人网络而设计{" "}
          <span>HYSTERIA 2 · TLS</span>
        </div>
      </div>
      <main className="login-main">
        <div className="login-mobile-brand">
          <Brand />
        </div>
        <div className="login-card">
          <span className="login-icon">
            <LockKeyhole size={23} />
          </span>
          <h2>
            {session.mustChangePassword ? "设置新的管理密码" : "欢迎回来"}
          </h2>
          <p>
            {session.mustChangePassword
              ? "为你的私人网络设置一道新的保护。"
              : "登录 HQIP，查看你的网络运行状态。"}
          </p>
          {session.mustChangePassword ? (
            <PasswordForm
              forced
              onDone={async () => {
                const value = await api<Session>("/session");
                setCSRF(value.csrfToken || "");
                onSession(value);
              }}
            />
          ) : (
            <form className="form-stack" onSubmit={login}>
              <Field label="管理员账号">
                <input
                  value={username}
                  autoComplete="username"
                  required
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="输入管理员账号"
                />
              </Field>
              <Field label="密码">
                <div className="password-input">
                  <input
                    value={password}
                    type={visible ? "text" : "password"}
                    autoComplete="current-password"
                    required
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="输入管理密码"
                  />
                  <button
                    type="button"
                    className="icon-button"
                    aria-label={visible ? "隐藏密码" : "显示密码"}
                    onClick={() => setVisible(!visible)}
                  >
                    {visible ? <EyeOff size={17} /> : <Eye size={17} />}
                  </button>
                </div>
              </Field>
              <ErrorNotice message={error} />
              <Button
                busy={busy}
                type="submit"
                icon={ArrowRight}
                className="full-width"
              >
                登录控制台
              </Button>
            </form>
          )}
          <div className="login-note">
            <ShieldCheck size={14} />
            仅限管理员访问 · 用户请前往网站首页
          </div>
        </div>
        <div className="login-footer">
          HQIP CONSOLE <span>个人专属，始终可控。</span>
        </div>
      </main>
    </div>
  );
}

export default function App() {
  const [session, setSession] = useState<Session | null>(null),
    [error, setError] = useState(""),
    [toast, setToast] = useState(""),
    [menu, setMenu] = useState(false),
    [passwordModal, setPasswordModal] = useState(false),
    [logoutBusy, setLogoutBusy] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined),
    location = useLocation();
  async function load() {
    setError("");
    try {
      const value = await api<Session>("/session");
      setCSRF(value.csrfToken || "");
      setSession(value);
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    void load();
    const unauthorized = () => {
      setCSRF("");
      setSession({ authenticated: false });
    };
    window.addEventListener("hqip:unauthorized", unauthorized);
    return () => {
      window.removeEventListener("hqip:unauthorized", unauthorized);
      clearTimeout(timer.current);
    };
  }, []);
  useEffect(() => {
    setMenu(false);
    window.scrollTo(0, 0);
  }, [location.pathname]);
  const notify = (message: string) => {
    setToast(message);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setToast(""), 4200);
  };
  if (!session)
    return (
      <div className="boot-screen">
        <Brand />
        {error ? (
          <>
            <ErrorNotice message={error} />
            <Button onClick={() => void load()}>重新连接</Button>
          </>
        ) : (
          <Loading />
        )}
      </div>
    );
  if (!session.authenticated || session.mustChangePassword)
    return <Login session={session} onSession={setSession} />;
  const titles: Record<string, string> = {
    "/": "网络总览",
    "/nodes": "节点管理",
    "/servers": "服务器",
    "/domains": "域名与证书",
    "/users": "用户管理",
    "/subscriptions": "订阅管理",
    "/subscription-requests": "旧申请历史",
    "/orders": "订单管理",
    "/payments": "支付设置",
    "/traffic-billing": "流量计费",
    "/turnstile": "注册与安全验证",
    "/notifications": "预警中心",
    "/operation-logs": "操作日志",
    "/guide": "使用指南",
  };
  const title =
    titles[location.pathname] ||
    (location.pathname.startsWith("/nodes/")
      ? "节点详情"
      : location.pathname.startsWith("/servers/")
        ? "服务器详情"
      : location.pathname.startsWith("/users/")
        ? "用户详情"
        : location.pathname.startsWith("/orders/")
          ? "订单详情"
        : "控制台");
  return (
    <ToastContext.Provider value={notify}>
      <div className="app-shell">
        {menu && (
          <div className="sidebar-scrim" onClick={() => setMenu(false)} />
        )}
        <aside className={`sidebar ${menu ? "sidebar-open" : ""}`}>
          <Link to="/" className="brand-link" aria-label="HQIP 首页">
            <Brand />
          </Link>
          <button
            className="icon-button mobile-close"
            aria-label="关闭导航"
            onClick={() => setMenu(false)}
          >
            <X size={22} />
          </button>
          <div className="sidebar-navigation">
            <div className="nav-caption">工作空间</div>
            <nav>
              <NavLink to="/" end>
                <LayoutDashboard size={19} />
                <span>网络总览</span>
              </NavLink>
              <NavLink to="/servers">
                <Server size={19} />
                <span>服务器</span>
              </NavLink>
              <NavLink to="/nodes">
                <Server size={19} />
                <span>节点管理</span>
              </NavLink>
              <NavLink to="/users">
                <UsersRound size={19} />
                <span>用户管理</span>
              </NavLink>
              <NavLink to="/subscriptions">
                <Package size={19} />
                <span>订阅管理</span>
              </NavLink>
              <NavLink to="/orders"><ReceiptText size={19} /><span>订单管理</span></NavLink>
              <NavLink to="/operation-logs"><ShieldCheck size={19}/><span>操作日志</span></NavLink>
              <NavLink to="/notifications" className="notification-nav">
                <BellRing size={19} />
                <span>预警中心</span>
                <AlertUnreadBadge />
              </NavLink>
            </nav>
            <div className="nav-caption nav-caption-second">系统设置</div>
            <nav>
              <NavLink to="/traffic-billing"><Activity size={18} /><span>流量计费</span></NavLink>
              <NavLink to="/payments">
                <CreditCard size={19} />
                <span>支付设置</span>
              </NavLink>
              <NavLink to="/turnstile">
                <ShieldCheck size={19} />
                <span>注册与安全验证</span>
              </NavLink>
              <NavLink to="/domains">
                <Globe2 size={19} />
                <span>域名与证书</span>
              </NavLink>
            </nav>
            <div className="nav-caption nav-caption-second">支持</div>
            <nav>
              <NavLink to="/guide">
                <BookOpen size={19} />
                <span>使用指南</span>
              </NavLink>
            </nav>
          </div>
          <div className="sidebar-bottom">
            <HostResources />
            <div className="sidebar-version">
              <span>
                <span className="live-dot" />
                HQIP CONSOLE
              </span>
              <span>v1.0</span>
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
              <span>控制台</span>
              <span className="topbar-slash">/</span>
              <strong>{title}</strong>
            </div>
            <div className="topbar-right">
              <span className="update-label">
                <Activity size={13} /> {location.pathname === "/operation-logs" ? "手动刷新" : "每 5 秒刷新"}
              </span>
              <span className="topbar-divider" />
              <button
                className="profile-button"
                onClick={() => setPasswordModal(true)}
                title="修改管理密码"
              >
                <span className="avatar">A</span>
                <span>{session.username || "admin"}</span>
              </button>
              <button
                className="icon-button logout-button"
                aria-label="退出登录"
                disabled={logoutBusy}
                onClick={async () => {
                  setLogoutBusy(true);
                  try {
                    await api("/logout", "POST");
                    setSession({ authenticated: false });
                    setCSRF("");
                  } catch (e) {
                    notify((e as Error).message);
                  } finally {
                    setLogoutBusy(false);
                  }
                }}
              >
                {logoutBusy ? (
                  <LoaderCircle size={17} className="spin" />
                ) : (
                  <LogOut size={17} />
                )}
              </button>
            </div>
          </header>
          <main className={`page-content${["/servers", "/nodes"].includes(location.pathname.replace(/\/$/, "")) ? " inventory-page" : ""}`}>
            <Routes>
              <Route path="/" element={<Dashboard />} />
              <Route path="/servers" element={<Servers />} />
              <Route path="/servers/:id" element={<ServerPage />} />
              <Route path="/domains" element={<Domains />} />
              <Route path="/nodes" element={<Nodes />} />
              <Route path="/nodes/:id" element={<NodePage />} />
              <Route path="/users" element={<Users />} />
              <Route path="/users/:id" element={<UserPage />} />
              <Route path="/subscriptions" element={<SubscriptionTemplates />} />
              <Route path="/subscription-requests" element={<SubscriptionRequests />} />
              <Route path="/orders" element={<Orders />} />
              <Route path="/orders/:id" element={<OrderDetail />} />
              <Route path="/payments" element={<Payments />} />
              <Route path="/traffic-billing" element={<TrafficBilling />} />
              <Route path="/turnstile" element={<TurnstileSettingsPage />} />
              <Route path="/notifications" element={<Notifications />} />
              <Route path="/operation-logs" element={<OperationLogs />} />
              <Route path="/devices/*" element={<Navigate to="/users" replace />} />
              <Route path="/guide" element={<Guide />} />
              <Route path="*" element={<NotFound />} />
            </Routes>
            <footer className="page-footer">
              <span>HQIP · 你的私人网络控制台</span>
              <span>所有流量为代理流量 · 时区 UTC+8</span>
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
      {passwordModal && (
        <Modal title="修改管理密码" onClose={() => setPasswordModal(false)}>
          <PasswordForm
            onDone={async () => {
              await load();
              setPasswordModal(false);
              notify("管理密码已更新");
            }}
          />
        </Modal>
      )}
    </ToastContext.Provider>
  );
}
