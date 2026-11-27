import type { UserSessionResponse } from "./types";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { CircleHelp, Download, FileDown, Globe2, Info, LayoutDashboard, Link2, LogOut, Menu, ReceiptText, ShieldCheck, UserRound, X } from "lucide-react";
import { ErrorNotice, Loading, ToastContext } from "../components";
import { setUserCSRF, userAPI } from "./api";
import { CommunityContact } from "./CommunityContact";
import { Auth } from "./Auth";
import { PortalBrand } from "./PortalBrand";
import { PortalHome } from "./PortalHome";
import { AnnouncementPopup } from "./AnnouncementPopup";
import "./portal.css";
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
                  "/imports",
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
    { to: "/imports", name: "复制订阅链接", icon: <FileDown size={19} /> },
    { to: "/orders", name: "我的订单", icon: <ReceiptText size={19} /> },
    { to: "/downloads", name: "客户端下载", icon: <Download size={19} /> },
    { to: "/account", name: "账户设置", icon: <UserRound size={19} /> },
  ];
  return (
    <ToastContext.Provider value={notify}>
      <AnnouncementPopup key={session.user?.id} />
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
              <Link
                to="/account"
                className="profile-button portal-header-account"
                title={session.user?.email || "账户设置"}
                aria-label={`账户设置：${session.user?.email || "我的账户"}`}
              >
                <span className="avatar" aria-hidden="true">
                  {session.user?.email[0]?.toUpperCase() || "U"}
                </span>
                <span className="portal-header-email">
                  {session.user?.email || "我的账户"}
                </span>
              </Link>
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
            <CommunityContact />
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
