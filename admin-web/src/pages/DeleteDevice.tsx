import { useState } from "react";
import { api } from "../api";
import { Button, ErrorNotice, Modal, Notice } from "../components";
import type { Device } from "../types";

export function DeleteDevice({ device, onClose, refresh }: { device: Device; onClose: () => void; refresh: () => Promise<void> }) {
  const [busy,setBusy]=useState(false), [error,setError]=useState("");
  async function remove() { setBusy(true);setError("");try {await api(`/devices/${device.id}`,"DELETE");await refresh();onClose();}catch(e){setError((e as Error).message);}finally{setBusy(false);} }
  return <Modal title={`删除设备：${device.name}`} onClose={() => {if(!busy)onClose();}}><div className="form-stack"><Notice tone="warning">设备账号、客户端登录和订阅链接将立即失效，节点会撤销现有连接。失联节点在同步前显示“撤销待同步”。历史流量仍保留，删除后不能重新启用此设备。</Notice><ErrorNotice message={error}/><div className="modal-actions"><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button variant="danger" busy={busy} onClick={() => void remove()}>删除并撤销设备</Button></div></div></Modal>;
}
