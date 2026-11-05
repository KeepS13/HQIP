import { useState, type FormEvent } from "react";
import { KeyRound, Monitor } from "lucide-react";
import {
  Badge,
  Button,
  Empty,
  ErrorNotice,
  Field,
  Modal,
  Notice,
  PageHeader,
  Panel,
  date,
  useToast,
} from "../components";
import { passwordError } from "../config";
import { userAPI } from "./api";
import type { UserProfile, UserSession } from "./types";
function Password({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: () => void;
}) {
  const [current, setCurrent] = useState(""),
    [password, setPassword] = useState(""),
    [confirm, setConfirm] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (passwordError(password)) {
      setError(passwordError(password));
      return;
    }
    if (password !== confirm) {
      setError("两次输入的新密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await userAPI("/password", "POST", {
        currentPassword: current,
        newPassword: password,
      });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title="修改账户密码"
      description="修改后会撤销登录会话，请使用新密码重新登录。"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={submit}>
        <Field label="当前密码">
          <input
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </Field>
        <Field label="新密码" hint="至少 6 个字符">
          <input
            type="password"
            required
            minLength={6}
            maxLength={1024}
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field label="确认新密码">
          <input
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </Field>
        <ErrorNotice message={error} />
        <Button type="submit" busy={busy}>
          保存新密码
        </Button>
      </form>
    </Modal>
  );
}

export function AccountSettings({
  profile,
  refresh,
  onLogout,
}: {
  profile: UserProfile;
  refresh: () => Promise<void>;
  onLogout: () => void;
}) {
  const [password, setPassword] = useState(false),
    [session, setSession] = useState<UserSession | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const toast = useToast();
  return (
    <>
      <PageHeader
        eyebrow="ACCOUNT & SECURITY"
        title="账户设置"
        description="管理账户密码与登录会话。"
      />
      <Panel title="账户信息">
        <div className="portal-account-row">
          <span className="portal-account-avatar">
            {profile.user.email[0]?.toUpperCase()}
          </span>
          <div>
            <strong>{profile.user.email}</strong>
            <p>注册于 {date(profile.user.createdAt)}</p>
          </div>
          <Button
            variant="secondary"
            icon={KeyRound}
            onClick={() => setPassword(true)}
          >
            修改密码
          </Button>
        </div>
      </Panel>
      <Panel title="登录会话" subtitle="退出不再使用的设备，保护你的账户">
        {profile.sessions?.length ? (
          <div className="portal-records">
            {profile.sessions.map((s) => (
              <div key={s.id}>
                <span className="portal-record-icon">
                  <Monitor size={19} />
                </span>
                <section>
                  <strong>
                    {s.clientName ||
                      (s.kind === "browser" ? "网页浏览器" : "HQIP 客户端")}
                  </strong>
                  <p>最近活动 {date(s.lastSeen)}</p>
                </section>
                {s.current && <Badge tone="info">当前会话</Badge>}
                {s.revoked && <Badge tone="muted">已退出</Badge>}
                <Button
                  variant="ghost"
                  disabled={s.revoked}
                  onClick={() => {
                    setError("");
                    setSession(s);
                  }}
                >
                  退出
                </Button>
              </div>
            ))}
          </div>
        ) : (
          <Empty
            icon={Monitor}
            title="暂无登录记录"
            description="登录信息同步后显示在这里。"
          />
        )}
      </Panel>
      <Notice>邮箱仅作为登录名。忘记密码时，请联系管理员重置。</Notice>
      {password && (
        <Password
          onClose={() => setPassword(false)}
          onDone={() => {
            setPassword(false);
            onLogout();
          }}
        />
      )}
      {session && (
        <Modal
          title="退出这个登录会话"
          description={
            session.current
              ? "将退出当前浏览器。再次访问需要重新登录。"
              : "所选客户端需要重新登录才能继续访问。"
          }
          onClose={() => {
            if (!busy) setSession(null);
          }}
        >
          <ErrorNotice message={error} />
          <div className="modal-actions">
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => setSession(null)}
            >
              取消
            </Button>
            <Button
              busy={busy}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await userAPI(`/sessions/${session.id}/revoke`, "POST", {});
                  const current = session.current;
                  setSession(null);
                  if (current) onLogout();
                  else await refresh();
                  toast("登录会话已退出");
                } catch (e) {
                  setError((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            >
              确认退出
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}
