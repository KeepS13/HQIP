import type { UserProfile, PortalConfig } from "./types";
import { Link, Route, Routes } from "react-router-dom";
import { Empty, ErrorNotice, Loading, Notice } from "../components";
import { useUserData } from "./api";
import { AccountSettings } from "./AccountSettings";
import { OwnedSubscriptions, UserOverview, UserSubscriptionDetail } from "./SubscriptionPages";
import { SubscriptionCatalog } from "./Commerce";
import { UserOrderDetail, UserOrders } from "./Orders";
import { ClientImports } from "./ClientImports";
import { Downloads } from "./Downloads";
import { Authorize } from "./Authorize";
export function PortalHome({ onLogout }: { onLogout: () => void }) {
  const profile = useUserData<UserProfile>("/profile", 5000),
    config = useUserData<PortalConfig>("/config", 30000);
  if (!profile.data)
    return profile.loading ? (
      <Loading />
    ) : (
      <ErrorNotice
        message={profile.error}
        retry={() => void profile.refresh()}
      />
    );
  const data = {
    ...profile.data,
    subscriptions: profile.data.subscriptions || [],
    sessions: profile.data.sessions || [],
  };
  return (
    <>
      {config.data?.recoveryHold && (
        <Notice tone="warning">备份恢复核对中，暂时不能建立新连接。</Notice>
      )}
      <ErrorNotice
        message={
          profile.error ? `${profile.error}；当前显示上次成功读取的数据。` : ""
        }
        retry={() => void profile.refresh()}
      />
      <Routes>
        <Route path="/" element={<UserOverview profile={data} />} />
        <Route
          path="/catalog"
          element={<SubscriptionCatalog userId={data.user.id} />}
        />
        <Route path="/orders" element={<UserOrders />} />
        <Route path="/orders/:id" element={<UserOrderDetail />} />
        <Route path="/subscriptions" element={<OwnedSubscriptions />} />
        <Route path="/subscriptions/:id" element={<UserSubscriptionDetail />} />
        <Route path="/imports" element={<ClientImports subscriptions={data.subscriptions} />} />
        <Route path="/downloads" element={<Downloads />} />
        <Route
          path="/account"
          element={
            <AccountSettings
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
