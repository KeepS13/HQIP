import { useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { ArrowRight, LockKeyhole, Monitor } from "lucide-react";
import { Button, ErrorNotice, Loading, date } from "../components";
import { userAPI, useUserData } from "./api";

export function Authorize() {
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
