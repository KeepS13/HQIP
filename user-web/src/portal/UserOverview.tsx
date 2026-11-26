import { Link } from "react-router-dom";
import { ArrowRight, ArrowUpRight, ClipboardList, Globe2, Package, Plus, ShieldCheck, Users } from "lucide-react";
import { CopyButton, Empty, Panel } from "../components";
import type { UserProfile } from "./types";
import { SubscriptionCard } from "./SubscriptionCard";
import { RenewalButton } from "./Renewal";
import { QQ_GROUP, QQ_GROUP_URL } from "./CommunityContact";

// Account summary and onboarding actions, including the public support group.
export function UserOverview({ profile }: { profile: UserProfile }) {
  const subscriptions = profile.subscriptions || [],
    // Exhaustion does not end the term: keep the sole renewal entry available
    // without implying that prepaying restores this period's used traffic.
    active = subscriptions.filter((s) => s.status === "active" || s.status === "exhausted"),
    pending = subscriptions.filter((s) => s.status === "pending");
  return (
    <>
      <div className="portal-welcome">
        <div>
          <span className="eyebrow">YOUR CONNECTION, AT A GLANCE</span>
          <h1>每一份连接，都有自己的空间。</h1>
          <p>订阅独立计量，在客户端选择这一台设备要使用的流量包。</p>
        </div>
        <div className="portal-welcome-icon">
          <Globe2 size={64} strokeWidth={1} />
          <span />
          <i />
        </div>
      </div>
      <div className="user-sub-overview">
        <div>
          <Package size={22} />
          <section>
            <span>有效订阅</span>
            <strong>
              {active.length}
              <small> / {subscriptions.length} 份全部</small>
            </strong>
          </section>
        </div>
        <div>
          <ShieldCheck size={22} />
          <section>
            <span>各份独立使用</span>
            <strong>额度 · 有效期 · 带宽</strong>
          </section>
        </div>
        <Link to="/catalog">
          <Plus size={22} />
          <section>
            <span>发现下一份订阅</span>
            <strong>浏览订阅商城 →</strong>
          </section>
        </Link>
      </div>
      {active.length ? (
        <>
          <div className="user-sub-section-title">
            <h2>正在生效的订阅</h2>
            <Link to="/subscriptions" className="text-link">
              查看全部 →
            </Link>
          </div>
          <div className="user-sub-grid">
            {active.map((s) => (
              <SubscriptionCard key={s.id} value={s} actions={<RenewalButton value={s} />} />
            ))}
          </div>
        </>
      ) : !pending.length ? (
        <Panel title="开始你的连接">
          <Empty
            icon={Package}
            title="还没有有效订阅"
            description="在商城选择一份订阅，正式开通后即可使用。"
            action={
              <Link to="/catalog" className="button button-primary">
                浏览订阅商城 <ArrowRight size={16} />
              </Link>
            }
          />
        </Panel>
      ) : null}
      {!!pending.length && <>
        <div className="user-sub-section-title"><h2>待开通的订阅</h2><span className="muted">分配节点并正式开通后开始计时</span></div>
        <div className="user-sub-grid">{pending.map(s => <SubscriptionCard key={s.id} value={s} />)}</div>
      </>}
      <Panel title="从这里开始">
        <div className="portal-start-grid">
          <Link to="/downloads">
            <span>
              <ArrowUpRight size={25} />
            </span>
            <div>
              <h3>下载 HQIP 客户端</h3>
              <p>登录后，先选订阅，再选节点。</p>
            </div>
            <ArrowRight size={19} />
          </Link>
          <Link to="/orders">
            <span>
              <ClipboardList size={25} />
            </span>
            <div>
              <h3>查看我的订单</h3>
              <p>付款与开通结果自动同步，开通时开始计时。</p>
            </div>
            <ArrowRight size={19} />
          </Link>
          <div className="portal-start-community">
            <span><Users size={25} aria-hidden="true" /></span>
            <div>
              <h3>
                <a className="community-group-link" href={QQ_GROUP_URL} target="_blank" rel="noopener noreferrer">
                  加入 QQ 交流群 <ArrowUpRight size={16} aria-hidden="true" />
                </a>
              </h3>
              <p>QQ群：{QQ_GROUP} · 使用交流与帮助</p>
              <CopyButton value={QQ_GROUP} label="复制群号" />
            </div>
          </div>
        </div>
      </Panel>
    </>
  );
}
