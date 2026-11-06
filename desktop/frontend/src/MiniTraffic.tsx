import {ArrowDown, ArrowUp, MemoryStick} from 'lucide-react';

type MiniTrafficProps = {
  points: {uploadRate:number;downloadRate:number}[];
  upload:string;
  download:string;
  memory:string;
  live:boolean;
  status:string;
};

export function MiniTraffic({points,upload,download,memory,live,status}:MiniTrafficProps) {
  const history=live?points:[];
  const peak=Math.max(1024,...history.flatMap(point=>[point.uploadRate,point.downloadRate]));
  const position=(index:number,key:'uploadRate'|'downloadRate')=>({
    x:history.length===1?158:2+index/(history.length-1)*156,
    y:46-Math.max(0,history[index][key])/peak*40,
  });
  const line=(key:'uploadRate'|'downloadRate')=>history.map((_,index)=>{
    const {x,y}=position(index,key);
    return `${x},${y}`;
  }).join(' ');
  return <section className="mini-traffic" aria-label="本机实时流量与内核内存" title="本机流量，不用于订阅扣费">
    <div className="mini-traffic-chart">
      <svg viewBox="0 0 160 50" preserveAspectRatio="none" role="img" aria-label={live?'最近 60 秒本机上传和下载速率':status}>
        <path d="M0 46H160 M0 25H160" className="mini-traffic-grid"/>
        {history.length>1&&<><polyline className="mini-traffic-upload" points={line('uploadRate')}/><polyline className="mini-traffic-download" points={line('downloadRate')}/></>}
        {history.length===1&&<><circle className="mini-traffic-upload-point" cx={position(0,'uploadRate').x} cy={position(0,'uploadRate').y} r="2"/><circle className="mini-traffic-download-point" cx={position(0,'downloadRate').x} cy={position(0,'downloadRate').y} r="2"/></>}
      </svg>
      {(!live||history.length===0)&&<span className="mini-traffic-status">{live?'等待采样':status}</span>}
    </div>
    <dl>
      <div className="mini-traffic-up"><dt><ArrowUp size={12} aria-hidden="true"/>上传</dt><dd>{upload}</dd></div>
      <div className="mini-traffic-down"><dt><ArrowDown size={12} aria-hidden="true"/>下载</dt><dd>{download}</dd></div>
      <div><dt><MemoryStick size={12} aria-hidden="true"/>内核内存</dt><dd>{memory}</dd></div>
    </dl>
  </section>;
}
