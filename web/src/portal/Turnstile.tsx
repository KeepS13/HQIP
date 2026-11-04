import { useEffect, useRef, useState } from "react";
import { ErrorNotice } from "../components";

type TurnstileAPI = {
  render: (container: HTMLElement, options: Record<string, unknown>) => string;
  remove: (id: string) => void;
};
declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}
let scriptPromise: Promise<TurnstileAPI> | null = null;
function loadTurnstile(): Promise<TurnstileAPI> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (scriptPromise) return scriptPromise;
  scriptPromise = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src =
      "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
    script.async = true;
    script.defer = true;
    const timer = window.setTimeout(() => {
      script.remove();
      scriptPromise = null;
      reject(new Error("安全验证加载超时，请检查网络后重试"));
    }, 20000);
    script.onload = () => {
      clearTimeout(timer);
      if (window.turnstile) resolve(window.turnstile);
      else {
        scriptPromise = null;
        script.remove();
        reject(new Error("无法初始化安全验证"));
      }
    };
    script.onerror = () => {
      clearTimeout(timer);
      script.remove();
      scriptPromise = null;
      reject(new Error("无法加载安全验证，请检查网络后重试"));
    };
    document.head.appendChild(script);
  });
  return scriptPromise;
}
export function Turnstile({
  siteKey,
  action,
  attempt,
  onToken,
}: {
  siteKey: string;
  action: "login" | "register";
  attempt: number;
  onToken: (token: string) => void;
}) {
  const container = useRef<HTMLDivElement>(null),
    callback = useRef(onToken);
  callback.current = onToken;
  const [error, setError] = useState(""),
    [retry, setRetry] = useState(0);
  useEffect(() => {
    let cancelled = false,
      widget: string | undefined,
      api: TurnstileAPI | undefined;
    setError("");
    callback.current("");
    void loadTurnstile()
      .then((value) => {
        if (cancelled || !container.current) return;
        api = value;
        widget = value.render(container.current, {
          sitekey: siteKey,
          action,
          theme: "dark",
          size: "flexible",
          callback: (token: string) => {
            setError("");
            callback.current(token);
          },
          "expired-callback": () => callback.current(""),
          "error-callback": () => {
            callback.current("");
            setError("安全验证未完成，请重试");
          },
          "timeout-callback": () => {
            callback.current("");
            setError("安全验证已超时，请重试");
          },
        });
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      });
    return () => {
      cancelled = true;
      if (widget && api) api.remove(widget);
    };
  }, [siteKey, action, attempt, retry]);
  return (
    <div className="turnstile-field">
      <div ref={container} />
      <ErrorNotice message={error} retry={() => setRetry((v) => v + 1)} />
    </div>
  );
}
