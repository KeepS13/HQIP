import { useState } from "react";
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
import { expectsNodeReport, hasNodeReport } from "../monitoring";
import { billingFor, billingTotal } from "../billing";
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
  const [period, setPeriod] = useState<"24h" | "7d">("24h");
  const { data, error, loading, refresh } = usePolling<Overview>(`/overview?period=${period}`);
  if (loading) return <Loading />;
  if (!data)
    return <ErrorNotice message={error} retry={() => void refresh()} />;
  const nodes = data.nodes || [],
    devices = data.devices || [],
    expected = nodes.filter(expectsNodeReport),
    live = expected.filter(hasNodeReport),
    ready = nodes.filter(
      (n) => n.status === "ready" && fresh(n) && n.coreHealthy,
    ),
    upload = live.reduce((v, n) => v + n.metrics.uploadRate, 0),
    download = live.reduce((v, n) => v + n.metrics.downloadRate, 0),
    monthUpload = data.usage.monthUpload,
    monthDownload = data.usage.monthDownload,
    month = monthUpload + monthDownload,
    online = devices.filter((d) => d.enabled && d.online).length,
    unknown = !!error || nodes.some((n) => !onlineKnown(n)),
    rateUnknown = !!error || live.length === 0,
    ratePartial = live.length > 0 && live.length < expected.length,
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
      <ErrorNotice message={error ? `${error}；以下保留上次成功数据，已过期。` : ""} retry={() => void refresh()} />
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
              {bytes(monthUpload)}
              <span className="hint-separator" />
              <ArrowDown size={12} />
              {bytes(monthDownload)}
            </>
          }
        />
        <StatCard
          label="在线凭据身份"
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
          label="实时下载速率（含探测）"
          value={rateUnknown ? "—" : `${ratePartial ? "≥ " : ""}${(download * 8 / 1e6).toFixed(2)}`}
          unit="Mbps"
          icon={ArrowDown}
          color="amber"
          hint={
            <>
              <ArrowUp size={12} />
              {rateUnknown ? "当前速率未知" : `上传 ${(upload * 8 / 1e6).toFixed(2)} Mbps${ratePartial ? " · 仅已知节点下限" : ""}`}
            </>
          }
        />
      </div>
      <div className="overview-middle">
        <TrafficChart points={data.traffic} subtitle="上传 + 下载 · 保留已删除节点历史 · 不含探测" period={period} onPeriodChange={setPeriod} error={error} />
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
                历史凭据身份
              </span>
              <strong>{devices.length}</strong>
            </div>
          </div>
          <Link to="/users" className="panel-bottom-link">
            管理用户订阅 <ArrowRight size={15} />
          </Link>
        </Panel>
      </div>
      <Panel
        title="节点概览"
        subtitle="查看健康状态、实时速率与周期额度"
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
                  <th>周期额度</th>
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
                      {!error && hasNodeReport(n) ? (
                        <Rate
                          upload={n.metrics.uploadRate}
                          download={n.metrics.downloadRate}
                        />
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td className="quota-cell">
                      <div className="table-small">{billingFor(n).status === "unconfigured" ? "本月 · 待设置到期" : "本周期"}</div>
                      <Meter
                        value={billingTotal(n)}
                        max={n.monthlyQuotaBytes}
                        label
                      />
                      {!billingFor(n).cycleComplete && <span className="table-small">数据不完整，按已知用量计算</span>}
                    </td>
                    <td>
                      {!error && onlineKnown(n) ? (
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
        <Panel title="需要关注" subtitle="服务器与节点容量提醒；用户订阅额度分别执行">
          <div className="alerts-list">
            {data.alerts.map((alert, i) => (
              <div
                className={`alert-row ${alert.level === "error" ? "alert-error" : ""}`}
                key={i}
              >
                <Info size={17} />
                <span>{alert.message}</span>
                {alert.serverId ? <Link to={`/servers/${alert.serverId}`}>查看服务器 <ArrowRight size={14} /></Link> : alert.nodeId && (
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
          <strong>为用户开通属于他们的连接</strong>
          <p>分配独立订阅，用户即可在客户端选择流量包与节点。</p>
        </div>
        <Link to="/users" className="button button-secondary">
          管理用户 <ArrowRight size={15} />
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
        description="添加节点、创建订阅模板，为用户开通独立流量包。"
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
            root 权限。服务器与节点的容量指标用于监控，用户订阅限制另行执行。
          </p>
          <Link className="text-link" to="/nodes?add=1">
            添加节点 <ArrowRight size={15} />
          </Link>
        </Panel>
        <Panel
          title="02 · 设置订阅与支付"
          subtitle="每份订阅独立累计流量与使用权限"
        >
          <span className="guide-icon green">
            <Monitor size={28} />
          </span>
          <p>
            创建订阅模板，设置售价、共享带宽、一次性流量、有效天数、连接上限与可用节点。
            在支付设置中配置商户后，用户可在商城购买，付款核验成功后自动开通；管理员也可在用户详情直接赠送。
          </p>
          <p>
            每次开通都会新增一份独立流量包，有效期从开通时起算。
            同名订阅可同时有效，额度、带宽与连接上限分别计算。
          </p>
          <Link className="text-link" to="/subscriptions">
            管理订阅 <ArrowRight size={15} />
          </Link>
        </Panel>
        <Panel
          title="03 · 登录客户端，开始连接"
          subtitle="HQIP Windows 客户端，或兼容的 Mihomo 客户端"
        >
          <span className="guide-icon blue">
            <CloudDownload size={28} />
          </span>
          <p>
            在 HQIP Windows
            客户端打开系统浏览器，使用邮箱账号登录并授权后获取节点与规则。选择规则分流或全局代理，按需开启
            TUN。手动断开后，配置刷新不会自动重连。
          </p>
          <p>
            使用 Clash
            时，在用户门户创建并导入订阅，再选择节点。两种客户端都需要当前网络支持
            UDP；TUN 通常需要管理员权限。
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
              <strong>VPN 连接上限</strong>
              <p>
                按真实 VPN 连接计算，跨设备、跨节点共享上限。登录网页或客户端不占名额，
                达到上限时拒绝新连接。
              </p>
            </div>
            <div>
              <strong>客户端连接实例</strong>
              <p>
                同一用户可以建立多个连接。可在用户详情查看并断开当前连接。
              </p>
            </div>
            <div>
              <strong>额度与带宽</strong>
              <p>
                每份订阅的带宽在使用该订阅的连接间共享，一次性流量不按月重置。
                订阅到期或额度耗尽时停止使用该订阅的连接；服务器容量提醒保持独立。
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
              <strong>停止用户访问</strong>
              <p>
                在用户详情停用账户，撤销该用户连接权限。需要短暂断开时，可以单独断开当前连接。
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
                超过 120 秒无法续租时，节点停止用户连接。恢复后同步累计流量与权限，用户可重新连接。
              </p>
            </div>
          </div>
        </Panel>
      </div>
      <Notice>
        <CheckCircle2 size={15} className="inline-icon" />{" "}
        日常使用前，请确认节点显示「运行正常」，客户端使用兼容 Mihomo
        的最新稳定版，且所选订阅处于有效状态。
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
