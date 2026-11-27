import type { Session } from "./types";
import { Megaphone } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { Activity, BookOpen, Info, LayoutDashboard, LoaderCircle, LogOut, Menu, Server, ShieldCheck, Globe2, X, UsersRound, Package, BellRing, CreditCard, ReceiptText } from "lucide-react";
import { api, setCSRF } from "./api";
import { Button, ErrorNotice, Loading, Modal, ToastContext } from "./components";
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
import { Brand } from "./Brand";
import { Login } from "./Login";
import { PasswordForm } from "./PasswordForm";
import { Announcements } from "./pages/Announcements";
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
    "/announcements": "用户公告",
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
              <NavLink to="/announcements"><Megaphone size={19} /><span>用户公告</span></NavLink>
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
                <Activity size={13} /> {["/operation-logs", "/announcements"].includes(location.pathname) ? "手动刷新" : "每 5 秒刷新"}
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
              <Route path="/announcements" element={<Announcements />} />
              <Route path="/operation-logs" element={<OperationLogs />} />
              <Route path="/devices/*" element={<Navigate to="/users" replace />} />
              <Route path="/guide" element={<Guide />} />
              <Route path="*" element={<NotFound />} />
            </Routes>
            <footer className="page-footer">
              <span>HQIP · 你的私人网络控制台</span>
              <span>代理用量与整机网卡流量分别标注 · 时区 UTC+8</span>
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
