import { useState, type FormEvent } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  Activity,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  ChevronRight,
  CloudDownload,
  Copy,
  ExternalLink,
  KeyRound,
  Link2,
  Monitor,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Search,
  Server,
  Settings2,
  ShieldCheck,
  Smartphone,
} from "lucide-react";
import { api, usePolling } from "../api";
import {
  Badge,
  Breadcrumb,
  Button,
  Confirm,
  CopyButton,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Modal,
  NodeBadge,
  Notice,
  PageHeader,
  Panel,
  RefreshButton,
  StatCard,
  TrafficChart,
  bytes,
  date,
  onlineKnown,
  useToast,
} from "../components";
import type { Device, DeviceDetail, Overview } from "../types";

export function AddDevice({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const navigate = useNavigate();
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const device = await api<Device>("/devices", "POST", {
        name: name.trim(),
      });
      onClose();
      navigate(`/devices/${device.id}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title="创建设备"
      description="为每台电脑、手机创建独立的连接凭据。"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="form-stack" onSubmit={submit}>
        <Field
          label="设备名称"
          hint="起一个容易辨认的名字，方便随时停用或轮换订阅。"
        >
          <input
            required
            maxLength={80}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：我的 MacBook、iPhone"
          />
        </Field>
        <Notice>
          创建后会生成独立订阅链接，包含全部验证通过的节点。建议一台设备使用一个订阅。
        </Notice>
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button
            type="button"
            variant="secondary"
            onClick={onClose}
            disabled={busy}
          >
            取消
          </Button>
          <Button type="submit" icon={Plus} busy={busy}>
            创建设备
          </Button>
        </div>
      </form>
    </Modal>
  );
}
function DeviceBadge({
  device,
  unknown = false,
}: {
  device: Device;
  unknown?: boolean;
}) {
  return (
    <Badge
      tone={
        device.pendingSync
          ? "warning"
          : !device.enabled
            ? "muted"
            : unknown
              ? "warning"
              : device.online
                ? "success"
                : "muted"
      }
    >
      {device.pendingSync
        ? "待同步"
        : !device.enabled
          ? "已停用"
          : unknown
            ? "在线未知"
            : device.online
              ? "在线"
              : "离线"}
    </Badge>
  );
}
export function Devices() {
  const { data, error, loading, refresh } = usePolling<Overview>("/overview"),
    [params, setParams] = useSearchParams(),
    [search, setSearch] = useState("");
  const devices = (data?.devices || []).filter((d) =>
      d.name.toLowerCase().includes(search.toLowerCase()),
    ),
    unknown = (data?.nodes || []).some((n) => !onlineKnown(n));
  return (
    <>
      <PageHeader
        eyebrow="DEVICES & SUBSCRIPTIONS"
        title="设备与订阅"
        description="独立订阅，独立掌控。让每一台设备安全连接。"
        actions={
          <>
            <RefreshButton refresh={refresh} />
            <Button icon={Plus} onClick={() => setParams({ add: "1" })}>
              创建设备
            </Button>
          </>
        }
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <div className="subscription-banner">
        <span className="subscription-banner-icon">
          <Link2 size={24} />
        </span>
        <div>
          <strong>一台设备，一个专属订阅</strong>
          <p>独立连接凭据，可随时停用或轮换。订阅支持 Clash / Mihomo。</p>
        </div>
        <Link to="/guide">
          查看连接指南 <ArrowRight size={15} />
        </Link>
      </div>
      <div className="list-toolbar">
        <div className="list-count">
          已登记设备 <span>{data?.devices?.length || 0}</span>
        </div>
        <div className="search-input">
          <Search size={16} />
          <input
            aria-label="搜索设备"
            placeholder="搜索设备名称"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>
      {loading ? (
        <Loading />
      ) : (
        <Panel
          title="设备列表"
          subtitle="在线状态按登记凭据识别，不代表真实物理设备数量"
        >
          {devices.length ? (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>设备</th>
                    <th>连接状态</th>
                    <th>本月代理流量</th>
                    <th>连接实例</th>
                    <th>创建时间</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {devices.map((d) => (
                    <tr key={d.id}>
                      <td>
                        <Link className="node-cell" to={`/devices/${d.id}`}>
                          <span className="device-icon">
                            {/手机|iphone|android|ipad/i.test(d.name) ? (
                              <Smartphone size={19} />
                            ) : (
                              <Monitor size={19} />
                            )}
                          </span>
                          <span>
                            <strong>{d.name}</strong>
                            <small>独立设备订阅</small>
                          </span>
                        </Link>
                      </td>
                      <td>
                        <DeviceBadge device={d} unknown={unknown} />
                      </td>
                      <td>
                        <strong className="table-amount">
                          {bytes(d.monthDownload + d.monthUpload)}
                        </strong>
                        <div className="table-small">
                          ↑ {bytes(d.monthUpload)}{" "}
                          <span>↓ {bytes(d.monthDownload)}</span>
                        </div>
                      </td>
                      <td>
                        {unknown ? "未知" : d.instances}
                        <span className="muted">{unknown ? "" : " 个"}</span>
                      </td>
                      <td className="muted">{date(d.createdAt, true)}</td>
                      <td>
                        <Link className="text-link" to={`/devices/${d.id}`}>
                          管理订阅 <ChevronRight size={15} />
                        </Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              icon={Monitor}
              title={search ? "没有匹配的设备" : "连接你的第一台设备"}
              description={
                search
                  ? "换个名称再试试。"
                  : "创建一个设备，获取可直接导入 Clash 的独立订阅链接。"
              }
              action={
                !search && (
                  <Button icon={Plus} onClick={() => setParams({ add: "1" })}>
                    创建设备
                  </Button>
                )
              }
            />
          )}
        </Panel>
      )}
      <div className="footnote">
        <ShieldCheck size={14} />
        订阅链接包含访问权限，请妥善保存。共享同一订阅的客户端会被计为同一登记设备。
      </div>
      {params.get("add") === "1" && (
        <AddDevice
          onClose={() =>
            setParams((p) => {
              p.delete("add");
              return p;
            })
          }
        />
      )}
    </>
  );
}
function RenameDevice({
  device,
  onClose,
  refresh,
}: {
  device: Device;
  onClose: () => void;
  refresh: () => Promise<void>;
}) {
  const [name, setName] = useState(device.name),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title="重命名设备"
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            await api(`/devices/${device.id}`, "PATCH", { name: name.trim() });
            await refresh();
            onClose();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field label="设备名称">
          <input
            value={name}
            required
            maxLength={80}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy}>
            保存名称
          </Button>
        </div>
      </form>
    </Modal>
  );
}
export function DevicePage() {
  const { id } = useParams(),
    { data, error, loading, refresh } = usePolling<DeviceDetail>(
      `/devices/${id}`,
    ),
    [confirm, setConfirm] = useState<"rotate" | "toggle" | null>(null),
    [rename, setRename] = useState(false);
  const toast = useToast();
  if (loading) return <Loading />;
  if (!data)
    return <ErrorNotice message={error} retry={() => void refresh()} />;
  const { device, nodes } = data,
    unknown = (nodes || []).some((n) => !onlineKnown(n)),
    ready = (nodes || []).filter((n) => n.status === "ready"),
    month = device.monthUpload + device.monthDownload;
  return (
    <>
      <Breadcrumb to="/devices" label="设备与订阅" current={device.name} />
      <PageHeader
        eyebrow="DEVICE DETAILS"
        title={device.name}
        description={`独立设备订阅 · 创建于 ${date(device.createdAt)}`}
        actions={
          <>
            <DeviceBadge device={device} unknown={unknown} />
            <Button
              variant="secondary"
              icon={Settings2}
              onClick={() => setRename(true)}
            >
              重命名
            </Button>
            <Button
              variant={device.enabled ? "secondary" : "primary"}
              icon={device.enabled ? Pause : Play}
              onClick={() => setConfirm("toggle")}
            >
              {device.enabled ? "停用设备" : "启用设备"}
            </Button>
          </>
        }
      />
      <ErrorNotice message={error} />
      {device.pendingSync && (
        <Notice tone="warning">
          设备变更正在同步到节点。失联节点恢复通信后才会应用，在所有节点确认前显示「待同步」。
        </Notice>
      )}
      {!device.enabled && (
        <Notice tone="warning">
          此设备已停用。订阅与新连接不可用，节点会撤销已有连接；重新启用后可恢复使用。
        </Notice>
      )}
      <div className="stats-grid">
        <StatCard
          label="本月代理流量"
          value={bytes(month).split(" ")[0]}
          unit={bytes(month).split(" ")[1]}
          icon={Activity}
          hint="北京时间自然月 · 全部节点合计"
        />
        <StatCard
          label="本月下载"
          value={bytes(device.monthDownload).split(" ")[0]}
          unit={bytes(device.monthDownload).split(" ")[1]}
          icon={ArrowDown}
          color="blue"
          hint="通过此设备凭据产生的下载"
        />
        <StatCard
          label="本月上传"
          value={bytes(device.monthUpload).split(" ")[0]}
          unit={bytes(device.monthUpload).split(" ")[1]}
          icon={ArrowUp}
          color="green"
          hint="通过此设备凭据产生的上传"
        />
        <StatCard
          label="客户端连接实例"
          value={unknown ? "—" : device.instances}
          icon={Monitor}
          color="amber"
          hint={
            unknown
              ? "节点状态异常，在线状态未知"
              : "使用此设备凭据的当前连接数"
          }
        />
      </div>
      <Panel
        title="专属订阅链接"
        subtitle="支持 Clash Verge Rev 等使用 Mihomo 核心的客户端"
        action={
          <span className="small-label">
            <ShieldCheck size={13} />
            独立凭据
          </span>
        }
      >
        <div className="subscription-content">
          <div className="subscription-url">
            <Link2 size={18} />
            <input
              aria-label="设备订阅链接"
              readOnly
              value={device.subscriptionUrl || "订阅链接暂不可用"}
              onFocus={(e) => e.currentTarget.select()}
            />
          </div>
          <div className="subscription-actions">
            <div>
              {device.subscriptionUrl && (
                <CopyButton value={device.subscriptionUrl} />
              )}{" "}
              {device.subscriptionUrl && device.enabled && (
                <a
                  className="button button-primary"
                  href={`clash://install-config?url=${encodeURIComponent(device.subscriptionUrl)}&name=${encodeURIComponent(`HQIP · ${device.name}`)}`}
                >
                  <CloudDownload size={16} />
                  导入 Clash <ExternalLink size={13} />
                </a>
              )}
            </div>
            <Button
              variant="ghost"
              icon={RefreshCw}
              onClick={() => setConfirm("rotate")}
            >
              轮换订阅与凭据
            </Button>
          </div>
          <div className="subscription-info">
            <KeyRound size={14} />
            <span>
              此链接包含节点访问权限。轮换后旧链接与连接凭据失效，需要重新导入客户端。
            </span>
          </div>
        </div>
      </Panel>
      {!ready.length && (
        <Notice tone="warning">
          目前还没有通过验证的节点。先完成节点部署和真实代理验证，再更新客户端订阅。
        </Notice>
      )}
      <TrafficChart
        points={data.traffic}
        title="设备流量趋势"
        subtitle="按设备凭据统计 · 各节点流量合计"
      />
      <div className="two-column">
        <Panel title="节点用量明细" subtitle="此设备在各节点产生的代理流量">
          {nodes?.length ? (
            <div className="table-scroll">
              <table className="compact-table">
                <thead>
                  <tr>
                    <th>节点</th>
                    <th>状态</th>
                    <th>本月用量</th>
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((n) => (
                    <tr key={n.id}>
                      <td>
                        <Link className="table-name" to={`/nodes/${n.id}`}>
                          {n.name}
                        </Link>
                        <div className="table-small">{n.domain}</div>
                      </td>
                      <td>
                        <NodeBadge node={n} />
                      </td>
                      <td>
                        <strong className="table-amount">
                          {bytes(n.monthUpload + n.monthDownload)}
                        </strong>
                        <div className="table-small">
                          ↑ {bytes(n.monthUpload)}{" "}
                          <span>↓ {bytes(n.monthDownload)}</span>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              icon={Server}
              title="暂无节点记录"
              description="添加并验证节点后，设备即可通过订阅连接。"
              action={
                <Link to="/nodes" className="text-link">
                  管理节点 <ArrowRight size={15} />
                </Link>
              }
            />
          )}
        </Panel>
        <Panel title="在新设备上开始使用" subtitle="复制、导入，然后连接">
          <ol className="onboarding-list">
            <li>
              <span>1</span>
              <div>
                <strong>复制专属订阅链接</strong>
                <p>每台设备建议创建一个独立订阅。</p>
              </div>
              <Copy size={17} />
            </li>
            <li>
              <span>2</span>
              <div>
                <strong>在 Clash 中添加订阅</strong>
                <p>打开「订阅」，粘贴链接并导入配置。</p>
              </div>
              <CloudDownload size={17} />
            </li>
            <li>
              <span>3</span>
              <div>
                <strong>选择节点，开启代理</strong>
                <p>客户端需要支持 Hysteria 2 和 UDP。</p>
              </div>
              <ShieldCheck size={17} />
            </li>
          </ol>
          <Link className="panel-bottom-link" to="/guide">
            查看完整连接指南 <ArrowRight size={15} />
          </Link>
        </Panel>
      </div>
      <div className="footnote">
        <ShieldCheck size={14} />
        累计使用 {bytes(device.totalUpload + device.totalDownload)} ·
        登记设备与真实物理设备并不等同。
      </div>
      {rename && (
        <RenameDevice
          device={device}
          onClose={() => setRename(false)}
          refresh={refresh}
        />
      )}{" "}
      {confirm && (
        <Confirm
          title={
            confirm === "rotate"
              ? "轮换订阅与连接凭据？"
              : device.enabled
                ? "停用这台设备？"
                : "启用这台设备？"
          }
          description={
            confirm === "rotate"
              ? "旧订阅链接将立即失效，所有节点会撤销旧连接凭据并断开旧会话。请将新订阅重新导入客户端。失联节点会在恢复后应用。"
              : device.enabled
                ? "此设备将无法获取订阅或建立新的连接，节点会断开已有会话。失联节点需要恢复同步后才能完成撤销。"
                : "设备将恢复订阅和连接权限。节点收到并应用新配置后，即可重新连接。"
          }
          confirm={
            confirm === "rotate"
              ? "确认轮换"
              : device.enabled
                ? "确认停用"
                : "确认启用"
          }
          danger={confirm === "rotate" || device.enabled}
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            if (confirm === "rotate")
              await api(`/devices/${id}/rotate`, "POST");
            else
              await api(`/devices/${id}`, "PATCH", {
                enabled: !device.enabled,
              });
            await refresh();
            toast(
              confirm === "rotate"
                ? "已轮换，请重新导入新订阅"
                : device.enabled
                  ? "停用请求已提交"
                  : "启用请求已提交",
            );
          }}
        />
      )}
    </>
  );
}
