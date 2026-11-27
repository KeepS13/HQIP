import type { Session } from "./types";
import { useState, type FormEvent } from "react";
import { ArrowRight, Eye, EyeOff, LockKeyhole, Monitor, Server, ShieldCheck, Wifi } from "lucide-react";
import { api, setCSRF } from "./api";
import { Button, ErrorNotice, Field } from "./components";
import { Brand } from "./Brand";
import { PasswordForm } from "./PasswordForm";
export function Login({
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
