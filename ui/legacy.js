const $ = (s) => document.querySelector(s);
const ICON = {
  copy: '<svg viewBox="0 0 24 24"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>',
  stop: '<svg viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>',
  redo: '<svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/></svg>',
  ok: '<svg viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></svg>',
  bad: '<svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>',
  run: '<svg viewBox="0 0 24 24" class="spin"><path d="M21 12a9 9 0 1 1-6.2-8.5"/></svg>',
  wait: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/></svg>',
  plus: '<svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>',
  trash:
    '<svg viewBox="0 0 24 24"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>',
  x: '<svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>',
  lock: '<svg viewBox="0 0 24 24" class="lock"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>',
};

// 界面挂在随机前缀下，请求一律走相对路径
async function api(path, opts) {
  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(),
    opts?.method === "POST" ? 90000 : 20000,
  );
  let r, d;
  try {
    r = await fetch(
      path.replace(/^\//, ""),
      Object.assign({}, opts, { signal: controller.signal }),
    );
    if (r.status === 401) {
      location.reload();
      throw new Error("登录已过期，请重新登录");
    }
    d = await r.json();
  } catch (err) {
    throw new Error(
      err.name === "AbortError" ? "请求超时，请稍后重试" : err.message,
    );
  } finally {
    clearTimeout(timer);
  }
  if (!r.ok) throw new Error(d.error || "HTTP " + r.status);
  return d;
}
function esc(s) {
  return String(s == null ? "" : s).replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
}

let toastTimer;
function toast(msg, bad) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = "toast show" + (bad ? " bad" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.className = "toast";
  }, 2400);
}
async function copy(text) {
  // navigator.clipboard 只在 HTTPS/localhost 下存在，而面板通常是 http://IP 访问，
  // 所以必须留一条 execCommand 兜底路径，否则复制在正常使用场景里必然失败。
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      toast("已复制");
      return;
    } catch (e) {}
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  // 放在视口内但不可见：置于视口外会让 iOS 在聚焦时滚动页面
  ta.style.cssText =
    "position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;padding:0;border:0";
  document.body.appendChild(ta);
  const prev = document.activeElement;
  ta.focus();
  ta.setSelectionRange(0, ta.value.length);
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch (e) {}
  ta.remove();
  if (prev && prev.focus) prev.focus();
  toast(ok ? "已复制" : "复制失败，请手动选中", !ok);
}

let view = { exits: [], direct: [], panel: "", backend: "", public_ip: "" };
let inbounds = [];

// 自建模式下入站由 fanout 自己管，界面要提供新建入口；
// 接管 3x-ui 时入站归面板管，这里只读不写。
function isNative() {
  return view.backend === "native";
}
// xray-cf-lite 模式下节点归它管，fanout 只改路由，界面不给新建入口
function isXCL() {
  return view.backend === "xray-cf-lite";
}
const BACKEND_NAME = {
  native: "自建 Xray",
  "3x-ui": "3x-ui",
  "xray-cf-lite": "xray-cf-lite",
};
function backendName() {
  return BACKEND_NAME[view.backend] || "3x-ui";
}

const STATUS = {
  up: "已连通",
  starting: "连接中",
  failed: "失败",
  stopped: "已停止",
};

function renderExits() {
  renderDashboardExits();
}

// 停掉出口后它的入站会留在面板里。这些入站现在走直连，
// 用户既看不出它们和 fanout 的关系，也没有清理入口，所以单独列出来。
function renderOrphans() {
  const box = $("#orphans");
  const list = view.direct || [];
  if (!list.length) {
    box.innerHTML = "";
    return;
  }
  const hasUp = view.exits.some((e) => e.status === "up");
  box.innerHTML =
    '<div class="orphan"><div class="top">' +
    '<h3>直连与未绑定节点</h3><span class="count">' +
    list.length +
    " 个</span>" +
    '<span class="spacer"></span>' +
    (isXCL()
      ? ""
      : '<button data-delorphans="1" title="删除这些入站">' +
        ICON.trash +
        "清理</button>") +
    "</div>" +
    list
      .map(
        (i) =>
          '<div class="orow">' +
          '<button class="chip" data-detail="' +
          i.id +
          '" title="' +
          esc((i.remark || i.protocol) + " · " + i.protocol + " :" + i.port) +
          '">' +
          esc(i.remark || i.protocol) +
          " :" +
          i.port +
          "</button>" +
          '<span class="meta">' +
          (i.bound_to
            ? "家宽离线，已阻断"
            : i.protected
              ? "原直连，已保护"
              : "直连") +
          "</span>" +
          '<span class="spacer"></span>' +
          (hasUp
            ? '<select class="obind" data-tag="' +
              esc(i.tag) +
              '">' +
              exitOptions("") +
              "</select>"
            : '<span class="dim">先开一个出口</span>') +
          (isXCL()
            ? ""
            : '<button class="icon danger" data-delone="' +
              i.id +
              '" data-name="' +
              esc((i.remark || i.protocol) + " :" + i.port) +
              '" title="删除这个入站">' +
              ICON.trash +
              "</button>") +
          "</div>",
      )
      .join("") +
    "</div>";
}

function renderJobs(jobs) {
  jobs = Array.isArray(jobs) ? jobs : [];
  const box = $("#jobs");
  box.innerHTML = jobs
    .map((j) => {
      const steps = (j.steps || [])
        .map((s) => {
          const ic =
            { ok: ICON.ok, failed: ICON.bad, running: ICON.run }[s.status] ||
            ICON.wait;
          const t = s.detail ? s.label + " — " + s.detail : s.label;
          return (
            '<span class="step ' +
            s.status +
            '" title="' +
            esc(t) +
            '">' +
            ic +
            esc(s.status === "ok" && s.detail ? s.detail : s.label) +
            "</span>"
          );
        })
        .join("");
      const close =
        j.status === "running"
          ? ""
          : '<button class="icon" data-job="' +
            esc(j.id) +
            '" title="关闭">' +
            ICON.x +
            "</button>";
      return (
        '<div class="job"><div class="top"><strong>' +
        esc(j.summary) +
        "</strong>" +
        '<span class="count">' +
        j.done +
        "/" +
        j.total +
        "</span>" +
        '<span class="spacer"></span>' +
        close +
        "</div>" +
        '<div class="steps">' +
        steps +
        "</div></div>"
      );
    })
    .join("");
}

let pollRunning = false;
async function poll() {
  if (pollRunning) return;
  pollRunning = true;
  try {
    const next = await api("/api/exits");
    if (!next || !Array.isArray(next.exits))
      throw new Error("出口数据格式异常");
    view = Object.assign({}, next, {
      direct: Array.isArray(next.direct) ? next.direct : [],
    });
    $("#panel").textContent = view.panel || view.panel_info || backendName();
    $("#newnode").hidden = isXCL();
    $("#exportAll").hidden = isXCL();
    renderExits();
    renderOrphans();
    uiConnectivity(true);
  } catch (err) {
    uiConnectivity(false, err.message);
  }
  try {
    renderJobs(await api("/api/jobs"));
  } catch (err) {
  } finally {
    pollRunning = false;
  }
}

// ---- 新建向导 ----
let regions = [],
  region = "",
  regionsLoaded = false;

function openModal(id) {
  $("#" + id).classList.add("open");
}
function closeModal(id) {
  $("#" + id).classList.remove("open");
}

document.addEventListener("click", (e) => {
  const c = e.target.closest("[data-close]");
  if (c) closeModal(c.dataset.close);
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape")
    document
      .querySelectorAll(".modal.open")
      .forEach((m) => m.classList.remove("open"));
});
document.querySelectorAll(".modal").forEach((m) => {
  m.onclick = (e) => {
    if (e.target === m) m.classList.remove("open");
  };
});

let regionInitialized = false;
function renderRegions() {
  const kw = $("#rgfilter").value.trim().toLowerCase();
  const list = regions.filter(
    (r) =>
      !kw ||
      r.code.toLowerCase().includes(kw) ||
      r.name.toLowerCase().includes(kw),
  );
  $("#regions").innerHTML = [
    '<button class="rg' +
      (region === "" ? " sel" : "") +
      '" data-rg=""><b>不限地区</b><em>速度优先</em></button>',
    '<button class="rg' +
      (region === "*" ? " sel" : "") +
      '" data-rg="*"><b>每个国家</b><em>' +
      regions.length +
      " 个国家各来几个</em></button>",
  ]
    .concat(
      list.map(
        (r) =>
          '<button class="rg' +
          (region === r.code ? " sel" : "") +
          '" data-rg="' +
          esc(r.code) +
          '"><b>' +
          esc(r.name || r.code) +
          "</b>" +
          "<em>" +
          r.available +
          " 个空闲 · " +
          r.best_speed_mbps.toFixed(0) +
          " Mbps</em></button>",
      ),
    )
    .join("");
  updateAvail();
}

function availOf(code) {
  if (code === "") return regions.reduce((a, r) => a + r.available, 0);
  if (code === "*") return regions.reduce((a, r) => a + r.available, 0);
  const r = regions.find((x) => x.code === code);
  return r ? r.available : 0;
}

function updateAvail() {
  const want = Number($("#count").value) || 0;
  const hint = $("#availhint");
  // 选了"每个国家"时，数量的意思是每国几个，提示要给出总条数
  $("#countlabel").textContent = region === "*" ? "每个国家几个" : "数量";
  if (region === "*") {
    const n = regions.length;
    const total = Math.min(n * want, availOf("*"));
    hint.className = "hint";
    hint.textContent = n
      ? n + " 个国家 × " + want + "，一共 " + total + " 条出口"
      : "还没有可用节点";
    $("#go").disabled = !n || !want;
    return;
  }
  const avail = availOf(region);
  hint.textContent = avail
    ? "可用 " + avail + " 个节点"
    : "这个地区没有空闲节点";
  hint.className = "hint" + (want > avail ? " bad" : "");
  if (want > avail && avail)
    hint.textContent = "只剩 " + avail + " 个，将全部使用";
  $("#go").disabled = !avail;
}

async function loadWizard() {
  try {
    regions = (await api("/api/regions")) || [];
    regionsLoaded = true;
    regions.sort((a, b) => (a.code === "JP" ? -1 : b.code === "JP" ? 1 : 0));
    if (!regionInitialized) {
      if (regions.some((r) => r.code === "JP")) region = "JP";
      regionInitialized = true;
    }
    renderRegions();
  } catch (e) {
    toast("读取地区失败: " + e.message, true);
  }

  const sel = $("#tpl");
  // xray-cf-lite 模式不能复制节点，向导退化成"只开出口"，之后在节点详情里挑出口
  if (isXCL()) {
    $("#tplwrap").hidden = true;
    sel.innerHTML = '<option value="0">只开出口，不建节点</option>';
    return;
  }
  $("#tplwrap").hidden = false;
  try {
    // 已经挂在出口上的多半是上一批复制出来的，拿它当模板会套娃，
    // 所以把没绑出口的排在前面并默认选中
    const v = await api("/api/exits");
    const free = v.direct || [];
    const bound = (v.exits || []).flatMap((e) => e.inbounds || []);
    inbounds = free.concat(bound);
    if (!inbounds.length) {
      sel.innerHTML = '<option value="0">还没有节点</option>';
      $("#tplhint").textContent =
        "先用上面的「新建节点」建一个，之后这里可以按它批量生成";
      return;
    }
    const opt = (i) =>
      '<option value="' +
      i.id +
      '">' +
      esc(i.remark || "端口 " + i.port) +
      " · " +
      esc(i.protocol) +
      " :" +
      i.port +
      "</option>";
    sel.innerHTML =
      (free.length
        ? '<optgroup label="未绑定出口">' +
          free.map(opt).join("") +
          "</optgroup>"
        : "") +
      (bound.length
        ? '<optgroup label="已挂在出口上">' +
          bound.map(opt).join("") +
          "</optgroup>"
        : "") +
      '<option value="0">只开出口，不建节点</option>';
    $("#tplhint").textContent =
      "每个出口复制一份，客户端 UUID 保持一致，只有端口不同";
  } catch (e) {
    sel.innerHTML = '<option value="0">' + backendName() + "不可用</option>";
    $("#tplhint").textContent = e.message;
  }
}

document.addEventListener("click", (e) => {
  if (e.target.closest("#newexit") || e.target.closest("#newexit2")) {
    openModal("wizard");
    if (!regionsLoaded) loadWizard();
    else {
      renderRegions();
      loadWizard();
    }
  }
  const rg = e.target.closest("[data-rg]");
  if (rg) {
    region = rg.dataset.rg;
    // "每个国家"是批量，默认每国 1 个，免得一点就开出几十条
    if (region === "*" && Number($("#count").value) > 3)
      $("#count").value = "1";
    renderRegions();
  }
});

// ---- 新建节点 ----
document.addEventListener("click", (e) => {
  if (e.target.closest("#newnode") || e.target.closest("#newnode2")) {
    $("#nnhint").textContent = "";
    syncNodeForm();
    openModal("newnodebox");
  }
});

// 表单随协议/传输/安全层联动：只露出当前组合真正用得到的字段
function syncNodeForm() {
  const proto = $("#nproto").value;
  const net = $("#nnet").value;
  const sec = $("#nsec").value;

  // REALITY 靠模仿 TLS 握手工作，套在自带头部的传输上没有意义
  const realityOK = net === "tcp" || net === "xhttp" || net === "grpc";
  const secSel = $("#nsec");
  for (const o of secSel.options) {
    if (o.value === "reality") o.disabled = !realityOK;
  }
  if (secSel.value === "reality" && !realityOK) secSel.value = "none";

  const cur = secSel.value;
  $("#nsniwrap").hidden = cur !== "tls";
  $("#ncertwrap").hidden = cur !== "tls";
  $("#nkeywrap").hidden = cur !== "tls";
  $("#ndestwrap").hidden = cur !== "reality";

  // Vision 只在 VLESS + 裸 TCP + TLS/REALITY 下有效
  const visionOK = proto === "vless" && net === "tcp" && cur !== "none";
  $("#nvisionwrap").hidden = !visionOK;
  if (!visionOK) $("#nvision").checked = false;

  const needPath =
    net === "ws" || net === "httpupgrade" || net === "xhttp" || net === "grpc";
  $("#npathwrap").hidden = !needPath;
  $("#npathlabel").textContent = net === "grpc" ? "服务名" : "路径";

  $("#nsechint").textContent =
    cur === "reality"
      ? "密钥与 shortId 自动生成"
      : cur === "tls"
        ? "不填证书就用自签，链接会带证书指纹"
        : "";
}
$("#nproto").onchange = syncNodeForm;
$("#nnet").onchange = syncNodeForm;
$("#nsec").onchange = syncNodeForm;

$("#ncreate").onclick = async (e) => {
  const q = new URLSearchParams({
    protocol: $("#nproto").value,
    network: $("#nnet").value,
    security: $("#nsec").value,
    port: ($("#nport").value || "").trim(),
    remark: ($("#nremark").value || "").trim(),
    path: ($("#npath").value || "").trim(),
    sni: ($("#nsni").value || "").trim(),
    cert: ($("#ncert").value || "").trim(),
    key: ($("#nkey").value || "").trim(),
    dest: ($("#ndest").value || "").trim(),
  });
  if ($("#nvision").checked) q.set("vision", "1");
  e.target.disabled = true;
  try {
    const r = await api("/api/panel/inbound/new?" + q.toString(), {
      method: "POST",
    });
    toast("已创建 " + r.protocol + " 节点，端口 " + r.port);
    closeModal("newnodebox");
    $("#nport").value = "";
    $("#nremark").value = "";
    poll();
  } catch (err) {
    toast(err.message, true);
  }
  e.target.disabled = false;
};

$("#rgfilter").oninput = renderRegions;
$("#minus").onclick = () => {
  step(-1);
};
$("#plus").onclick = () => {
  step(1);
};
function step(d) {
  const el = $("#count");
  el.value = Math.min(20, Math.max(1, (Number(el.value) || 1) + d));
  updateAvail();
}
$("#count").oninput = updateAvail;

$("#go").onclick = async (e) => {
  const want = Math.min(Number($("#count").value) || 1, availOf(region) || 1);
  const tpl = $("#tpl").value || "0";
  e.target.disabled = true;
  try {
    await api(
      "/api/provision?count=" +
        want +
        (region === "*"
          ? "&every=1"
          : "&region=" + encodeURIComponent(region)) +
        "&template=" +
        tpl,
      { method: "POST" },
    );
    closeModal("wizard");
    poll();
  } catch (err) {
    toast(err.message, true);
  }
  e.target.disabled = false;
};

// ---- 出口操作 ----
document.addEventListener("click", async (e) => {
  const diag = e.target.closest("[data-diag]");
  if (diag) {
    diag.disabled = true;
    openModal("connection-results");
    $("#connection-results-body").textContent =
      "正在检查出口、HTTPS 和节点连接…";
    try {
      const d = await api("/api/diagnostics?slot=" + diag.dataset.diag, {
        method: "POST",
      });
      uiRenderDiagnostics(d);
      poll();
    } catch (err) {
      $("#connection-results-body").textContent = "检查失败：" + err.message;
    } finally {
      diag.disabled = false;
    }
    return;
  }
  const stop = e.target.closest("[data-stop]");
  if (stop) {
    if (!confirm("停止此出口？绑定的节点会暂停连接。")) return;
    stop.disabled = true;
    try {
      await api("/api/stop?slot=" + stop.dataset.stop, { method: "POST" });
    } catch (err) {
      toast(err.message, true);
    }
    poll();
    return;
  }
  const swap = e.target.closest("[data-swap]");
  if (swap) {
    if (!confirm("更换此出口的 IP？将先核验同国候选，成功后再切换。")) return;
    swap.disabled = true;
    try {
      await api("/api/swap?slot=" + swap.dataset.swap, { method: "POST" });
      toast("正在换节点");
    } catch (err) {
      toast(err.message, true);
    }
    poll();
    return;
  }
  const cred = e.target.closest("[data-cred]");
  if (cred) {
    openCred(Number(cred.dataset.cred));
    return;
  }
  const job = e.target.closest("[data-job]");
  if (job) {
    try {
      await api("/api/jobs/dismiss?id=" + job.dataset.job, { method: "POST" });
    } catch (err) {}
    poll();
    return;
  }
  const del = e.target.closest("[data-delorphans]");
  if (del) {
    const list = (view.direct || []).filter((i) => !i.protected);
    if (!list.length) {
      toast("原直连节点已保护");
      return;
    }
    if (!confirm("删除这 " + list.length + " 个未绑定节点？此操作不可撤销。"))
      return;
    del.disabled = true;
    try {
      await api("/api/xui/delete?ids=" + list.map((i) => i.id).join(","), {
        method: "POST",
      });
      toast("已清理 " + list.length + " 个入站");
    } catch (err) {
      toast(err.message, true);
    }
    poll();
    return;
  }

  const one = e.target.closest("[data-delone]");
  if (one) {
    if (!confirm("删除入站 " + one.dataset.name + "？此操作不可撤销。")) return;
    one.disabled = true;
    try {
      await api("/api/xui/delete?ids=" + one.dataset.delone, {
        method: "POST",
      });
      toast("已删除 " + one.dataset.name);
    } catch (err) {
      toast(err.message, true);
    }
    poll();
  }
});

$("#stopall").onclick = async (e) => {
  if (!confirm("停止全部 " + view.exits.length + " 个出口？")) return;
  e.target.disabled = true;
  for (const x of view.exits) {
    try {
      await api("/api/stop?slot=" + x.slot, { method: "POST" });
    } catch (err) {}
  }
  poll();
};

// ---- 节点详情 ----
let curDetail = null;

// 详情弹窗的重绘要跟轮询解耦：正在编辑时被 poll 刷掉输入会很烦
async function openDetail(id) {
  $("#dbody").innerHTML = '<div class="empty">读取中…</div>';
  curDetail = null;
  $("#ddel").disabled = true;
  openModal("detail");
  try {
    const d = await api("/api/xui/detail?id=" + id);
    curDetail = d;
    renderDetail(d);
    $("#ddel").hidden = isXCL();
    $("#ddel").disabled = isXCL();
  } catch (err) {
    $("#dbody").innerHTML =
      '<div class="empty">读取失败: ' + esc(err.message) + "</div>";
  }
}

// 出口下拉：列出所有已连通的隧道，外加"直连"。绑定按 Xray 的 inboundTag 走。
function exitOptions(currentHost) {
  const up = view.exits.filter((e) => e.status === "up");
  return (
    '<option value=""' +
    (currentHost ? "" : " selected") +
    ">直连（不走隧道）</option>" +
    up
      .map(
        (e) =>
          '<option value="' +
          esc(e.host) +
          '"' +
          (e.host === currentHost ? " selected" : "") +
          ">" +
          esc((e.exit_ip || e.host) + " · " + (e.country || e.region)) +
          "</option>",
      )
      .join("")
  );
}

function renderDetail(d) {
  const owner = view.exits.find((x) =>
    (x.inbounds || []).some((i) => i.id === d.id),
  );

  const clients = (d.clients || [])
    .map((c, i) => {
      const link = (d.links || [])[i] || "";
      return (
        '<div class="client">' +
        '<div class="crow">' +
        '<span class="cemail">' +
        esc(c.email) +
        "</span>" +
        '<span class="cid">' +
        esc(c.id) +
        "</span>" +
        '<span class="spacer"></span>' +
        (link
          ? '<button class="icon" data-copy="' +
            esc(link) +
            '" title="复制链接">' +
            ICON.copy +
            "</button>"
          : "") +
        '<button class="icon" data-creset="' +
        esc(c.email) +
        '" title="换一套凭据，旧链接立即失效">' +
        ICON.redo +
        "</button>" +
        '<button class="icon" data-cdel="' +
        esc(c.email) +
        '" title="删除这个客户端">' +
        ICON.trash +
        "</button>" +
        "</div>" +
        (link ? '<div class="share">' + esc(link) + "</div>" : "") +
        "</div>"
      );
    })
    .join("");

  $("#dtitle").textContent = (d.remark || "节点") + "　:" + d.port;
  // xray-cf-lite 的节点归它自己管，这里只留出口选择，改端口/备注/客户端都不给
  const editable = !isXCL();
  $("#dbody").innerHTML =
    '<dl class="kv">' +
    '<dt>出口</dt><dd><select id="dbind" data-tag="' +
    esc(d.tag) +
    '">' +
    exitOptions(owner ? owner.host : "") +
    "</select></dd>" +
    "<dt>协议</dt><dd>" +
    esc(d.protocol) +
    "　" +
    esc(d.network || "") +
    (d.tls && d.tls !== "none" ? "　" + esc(d.tls) : "") +
    "</dd>" +
    "<dt>监听</dt><dd>" +
    esc(d.listen || "0.0.0.0") +
    "</dd>" +
    "</dl>" +
    (editable
      ? '<div class="editbar">' +
        '<label class="ef"><span>备注</span>' +
        '<input id="dremark" type="text" value="' +
        esc(d.remark || "") +
        '"></label>' +
        '<label class="ef"><span>端口</span>' +
        '<input id="dport" type="text" inputmode="numeric" value="' +
        d.port +
        '"></label>' +
        '<label class="chk"><input type="checkbox" id="denable"' +
        (d.enable === false ? "" : " checked") +
        "> 启用</label>" +
        '<span class="spacer"></span>' +
        '<button class="primary" id="dsave">保存</button>' +
        "</div>" +
        '<div class="chead"><h3>客户端</h3><span class="count">' +
        (d.clients || []).length +
        ' 个</span><span class="spacer"></span>' +
        '<button id="dcadd">' +
        ICON.plus +
        "添加</button></div>" +
        (clients || '<div class="empty">没有客户端</div>')
      : '<div class="hint">这个节点由 xray-cf-lite 管，端口、UUID 和分享链接都去它那边改。这里只决定它走哪条出口。</div>');
}

// 未绑定区的出口下拉，选中即绑
document.addEventListener("change", async (e) => {
  const sel = e.target.closest(".obind");
  if (!sel || !sel.value) return;
  sel.disabled = true;
  try {
    await api(
      "/api/xui/bind?tag=" +
        encodeURIComponent(sel.dataset.tag) +
        "&host=" +
        encodeURIComponent(sel.value),
      { method: "POST" },
    );
    toast("已绑定");
    poll();
  } catch (err) {
    toast(err.message, true);
    sel.disabled = false;
  }
});

// 出口下拉改动即生效。绑定按 inboundTag 走，host 传空表示解绑回直连。
document.addEventListener("change", async (e) => {
  const sel = e.target.closest("#dbind");
  if (!sel) return;
  sel.disabled = true;
  try {
    await api(
      "/api/xui/bind?tag=" +
        encodeURIComponent(sel.dataset.tag) +
        "&host=" +
        encodeURIComponent(sel.value),
      { method: "POST" },
    );
    toast(sel.value ? "已绑定" : "已解绑");
    poll();
  } catch (err) {
    toast(err.message, true);
  }
  sel.disabled = false;
});

document.addEventListener("click", async (e) => {
  const link = e.target.closest("[data-detail]");
  if (link) return openDetail(link.dataset.detail);

  if (e.target.closest("#dsave")) {
    const btn = e.target.closest("#dsave");
    btn.disabled = true;
    const q = new URLSearchParams({
      id: curDetail.id,
      port: ($("#dport").value || "").trim(),
      remark: ($("#dremark").value || "").trim(),
      enable: $("#denable").checked ? "1" : "0",
    });
    try {
      await api("/api/panel/inbound/update?" + q, { method: "POST" });
      toast("已保存");
      await openDetail(curDetail.id);
      poll();
    } catch (err) {
      toast(err.message, true);
      btn.disabled = false;
    }
    return;
  }

  const add = e.target.closest("#dcadd");
  if (add) {
    add.disabled = true;
    try {
      await api("/api/panel/client/add?id=" + curDetail.id, { method: "POST" });
      toast("已添加客户端");
      await openDetail(curDetail.id);
    } catch (err) {
      toast(err.message, true);
      add.disabled = false;
    }
    return;
  }

  const del = e.target.closest("[data-cdel]");
  if (del) {
    if (!confirm("删除客户端 " + del.dataset.cdel + "？它的链接会立即失效。"))
      return;
    del.disabled = true;
    try {
      await api(
        "/api/panel/client/del?id=" +
          curDetail.id +
          "&email=" +
          encodeURIComponent(del.dataset.cdel),
        { method: "POST" },
      );
      toast("已删除");
      await openDetail(curDetail.id);
    } catch (err) {
      toast(err.message, true);
      del.disabled = false;
    }
    return;
  }

  const reset = e.target.closest("[data-creset]");
  if (reset) {
    if (
      !confirm(
        "重置 " + reset.dataset.creset + " 的凭据？已分发的旧链接会立即失效。",
      )
    )
      return;
    reset.disabled = true;
    try {
      await api(
        "/api/panel/client/reset?id=" +
          curDetail.id +
          "&email=" +
          encodeURIComponent(reset.dataset.creset),
        { method: "POST" },
      );
      toast("已重置");
      await openDetail(curDetail.id);
    } catch (err) {
      toast(err.message, true);
      reset.disabled = false;
    }
    return;
  }

  // 详情弹窗里删掉当前这个入站
  const dd = e.target.closest("#ddel");
  if (dd && curDetail) {
    const name =
      (curDetail.remark || curDetail.protocol || "节点") +
      " :" +
      curDetail.port;
    if (
      !confirm(
        "删除入站 " + name + "？它的所有客户端链接都会失效，且不可撤销。",
      )
    )
      return;
    dd.disabled = true;
    try {
      await api("/api/xui/delete?ids=" + curDetail.id, { method: "POST" });
      toast("已删除 " + name);
      curDetail = null;
      closeModal("detail");
      poll();
    } catch (err) {
      toast(err.message, true);
      dd.disabled = false;
    }
  }
});

document.addEventListener("click", (e) => {
  const c = e.target.closest("[data-copy]");
  if (c) copy(c.dataset.copy);
});

// ---- SOCKS5 凭据 ----
let curCred = null;

function socksURL(host, port, user, pass) {
  if (!user) return "socks5://" + host + ":" + port;
  return "socks5://" + user + ":" + pass + "@" + host + ":" + port;
}

// SOCKS5 端口监听在母机（跑 fanout 的这台服务器）上，客户端要连的是母机的
// 公网 IPv4，流量再从出口 IP 出去。出口 IP 是"出去以后"的地址，不能当连接地址。
// public_ip 是后端探测到的母机公网地址；探测不到才退回访问面板用的主机名。
function credHost(e) {
  return view.public_ip || location.hostname || e.host;
}

function openCred(slot) {
  const e = view.exits.find((x) => x.slot === slot);
  if (!e) {
    toast("这个出口不在了", true);
    return;
  }
  curCred = { slot: slot, port: e.port, host: credHost(e) };
  $("#crtitle").textContent = (e.country || e.region) + " · :" + e.port;
  $("#cruser").value = e.socks_user || "";
  $("#crpass").value = e.socks_pass || "";
  refreshCredURL();
  openModal("credbox");
}

function refreshCredURL() {
  if (!curCred) return;
  $("#crurl").textContent = socksURL(
    curCred.host,
    curCred.port,
    $("#cruser").value.trim(),
    $("#crpass").value.trim(),
  );
}
$("#cruser").oninput = refreshCredURL;
$("#crpass").oninput = refreshCredURL;

$("#crrand").onclick = () => {
  // 客户端和服务端都要能识别，只用无歧义、无需转义的字符
  const abc = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const gen = (n) =>
    Array.from(crypto.getRandomValues(new Uint8Array(n)))
      .map((v) => abc[v % abc.length])
      .join("");
  $("#cruser").value = "fo" + gen(6);
  $("#crpass").value = gen(14);
  refreshCredURL();
};

$("#crcopy").onclick = () => {
  copy($("#crurl").textContent);
};

$("#crsave").onclick = async (e) => {
  if (!curCred) return;
  const btn = e.target;
  btn.disabled = true;
  const q = new URLSearchParams({
    slot: curCred.slot,
    user: $("#cruser").value.trim(),
    pass: $("#crpass").value.trim(),
  });
  try {
    const r = await api("/api/cred?" + q, { method: "POST" });
    $("#cruser").value = r.user;
    $("#crpass").value = r.pass;
    refreshCredURL();
    toast("已保存，立即生效");
    poll();
  } catch (err) {
    toast(err.message, true);
  }
  btn.disabled = false;
};

// ---- 导出 ----
$("#exportAll").onclick = async () => {
  const ids = view.exits.flatMap((x) => (x.inbounds || []).map((i) => i.id));
  if (!ids.length) {
    toast("还没有节点可导出", true);
    return;
  }
  $("#exbox").value = "读取中…";
  $("#excount").textContent = "";
  openModal("export");
  try {
    const d = await api("/api/xui/links?ids=" + ids.join(","));
    $("#exbox").value = (d.links || []).join("\n");
    $("#excount").textContent = (d.links || []).length + " 条";
  } catch (err) {
    $("#exbox").value = "导出失败: " + err.message;
  }
};
$("#copyall").onclick = () => {
  const v = $("#exbox").value;
  if (v) copy(v);
};

// ---- 订阅 ----
// 地址用 location.origin 拼：后端给的是访问路径下的相对部分，
// 这样反代、改端口、换路径之后拿到的都是用户此刻真正能访问的地址。
async function showSub(d) {
  $("#suburl").value = location.origin + d.path;
  const n = view ? view.exits.flatMap((x) => x.inbounds || []).length : 0;
  $("#subcount").textContent = n
    ? n + " 个节点"
    : "还没有节点，先开出口再建节点链接";
}
$("#subBtn").onclick = async () => {
  $("#suburl").value = "读取中…";
  $("#subcount").textContent = "";
  openModal("subbox");
  try {
    showSub(await api("/api/sub"));
  } catch (err) {
    $("#suburl").value = "读取失败: " + err.message;
  }
};
$("#subcopy").onclick = () => {
  const v = $("#suburl").value;
  if (v) copy(v);
};
$("#subreset").onclick = async (e) => {
  if (!confirm("换一串口令？旧地址立刻失效，已经配过的客户端要重新填一次。"))
    return;
  e.target.disabled = true;
  try {
    showSub(await api("/api/sub/reset", { method: "POST" }));
    toast("已换新地址");
  } catch (err) {
    toast(err.message, true);
  }
  e.target.disabled = false;
};

// ---- 设置：改密码 / 改路径 / 改端口 / 改本地监听 ----
let curSettings = null;
let curBackend = null;

// 后端切换：把本机能用的模式列出来，装了的可选，没装的置灰并说明原因
async function loadBackendModes() {
  const sel = $("#setBackend");
  const hint = $("#setBackendHint");
  try {
    const m = await api("/api/panel/mode");
    curBackend = m.mode || "";
    sel.innerHTML =
      '<option value="">自动（按本机装了什么挑）</option>' +
      (m.modes || [])
        .map(
          (x) =>
            '<option value="' +
            esc(x.mode) +
            '"' +
            (x.available ? "" : " disabled") +
            ">" +
            esc(x.label) +
            (x.available ? "" : "（没装）") +
            "</option>",
        )
        .join("");
    sel.value = curBackend;
    const bad = (m.modes || []).filter((x) => !x.available);
    hint.textContent = m.describe
      ? "当前：" + m.describe + (bad.length ? "。灰掉的是本机没装的。" : "")
      : "节点从哪来。装了 3x-ui 或 xray-cf-lite 就能直接接管，都没有就用自建。";
  } catch (err) {
    sel.innerHTML = '<option value="">读取失败</option>';
    hint.textContent = err.message;
  }
}

$("#settingsBtn").onclick = async () => {
  $("#setPw").value = "";
  $("#setPath").value = "";
  $("#setPathHint").textContent = "读取中…";
  openModal("settings");
  loadBackendModes();
  try {
    const s = await api("/api/settings");
    curSettings = s;
    $("#setPath").value = (s.base_path || "").replace(/^\//, "");
    $("#setPort").value = s.port || "";
    $("#setListen").value = s.listen_addr || "0.0.0.0";
    $("#setResi").checked = s.residential_only !== false;
    $("#setSources").value = (s.additional_sources || []).join("\n");
    $("#setPathHint").textContent =
      "界面挂在这个路径下，扫端口的探不到。只能用字母数字和 - _。";
    $("#updCur").textContent = s.version || "-";
    $("#updLatest").textContent = "";
    $("#updNotes").hidden = true;
    $("#updApply").hidden = true;
    $("#updCheck").disabled = false;
    $("#updCheck").textContent = "检查更新";
  } catch (err) {
    $("#setPathHint").textContent = "读取失败: " + err.message;
  }
};

// 检查更新：问后端 GitHub 最新版，有新版就亮出更新按钮和更新内容
$("#updCheck").onclick = async (e) => {
  e.target.disabled = true;
  e.target.textContent = "检查中…";
  try {
    const u = await api("/api/update/check");
    $("#updCur").textContent = u.current || "-";
    if (u.has_update) {
      $("#updLatest").textContent = "有新版本 " + u.latest;
      $("#updApplyVer").textContent = u.latest;
      $("#updApply").hidden = false;
      $("#updNotes").textContent = u.notes || "（这个版本没写更新说明）";
      $("#updNotes").hidden = false;
    } else {
      $("#updLatest").textContent = "已是最新";
      $("#updApply").hidden = true;
      $("#updNotes").hidden = true;
    }
  } catch (err) {
    toast(err.message, true);
  }
  e.target.disabled = false;
  e.target.textContent = "检查更新";
};

// 一键更新：后端下载替换二进制并重启服务，进程重启期间界面会短暂断连
$("#updApply").onclick = async (e) => {
  if (
    !confirm(
      "更新到 " +
        $("#updApplyVer").textContent +
        "？服务会重启，界面会短暂断开。",
    )
  )
    return;
  e.target.disabled = true;
  e.target.textContent = "更新中…";
  try {
    const r = await api("/api/update/apply", { method: "POST" });
    if (r.restarting) {
      $("#updNotes").textContent =
        "已下载新版本，服务正在重启，几秒后刷新页面即可。";
      $("#updNotes").hidden = false;
      toast("更新中，服务重启后刷新页面");
      // 给服务重启留点时间再自动刷新
      setTimeout(() => location.reload(), 6000);
    } else {
      toast(r.message || "已是最新版");
      e.target.disabled = false;
      e.target.textContent = "更新到 " + $("#updApplyVer").textContent;
    }
  } catch (err) {
    toast(err.message, true);
    e.target.disabled = false;
    e.target.textContent = "更新到 " + $("#updApplyVer").textContent;
  }
};

// 端口/监听地址变了要提示用户之后从新地址进；密码/路径可原地生效
function nextURL(port, listen, path) {
  const host = listen && listen !== "0.0.0.0" ? listen : location.hostname;
  return (
    location.protocol +
    "//" +
    host +
    ":" +
    port +
    (path ? "/" + path : "") +
    "/"
  );
}

$("#setSave").onclick = async (e) => {
  e.target.disabled = true;
  const body = {};
  const pw = $("#setPw").value.trim();
  if (pw) body.password = pw;
  body.base_path = $("#setPath").value.trim();
  const port = parseInt($("#setPort").value.trim(), 10);
  if (port) body.port = port;
  body.listen_addr = $("#setListen").value;
  body.residential_only = $("#setResi").checked;
  body.additional_sources = $("#setSources")
    .value.split("\n")
    .map((s) => s.trim())
    .filter(Boolean);

  const portChanged =
    curSettings &&
    (port !== curSettings.port ||
      body.listen_addr !== (curSettings.listen_addr || "0.0.0.0"));

  try {
    // 后端和其它设置分属两个接口，先切后端：切失败就别继续，免得用户以为整单都生效了
    const backend = $("#setBackend").value;
    if (curBackend !== null && backend !== curBackend) {
      const r = await api("/api/panel/mode", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode: backend }),
      });
      curBackend = r.mode || "";
      $("#setBackendHint").textContent =
        "当前：" + (r.describe || r.kind || "已切换");
    }
    await api("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (portChanged) {
      const url = nextURL(port, body.listen_addr, body.base_path);
      $("#setPortHint").innerHTML =
        '监听已切换，请从新地址打开：<a href="' +
        esc(url) +
        '">' +
        esc(url) +
        "</a>";
      toast("监听已切换，用新地址重新打开");
      // 端口变了当前连接会断，不自动跳转，让用户看清新地址
    } else {
      toast("已保存");
      // 路径可能变了，重新加载到新路径下
      const np = body.base_path;
      const cur = ((curSettings && curSettings.base_path) || "").replace(
        /^\//,
        "",
      );
      if (np !== cur) {
        location.href =
          location.protocol + "//" + location.host + (np ? "/" + np : "") + "/";
        return;
      }
      closeModal("settings");
      poll();
    }
  } catch (err) {
    toast(err.message, true);
  }
  e.target.disabled = false;
};

document.addEventListener("DOMContentLoaded", () => {
  poll();
  setInterval(poll, 3000);
});
