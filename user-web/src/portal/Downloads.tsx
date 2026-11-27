import type { Release } from "./types";
import { Link } from "react-router-dom";
import { ArrowRight, Download, Monitor, ShieldCheck } from "lucide-react";
import { CopyButton, Empty, ErrorNotice, Loading, Notice, PageHeader, Panel, bytes } from "../components";
import { useUserData } from "./api";

export function Downloads() {
  const releases = useUserData<{ releases: Release[] }>("/installers", 60000);
  return (
    <>
      <PageHeader
        eyebrow="HQIP CLIENT"
        title="每一台设备，轻松连接"
        description="Windows 下载单个安装包，Android 下载 APK；安装后自动检查更新。"
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
        Windows 更新下载完成后，下次启动自动启用。Android 更新需要确认系统安装提示；大多数安卓设备请选择 ARM64。
      </Notice>
    </>
  );
}
