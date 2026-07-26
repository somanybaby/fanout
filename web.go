package main

import "net/http"

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fanout</title>
<style>
:root{
  --bg:#12151a; --panel:#181c23; --line:#262c36; --text:#dde3ec;
  --dim:#8b95a5; --accent:#4a9eda; --ok:#3fa66b; --warn:#c9903a; --bad:#c25450;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);
  font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
header{display:flex;align-items:center;gap:16px;padding:10px 16px;
  border-bottom:1px solid var(--line);background:var(--panel)}
h1{font-size:13px;font-weight:600;margin:0;letter-spacing:0}
.spacer{flex:1}
button{font:inherit;color:var(--text);background:#222833;border:1px solid var(--line);
  border-radius:4px;padding:4px 10px;cursor:pointer}
button:hover{border-color:var(--accent)}
button:disabled{opacity:.45;cursor:default}
button.primary{background:var(--accent);border-color:var(--accent);color:#0b0e12;font-weight:600}
.wrap{display:grid;grid-template-columns:1fr 1fr;gap:1px;background:var(--line);
  height:calc(100vh - 41px)}
section{background:var(--bg);display:flex;flex-direction:column;min-height:0}
.head{display:flex;align-items:center;gap:10px;padding:8px 12px;
  border-bottom:1px solid var(--line);background:var(--panel)}
.head h2{font-size:12px;margin:0;font-weight:600}
.count{color:var(--dim);font-size:11px}
.scroll{overflow:auto;flex:1}
table{width:100%;border-collapse:collapse}
th{position:sticky;top:0;background:var(--panel);text-align:left;font-weight:600;
  color:var(--dim);font-size:11px;padding:6px 10px;border-bottom:1px solid var(--line);
  white-space:nowrap}
td{padding:5px 10px;border-bottom:1px solid #1e232b;white-space:nowrap}
tr:hover td{background:#1a1f27}
.num{text-align:right;font-variant-numeric:tabular-nums}
.dim{color:var(--dim)}
.tag{display:inline-block;padding:1px 6px;border-radius:3px;font-size:11px}
.s-up{background:rgba(63,166,107,.16);color:var(--ok)}
.s-starting{background:rgba(201,144,58,.16);color:var(--warn)}
.s-failed{background:rgba(194,84,80,.16);color:var(--bad)}
.port{color:var(--accent);font-weight:600}
.empty{padding:24px 12px;color:var(--dim);text-align:center}
input[type=search]{font:inherit;background:#0e1116;border:1px solid var(--line);
  color:var(--text);border-radius:4px;padding:4px 8px;width:150px}
input[type=search]:focus{outline:none;border-color:var(--accent)}
.err{color:var(--bad);font-size:11px;max-width:260px;overflow:hidden;
  text-overflow:ellipsis;display:inline-block;vertical-align:bottom}
@media(max-width:860px){.wrap{grid-template-columns:1fr;height:auto}}
select{font:inherit;background:#0e1116;border:1px solid var(--line);color:var(--text);
  border-radius:4px;padding:3px 6px;max-width:150px}
select:focus{outline:none;border-color:var(--accent)}
.modal{position:fixed;inset:0;background:rgba(8,10,14,.72);display:none;
  align-items:center;justify-content:center;z-index:50}
.modal.open{display:flex}
.sheet{background:var(--bg);border:1px solid var(--line);border-radius:6px;
  width:min(760px,92vw);max-height:82vh;display:flex;flex-direction:column}
.sheet .head{border-radius:6px 6px 0 0}
.sheet .scroll{max-height:64vh}
a.lnk{color:var(--text);text-decoration:none;border-bottom:1px dotted var(--dim)}
a.lnk:hover{color:var(--accent);border-color:var(--accent)}
.kv{display:grid;grid-template-columns:88px 1fr;gap:4px 12px;padding:12px 14px}
.kv dt{color:var(--dim)}
.kv dd{margin:0;word-break:break-all}
.share{margin:0 14px 14px;padding:10px;background:#0e1116;border:1px solid var(--line);
  border-radius:4px;word-break:break-all;font-size:12px;line-height:1.7}
.share button{margin-top:8px}
#exbox{width:100%;min-height:320px;background:#0e1116;border:0;color:var(--text);
  font:12px/1.8 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  padding:12px 14px;resize:vertical}
#exbox:focus{outline:none}
</style>
</head>
<body>
<header>
  <h1>fanout</h1>
  <span class="count" id="xui">正在检测 3x-ui…</span>
  <span class="spacer"></span>
  <button id="refresh">重新拉取节点</button>
</header>

<div class="wrap">
  <section>
    <div class="head">
      <h2>运行中</h2>
      <span class="count" id="tcount"></span>
      <span class="spacer"></span>
      <button class="primary" id="addnode">添加节点</button>
      <button id="stopall">全部停止</button>
    </div>
    <div class="scroll">
      <table>
        <thead><tr>
          <th style="width:28px"><input type="checkbox" id="checkall" title="全选已连通"></th>
          <th>端口</th><th>节点</th><th>地区</th><th>出口 IP</th><th>状态</th><th></th>
        </tr></thead>
        <tbody id="tbody"></tbody>
      </table>
      <div class="empty" id="tempty">还没有运行中的隧道，从右侧选一个节点启动</div>
    </div>
  </section>

  <section>
    <div class="head">
      <h2>3x-ui 入站</h2>
      <span class="count" id="icount"></span>
      <span class="spacer"></span>
      <button id="exportBtn" title="导出勾选入站的分享链接">导出链接</button>
      <button id="cloneBtn" title="以选中的入站为模板，为每个勾选的出口各复制一个">按出口复制…</button>
      <button id="reloadin">刷新</button>
    </div>
    <div class="scroll">
      <table>
        <thead><tr>
          <th style="width:26px"><input type="checkbox" id="icheckall" title="全选"></th>
          <th style="width:26px" title="复制模板">模板</th><th>备注</th><th>端口</th><th>出口走</th>
        </tr></thead>
        <tbody id="ibody"></tbody>
      </table>
      <div class="empty" id="iempty">面板里还没有入站，先去 3x-ui 建一个</div>
    </div>
  </section>
</div>

<div class="modal" id="nodemodal">
  <div class="sheet">
    <div class="head">
      <h2>选择节点启动</h2>
      <span class="count" id="ncount"></span>
      <span class="spacer"></span>
      <input type="search" id="filter" placeholder="按地区/主机名筛选">
      <button id="closemodal">关闭</button>
    </div>
    <div class="scroll">
      <table>
        <thead><tr>
          <th>主机名</th><th>地区</th><th class="num">延迟</th>
          <th class="num">带宽</th><th class="num">会话</th><th></th>
        </tr></thead>
        <tbody id="nbody"></tbody>
      </table>
    </div>
  </div>
</div>

<div class="modal" id="exportmodal">
  <div class="sheet">
    <div class="head">
      <h2>导出链接</h2>
      <span class="count" id="excount"></span>
      <span class="spacer"></span>
      <button id="copyall">全部复制</button>
      <button id="closeexport">关闭</button>
    </div>
    <div class="scroll"><textarea id="exbox" spellcheck="false"></textarea></div>
  </div>
</div>

<div class="modal" id="detailmodal">
  <div class="sheet">
    <div class="head">
      <h2 id="dtitle">入站详情</h2>
      <span class="spacer"></span>
      <button id="closedetail">关闭</button>
    </div>
    <div class="scroll" id="dbody"></div>
  </div>
</div>

<script>
const $ = s => document.querySelector(s);
let nodes = [], tunnels = [];
const picked = new Set();
let tplId = 0;
const ipicked = new Set();

// 界面挂在随机前缀下，请求一律走相对路径，去掉开头的斜杠即可
async function api(path, opts){
  const r = await fetch(path.replace(/^\//, ''), opts);
  const d = await r.json().catch(()=>({}));
  if(!r.ok) throw new Error(d.error || ('HTTP '+r.status));
  return d;
}

function statusTag(s){
  const label = {up:'已连通', starting:'连接中', failed:'失败', stopped:'已停止'}[s] || s;
  return '<span class="tag s-'+s+'">'+label+'</span>';
}

function renderTunnels(){
  const tb = $('#tbody');
  // 隧道停掉后把它的勾选也去掉，避免复制到已经不存在的出口
  const alive = new Set(tunnels.filter(t => t.status === 'up').map(t => t.slot));
  for(const s of [...picked]) if(!alive.has(s)) picked.delete(s);
  $('#tempty').style.display = tunnels.length ? 'none' : '';
  updatePickCount();
  tb.innerHTML = tunnels.map(t => {
    const detail = t.status === 'failed' && t.err
      ? '<span class="err" title="'+esc(t.err)+'">'+esc(t.err)+'</span>' : '';
    const sel = t.status === 'up'
      ? '<input type="checkbox" class="pick" value="'+t.slot+'"'+(picked.has(t.slot)?' checked':'')+'>'
      : '';
    return '<tr>'
      + '<td>'+sel+'</td>'
      + '<td class="port">'+t.port+'</td>'
      + '<td>'+esc(t.node.hostname)+'</td>'
      + '<td class="dim">'+esc(t.node.country_code)+'</td>'
      + '<td>'+(t.exit_ip || '<span class="dim">—</span>')+'</td>'
      + '<td>'+statusTag(t.status)+' '+detail+'</td>'
      + '<td><button data-stop="'+t.slot+'">停止</button></td>'
      + '</tr>';
  }).join('');
}

function renderNodes(){
  const kw = $('#filter').value.trim().toLowerCase();
  const running = new Set(tunnels.map(t => t.node.hostname));
  const list = nodes.filter(n => !kw
    || n.hostname.toLowerCase().includes(kw)
    || n.country.toLowerCase().includes(kw)
    || n.country_code.toLowerCase().includes(kw));
  $('#ncount').textContent = list.length + ' 个';
  $('#nbody').innerHTML = list.slice(0,150).map(n => {
    const busy = running.has(n.hostname);
    return '<tr>'
      + '<td>'+esc(n.hostname)+'</td>'
      + '<td class="dim">'+esc(n.country_code)+' '+esc(n.country)+'</td>'
      + '<td class="num">'+n.ping+' ms</td>'
      + '<td class="num">'+n.speed_mbps.toFixed(0)+' Mbps</td>'
      + '<td class="num dim">'+n.sessions+'</td>'
      + '<td><button data-start="'+esc(n.hostname)+'"'+(busy?' disabled':'')+'>'
      + (busy?'已启动':'启动')+'</button></td>'
      + '</tr>';
  }).join('');
}

function esc(s){ return String(s).replace(/[&<>"']/g, c =>
  ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }

async function poll(){
  try{
    tunnels = await api('/api/tunnels') || [];
    renderTunnels(); renderNodes();
    if(inbounds.length) renderInbounds();
  }catch(e){}
}

async function loadNodes(){
  const d = await api('/api/nodes');
  nodes = d.nodes || [];
  renderNodes();
}

document.addEventListener('click', async e => {
  const start = e.target.dataset.start, stop = e.target.dataset.stop;
  if(start){
    e.target.disabled = true; e.target.textContent = '启动中';
    try{ await api('/api/start?host='+encodeURIComponent(start), {method:'POST'}); }
    catch(err){ alert('启动失败: '+err.message); }
    poll();
  }
  if(stop){
    e.target.disabled = true;
    try{ await api('/api/stop?slot='+stop, {method:'POST'}); }
    catch(err){ alert('停止失败: '+err.message); }
    poll();
  }
});

$('#refresh').onclick = async e => {
  e.target.disabled = true; e.target.textContent = '拉取中';
  try{ await api('/api/refresh', {method:'POST'}); await loadNodes(); }
  catch(err){ alert('拉取失败: '+err.message); }
  e.target.disabled = false; e.target.textContent = '重新拉取节点';
};

$('#stopall').onclick = async () => {
  for(const t of tunnels){
    try{ await api('/api/stop?slot='+t.slot, {method:'POST'}); }catch(e){}
  }
  poll();
};

$('#filter').oninput = renderNodes;

let inbounds = [];

async function checkXui(){
  try{
    const d = await api('/api/xui');
    if(d.available){
      $('#xui').textContent = '3x-ui 面板 ' + (d.scheme || 'http') + ' :' + d.port;
      loadInbounds();
    }else{
      $('#xui').textContent = '3x-ui: ' + d.reason;
      $('#iempty').textContent = d.reason;
    }
  }catch(e){}
}

async function loadInbounds(){
  try{
    inbounds = await api('/api/xui/inbounds') || [];
    renderInbounds();
  }catch(e){
    $('#iempty').textContent = '读取入站失败: ' + e.message;
  }
}

function updateInboundCount(){
  const n = ipicked.size;
  $('#icount').textContent = inbounds.length
    ? inbounds.length + ' 个' + (n ? '，已选 ' + n : '')
    : '';
}

function renderInbounds(){
  // 入站被删掉后同步清理勾选
  const alive = new Set(inbounds.map(i => i.id));
  for(const id of [...ipicked]) if(!alive.has(id)) ipicked.delete(id);
  updateInboundCount();
  $('#iempty').style.display = inbounds.length ? 'none' : '';
  const box = $('#icheckall');
  if(box) box.checked = inbounds.length > 0 && ipicked.size === inbounds.length;
  const up = tunnels.filter(t => t.status === 'up');
  $('#ibody').innerHTML = inbounds.map(i => {
    const opts = ['<option value="">直连（不走隧道）</option>'].concat(
      up.map(t => '<option value="'+esc(t.node.hostname)+'"'
        + (i.bound_to === t.node.hostname ? ' selected' : '') + '>'
        + (t.exit_ip || t.node.hostname) + ' · ' + t.port + '</option>')
    );
    // 绑定的节点当前没在跑时照实显示，不要悄悄改掉用户的选择
    if(i.bound_to && !i.bound_up){
      opts.push('<option value="'+esc(i.bound_to)+'" selected>'
        + esc(i.bound_to) + '（未运行）</option>');
    }
    return '<tr>'
      + '<td><input type="checkbox" class="ipick" value="'+i.id+'"'
      +   (ipicked.has(i.id)?' checked':'')+'></td>'
      + '<td><input type="radio" name="tpl" class="tpl" value="'+i.id+'"'
      +   (tplId===i.id?' checked':'')+' title="选作复制模板"></td>'
      + '<td><a href="#" class="lnk" data-detail="'+i.id+'">'
      +   esc(i.remark || '(无备注)')+'</a>'+(i.enable?'':' <span class="dim">停用</span>')
      +   ' <span class="dim">'+esc(i.protocol)+'</span></td>'
      + '<td class="num">'+i.port+'</td>'
      + '<td><select data-tag="'+esc(i.tag)+'">'+opts.join('')+'</select></td>'
      + '</tr>';
  }).join('');
}

$('#cloneBtn').onclick = async e => {
  if(!tplId){ alert('先在下面选一个入站作为模板'); return; }
  const up = tunnels.filter(t => t.status === 'up' && picked.has(t.slot));
  if(!up.length){ alert('先在左边勾选要用的出口'); return; }
  const tpl = inbounds.find(i => i.id === tplId);
  const names = up.map(t => '  · ' + (t.exit_ip || ('槽位'+t.slot))).join('\n');
  if(!confirm('以「' + (tpl ? tpl.remark : tplId) + '」为模板复制 ' + up.length + ' 个入站：\n\n' + names)) return;
  e.target.disabled = true; e.target.textContent = '复制中';
  try{
    const slots = up.map(t => t.slot).join(',');
    const d = await api('/api/xui/clone?id='+tplId+'&slots='+slots, {method:'POST'});
    alert('已创建入站端口: ' + d.created.join(', '));
    await loadInbounds();
  }catch(err){ alert('复制失败: ' + err.message); }
  e.target.disabled = false; e.target.textContent = '按出口复制…';
};

document.addEventListener('change', async e => {
  const tag = e.target.dataset.tag;
  if(!tag) return;
  e.target.disabled = true;
  try{
    await api('/api/xui/bind?tag='+encodeURIComponent(tag)
      +'&host='+encodeURIComponent(e.target.value), {method:'POST'});
    await loadInbounds();
  }catch(err){
    alert('绑定失败: ' + err.message);
    await loadInbounds();
  }
  e.target.disabled = false;
});

$('#reloadin').onclick = loadInbounds;

const xmodal = $('#exportmodal');
$('#closeexport').onclick = () => xmodal.classList.remove('open');
xmodal.onclick = e => { if(e.target === xmodal) xmodal.classList.remove('open'); };

$('#exportBtn').onclick = async e => {
  const ids = ipicked.size ? [...ipicked] : inbounds.map(i => i.id);
  if(!ids.length){ alert('没有可导出的入站'); return; }
  $('#exbox').value = '读取中…';
  $('#excount').textContent = '';
  xmodal.classList.add('open');
  try{
    const d = await api('/api/xui/links?ids=' + ids.join(','));
    const links = d.links || [];
    $('#exbox').value = links.join('\n');
    $('#excount').textContent = links.length + ' 条';
  }catch(err){
    $('#exbox').value = '导出失败: ' + err.message;
  }
};

$('#copyall').onclick = async e => {
  const v = $('#exbox').value;
  if(!v) return;
  try{
    await navigator.clipboard.writeText(v);
    e.target.textContent = '已复制';
    setTimeout(() => { e.target.textContent = '全部复制'; }, 1200);
  }catch(err){
    $('#exbox').select();
    alert('自动复制失败，已选中，请按 Cmd/Ctrl+C');
  }
};

const dmodal = $('#detailmodal');
$('#closedetail').onclick = () => dmodal.classList.remove('open');
dmodal.onclick = e => { if(e.target === dmodal) dmodal.classList.remove('open'); };

document.addEventListener('click', async e => {
  const link = e.target.closest('[data-detail]');
  if(!link) return;
  e.preventDefault();
  $('#dbody').innerHTML = '<div class="empty">读取中…</div>';
  dmodal.classList.add('open');
  try{
    const d = await api('/api/xui/detail?id=' + link.dataset.detail);
    const t = tunnels.find(x => x.node.hostname === d.bound_to);
    const exit = t ? (t.exit_ip + '（' + t.node.hostname + '）')
      : (d.bound_to ? d.bound_to + '（未运行）' : '直连（未绑定隧道）');
    const clients = d.clients.length
      ? d.clients.map(c => c.email + '　' + c.id).join('<br>')
      : '<span class="dim">无</span>';
    const links = (d.links || []).length
      ? d.links.map(l => '<div class="share">' + esc(l)
          + '<br><button data-copy="' + esc(l) + '">复制链接</button></div>').join('')
      : '<div class="share dim">面板未生成分享链接</div>';
    $('#dtitle').textContent = (d.remark || '入站') + '　:' + d.port;
    $('#dbody').innerHTML = '<dl class="kv">'
      + '<dt>出口</dt><dd>' + esc(exit) + '</dd>'
      + '<dt>协议</dt><dd>' + esc(d.protocol) + '　' + esc(d.network || '')
      +   (d.tls && d.tls !== 'none' ? '　' + esc(d.tls) : '') + '</dd>'
      + '<dt>监听</dt><dd>' + esc(d.listen || '0.0.0.0') + ':' + d.port + '</dd>'
      + '<dt>Xray tag</dt><dd>' + esc(d.tag) + '</dd>'
      + '<dt>客户端</dt><dd>' + clients + '</dd>'
      + '</dl>' + links;
  }catch(err){
    $('#dbody').innerHTML = '<div class="empty">读取失败: ' + esc(err.message) + '</div>';
  }
});

document.addEventListener('click', async e => {
  const val = e.target.dataset.copy;
  if(!val) return;
  try{
    await navigator.clipboard.writeText(val);
    const old = e.target.textContent;
    e.target.textContent = '已复制';
    setTimeout(() => { e.target.textContent = old; }, 1200);
  }catch(err){ alert('复制失败，请手动选中'); }
});

const modal = $('#nodemodal');
$('#addnode').onclick = () => { modal.classList.add('open'); $('#filter').focus(); };
$('#closemodal').onclick = () => modal.classList.remove('open');
modal.onclick = e => { if(e.target === modal) modal.classList.remove('open'); };
document.addEventListener('keydown', e => {
  if(e.key === 'Escape'){
    modal.classList.remove('open');
    dmodal.classList.remove('open');
    xmodal.classList.remove('open');
  }
});

// 勾选出口：单个 + 全选
document.addEventListener('change', e => {
  if(e.target.classList.contains('pick')){
    const slot = Number(e.target.value);
    e.target.checked ? picked.add(slot) : picked.delete(slot);
    updatePickCount();
  }
  if(e.target.classList.contains('ipick')){
    const id = Number(e.target.value);
    e.target.checked ? ipicked.add(id) : ipicked.delete(id);
    updateInboundCount();
  }
  if(e.target.id === 'icheckall'){
    ipicked.clear();
    if(e.target.checked) inbounds.forEach(i => ipicked.add(i.id));
    renderInbounds();
  }
  if(e.target.classList.contains('tpl')){
    tplId = Number(e.target.value);
  }
  if(e.target.id === 'checkall'){
    picked.clear();
    if(e.target.checked){
      tunnels.filter(t => t.status === 'up').forEach(t => picked.add(t.slot));
    }
    renderTunnels(); updatePickCount();
  }
});

function updatePickCount(){
  const n = picked.size;
  $('#tcount').textContent = tunnels.length
    ? tunnels.length + ' 条' + (n ? '，已勾选 ' + n : '')
    : '';
}

loadNodes().catch(()=>{});
poll();
checkXui();
setInterval(poll, 3000);
</script>
</body>
</html>`
