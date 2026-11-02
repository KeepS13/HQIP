import { Link } from "react-router-dom";
import {
  Activity,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  CheckCircle2,
  ChevronRight,
  CloudDownload,
  ExternalLink,
  Globe2,
  Info,
  Monitor,
  Plus,
  Server,
  ShieldCheck,
  Sparkles,
  Wifi,
} from "lucide-react";
import { usePolling } from "../api";
import {
  Empty,
  ErrorNotice,
  Loading,
  Meter,
  NodeBadge,
  Notice,
  PageHeader,
  Panel,
  Rate,
  RefreshButton,
  StatCard,
  TrafficChart,
  bytes,
  fresh,
  onlineKnown,
} from "../components";
import type { Overview } from "../types";

export function Dashboard() {
  const { data, error, loading, refresh } = usePolling<Overview>("/overview");
  if (loading) return <Loading />;
  if (!data)
    return <ErrorNotice message={error} retry={() => void refresh()} />;
  const nodes = data.nodes || [],
    devices = data.devices || [],
    live = nodes.filter((n) => fresh(n)),
    ready = nodes.filter(
      (n) => n.status === "ready" && fresh(n) && n.coreHealthy,
    ),
    upload = live.reduce((v, n) => v + n.metrics.uploadRate, 0),
    download = live.reduce((v, n) => v + n.metrics.downloadRate, 0),
    month = nodes.reduce((v, n) => v + n.monthDownload + n.monthUpload, 0),
    online = devices.filter((d) => d.enabled && d.online).length,
    unknown = nodes.some((n) => !onlineKnown(n)),
    instances = live.filter(onlineKnown).reduce((v, n) => v + n.instances, 0);
  return (
    <>
      <PageHeader
        eyebrow="WORKSPACE OVERVIEW"
        title="网络总览"
        description="每一个节点，每一次连接，尽在掌握。"
        actions={
          <>
            <RefreshButton refresh={refresh} />
            <Link className="button button-primary" to="/nodes?add=1">
              <Plus size={16} />
              添加节点
            </Link>
          </>
        }
      />
      <ErrorNotice message={error} retry={() => void refresh()} />
      <div className="overview-banner">
        <div className="banner-mark">
          <Globe2 size={28} strokeWidth={1.4} />
        </div>
        <div>
          <strong>
            {ready.length ? "你的私人网络正在运行" : "从你的第一个节点开始"}
          </strong>
          <p>
            {ready.length
              ? `${ready.length} 个节点运行正常，连接信息每 5 秒更新。`
              : "添加服务器，自动部署专属于你的加密连接。"}
          </p>
        </div>
        <div className="banner-tag">
          <ShieldCheck size={15} /> Hysteria 2<span> / </span>TLS
        </div>
      </div>
      <div className="stats-grid">
        <StatCard
          label="可用节点"
          value={ready.length}
          unit={`/ ${nodes.length}`}
          icon={Server}
          hint={
            <>
              <span className="live-dot" />
              {nodes.length - ready.length
                ? `${nodes.length - ready.length} 个节点需要关注`
                : "节点状态正常"}
            </>
          }
        />
        <StatCard
          label="本月代理流量"
          value={bytes(month).split(" ")[0]}
          unit={bytes(month).split(" ")[1]}
          icon={Activity}
          color="blue"
          hint={
            <>
              <ArrowUp size={12} />
              {bytes(nodes.reduce((v, n) => v + n.monthUpload, 0))}
              <span className="hint-separator" />
              <ArrowDown size={12} />
              {bytes(nodes.reduce((v, n) => v + n.monthDownload, 0))}
            </>
          }
        />
        <StatCard
          label="在线登记设备"
          value={unknown ? "—" : online}
          unit={`/ ${devices.length}`}
          icon={Monitor}
          color="green"
          hint={
            unknown
              ? "存在状态异常节点，在线总数未知"
              : `${instances} 个客户端连接实例`
          }
        />
        <StatCard
          label="实时下载速率"
          value={bytes(download).split(" ")[0]}
          unit={`${bytes(download).split(" ")[1]}/s`}
          icon={ArrowDown}
          color="amber"
          hint={
            <>
              <ArrowUp size={12} />
              上传 {bytes(upload)}/s{unknown ? " · 仅含心跳正常节点" : ""}
            </>
          }
        />
      </div>
      <div className="overview-middle">
        <TrafficChart points={data.traffic} />
        <Panel title="网络概况" subtitle="当前连接状态">
          <div className="health-visual">
            <div className={`health-ring ${ready.length ? "is-live" : ""}`}>
              <Wifi size={27} />
              <strong>{ready.length ? "已就绪" : "待连接"}</strong>
              <small>
                {nodes.length
                  ? `${ready.length} / ${nodes.length} 个节点可用`
                  : "尚未添加节点"}
              </small>
            </div>
          </div>
          <div className="health-items">
            <div>
              <span>
                <span className="status-dot green" />
                可用节点
              </span>
              <strong>{ready.length}</strong>
            </div>
            <div>
              <span>
                <span className="status-dot amber" />
                待处理节点
              </span>
              <strong>{nodes.length - ready.length}</strong>
            </div>
            <div>
              <span>
                <span className="status-dot purple" />
                已登记设备
              </span>
              <strong>{devices.length}</strong>
            </div>
          </div>
          <Link to="/devices" className="panel-bottom-link">
            管理设备订阅 <ArrowRight size={15} />
          </Link>
        </Panel>
      </div>
      <Panel
        title="节点概览"
        subtitle="查看健康状态、实时速率与本月用量"
        action={
          <Link className="text-link" to="/nodes">
            全部节点 <ChevronRight size={15} />
          </Link>
        }
      >
        {nodes.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>节点</th>
                  <th>运行状态</th>
                  <th>实时速率</th>
                  <th>本月用量</th>
                  <th>在线设备</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {nodes.slice(0, 5).map((n) => (
                  <tr key={n.id}>
                    <td>
                      <Link className="node-cell" to={`/nodes/${n.id}`}>
                        <span className="node-icon">
                          <Server size={18} />
                        </span>
                        <span>
                          <strong>{n.name}</strong>
                          <small>{n.domain}</small>
                        </span>
                      </Link>
                    </td>
                    <td>
                      <NodeBadge node={n} />
                    </td>
                    <td>
                      {fresh(n) ? (
                        <Rate
                          upload={n.metrics.uploadRate}
                          download={n.metrics.downloadRate}
                        />
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td className="quota-cell">
                      <Meter
                        value={n.monthDownload + n.monthUpload}
                        max={n.monthlyQuotaBytes}
                        label
                      />
                    </td>
                    <td>
                      {onlineKnown(n) ? (
                        <span>
                          {n.onlineDevices}
                          <span className="muted"> 台</span>
                        </span>
                      ) : (
                        <span className="muted">未知</span>
                      )}
                    </td>
                    <td>
                      <Link
                        to={`/nodes/${n.id}`}
                        className="icon-button"
                        aria-label={`查看 ${n.name}`}
                      >
                        <ChevronRight size={17} />
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty
            icon={Server}
            title="你的网络，由第一个节点开始"
            description="准备好服务器的 SSH 信息和已解析的域名，剩下的交给 HQIP。"
            action={
              <Link to="/nodes?add=1" className="button button-primary">
                <Plus size={16} />
                添加第一个节点
              </Link>
            }
          />
        )}
      </Panel>
      {(data.alerts || []).length > 0 && (
        <Panel title="需要关注" subtitle="额度仅提醒，不会限速或断网">
          <div className="alerts-list">
            {data.alerts.map((alert, i) => (
              <div
                className={`alert-row ${alert.level === "error" ? "alert-error" : ""}`}
                key={i}
              >
                <Info size={17} />
                <span>{alert.message}</span>
                {alert.nodeId && (
                  <Link to={`/nodes/${alert.nodeId}`}>
                    查看节点 <ArrowRight size={14} />
                  </Link>
                )}
              </div>
            ))}
          </div>
        </Panel>
      )}
      <div className="quick-start-strip">
        <span className="quick-start-icon">
          <Sparkles size={20} />
        </span>
        <div>
          <strong>让新设备连接到你的私人网络</strong>
          <p>为每台设备创建独立订阅，复制链接即可导入 Clash。</p>
        </div>
        <Link to="/devices?add=1" className="button button-secondary">
          创建设备 <ArrowRight size={15} />
        </Link>
      </div>
    </>
  );
}

export function Guide() {
  return (
    <>
      <PageHeader
        eyebrow="QUICK START"
        title="连接指南"
        description="三步连接你的私人网络，开始使用独立设备订阅。"
      />
      <div className="guide-steps">
        <Panel title="01 · 添加服务器" subtitle="准备好可通过 SSH 连接的服务器">
          <span className="guide-icon">
            <Server size={28} />
          </span>
          <p>
            在节点管理中填写 SSH 地址、公网 IPv4、登录密码和节点域名。域名的 A
            记录应直接解析到服务器公网 IP，并开放 TCP 80 和 UDP 443。
          </p>
          <p>
            支持 Ubuntu 22.04 / 24.04、Debian 12，amd64 / arm64。首次部署需要
            root 权限，带宽和流量额度只用于记录与提醒。
          </p>
          <Link className="text-link" to="/nodes?add=1">
            添加节点 <ArrowRight size={15} />
          </Link>
        </Panel>
        <Panel title="02 · 创建设备订阅" subtitle="电脑、手机分别创建一台设备">
          <span className="guide-icon green">
            <Monitor size={28} />
          </span>
          <p>
            每台登记设备有独立的订阅链接和节点连接凭据。打开设备详情，复制订阅地址，或者在安装了兼容客户端的电脑上点击「导入
            Clash」。
          </p>
          <p>
            订阅会包含全部验证通过的可用节点。请妥善保存订阅链接，持有链接的人可以使用你的节点。
          </p>
          <Link className="text-link" to="/devices?add=1">
            创建设备 <ArrowRight size={15} />
          </Link>
        </Panel>
        <Panel
          title="03 · 在 Clash 中连接"
          subtitle="使用支持 Hysteria 2 的 Mihomo 客户端"
        >
          <span className="guide-icon blue">
            <CloudDownload size={28} />
          </span>
          <p>
            打开 Clash Verge Rev 等 Mihomo
            客户端，在「订阅」页面粘贴链接并导入。更新订阅后，在代理中选择节点，并开启系统代理或虚拟网卡模式。
          </p>
          <p>
            首次连接需要客户端所处网络支持
            UDP。无法连接时，先检查节点验证状态、服务器防火墙与本地网络的 UDP
            连通性。
          </p>
          <a
            className="text-link"
            href="https://github.com/clash-verge-rev/clash-verge-rev/releases"
            target="_blank"
            rel="noreferrer"
          >
            Clash Verge Rev <ExternalLink size={14} />
          </a>
        </Panel>
      </div>
      <div className="two-column">
        <Panel title="看懂你的监控数据" subtitle="数据定义与统计口径">
          <div className="definition-list">
            <div>
              <strong>代理流量</strong>
              <p>
                记录经过 Hysteria
                的上传与下载数据，不等同于服务商账单。按北京时间自然月统计，断网后自动补报。
              </p>
            </div>
            <div>
              <strong>在线登记设备</strong>
              <p>
                根据独立连接凭据识别并跨节点去重，不代表真实物理设备数量。节点心跳超过
                30 秒后，在线状态显示为未知。
              </p>
            </div>
            <div>
              <strong>客户端连接实例</strong>
              <p>
                同一设备凭据可能存在多个连接；这项指标反映当前活跃连接数量。
              </p>
            </div>
            <div>
              <strong>额度与带宽</strong>
              <p>
                流量在 80% 和 100%
                时站内提醒；不会限速、断网。带宽为容量参考值。
              </p>
            </div>
          </div>
        </Panel>
        <Panel title="常见操作与排查" subtitle="让连接持续、稳定地运行">
          <div className="definition-list">
            <div>
              <strong>节点显示「待外部验证」</strong>
              <p>
                与网站部署在同一台服务器的节点，必须从外部电脑使用 Mihomo
                验证真实代理连接。部署成功不代表公网 UDP 已连通。
              </p>
            </div>
            <div>
              <strong>停止某台设备的访问</strong>
              <p>
                在设备详情中停用设备。节点先拒绝新鉴权，再断开已有连接；失联节点显示「待同步」，重连后应用。
              </p>
            </div>
            <div>
              <strong>订阅链接泄漏或需要更换</strong>
              <p>
                轮换订阅会同步更换订阅令牌和代理凭据，旧链接失效；需要把新链接重新导入客户端。
              </p>
            </div>
            <div>
              <strong>节点与控制台短暂断开</strong>
              <p>
                节点保留最近成功配置，现有连接继续运行；恢复后上报累计流量并同步变更。
              </p>
            </div>
          </div>
        </Panel>
      </div>
      <Notice>
        <CheckCircle2 size={15} className="inline-icon" />{" "}
        日常使用前，请确认节点显示「运行正常」，客户端使用兼容 Mihomo
        的最新稳定版，且设备订阅处于启用状态。
      </Notice>
    </>
  );
}
export function NotFound() {
  return (
    <Empty
      title="这个页面不存在"
      description="可以返回控制台，继续管理你的私人网络。"
      action={
        <Link className="button button-primary" to="/">
          返回总览
        </Link>
      }
    />
  );
}
