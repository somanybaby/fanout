package main

const selectionHTML = `
<style>
.ip-tools{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0}.ip-tools input,.ip-tools select{width:auto;min-width:130px}.ip-scroll{overflow:auto;max-height:55vh}.ip-table{width:100%;border-collapse:collapse;min-width:850px;font-size:12px}.ip-table th,.ip-table td{padding:10px;text-align:left;border-bottom:1px solid var(--line);vertical-align:top}.ip-table th{position:sticky;top:0;background:var(--panel)}.ip-sub{display:block;color:var(--muted);font-size:11px;margin-top:4px}.ip-tag{color:var(--accent)}.ip-row-acts{display:flex;gap:5px;flex-wrap:wrap}.ip-info{padding:10px 12px;border:1px solid var(--line);border-radius:5px;line-height:1.8;margin:10px 0}.ip-note{font-size:12px;color:var(--muted);line-height:1.8}.ip-mini{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin:6px 0}.ip-mini select{width:auto;min-width:110px}
</style>
<div class="modal" id="ip-browser"><div class="sheet" style="width:min(96vw,1180px);max-width:1180px"><div class="head"><h2>选择出口 IP</h2><span class="spacer"></span><button id="ip-close">关闭</button></div><div class="body">
 <div class="ip-tools"><select id="ip-target" aria-label="选用到哪个出口"></select><select id="ip-country" aria-label="筛选国家"></select><input id="ip-search" type="search" placeholder="搜索 IP、运营商、地区"><select id="ip-filter" aria-label="筛选状态"><option value="all">全部候选</option><option value="favorite">已收藏</option><option value="blocked">已排除</option></select><select id="ip-sort" aria-label="排序"><option value="quality">实测质量优先</option><option value="latency">HTTPS 耗时优先</option><option value="favorite">收藏优先</option></select><button id="ip-reload">刷新目录</button></div>
 <div class="ip-note">公布地址用于连接 VPN，实际出口需试连核验。测速是服务器经出口访问 HTTPS 的耗时，不能代表手机延迟。地区以 IP 定位为准，可能存在偏差。</div>
 <div class="ip-info"><label><input type="checkbox" id="ip-risk"> 启用 IPPure 风险筛选：仅接入风险分 &lt;10 的出口</label><div class="ip-note">仅在启用新出口前查询；缓存 30 分钟，低频访问公开 API。查询失败、字段缺失或分数 ≥10 均不放行。评分不保证 AI 或影视解锁。当前正在使用的出口不立即切换。</div></div>
 <div id="ip-status" class="ip-note" aria-live="polite"></div><div class="ip-scroll"><table class="ip-table"><thead><tr><th>IP / 状态</th><th>国家 / 地区</th><th>运营商 / 风险分</th><th>实测与历史</th><th>操作</th></tr></thead><tbody id="ip-rows"></tbody></table></div>
 <details><summary>收藏与排除记录（包含已离开目录的 IP）</summary><div id="ip-marks"></div></details>
</div></div></div>
<div class="modal" id="ip-choice"><div class="sheet"><div class="head"><h2>选用 IP</h2><span class="spacer"></span><button id="ip-choice-close">取消</button></div><div class="body"><div id="ip-choice-info" class="ip-info"></div>
 <label class="f"><span>选择模式</span><select id="ip-mode"><option value="preferred">首选 IP，失效后允许同国接替</option><option value="strict">严格锁定 IP，失效后暂停并重试</option><option value="auto">自动选择，同地区优先、同国备用</option></select></label>
 <label class="f" id="ip-expected-wrap"><span>预期的实际出口 IPv4</span><input id="ip-expected" placeholder="建议先试连，取得实际出口 IP"><small>首选节点的实际出口不匹配时会尝试同国备用；严格锁定时始终拒绝其他 IP。</small></label>
 <label class="f" id="ip-new-rotation-wrap"><span>定时轮换</span><select id="ip-new-rotation"><option value="60">每 60 分钟轮换</option><option value="30">每 30 分钟轮换</option><option value="120">每 120 分钟轮换</option><option value="360">每 360 分钟轮换</option><option value="0">关闭定时轮换</option></select></label>
 <label class="f" id="ip-template-wrap"><span>创建节点链接</span><select id="ip-template"></select><small>按模板复制新入站；原直连的端口和凭据保持。</small></label>
 <div class="ip-note">定时轮换可在出口下设置。严格锁定时轮换关闭；查无合格出口时保持阻断。</div><div id="ip-choice-error" class="errline" aria-live="polite"></div>
 </div><div class="foot"><span class="spacer"></span><button class="primary" id="ip-apply">选用</button></div></div></div>
<div class="modal" id="ip-history"><div class="sheet" style="width:min(96vw,1000px);max-width:1000px"><div class="head"><h2>IP 切换记录</h2><span class="spacer"></span><button id="ip-history-close">关闭</button></div><div class="body"><p class="ip-note">保存最近 200 条核验、切换和失败记录；时间按浏览器所在时区显示。</p><div class="ip-scroll" id="ip-history-body"></div></div></div></div>
<script>
let ipRows=[],ipMarks={},ipPicked=null,ipRefreshing=false;
const ipModeLabel=p=>({auto:'自动选择',preferred:'首选 IP',strict:'严格锁定'})[(p||{}).mode]||'自动选择';
const ipTime=v=>v&&!v.startsWith('0001')?new Date(v).toLocaleString():'尚未检测';
const ipRiskLabel=r=>r&&r.score!==null&&r.score!==undefined?String(r.score)+(r.expires_at&&new Date(r.expires_at)<new Date()?' · 已过期，接入前重查':r.status==='pass'?' · 合格':' · 不合格'):'未查询';
const ipHours=n=>n>0?(n/3600).toFixed(1)+' 小时':'—';
function ipToolsOnExits(){
 document.querySelectorAll('[data-swap]').forEach(b=>{
  const e=view.exits.find(x=>x.slot===Number(b.dataset.swap));if(!e)return;
  const acts=b.parentElement;if(acts.querySelector('[data-ip-open]'))return;
  b.disabled=(e.selection||{}).mode==='strict';
  const select=document.createElement('button');select.textContent='选 IP';select.dataset.ipOpen=e.slot;acts.prepend(select);
  const line=document.createElement('div');line.className='ip-mini';const p=e.selection||{};
  const info=document.createElement('span');info.className='ip-note';info.textContent=ipModeLabel(p)+(p.ip?' · '+p.ip:'')+' · 地区优先：'+(p.region||'首次连接后定位')+' · 风险分：'+ipRiskLabel(e.risk);line.append(info);
  const rotate=document.createElement('select');rotate.setAttribute('aria-label','出口 '+e.slot+' 的定时轮换');rotate.dataset.ipRotate=e.slot;
  [0,30,60,120,360].forEach(n=>{const o=document.createElement('option');o.value=n;o.textContent=n?'每 '+n+' 分钟轮换':'关闭定时轮换';rotate.append(o)});rotate.value=p.rotate_minutes||0;rotate.disabled=p.mode==='strict';line.append(rotate);
  const history=document.createElement('button');history.textContent='切换记录';history.dataset.ipEvents=e.slot;line.append(history);
  b.closest('.exit').append(line);
 });
}
const ipOldRender=renderExits;renderExits=function(){ipOldRender();ipToolsOnExits()};ipToolsOnExits();
const ipOpenButton=document.createElement('button');ipOpenButton.id='ip-open-all';ipOpenButton.textContent='IP 列表';$('#newexit').before(ipOpenButton);
ipOpenButton.onclick=()=>ipOpen(0);
$('#ip-close').onclick=()=>$('#ip-browser').classList.remove('open');$('#ip-choice-close').onclick=()=>$('#ip-choice').classList.remove('open');$('#ip-history-close').onclick=()=>$('#ip-history').classList.remove('open');
document.querySelectorAll('#ip-browser,#ip-choice,#ip-history').forEach(m=>m.addEventListener('click',e=>{if(e.target===m)m.classList.remove('open')}));
async function ipOpen(slot){
 $('#ip-target').innerHTML='<option value="0">新建出口（使用空闲位置）</option>'+view.exits.map(e=>'<option value="'+e.slot+'">出口 '+e.slot+' · '+esc(e.exit_ip||e.region)+'</option>').join('');$('#ip-target').value=slot||0;
 $('#ip-browser').classList.add('open');try{await ipLoad(true)}catch(e){$('#ip-status').textContent=e.message;}
}
async function ipLoad(reset){
 if(ipRefreshing)return;ipRefreshing=true;$('#ip-status').textContent='读取目录与检测记录…';
 try{const all=await Promise.all([api('/api/candidates'),api('/api/ip/marks'),api('/api/ip/risk')]);ipRows=all[0].nodes||[];ipMarks=all[1]||{};$('#ip-risk').checked=all[2].enabled;
  const prev=$('#ip-country').value;const countries=[...new Map(ipRows.map(n=>[n.country,n.country_name||n.country])).entries()].filter(x=>x[0]).sort((a,b)=>a[0]==='JP'?-1:b[0]==='JP'?1:a[0].localeCompare(b[0]));
  $('#ip-country').innerHTML='<option value="">全部国家</option>'+countries.map(c=>'<option value="'+esc(c[0])+'">'+esc(c[1])+'</option>').join('');
  const target=view.exits.find(e=>e.slot===Number($('#ip-target').value));$('#ip-country').value=target?target.region:reset?(countries.some(c=>c[0]==='JP')?'JP':''):prev;$('#ip-country').disabled=!!target;
  $('#ip-status').textContent=ipRows.length+' 个候选 · 目录更新：'+ipTime(all[0].fetched);ipRender();
 }finally{ipRefreshing=false}
}
function ipRender(){
 const kw=$('#ip-search').value.trim().toLowerCase(),country=$('#ip-country').value,filter=$('#ip-filter').value,order=$('#ip-sort').value;
 const rows=ipRows.filter(n=>(!country||n.country===country)&&(filter!=='favorite'||n.favorite)&&(filter!=='blocked'||n.blocked)&&(!kw||[n.ip,n.host,n.country_name,n.identity.ip,n.identity.operator,n.identity.region,n.identity.city].join(' ').toLowerCase().includes(kw)));
 rows.sort((a,b)=>{const qa=a.quality,qb=b.quality;if(order==='favorite'&&a.favorite!==b.favorite)return a.favorite?-1:1;if(order==='latency')return (qa.latency_ms||Infinity)-(qb.latency_ms||Infinity);const ratio=q=>(q.successes||0)/Math.max(1,(q.successes||0)+(q.failures||0));return Number(a.blocked)-Number(b.blocked)||ratio(qb)-ratio(qa)||(qa.latency_ms||Infinity)-(qb.latency_ms||Infinity)});
 $('#ip-rows').innerHTML=rows.map(n=>{const q=n.quality,identity=n.identity,total=(q.successes||0)+(q.failures||0);const pct=total?Math.round(100*q.successes/total)+'% / '+total+' 次':'尚未测量';
  return '<tr><td><b>'+esc(identity.ip||n.ip||'地址待核验')+'</b><small class="ip-sub">公布地址：'+esc(n.ip||'—')+'</small><small class="ip-sub">'+(n.active_slot?'出口 '+n.active_slot+' 使用中':n.blocked?'已排除':'待连接核验')+(n.favorite?' · 已收藏':'')+'</small></td><td>'+esc(n.country_name)+'<small class="ip-sub">'+esc([identity.region,identity.city].filter(Boolean).join(' · ')||'地区待核验')+'</small></td><td>'+esc(identity.operator||'待核验')+'<small class="ip-sub">IPPure：'+esc(ipRiskLabel(n.risk))+'</small></td><td>'+pct+'<small class="ip-sub">HTTPS：'+(q.latency_ms?Math.round(q.latency_ms)+' ms':'尚未测量')+' · 连续运行：'+ipHours(n.stable_seconds)+'</small><small class="ip-sub">来源公布带宽：'+n.advertised_speed_mbps.toFixed(0)+' Mbps（非实测）</small><small class="ip-sub">'+ipTime(q.last_checked)+'</small></td><td><div class="ip-row-acts"><button data-ip-use="'+esc(n.host)+'" '+(n.blocked?'disabled':'')+'>选用</button><button data-ip-trial="'+esc(n.host)+'" '+(n.active_slot||n.blocked?'disabled':'')+'>试连</button><button data-ip-star="'+esc(n.host)+'">'+(n.favorite?'取消收藏':'收藏')+'</button><button data-ip-ban="'+esc(n.host)+'">'+(n.blocked?'取消排除':'排除')+'</button></div></td></tr>'}).join('')||'<tr><td colspan="5">当前筛选没有候选，请更换国家或筛选条件。</td></tr>';
 $('#ip-marks').innerHTML=Object.entries(ipMarks).filter(x=>x[1].favorite||x[1].blocked).map(([ip,m])=>'<div class="ip-mini"><span>'+esc(ip)+' · '+(m.favorite?'已收藏 ':'')+(m.blocked?'已排除':'')+'</span><button data-ip-clear="'+esc(ip)+'">清除记录</button></div>').join('')||'<p class="ip-note">暂无记录</p>';
}
['ip-country','ip-filter','ip-sort'].forEach(id=>$('#'+id).onchange=ipRender);$('#ip-search').oninput=ipRender;
$('#ip-target').onchange=()=>ipLoad(true).catch(e=>toast(e.message,true));
$('#ip-reload').onclick=async()=>{try{await api('/api/refresh',{method:'POST'});await ipLoad(false)}catch(e){toast(e.message,true)}};
$('#ip-risk').onchange=async e=>{const enabled=e.target.checked;try{await api('/api/ip/risk',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({enabled})});toast('风险筛选已'+(enabled?'开启':'关闭'))}catch(err){e.target.checked=!enabled;toast(err.message,true)}};
function ipChoiceMode(){const locked=$('#ip-mode').value==='strict';$('#ip-new-rotation').disabled=locked;if(locked)$('#ip-new-rotation').value='0';$('#ip-expected-wrap').hidden=$('#ip-mode').value==='auto';}
$('#ip-mode').onchange=ipChoiceMode;
async function ipChoose(n){
 ipPicked=n;$('#ip-choice-info').textContent=(n.identity.ip||n.ip||n.host)+' · '+n.country_name+' · '+(n.identity.region||'地区待核验');$('#ip-choice-error').textContent='';$('#ip-expected').value=n.identity.ip||n.ip||'';$('#ip-mode').value='preferred';ipChoiceMode();
 const slot=Number($('#ip-target').value);$('#ip-template-wrap').hidden=slot>0;$('#ip-new-rotation-wrap').hidden=slot>0;if(slot===0)$('#ip-new-rotation').value=$('#ip-mode').value==='strict'?'0':'60';
 const ibs=(view.direct||[]).concat(view.exits.flatMap(e=>e.inbounds||[]));$('#ip-template').innerHTML='<option value="0">只开出口，不创建节点链接</option>'+ibs.map(i=>'<option value="'+i.id+'">按 '+esc(i.remark||i.protocol)+' :'+i.port+' 复制节点链接</option>').join('');$('#ip-choice').classList.add('open');
}
$('#ip-apply').onclick=async e=>{if(!ipPicked)return;e.target.disabled=true;try{const r=await api('/api/ip/select',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({slot:Number($('#ip-target').value),host:ipPicked.host,mode:$('#ip-mode').value,ip:$('#ip-expected').value.trim(),template_id:Number($('#ip-template').value),rotate_minutes:Number($('#ip-new-rotation').value)})});$('#ip-choice').classList.remove('open');$('#ip-browser').classList.remove('open');toast('正在按规则核验出口，结果见出口和切换记录');poll()}catch(err){$('#ip-choice-error').textContent=err.message}finally{e.target.disabled=false}};
async function ipMark(ip,mark){await api('/api/ip/marks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(Object.assign({ip},mark))});await ipLoad(false)}
document.addEventListener('change',async e=>{if(!e.target.dataset.ipRotate)return;const el=e.target;el.disabled=true;try{await api('/api/ip/rotation?slot='+el.dataset.ipRotate+'&minutes='+el.value,{method:'POST'});toast('轮换间隔已保存');poll()}catch(err){toast(err.message,true)}finally{el.disabled=false}});
document.addEventListener('click',async e=>{
 const open=e.target.closest('[data-ip-open]');if(open){ipOpen(Number(open.dataset.ipOpen));return}
 const hist=e.target.closest('[data-ip-events]');if(hist){$('#ip-history').classList.add('open');$('#ip-history-body').textContent='读取中…';try{const rows=await api('/api/ip/events?slot='+hist.dataset.ipEvents);$('#ip-history-body').innerHTML=rows.map(r=>'<div class="ip-info"><b>'+esc(ipTime(r.time))+'</b> · '+esc(r.country)+'<br>'+esc(r.reason)+'<br>'+esc(r.old_ip||'—')+' → '+esc(r.new_ip||'—')+'</div>').join('')||'暂无切换记录'}catch(err){$('#ip-history-body').textContent=err.message}return}
 const use=e.target.closest('[data-ip-use]');if(use){ipChoose(ipRows.find(n=>n.host===use.dataset.ipUse));return}
 const trial=e.target.closest('[data-ip-trial]');if(trial){trial.disabled=true;try{await api('/api/ip/trial',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({host:trial.dataset.ipTrial})});toast('正在临时试连，完成后会释放位置');poll()}catch(err){toast(err.message,true)}return}
 const star=e.target.closest('[data-ip-star]'),ban=e.target.closest('[data-ip-ban]'),clear=e.target.closest('[data-ip-clear]');if(star||ban||clear){try{if(clear){await ipMark(clear.dataset.ipClear,{favorite:false,blocked:false});return}const row=ipRows.find(n=>n.host===(star?star.dataset.ipStar:ban.dataset.ipBan));const ip=row.identity.ip||row.ip;if(!ip)throw new Error('该节点尚无有效 IP，请先试连');if(ban&&row.active_slot&&!row.blocked)throw new Error('此 IP 正在使用，请先选用其他 IP 或停止该出口');await ipMark(ip,{favorite:star?!row.favorite:row.favorite,blocked:ban?!row.blocked:row.blocked})}catch(err){toast(err.message,true)}return}
});
setInterval(()=>{if($('#ip-browser').classList.contains('open'))ipLoad(false).catch(()=>{})},10000);
</script>
`
