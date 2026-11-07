import {Gauge, X} from 'lucide-react';
import './node-latency.css';

export type NodeTests = {subscriptionId:string;pending:boolean;results:{nodeId:string;status:string;delayMs:number}[]};

const statuses:Record<string,string>={waiting:'等待测试',testing:'测试中…',timeout:'超时',failed:'连接失败',unavailable:'未就绪',direct_unavailable:'直连不可用',certificate_error:'证书失败',cancelled:'已取消'};
export function nodeLatency(tests:NodeTests|undefined,nodeId:string) {
  const result=tests?.results?.find(item=>item.nodeId===nodeId);
  return result?{status:result.status==='success'?`${result.delayMs} ms`:statuses[result.status]||'—',statusKind:result.status}:{};
}
export function NodeLatencyButton({pending,disabled,onClick}:{pending:boolean;disabled:boolean;onClick:()=>void}) {
  return <button type="button" className="text-button node-test-button" disabled={disabled&&!pending} onClick={onClick} title="测试本订阅全部节点的 QUIC/TLS 握手延迟，不切换当前连接；不代表代理网页访问延迟。" aria-label={pending?'取消节点延迟测试':'测试全部节点延迟'}>{pending?<X size={12}/>:<Gauge size={12}/>}<span>{pending?'取消测试':'延迟测试'}</span></button>;
}
