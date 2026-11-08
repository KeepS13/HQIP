import { useState, type FormEvent } from "react";
import {
  CalendarClock,
  CheckCircle2,
  Clock3,
  History,
  Settings2,
} from "lucide-react";
import { api, usePolling } from "../api";
import { DateInput, ExpiryShortcuts } from "../DatePicker";
import {
  Badge,
  Button,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Modal,
  Notice,
  RefreshButton,
  bytes,
} from "../components";
import QuotaFields from "../QuotaFields";
import {
  BILLING_MONTH,
  beijingHourParts,
  billingDate,
  billingExpiry,
  billingFor,
  billingQuotaLabel,
  billingTotal,
  nextBillingReset,
  parseBeijingExpiry,
  type BillingResource,
} from "../billing";
import type { ManagedServer, ServerRenewal } from "../types";
import "./server-billing.css";

export function ExpiryFields({
  day,
  hour,
  onChange,
  required = true,
}: {
  day: string;
  hour: string;
  onChange: (value: { day: string; hour: string }) => void;
  required?: boolean;
}) {
  return (
    <div className="billing-expiry-fields">
      <Field label="到期日期（北京时间）">
        <DateInput
          aria-label="到期日期（北京时间）"
          required={required || !!hour}
          min={beijingHourParts(Date.now()).day}
          value={day}
          onChange={(event) => onChange({ day: event.target.value, hour })}
        />
      </Field>
      <Field label="到期小时">
        <select
          aria-label="到期小时"
          required={required || !!day}
          value={hour}
          onChange={(event) => onChange({ day, hour: event.target.value })}
        >
          <option value="">选择小时</option>
          {Array.from({ length: 24 }, (_, index) =>
            String(index).padStart(2, "0"),
          ).map((value) => (
            <option key={value} value={value}>
              {value}:00
            </option>
          ))}
        </select>
      </Field>
      <ExpiryShortcuts day={day} hour={hour} onChange={onChange} />
      <div style={{ gridColumn: "1 / -1" }}>
        <ExpiryResetPreview expiresAt={day && hour ? `${day}T${hour}:00:00+08:00` : null} />
      </div>
    </div>
  );
}

function ExpiryResetPreview({ expiresAt }: { expiresAt: string | null }) {
  const valid = !!expiresAt && Number.isFinite(Date.parse(expiresAt)) && Date.parse(expiresAt) > Date.now();
  const next = valid ? nextBillingReset(expiresAt) : null;
  return <div className="billing-preview"><span>下次周期重置（北京时间）</span><strong>{!valid ? "请选择未来的日期和小时" : next ? billingDate(next) : "到期前无重置安排"}</strong><small>从到期时间向前每 30 天计算；到期本身不重置用量。</small></div>;
}

export function BillingSummary({
  resource,
  compact = false,
  followsServer = false,
}: {
  resource: BillingResource;
  compact?: boolean;
  followsServer?: boolean;
}) {
  const billing = billingFor(resource),
    expiry = billingExpiry(billing);
  return (
    <div className={`billing-summary${compact ? " compact" : ""}`}>
      <div className="billing-summary-heading">
        <span>
          <CalendarClock size={15} />
          {followsServer ? "周期随服务器" : "服务器有效期"}
        </span>
        <Badge tone={expiry.tone}>{expiry.label}</Badge>
      </div>
      {billing.status === "unconfigured" ? (
        <p>暂按北京时间自然月统计。设置到期时间时保留现有用量。</p>
      ) : (
        <>
          <dl>
            <div>
              <dt>到期时间</dt>
              <dd>{billingDate(billing.expiresAt)}</dd>
            </div>
            <div>
              <dt>下次周期重置</dt>
              <dd>
                {expiry.state === "expired"
                  ? "待续期确认"
                  : billing.nextResetAt
                    ? billingDate(billing.nextResetAt)
                    : "未安排"}
              </dd>
            </div>
          </dl>
          {!compact && (
            <p>
              北京时间 · 设置或修改有效期后，按最新到期时间向前每 30 天计算未来重置时间。保存时保留本周期用量；到期本身不清空用量，也不停止节点。
            </p>
          )}
          {!compact && billing.cycleStartedAt && (
            <p>
              本周期开始：{billingDate(billing.cycleStartedAt)}
              {!billing.cycleComplete ? " · 数据不完整，按已知用量计算" : ""}
            </p>
          )}
        </>
      )}
    </div>
  );
}

function RenewalHistory({ serverId }: { serverId: string }) {
  const { data, error, loading, refresh } = usePolling<ServerRenewal[]>(
    `/servers/${serverId}/renewals`,
    30000,
  );
  const labels = {
    initial: "首次设置",
    advance: "提前续期",
    overdue: "到期后续期",
    correction: "修改到期时间",
  };
  return (
    <section className="billing-history">
      <div className="billing-section-title">
        <div>
          <h3>有效期与续期记录</h3>
          <p>仅记录实际保存的有效期变更，确认预警不代表续期。</p>
        </div>
        <RefreshButton refresh={refresh} />
      </div>
      <ErrorNotice
        message={error ? `${error}${data ? "；保留上次成功记录。" : ""}` : ""}
        retry={() => void refresh()}
      />
      {loading ? (
        <Loading />
      ) : data?.length ? (
        <div className="billing-history-list">
          {data.map((record) => (
            <article key={record.id}>
              <div className="billing-history-heading">
                <Badge tone={record.kind === "overdue" ? "warning" : "info"}>
                  {labels[record.kind]}
                </Badge>
                <span>
                  {billingDate(record.confirmedAt)} · {record.actor || "管理员"}
                </span>
              </div>
              <dl>
                <div>
                  <dt>原到期</dt>
                  <dd>{billingDate(record.previousExpiresAt)}</dd>
                </div>
                <div>
                  <dt>新到期</dt>
                  <dd>{billingDate(record.expiresAt)}</dd>
                </div>
              </dl>
              <p>
                {record.kind === "advance"
                  ? `增加 ${record.months} 个月（${record.months * 30} 天），确认时保留本周期用量。`
                  : record.kind === "overdue"
                    ? "确认时开启新周期，服务器及其节点周期用量归零；历史数据保留。"
                    : record.kind === "correction"
                      ? "保留当前周期与已有用量，按新到期时间向前每 30 天重新安排未来重置。"
                      : "首次配置有效期，保留此前已记录用量。"}
              </p>
            </article>
          ))}
        </div>
      ) : (
        !error && (
          <Empty
            icon={History}
            title="暂无有效期记录"
            description="首次配置到期时间或确认续期后，记录会显示在这里。"
          />
        )
      )}
    </section>
  );
}

type RenewalDraft = {
  expectedRevision: number;
  requestId: string;
  months?: number;
  expiresAt?: string;
  previewExpiresAt: string;
  overdue: boolean;
};

function RenewalForm({
  server,
  onSaved,
  onBusy,
}: {
  server: ManagedServer;
  onSaved: (message: string) => void;
  onBusy: (value: boolean) => void;
}) {
  const billing = billingFor(server),
    expiry = billingExpiry(billing);
  const [months, setMonths] = useState("1"),
    [dateParts, setDateParts] = useState({ day: "", hour: "" });
  const [draft, setDraft] = useState<RenewalDraft | null>(null),
    [confirmed, setConfirmed] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const overdue = expiry.state === "expired";
  const monthsNumber = Number(months);
  const previewTimestamp =
    billing.expiresAt &&
    Number.isInteger(monthsNumber) &&
    monthsNumber >= 1 &&
    monthsNumber <= 1200
      ? Date.parse(billing.expiresAt) + monthsNumber * BILLING_MONTH
      : Number.NaN;
  const previewExpiry = Number.isFinite(previewTimestamp)
    ? new Date(previewTimestamp).toISOString()
    : null;
  function changed() {
    setDraft(null);
    setConfirmed(false);
    setError("");
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      if (!draft) {
        if (overdue) {
          const expiresAt = parseBeijingExpiry(dateParts.day, dateParts.hour);
          setDraft({
            requestId: crypto.randomUUID(),
            expectedRevision: server.revision,
            expiresAt,
            previewExpiresAt: expiresAt,
            overdue: true,
          });
        } else {
          if (
            !Number.isInteger(monthsNumber) ||
            monthsNumber < 1 ||
            monthsNumber > 1200 ||
            !previewExpiry
          )
            throw new Error("续期月数应为 1–1200 的整数，每月按 30 天计算");
          setDraft({
            requestId: crypto.randomUUID(),
            expectedRevision: server.revision,
            months: monthsNumber,
            previewExpiresAt: previewExpiry,
            overdue: false,
          });
        }
        return;
      }
      if (!confirmed) throw new Error("请确认实际续费及新的到期时间");
      setBusy(true);
      onBusy(true);
      const { previewExpiresAt: _, overdue: __, ...request } = draft;
      await api<{ server: ManagedServer; renewal: ServerRenewal }>(
        `/servers/${server.id}/renewals`,
        "POST",
        request,
      );
      onSaved(
        draft.overdue
          ? "已确认到期后续期，服务器与节点已开启新周期"
          : "已记录提前续期，本周期用量保留",
      );
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
      onBusy(false);
    }
  }
  if (billing.status === "unconfigured")
    return (
      <Notice>
        此服务器尚未配置有效期。请先在“额度设置”中设置首次到期时间；设置时保留当前用量。
      </Notice>
    );
  return (
    <form className="form-stack" onSubmit={submit}>
      <BillingSummary resource={server} />
      {!draft ? (
        <>
          {overdue ? (
            <>
              <Notice tone="warning">
                服务器已到期。请根据实际续费结果手动选择新的到期日期与小时，系统不会自动推算为
                30 天后。
              </Notice>
              <ExpiryFields
                {...dateParts}
                onChange={(value) => {
                  setDateParts(value);
                  changed();
                }}
              />
              <p className="billing-help">
                确认后立即开启新周期，服务器和全部所属节点的周期用量归零；自然月、累计流量与历史记录保留。
              </p>
            </>
          ) : (
            <>
              <Field
                label="续期月数"
                hint="从原到期时间增加，每月固定 30 天，不按自然月计算。"
              >
                <input
                  type="number"
                  required
                  min={1}
                  max={1200}
                  step={1}
                  value={months}
                  onChange={(event) => {
                    setMonths(event.target.value);
                    changed();
                  }}
                />
              </Field>
              <div className="billing-preview">
                <span>新的到期时间（北京时间）</span>
                <strong>{billingDate(previewExpiry)}</strong>
                <small>现在确认续期只延长有效期，本周期用量保持不变。</small>
              </div>
              <ExpiryResetPreview expiresAt={previewExpiry} />
            </>
          )}
        </>
      ) : (
        <div className="billing-confirmation">
          <div className="billing-section-title">
            <div>
              <h3>核对本次续期</h3>
              <p>这项操作会保存实际续费记录。</p>
            </div>
            <CheckCircle2 size={22} />
          </div>
          <dl>
            <div>
              <dt>服务器</dt>
              <dd>{server.name}</dd>
            </div>
            <div>
              <dt>原到期</dt>
              <dd>{billingDate(billing.expiresAt)}</dd>
            </div>
            <div>
              <dt>新到期</dt>
              <dd>{billingDate(draft.previewExpiresAt)}</dd>
            </div>
            <div>
              <dt>用量处理</dt>
              <dd>{draft.overdue ? "立即开启新周期" : "保留当前周期用量"}</dd>
            </div>
            <div>
              <dt>下次周期重置</dt>
              <dd>{billingDate(nextBillingReset(draft.previewExpiresAt))}</dd>
            </div>
          </dl>
          <Notice tone={draft.overdue ? "warning" : "info"}>
            {draft.overdue
              ? `确认后服务器及全部 IP 节点的周期统计重新开始。服务器当前已记录 ${bytes(billingTotal(server))}，不会从自然月及累计记录中删除。`
              : "当前周期不会立即归零。后续按已确认的有效期和周期边界重置。"}
          </Notice>
          <label className="billing-confirm-check">
            <input
              type="checkbox"
              required
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            <span>
              我已确认实际续费结果和新的到期时间
              {draft.overdue ? "，同意开启新周期" : ""}。
            </span>
          </label>
        </div>
      )}
      <ErrorNotice message={error} />
      <div className="modal-actions">
        {draft && (
          <Button
            type="button"
            variant="secondary"
            disabled={busy}
            onClick={() => {
              setDraft(null);
              setConfirmed(false);
              setError("");
            }}
          >
            返回修改
          </Button>
        )}
        <Button
          type="submit"
          busy={busy}
          icon={draft ? CheckCircle2 : CalendarClock}
        >
          {draft ? "确认续期" : "核对续期"}
        </Button>
      </div>
    </form>
  );
}

function ExpiryCorrectionForm({ server, onSaved, onBusy, onCancel }: {
  server: ManagedServer;
  onSaved: (message: string) => void;
  onBusy: (value: boolean) => void;
  onCancel: () => void;
}) {
  const [dateParts, setDateParts] = useState(() => beijingHourParts(billingFor(server).expiresAt || ""));
  const [requestId, setRequestId] = useState(() => crypto.randomUUID());
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const expiresAt = parseBeijingExpiry(dateParts.day, dateParts.hour);
      setBusy(true);
      onBusy(true);
      await api(`/servers/${server.id}/expiry`, "PATCH", { expectedRevision: server.revision, requestId, expiresAt });
      onSaved("已修改到期时间，保留当前用量并重新安排未来重置");
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
      onBusy(false);
    }
  }
  return <form className="form-stack" onSubmit={save}>
    <h3>修改到期时间</h3>
    <Notice>保存后保留当前周期和服务器、节点的已有用量，并从新到期时间向前每 30 天重新计算未来的重置时间。变更会记入有效期历史。</Notice>
    <fieldset className="form-stack" disabled={busy} style={{ border: 0, padding: 0, margin: 0 }}>
      <ExpiryFields {...dateParts} onChange={(value) => { setDateParts(value); setRequestId(crypto.randomUUID()); setError(""); }} />
    </fieldset>
    <ErrorNotice message={error} />
    <div className="modal-actions"><Button type="button" variant="secondary" disabled={busy} onClick={onCancel}>取消修改</Button><Button type="submit" busy={busy}>保存到期时间</Button></div>
  </form>;
}

export function ServerBillingSettings({
  server,
  onClose,
  onSaved,
}: {
  server: ManagedServer;
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const [snapshot] = useState(server),
    billing = billingFor(snapshot);
  const [tab, setTab] = useState<"quota" | "renewal" | "history">("quota");
  const [editingExpiry, setEditingExpiry] = useState(false);
  const [name, setName] = useState(snapshot.name),
    [bandwidth, setBandwidth] = useState(
      snapshot.bandwidthMbps?.toString() || "",
    );
  const [unit, setUnit] = useState(
    snapshot.monthlyQuotaBytes && snapshot.monthlyQuotaBytes >= 1e12
      ? "TB"
      : "GB",
  );
  const [quota, setQuota] = useState(
    snapshot.monthlyQuotaBytes == null
      ? ""
      : String(
          snapshot.monthlyQuotaBytes /
            (snapshot.monthlyQuotaBytes >= 1e12 ? 1e12 : 1e9),
        ),
  );
  const [dateParts, setDateParts] = useState({ day: "", hour: "" });
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function saveSettings(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const expiresAt =
        billing.status === "unconfigured" && (dateParts.day || dateParts.hour)
          ? parseBeijingExpiry(dateParts.day, dateParts.hour)
          : undefined;
      await api(`/servers/${snapshot.id}/settings`, "PATCH", {
        name: name.trim(),
        bandwidthMbps: bandwidth === "" ? null : Number(bandwidth),
        monthlyQuotaBytes:
          quota === ""
            ? null
            : Math.round(Number(quota) * (unit === "GB" ? 1e9 : 1e12)),
        expectedRevision: snapshot.revision,
        ...(expiresAt ? { expiresAt } : {}),
      });
      onSaved(
        expiresAt
          ? "已设置有效期并保留已有用量"
          : "服务器额度已更新，用量保持不变",
      );
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title="额度与续期"
      wide
      description="服务器容量、实际有效期和续期记录分开管理，无需 SSH 密码。"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <div
        className="billing-tabs"
        role="tablist"
        aria-label="服务器额度与续期"
      >
        {(
          [
            { id: "quota", label: "额度设置", icon: Settings2 },
            { id: "renewal", label: "续期确认", icon: CalendarClock },
            { id: "history", label: "续期记录", icon: History },
          ] as const
        ).map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={tab === item.id}
            disabled={busy}
            onClick={() => {
              setTab(item.id);
              setEditingExpiry(false);
              setError("");
            }}
          >
            <item.icon size={15} />
            {item.label}
          </button>
        ))}
      </div>
      {server.revision !== snapshot.revision && (
        <Notice tone="warning">
          服务器设置已被更新。当前编辑保留原值；保存遇到版本冲突时，请关闭并重新打开。
        </Notice>
      )}
      {tab === "quota" && (
        editingExpiry ? <ExpiryCorrectionForm server={snapshot} onSaved={onSaved} onBusy={setBusy} onCancel={() => setEditingExpiry(false)} /> : <form className="form-stack" onSubmit={saveSettings}>
          <Field label="服务器名称">
            <input
              required
              maxLength={80}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <QuotaFields
            {...{ bandwidth, setBandwidth, quota, setQuota, unit, setUnit }}
            quotaLabel={
              billing.status === "unconfigured" &&
              (dateParts.day || dateParts.hour)
                ? "每周期流量额度"
                : billingQuotaLabel(snapshot)
            }
          />
          {billing.status === "unconfigured" ? (
            <section className="billing-first-setup">
              <h3>
                <Clock3 size={17} />
                首次配置有效期
              </h3>
              <p>
                可同时设置到期时间；留空则继续按北京时间自然月统计。首次配置会保留服务器与各节点当前用量。
              </p>
              <ExpiryFields
                {...dateParts}
                required={false}
                onChange={setDateParts}
              />
            </section>
          ) : (
            <><BillingSummary resource={snapshot} /><Button type="button" variant="secondary" icon={CalendarClock} onClick={() => setEditingExpiry(true)}>修改到期时间</Button></>
          )}
          <Notice>
            容量仅提醒，不限速、不断网。普通保存只修改名称和额度，不会重置流量；可单独修改到期时间，实际续费请在“续期确认”中记录。
          </Notice>
          <ErrorNotice message={error} />
          <div className="modal-actions">
            <Button
              type="button"
              variant="secondary"
              disabled={busy}
              onClick={onClose}
            >
              取消
            </Button>
            <Button type="submit" busy={busy}>
              保存额度设置
            </Button>
          </div>
        </form>
      )}
      {tab === "renewal" && (
        <RenewalForm server={snapshot} onSaved={onSaved} onBusy={setBusy} />
      )}
      {tab === "history" && <RenewalHistory serverId={snapshot.id} />}
    </Modal>
  );
}
