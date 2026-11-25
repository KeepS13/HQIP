import { useEffect, useState } from "react";
import { CheckCircle2, LoaderCircle, Terminal } from "lucide-react";
import { API_URL } from "../config";
import { api } from "../api";
import { Button, ErrorNotice, Field, Modal, Panel } from "../components";
import { DeployNode } from "./NodeActions";
import type { Node, TaskEvent } from "../types";

export function EditNode({
  node,
  onClose,
  refresh,
}: {
  node: Node;
  onClose: () => void;
  refresh: () => Promise<void>;
}) {
  const [name, setName] = useState(node.name),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title="编辑节点设置"
      description="调整节点名称，变更不会中断现有连接。"
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
            await api(`/nodes/${node.id}`, "PATCH", {
              name: name.trim(),
            });
            await refresh();
            onClose();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field label="节点名称">
          <input
            required
            maxLength={80}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <ErrorNotice message={error} />
        <div className="modal-actions">
          <Button variant="secondary" type="button" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy}>
            保存设置
          </Button>
        </div>
      </form>
    </Modal>
  );
}
export function RetryNode(props: { node: Node; onClose: () => void; refresh: () => Promise<void> }) {
  return <DeployNode {...props} />;
}

const stageLabels: Record<string, string> = {
  preflight: "环境预检",
  connect: "SSH 连接",
  ssh: "SSH 连接",
  install: "安装服务",
  certificate: "申请证书",
  tls: "申请证书",
  start: "启动服务",
  verify: "连接验证",
  done: "部署完成",
  ready: "部署完成",
  error: "部署失败",
  queued: "等待执行",
  deploy: "部署服务",
  external: "外部验证",
  warning: "提醒",
};
export function TaskLog({ node, events }: { node: Node; events: TaskEvent[] }) {
  const [liveEvents, setLiveEvents] = useState<TaskEvent[]>([]),
    [connected, setConnected] = useState(false);
  useEffect(() => {
    setLiveEvents([]);
    const stream = new EventSource(`${API_URL}/api/v1/nodes/${node.id}/events`, { withCredentials: true });
    stream.onopen = () => setConnected(true);
    stream.onerror = () => setConnected(false);
    stream.addEventListener("progress", (event) => {
      try {
        const value = JSON.parse((event as MessageEvent).data) as TaskEvent;
        setLiveEvents((previous) =>
          [...previous.filter((e) => e.id !== value.id), value].slice(-200),
        );
      } catch {
        /* periodic detail refresh supplies a fallback */
      }
    });
    return () => stream.close();
  }, [node.id]);
  const all = Array.from(
    new Map([...(events || []), ...liveEvents].map((e) => [e.id, e])).values(),
  )
    .sort((a, b) => b.id - a.id)
    .slice(0, 100);
  return (
    <Panel
      title="部署与验证日志"
      subtitle="任务进度实时更新，敏感信息由服务端脱敏"
      action={
        <span className="log-connection">
          <span className={`status-dot ${connected ? "green" : "amber"}`} />
          {connected ? "实时连接" : "轮询更新"}
        </span>
      }
    >
      <div className="task-log">
        {all.length ? (
          all.map((event, i) => (
            <div
              className={`task-event ${event.stage === "error" ? "task-error" : ""}`}
              key={event.id}
            >
              <span className="task-marker">
                {event.stage === "error" ? (
                  "!"
                ) : i === 0 &&
                  ["pending", "installing", "verifying"].includes(
                    node.status,
                  ) ? (
                  <LoaderCircle size={13} className="spin" />
                ) : (
                  <CheckCircle2 size={13} />
                )}
              </span>
              <span className="task-time">
                {new Date(event.createdAt).toLocaleTimeString("zh-CN", {
                  timeZone: "Asia/Shanghai",
                  hour12: false,
                })}
              </span>
              <div>
                <strong>{stageLabels[event.stage] || event.stage}</strong>
                <p>{event.message}</p>
              </div>
            </div>
          ))
        ) : (
          <div className="log-empty">
            <Terminal size={19} />
            部署启动后，进度日志将显示在这里。
          </div>
        )}
      </div>
    </Panel>
  );
}

