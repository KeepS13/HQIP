import {
  createContext,
  lazy,
  Suspense,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link } from "react-router-dom";
import {
  ArrowDown,
  ArrowUp,
  Check,
  CheckCircle2,
  ChevronRight,
  Copy,
  Inbox,
  Info,
  LoaderCircle,
  RefreshCw,
  TriangleAlert,
  X,
  type LucideIcon,
} from "lucide-react";
import type { Node, TrafficPoint } from "./types";

export const ToastContext = createContext<(message: string) => void>(() => {});
export const useToast = () => useContext(ToastContext);
export function bytes(value = 0, precision = 1) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const index = Math.min(
    Math.floor(Math.log(value) / Math.log(1000)),
    units.length - 1,
  );
  return `${(value / 1000 ** index).toLocaleString("zh-CN", { maximumFractionDigits: precision })} ${units[index]}`;
}
export function date(value: string | number | null | undefined, short = false) {
  if (!value) return "尚无记录";
  return new Date(
    typeof value === "number" ? value * 1000 : value,
  ).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    ...(short ? {} : { year: "numeric" }),
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}
export function fresh(node: Node) {
  return Boolean(
    node.lastSeen && Date.now() - new Date(node.lastSeen).getTime() < 30000,
  );
}
export function onlineKnown(node: Node) {
  return fresh(node) && node.coreHealthy;
}
export function nodeState(node: Node): { text: string; tone: string } {
  if (node.lifecycle === "retiring") return { text: "待停止 / 清理中", tone: "warning" };
  if (node.lifecycle === "deleted") return { text: "已删除", tone: "muted" };
  if (node.status === "ready" && !fresh(node))
    return { text: "心跳失联", tone: "warning" };
  if (node.status === "ready" && !node.coreHealthy)
    return { text: "核心异常", tone: "danger" };
  return (
    (
      {
        pending: { text: "等待部署", tone: "muted" },
        installing: { text: "部署中", tone: "info" },
        verifying: { text: "验证中", tone: "info" },
        awaiting_external: { text: "待外部验证", tone: "warning" },
        ready: { text: "运行正常", tone: "success" },
        error: { text: "部署失败", tone: "danger" },
      } as Record<string, { text: string; tone: string }>
    )[node.status] || { text: "状态未知", tone: "muted" }
  );
}
export function NodeBadge({ node }: { node: Node }) {
  const state = nodeState(node);
  return <Badge tone={state.tone}>{state.text}</Badge>;
}
export function Badge({
  children,
  tone = "muted",
}: {
  children: ReactNode;
  tone?: string;
}) {
  return (
    <span className={`badge badge-${tone}`}>
      <span />
      {children}
    </span>
  );
}
export function Button({
  children,
  icon: Icon,
  variant = "primary",
  busy = false,
  className = "",
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  icon?: LucideIcon;
  variant?: "primary" | "secondary" | "ghost" | "danger";
  busy?: boolean;
}) {
  return (
    <button
      className={`button button-${variant} ${className}`}
      {...props}
      disabled={busy || props.disabled}
    >
      {busy ? (
        <LoaderCircle size={16} className="spin" />
      ) : Icon ? (
        <Icon size={16} />
      ) : null}
      {children}
    </button>
  );
}
export function ErrorNotice({
  message,
  retry,
}: {
  message: string;
  retry?: () => void;
}) {
  if (!message) return null;
  return (
    <div className="notice notice-danger" role="alert">
      <TriangleAlert size={18} />
      <span>{message}</span>
      {retry && (
        <button className="text-button" onClick={retry}>
          重试
        </button>
      )}
    </div>
  );
}
export function Notice({
  children,
  tone = "info",
}: {
  children: ReactNode;
  tone?: "info" | "warning" | "success";
}) {
  return (
    <div className={`notice notice-${tone}`}>
      <Info size={18} />
      <div>{children}</div>
    </div>
  );
}
export function Loading() {
  return (
    <div className="loading-state" aria-live="polite">
      <LoaderCircle className="spin" size={25} />
      <span>正在读取控制台…</span>
    </div>
  );
}
export function Empty({
  title,
  description,
  action,
  icon: Icon = Inbox,
}: {
  title: string;
  description: string;
  action?: ReactNode;
  icon?: LucideIcon;
}) {
  return (
    <div className="empty-state">
      <span className="empty-icon">
        <Icon size={28} strokeWidth={1.5} />
      </span>
      <h3>{title}</h3>
      <p>{description}</p>
      {action}
    </div>
  );
}
export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
}: {
  eyebrow: string;
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        <div className="eyebrow">{eyebrow}</div>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}
export function StatCard({
  label,
  value,
  unit,
  hint,
  icon: Icon,
  color = "purple",
}: {
  label: string;
  value: ReactNode;
  unit?: string;
  hint: ReactNode;
  icon: LucideIcon;
  color?: string;
}) {
  return (
    <div className="stat-card">
      <div className="stat-top">
        <span>{label}</span>
        <div className={`stat-icon icon-${color}`}>
          <Icon size={18} />
        </div>
      </div>
      <div className="stat-value">
        {value}
        <small>{unit}</small>
      </div>
      <div className="stat-hint">{hint}</div>
    </div>
  );
}
export function Panel({
  title,
  subtitle,
  action,
  children,
  className = "",
}: {
  title: string;
  subtitle?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`panel ${className}`}>
      <div className="panel-heading">
        <div>
          <h2>{title}</h2>
          {subtitle && <p>{subtitle}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}
export function Rate({
  upload,
  download,
}: {
  upload: number;
  download: number;
}) {
  return (
    <div className="rates">
      <span>
        <ArrowUp size={13} />
        {bytes(upload)}/s
      </span>
      <span>
        <ArrowDown size={13} />
        {bytes(download)}/s
      </span>
    </div>
  );
}
export function Meter({
  value,
  max,
  label,
}: {
  value: number;
  max: number | null;
  label?: boolean;
}) {
  const progress = max ? Math.min((value / max) * 100, 100) : 0;
  return (
    <div className="meter-wrap">
      {label && (
        <div className="meter-label">
          <span>{bytes(value)}</span>
          <span>{max ? `/ ${bytes(max)}` : "不限量"}</span>
        </div>
      )}
      <div
        className={`meter ${progress >= 100 ? "over" : progress >= 80 ? "warn" : ""} ${max === null ? "unlimited" : ""}`}
      >
        <i style={{ width: max === null ? "0%" : `${progress}%` }} />
      </div>
    </div>
  );
}
export function Resource({
  label,
  value,
  text,
  color = "purple",
}: {
  label: string;
  value: number;
  text: string;
  color?: string;
}) {
  return (
    <div className="resource">
      <div>
        <span>{label}</span>
        <strong>{text}</strong>
      </div>
      <div className={`resource-bar bar-${color}`}>
        <i style={{ width: `${Math.min(100, Math.max(0, value))}%` }} />
      </div>
    </div>
  );
}
export function CopyButton({
  value,
  label = "复制链接",
}: {
  value: string;
  label?: string;
}) {
  const [copied, setCopied] = useState(false);
  const toast = useToast();
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 2000);
      toast("已复制到剪贴板");
    } catch {
      toast("无法访问剪贴板，请选中链接后手动复制");
    }
  }
  return (
    <Button
      variant="secondary"
      icon={copied ? Check : Copy}
      onClick={() => void copy()}
    >
      {copied ? "已复制" : label}
    </Button>
  );
}
export function Modal({
  title,
  description,
  children,
  onClose,
  wide = false,
}: {
  title: string;
  description?: string;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  const heading = useId(),
    container = useRef<HTMLDivElement>(null),
    closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const old = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const el = container.current;
    const focusable = () =>
      Array.from(
        el?.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), select:not([disabled]), a[href], textarea:not([disabled]), [tabindex="0"]',
        ) || [],
      );
    focusable()[0]?.focus();
    const handle = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeRef.current();
      if (e.key === "Tab") {
        const list = focusable(),
          first = list[0],
          last = list.at(-1);
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last?.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", handle);
    return () => {
      document.body.style.overflow = overflow;
      document.removeEventListener("keydown", handle);
      old?.focus();
    };
  }, []);
  return (
    <div
      className="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={container}
        className={`modal ${wide ? "modal-wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={heading}
      >
        <div className="modal-heading">
          <div>
            <h2 id={heading}>{title}</h2>
            {description && <p>{description}</p>}
          </div>
          <button
            type="button"
            className="icon-button"
            aria-label="关闭对话框"
            onClick={onClose}
          >
            <X size={20} />
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
export function Confirm({
  title,
  description,
  confirm = "确认",
  danger = false,
  onConfirm,
  onClose,
}: {
  title: string;
  description: string;
  confirm?: string;
  danger?: boolean;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title={title}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <p className="confirm-copy">{description}</p>
      <ErrorNotice message={error} />
      <div className="modal-actions">
        <Button variant="secondary" onClick={onClose} disabled={busy}>
          取消
        </Button>
        <Button
          variant={danger ? "danger" : "primary"}
          busy={busy}
          onClick={async () => {
            setBusy(true);
            setError("");
            try {
              await onConfirm();
              onClose();
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          {confirm}
        </Button>
      </div>
    </Modal>
  );
}
export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint && <small>{hint}</small>}
    </label>
  );
}
export function Toggle({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (value: boolean) => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      className={`toggle-label ${checked ? "checked" : ""}`}
      onClick={() => onChange(!checked)}
    >
      <span className="toggle">
        <i />
      </span>
      {label}
    </button>
  );
}
export function Breadcrumb({
  to,
  label,
  current,
}: {
  to: string;
  label: string;
  current: string;
}) {
  return (
    <div className="breadcrumb">
      <Link to={to}>{label}</Link>
      <ChevronRight size={14} />
      <span>{current}</span>
    </div>
  );
}
const LazyTrafficChart = lazy(() => import("./TrafficChart"));
export function TrafficChart(props: {
  points: TrafficPoint[];
  title?: string;
  subtitle?: string;
  period?: "24h" | "7d";
  onPeriodChange?: (period: "24h" | "7d") => void;
  loading?: boolean;
  error?: string;
}) {
  return (
    <Suspense
      fallback={
        <Panel title={props.title || "代理流量趋势"}>
          <div className="traffic-chart">
            <Loading />
          </div>
        </Panel>
      }
    >
      <LazyTrafficChart {...props} />
    </Suspense>
  );
}
export function RefreshButton({ refresh }: { refresh: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  return (
    <Button
      variant="secondary"
      icon={RefreshCw}
      busy={busy}
      onClick={async () => {
        setBusy(true);
        await refresh();
        setBusy(false);
      }}
    >
      刷新
    </Button>
  );
}
export function SuccessIcon() {
  return <CheckCircle2 size={16} className="text-green" />;
}
