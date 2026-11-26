import { useState } from "react";
import { ArrowUpRight, Users } from "lucide-react";
import { CopyButton, ToastContext } from "../components";
import "./community-contact.css";

// Public community contact shared by authentication, portal pages and onboarding.
// Keep the number and owner-provided invitation together. The HTTPS invitation
// lets QQ handle platform-specific opening without guessing an app URI scheme.
export const QQ_GROUP = "383590149";
export const QQ_GROUP_URL = "https://qm.qq.com/q/swSXg1lhPW";

/** Displays the public group without requiring a session or a network request.
 * CopyButton owns clipboard access; feedback stays local so errors also remain
 * visible on the login page, which has no application toast provider.
 */
export function CommunityContact({ compact = false }: { compact?: boolean }) {
  const [feedback, setFeedback] = useState("");
  return (
    <aside
      className={`community-contact${compact ? " community-contact-compact" : ""}`}
      aria-label="QQ 交流群"
    >
      <span className="community-contact-icon" aria-hidden="true">
        <Users size={24} />
      </span>
      <div className="community-contact-info">
        <strong>加入 QQ 交流群</strong>
        <span className="community-contact-number">{QQ_GROUP}</span>
        <p>使用交流与帮助 · 点击加群或复制群号</p>
      </div>
      <div className="community-contact-action">
        <div className="community-contact-buttons">
          <a
            className="button button-primary"
            href={QQ_GROUP_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <ArrowUpRight size={16} aria-hidden="true" />打开 QQ 加群
          </a>
          <ToastContext.Provider value={(message) => setFeedback(message.replace("链接", "群号"))}>
            <CopyButton value={QQ_GROUP} label="复制群号" />
          </ToastContext.Provider>
        </div>
        <span role="status">{feedback}</span>
      </div>
    </aside>
  );
}
