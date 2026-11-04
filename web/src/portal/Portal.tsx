import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  Link,
  NavLink,
  Route,
  Routes,
  useLocation,
} from "react-router-dom";
import {
  Activity,
  ArrowDownToLine,
  ArrowRight,
  ArrowUpRight,
  Cable,
  Check,
  CircleHelp,
  Clock3,
  Download,
  Gauge,
  Globe2,
  Info,
  KeyRound,
  LayoutDashboard,
  Link2,
  LockKeyhole,
  LogOut,
  Menu,
  Monitor,
  RefreshCw,
  ShieldCheck,
  Unplug,
  UserRound,
  Wifi,
  X,
} from "lucide-react";
import {
  Badge,
  Button,
  CopyButton,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Meter,
  Modal,
  Notice,
  PageHeader,
  Panel,
  ToastContext,
  bytes,
  date,
  useToast,
} from "../components";
import { setUserCSRF, userAPI, useUserData } from "./api";
import { Turnstile } from "./Turnstile";
import {
  connectionActive,
  connectionState,
  normalizeProfile,
  type Entitlement,
  type PortalConfig,
  type Release,
  type UserProfile,
  type UserProfileResponse,
  type UserSessionResponse,
} from "./types";
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
export const entitlementLabels: Record<string, string> = {
  active: "使用中",
  expired: "已到期",
  exhausted: "流量已用尽",
  unassigned: "待开通",
  disabled: "已停用",
};
export function EntitlementBadge({ value }: { value?: Entitlement }) {
  return (
    <Badge
      tone={
        value?.status === "active"
          ? "success"
          : value?.status === "unassigned"
            ? "muted"
            : "warning"
      }
    >
      {entitlementLabels[value?.status || "unassigned"] || "待同步"}
    </Badge>
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
            套餐、节点、每一份用量，清晰可见。
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
              ? "注册后，联系管理员开通适合你的套餐。"
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
                  register ? "至少 12 个字符，建议使用独立密码。" : undefined
                }
              >
                <input
                  type="password"
                  autoComplete={register ? "new-password" : "current-password"}
                  required
                  minLength={register ? 12 : undefined}
                  maxLength={128}
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
                    minLength={12}
                    maxLength={128}
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
          <a href="/admin">
            管理入口 <ArrowUpRight size={12} />
          </a>
        </footer>
      </main>
    </div>
  );
}
function Summary({ profile }: { profile: UserProfile }) {
  const entitlement = profile.entitlement;
  const assigned = !!entitlement.planId;
  const remaining =
    entitlement.quotaBytes === null
      ? null
      : Math.max(0, entitlement.quotaBytes - entitlement.usedBytes);
  return (
    <>
      <div className="portal-welcome">
        <div>
          <span className="eyebrow">YOUR CONNECTION, AT A GLANCE</span>
          <h1>连接世界，从容一点。</h1>
          <p>你的套餐与连接状态，都在这里。</p>
        </div>
        <div className="portal-welcome-icon">
          <Globe2 size={64} strokeWidth={1} />
          <span />
          <i />
        </div>
      </div>
      <div className="portal-summary-grid">
        <section className="portal-plan-card">
          <div className="portal-card-label">
            <span>
              <ShieldCheck size={17} /> 我的套餐
            </span>
            <EntitlementBadge value={entitlement} />
          </div>
          <h2>{entitlement.planName || "还没有套餐"}</h2>
          <p>
            {entitlement.status === "unassigned"
              ? "联系管理员开通后，即可开始连接。"
              : `有效期至 ${date(entitlement.expiresAt)}`}
          </p>
          <div className="portal-plan-footer">
            <span>
              <Globe2 size={15} /> {profile.nodes.length} 个可用节点
            </span>
            <Link to="/nodes">
              查看节点 <ArrowRight size={15} />
            </Link>
          </div>
        </section>
        <section className="portal-usage-card">
          <div className="portal-card-label">
            <span>
              <Activity size={17} /> 一次性流量
            </span>
            <span>上传 + 下载</span>
          </div>
          <div className="portal-usage-number">
            {!assigned
              ? "待开通"
              : remaining === null
                ? "不限量"
                : bytes(remaining)}
            <small>
              {!assigned
                ? "流量额度"
                : remaining === null
                  ? "流量额度"
                  : "剩余额度"}
            </small>
          </div>
          <Meter value={entitlement.usedBytes} max={entitlement.quotaBytes} />
          <div className="portal-usage-footer">
            <span>已用 {bytes(entitlement.usedBytes)}</span>
            <span>
              共{" "}
              {!assigned
                ? "—"
                : entitlement.quotaBytes === null
                  ? "不限量"
                  : bytes(entitlement.quotaBytes)}
            </span>
          </div>
          <p>
            额度不按月重置
            {entitlement.reservedBytes > 0
              ? ` · ${bytes(entitlement.reservedBytes)} 正在连接中预留`
              : ""}
          </p>
        </section>
      </div>
      <div className="portal-mini-grid">
        <div>
          <span className="portal-mini-icon">
            <Gauge />
          </span>
          <section>
            <span>共享带宽</span>
            <strong>
              {!assigned
                ? "待开通"
                : entitlement.bandwidthMbps === null
                  ? "不限速"
                  : `${entitlement.bandwidthMbps} Mbps`}
            </strong>
            <small>所有设备与节点共享</small>
          </section>
        </div>
        <div>
          <span className="portal-mini-icon">
            <Cable />
          </span>
          <section>
            <span>连接名额</span>
            <strong>
              {profile.entitlement.occupied}
              <em> / {entitlement.maxConnections || 0}</em>
            </strong>
            <small>每个 VPN 连接占一个名额</small>
          </section>
        </div>
        <div>
          <span className="portal-mini-icon">
            <Clock3 />
          </span>
          <section>
            <span>有效期</span>
            <strong className="portal-expiry">
              {entitlement.expiresAt ? date(entitlement.expiresAt) : "等待开通"}
            </strong>
            <small>到期或流量用尽后停止服务</small>
          </section>
        </div>
      </div>
      {entitlement.status !== "active" && (
        <Notice tone="warning">
          {entitlement.status === "unassigned"
            ? "账户已准备好。请联系管理员分配套餐并设置有效期。"
            : `当前套餐${entitlementLabels[entitlement.status] || "不可用"}，请联系管理员处理后再连接。`}
        </Notice>
      )}
      <Panel
        title="下一次连接，很简单"
        subtitle="使用官方客户端，或导入第三方订阅"
      >
        <div className="portal-start-grid">
          <Link to="/downloads">
            <span>
              <Monitor size={25} />
            </span>
            <div>
              <h3>下载 HQIP 客户端</h3>
              <p>在浏览器安全登录，一键连接节点。</p>
            </div>
            <ArrowUpRight size={19} />
          </Link>
          <Link to="/subscriptions">
            <span>
              <Link2 size={25} />
            </span>
            <div>
              <h3>使用订阅连接</h3>
              <p>支持 Clash / Mihomo，按你的习惯连接。</p>
            </div>
            <ArrowUpRight size={19} />
          </Link>
        </div>
      </Panel>
      <Panel title="连接须知">
        <div className="portal-help-grid">
          <div>
            <Check size={17} />
            <p>登录账户不会占用连接名额；建立 VPN 连接才会计数。</p>
          </div>
          <div>
            <Check size={17} />
            <p>达到连接上限时，新连接会被拒绝。可在账户页面断开已有连接。</p>
          </div>
          <div>
            <Check size={17} />
            <p>节点与控制服务失联超过 120 秒后，连接停止；恢复后可重新连接。</p>
          </div>
        </div>
      </Panel>
    </>
  );
}
function UserNodes({ profile }: { profile: UserProfile }) {
  return (
    <>
      <PageHeader
        eyebrow="YOUR NODES"
        title="我的节点"
        description="套餐包含的可用节点。默认节点仅在当前选择被移除时接替。"
      />
      {profile.nodes.length ? (
        <div className="portal-node-grid">
          {profile.nodes.map((node) => (
            <section className="portal-node-card" key={node.id}>
              <div className="portal-node-top">
                <span className="portal-icon">
                  <Globe2 size={22} />
                </span>
                <Badge tone={node.ready ? "success" : "warning"}>
                  {node.ready ? "可以连接" : "同步中"}
                </Badge>
              </div>
              <h2>{node.name}</h2>
              <p>
                {node.id === profile.entitlement.defaultNodeId
                  ? "套餐默认节点"
                  : "套餐可选节点"}
              </p>
              <div className="portal-node-bottom">
                <ShieldCheck size={14} /> Hysteria 2 · 加密连接
              </div>
            </section>
          ))}
        </div>
      ) : (
        <Panel title="可用节点">
          <Empty
            icon={Globe2}
            title="暂时没有可用节点"
            description="开通套餐并完成节点配置后，它们会显示在这里。"
          />
        </Panel>
      )}
    </>
  );
}
function ConfirmAction({
  title,
  description,
  onClose,
  action,
  label = "确认",
}: {
  title: string;
  description: string;
  onClose: () => void;
  action: () => Promise<void>;
  label?: string;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title={title}
      description={description}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <ErrorNotice message={error} />
      <div className="modal-actions">
        <Button variant="secondary" disabled={busy} onClick={onClose}>
          取消
        </Button>
        <Button
          variant="danger"
          busy={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await action();
              onClose();
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          {label}
        </Button>
      </div>
    </Modal>
  );
}
function Subscriptions({
  profile,
  refresh,
}: {
  profile: UserProfile;
  refresh: () => Promise<void>;
}) {
  const [confirm, setConfirm] = useState<"rotate" | "revoke" | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const toast = useToast(),
    url = profile.subscriptionUrl || "";
  async function create() {
    setBusy(true);
    setError("");
    try {
      await userAPI("/subscription", "POST", {});
      await refresh();
      toast("订阅已创建");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const clash = `clash://install-config?url=${encodeURIComponent(url)}&name=${encodeURIComponent("HQIP")}`;
  return (
    <>
      <PageHeader
        eyebrow="SUBSCRIPTIONS"
        title="我的订阅"
        description="将套餐内的节点导入你喜欢的客户端。"
      />
      <ErrorNotice message={error} />
      <Panel
        title="Clash / Mihomo 订阅"
        subtitle="订阅链接包含连接凭据，请妥善保存"
        action={
          <Badge tone={url ? "success" : "muted"}>
            {url ? "已创建" : "未创建"}
          </Badge>
        }
      >
        {url ? (
          <div className="portal-subscription">
            <label htmlFor="subscription-url">你的专属订阅链接</label>
            <input
              id="subscription-url"
              className="mono"
              readOnly
              value={url}
              onFocus={(e) => e.currentTarget.select()}
            />
            <div className="portal-actions">
              <CopyButton value={url} />
              <a className="button button-primary" href={clash}>
                <ArrowDownToLine size={16} /> 一键导入 Clash
              </a>
              <Button
                variant="secondary"
                icon={RefreshCw}
                onClick={() => setConfirm("rotate")}
              >
                轮换链接
              </Button>
              <Button variant="ghost" onClick={() => setConfirm("revoke")}>
                撤销订阅
              </Button>
            </div>
            <Notice>
              导入后请在客户端更新订阅，再选择节点。共享链接会消耗你的流量与连接名额。
            </Notice>
          </div>
        ) : (
          <Empty
            icon={Link2}
            title="为你的客户端创建订阅"
            description="兼容支持 Hysteria 2 的 Clash / Mihomo 客户端。"
            action={
              <Button
                busy={busy}
                icon={Link2}
                disabled={profile.entitlement.status !== "active"}
                onClick={() => void create()}
              >
                创建订阅
              </Button>
            }
          />
        )}
      </Panel>
      <Panel title="开始使用">
        <ol className="portal-steps">
          <li>
            <span>01</span>
            <div>
              <h3>安装兼容客户端</h3>
              <p>选择支持 Hysteria 2 的 Clash / Mihomo 客户端。</p>
            </div>
          </li>
          <li>
            <span>02</span>
            <div>
              <h3>导入你的订阅</h3>
              <p>点击一键导入，或复制链接粘贴到客户端的订阅设置。</p>
            </div>
          </li>
          <li>
            <span>03</span>
            <div>
              <h3>选择节点，连接网络</h3>
              <p>连接上限、共享带宽和总流量与官方客户端共同计算。</p>
            </div>
          </li>
        </ol>
      </Panel>
      {confirm && (
        <ConfirmAction
          title={confirm === "rotate" ? "轮换订阅链接" : "撤销订阅"}
          description={
            confirm === "rotate"
              ? "旧订阅链接与对应凭据将失效。请将新链接重新导入第三方客户端。"
              : "订阅链接与对应连接权限将失效。之后可以重新创建订阅。"
          }
          label={confirm === "rotate" ? "确认轮换" : "撤销订阅"}
          onClose={() => setConfirm(null)}
          action={async () => {
            await userAPI(`/subscription/${confirm}`, "POST", {});
            await refresh();
            toast(
              confirm === "rotate" ? "新订阅已生成，请重新导入" : "订阅已撤销",
            );
          }}
        />
      )}
    </>
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
        <Field label="新密码" hint="至少 12 个字符">
          <input
            type="password"
            required
            minLength={12}
            maxLength={128}
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
function Account({
  profile,
  refresh,
  onLogout,
}: {
  profile: UserProfile;
  refresh: () => Promise<void>;
  onLogout: () => void;
}) {
  const [password, setPassword] = useState(false),
    [confirm, setConfirm] = useState<{
      path: string;
      title: string;
      description: string;
    } | null>(null);
  const toast = useToast();
  return (
    <>
      <PageHeader
        eyebrow="ACCOUNT & SECURITY"
        title="账户与连接"
        description="管理登录记录、密码和当前 VPN 连接。"
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
      <Panel
        title="VPN 连接记录"
        subtitle={`占用 ${profile.entitlement.occupied} / ${profile.entitlement.maxConnections || 0} 个名额；断开中连接确认关闭前仍占用名额`}
      >
        {profile.connections.length ? (
          <div className="portal-records">
            {profile.connections.map((connection) => (
              <div key={connection.id}>
                <span className="portal-record-icon">
                  <Wifi size={19} />
                </span>
                <section>
                  <strong>
                    {connection.nodeName ||
                      profile.nodes.find((n) => n.id === connection.nodeId)
                        ?.name ||
                      "节点连接"}
                  </strong>
                  <p>建立于 {date(connection.createdAt)}</p>
                </section>
                <Badge
                  tone={
                    connectionActive(connection)
                      ? connection.ratePending
                        ? "warning"
                        : "success"
                      : "muted"
                  }
                >
                  {connectionState(connection)}
                </Badge>
                <Button
                  variant="secondary"
                  disabled={!connectionActive(connection)}
                  icon={Unplug}
                  onClick={() =>
                    setConfirm({
                      path: `/connections/${encodeURIComponent(connection.id)}/revoke`,
                      title: "断开这个连接",
                      description:
                        "连接将被撤销。客户端可能尝试重新连接；如需停止自动重连，请同时关闭客户端。",
                    })
                  }
                >
                  断开
                </Button>
              </div>
            ))}
          </div>
        ) : (
          <Empty
            icon={Wifi}
            title="当前没有 VPN 连接"
            description="从官方客户端或订阅客户端连接后，会显示在这里。"
          />
        )}
      </Panel>
      <Panel title="登录会话" subtitle="退出不再使用的设备，保护你的账户">
        {profile.sessions.length ? (
          <div className="portal-records">
            {profile.sessions.map((session) => (
              <div key={session.id}>
                <span className="portal-record-icon">
                  <Monitor size={19} />
                </span>
                <section>
                  <strong>
                    {session.clientName ||
                      (session.kind === "browser"
                        ? "网页浏览器"
                        : "HQIP 客户端")}
                  </strong>
                  <p>最近活动 {date(session.lastSeen)}</p>
                </section>
                {session.current && <Badge tone="info">当前会话</Badge>}
                {session.revoked && <Badge tone="muted">已退出</Badge>}
                <Button
                  variant="ghost"
                  disabled={session.revoked}
                  onClick={() =>
                    setConfirm({
                      path: `/sessions/${encodeURIComponent(session.id)}/revoke`,
                      title: "退出这个会话",
                      description: session.current
                        ? "将退出当前浏览器。再次访问需要重新登录。"
                        : "这个客户端需要重新登录才能访问账户。",
                    })
                  }
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
            description="登录信息同步后会显示在这里。"
          />
        )}
      </Panel>
      <Notice>邮箱当前只作为登录名。忘记密码时，请联系管理员重置。</Notice>
      {password && (
        <Password
          onClose={() => setPassword(false)}
          onDone={() => {
            setPassword(false);
            onLogout();
          }}
        />
      )}
      {confirm && (
        <ConfirmAction
          {...confirm}
          onClose={() => setConfirm(null)}
          action={async () => {
            await userAPI(confirm.path, "POST", {});
            await refresh();
            toast("操作已完成");
          }}
        />
      )}
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
  const profile = useUserData<UserProfileResponse>("/profile");
  const config = useUserData<PortalConfig>("/config", 30000);
  if (profile.loading) return <Loading />;
  if (!profile.data)
    return (
      <ErrorNotice
        message={profile.error}
        retry={() => void profile.refresh()}
      />
    );
  const data = normalizeProfile(profile.data);
  return (
    <>
      {config.data?.recoveryHold && (
        <Notice tone="warning">
          备份恢复核对中，暂停新连接。管理员完成核对后即可恢复使用。
        </Notice>
      )}
      <ErrorNotice
        message={
          profile.error ? `${profile.error}；当前显示上次成功读取的数据。` : ""
        }
        retry={() => void profile.refresh()}
      />
      <Routes>
        <Route path="/" element={<Summary profile={data} />} />
        <Route path="/nodes" element={<UserNodes profile={data} />} />
        <Route
          path="/subscriptions"
          element={<Subscriptions profile={data} refresh={profile.refresh} />}
        />
        <Route path="/downloads" element={<Downloads />} />
        <Route
          path="/account"
          element={
            <Account
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
  const authPage = ["/login", "/register", "/authorize"].includes(location.pathname);
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
      window.location.replace(`/login?returnTo=${encodeURIComponent(returnTo)}`);
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
            const requested = new URLSearchParams(location.search).get("returnTo") || "/";
            let destination = "/";
            try {
              const target = new URL(requested, window.location.origin);
              if (
                target.origin === window.location.origin &&
                ["/", "/nodes", "/subscriptions", "/downloads", "/account", "/authorize"].includes(target.pathname)
              ) destination = target.pathname + target.search;
            } catch { /* Invalid return URLs use the account overview. */ }
            window.location.replace(destination);
          }
        }}
      />
    );
  const links: { to: string; name: string; icon: ReactNode }[] = [
    { to: "/", name: "账户概览", icon: <LayoutDashboard size={19} /> },
    { to: "/nodes", name: "我的节点", icon: <Globe2 size={19} /> },
    { to: "/subscriptions", name: "我的订阅", icon: <Link2 size={19} /> },
    { to: "/downloads", name: "客户端下载", icon: <Download size={19} /> },
    { to: "/account", name: "账户与连接", icon: <UserRound size={19} /> },
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
                套餐开通与密码重置，
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
                  "客户端授权"}
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
