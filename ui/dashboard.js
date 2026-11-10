// Presentation only: all node operations keep using the existing authenticated API.
let uiPage = "overview",
  uiCardSignature = "",
  uiEventsLoading = false;
let uiLastEvents = 0,
  uiEvents = [],
  uiConfigLoading = false,
  uiLastFocus = null;
const uiPages = {
  overview: ["出口管理", "查看出口状态，管理节点连接与 IP 轮换"],
  ips: ["IP 候选库", "选择国家与地区，核验并挑选适合的出口 IP"],
  automation: ["轮换与检测", "设置轮换周期、风险筛选与候选检测规则"],
  notifications: ["Telegram 通知", "管理出口 IP 变化的通知方式"],
  settings: ["设置", "管理面板访问、节点来源和版本更新"],
};
const uiCountryNames = {
  JP: "日本",
  US: "美国",
  KR: "韩国",
  TW: "中国台湾",
  HK: "中国香港",
  SG: "新加坡",
  DE: "德国",
  GB: "英国",
  CA: "加拿大",
  FR: "法国",
  AU: "澳大利亚",
};
const uiRegionNames = {
  Tokyo: "东京",
  Osaka: "大阪",
  Texas: "德州",
  Virginia: "弗吉尼亚州",
  Oregon: "俄勒冈州",
  California: "加利福尼亚州",
  Washington: "华盛顿州",
};
function uiCountry(e) {
  return (
    (e.identity || {}).country || (e.selection || {}).country || e.region || ""
  );
}
function uiPlace(e) {
  const code = uiCountry(e),
    country =
      uiCountryNames[code] ||
      (e.country || code || "地区待核验")
        .replace(/[\u{1F1E6}-\u{1F1FF}]/gu, "")
        .trim();
  const region = (e.identity || {}).region || (e.selection || {}).region || "";
  return country + (region ? " · " + (uiRegionNames[region] || region) : "");
}
function uiHealth(e) {
  if (e.status !== "up")
    return {
      label:
        { starting: "连接中", failed: "连接失败", stopped: "已停止" }[
          e.status
        ] || "状态待确认",
      tone: e.status === "failed" ? "bad" : "warn",
      detail: e.err || "等待出口连接",
    };
  const d = e.diagnostics || {},
    checked = Date.parse(d.checked_at);
  if (!Number.isFinite(checked) || checked < 1)
    return { label: "在线", tone: "", detail: "已建立连接 · 等待健康检查" };
  if (Date.now() - checked > 180000)
    return {
      label: "待复查",
      tone: "warn",
      detail: "连接状态待复查，请检查连接",
    };
  if (!d.socks?.ok || !d.https?.ok)
    return {
      label: "连接异常",
      tone: "warn",
      detail: "检测未通过 · 自动尝试恢复",
    };
  return { label: "在线", tone: "", detail: "连接正常 · 自动检测已开启" };
}
function uiNextRotation(e) {
  const p = e.selection || {},
    minutes = Number(p.rotate_minutes),
    since = Date.parse(e.since);
  if (p.mode === "strict") return { text: "严格锁定", note: "不自动更换 IP" };
  if (!minutes) return { text: "未开启", note: "故障时仍可自动接替" };
  if (!Number.isFinite(since))
    return { text: "等待连接", note: "连接成功后开始计时" };
  const due = new Date(since + minutes * 60000),
    now = new Date();
  if (due <= now) return { text: "等待合格候选", note: "到期后核验通过再切换" };
  const tomorrow = new Date(now);
  tomorrow.setDate(tomorrow.getDate() + 1);
  const day =
    due.toDateString() === now.toDateString()
      ? "今天"
      : due.toDateString() === tomorrow.toDateString()
        ? "明天"
        : due.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
  return {
    text:
      day +
      " " +
      due.toLocaleTimeString("zh-CN", {
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      }),
    note: "每 " + minutes / 60 + " 小时轮换",
  };
}
function uiRisk(e) {
  const r = e.risk || {},
    score = r.score;
  if (score === null || score === undefined || !Number.isFinite(Number(score)))
    return { text: "待查询", tone: "risk-warn", note: "尚无有效评分" };
  const high = Number(score) > 20;
  return {
    text: score + " / 100",
    tone: high ? "risk-bad" : "risk-good",
    note: high ? "高于当前准入阈值" : "最近评分 · 不高于 20",
  };
}
function uiPolicy(e) {
  const p = e.selection || {};
  if (p.mode === "strict") return "严格锁定 IP · 失效后暂停并重试";
  const region = uiRegionNames[p.region] || p.region;
  return (
    (p.mode === "preferred" ? "首选 IP · " : "") +
    (region ? region + "优先 · " : "") +
    "同地区优先，同国备用"
  );
}
function uiRotationOptions(e) {
  const p = e.selection || {};
  return [0, 60, 360, 720, 1440, 2880]
    .map(
      (n) =>
        '<option value="' +
        n +
        '"' +
        (Number(p.rotate_minutes) === n ? " selected" : "") +
        ">" +
        (n ? "每 " + n / 60 + " 小时轮换" : "关闭定时轮换") +
        "</option>",
    )
    .join("");
}
function uiConnectivity(ok, message) {
  const state = $("#service-state");
  state.className = "service-state " + (ok ? "good" : "bad");
  state.querySelector("span").textContent = ok
    ? "服务运行正常"
    : "连接暂时异常";
  $("#connection-banner").hidden = ok;
  if (!ok)
    $("#connection-banner").textContent =
      "暂时无法更新面板数据，正在重试。" +
      (message ? " " + message : "") +
      " 已显示的数据可能不是最新状态。";
}
function renderDashboardExits() {
  const exits = Array.isArray(view.exits) ? view.exits : [],
    online = exits.filter((e) => uiHealth(e).label === "在线").length;
  $("#online-summary").innerHTML =
    '<i class="status-dot"></i>' +
    online +
    " 个出口在线" +
    (online < exits.length
      ? " · " + (exits.length - online) + " 个待处理"
      : "");
  const rotations = [
    ...new Set(
      exits
        .filter((e) => e.selection?.mode !== "strict")
        .map((e) => Number(e.selection?.rotate_minutes) || 0),
    ),
  ];
  $("#rotation-summary").textContent =
    rotations.length === 1 && rotations[0] > 0
      ? "自动轮换：每 " + rotations[0] / 60 + " 小时"
      : rotations.length === 1
        ? "定时轮换未开启"
        : exits.length
          ? "轮换规则：按出口设置"
          : "添加出口后可设置轮换";
  const available = Number.isInteger(view.available_slots)
    ? view.available_slots
    : null;
  $("#check-summary").textContent =
    available === null
      ? "自动检查连接 · 异常时尝试同国接替"
      : available +
        " 个空闲位置" +
        (available === 0 ? " · 自动切换需先释放位置" : "");
  $("#check-summary").style.color = available === 0 ? "var(--warn)" : "";
  $("#ecount").textContent = exits.length ? exits.length + " 个出口" : "";
  $("#exportAll").disabled = !exits.some((e) => (e.inbounds || []).length);
  $("#stopall").disabled = !exits.length;
  const signature = JSON.stringify(
    exits.map((e) => ({
      slot: e.slot,
      host: e.host,
      status: e.status,
      ip: e.exit_ip,
      identity: e.identity,
      policy: e.selection,
      risk: e.risk,
      inbounds: e.inbounds,
      err: e.err,
      health: uiHealth(e),
      next: uiNextRotation(e),
    })),
  );
  if (signature === uiCardSignature) return;
  if (
    document.activeElement?.closest("#list") &&
    document.activeElement.matches("select,input")
  )
    return;
  const menus = new Set(
    [...document.querySelectorAll("#list [data-exit-menu][open]")].map(
      (x) => x.dataset.exitMenu,
    ),
  );
  const expanded = new Set(
    [...document.querySelectorAll("#list .exit-settings:not([hidden])")].map(
      (x) => x.dataset.settingsSlot,
    ),
  );
  uiCardSignature = signature;
  if (!exits.length) {
    $("#list").innerHTML =
      '<div class="empty"><h2>还没有家宽出口</h2><p>添加一个出口后，即可查看状态、复制节点和设置自动轮换。</p><button class="primary" id="newexit2">' +
      ICON.plus +
      "新建出口</button></div>";
    uiRenderRotationSettings();
    return;
  }
  $("#list").innerHTML = exits
    .map((e) => {
      const h = uiHealth(e),
        r = uiRisk(e),
        next = uiNextRotation(e),
        p = e.selection || {},
        ibs = e.inbounds || [],
        code = uiCountry(e),
        slot = e.slot;
      const noLinks = !ibs.length || isXCL(),
        disabled = e.status === "starting" ? " disabled" : "";
      const chips =
        ibs
          .map(
            (i) =>
              '<button class="chip" data-detail="' +
              i.id +
              '">' +
              esc(i.remark || i.protocol) +
              " :" +
              i.port +
              "</button>",
          )
          .join("") ||
        '<span class="muted">尚未绑定节点，可通过“新建出口”向导创建。</span>';
      return (
        '<article class="exit" data-exit="' +
        slot +
        '"><div class="exit-heading"><span class="country-badge ' +
        (code === "JP" ? "jp" : code === "US" ? "us" : "") +
        '" aria-hidden="true">' +
        esc(code) +
        '</span><div class="exit-title-block"><div class="exit-name"><h2>' +
        esc(uiPlace(e)) +
        '</h2><span class="status-pill ' +
        h.tone +
        '">' +
        esc(h.label) +
        '</span></div><p class="exit-policy">' +
        esc(uiPolicy(e)) +
        "</p></div></div>" +
        '<div class="exit-metrics"><div class="exit-metric"><span>当前出口 IP</span><strong class="ip-address">' +
        esc(e.exit_ip || (e.status === "starting" ? "连接中…" : "待核验")) +
        "</strong></div>" +
        '<div class="exit-metric"><span>IPPure 风险</span><strong class="' +
        r.tone +
        '">' +
        esc(r.text) +
        '</strong><small class="metric-note">' +
        esc(r.note) +
        "</small></div>" +
        '<div class="exit-metric"><span>下次轮换</span><strong>' +
        esc(next.text) +
        '</strong><small class="metric-note">' +
        esc(next.note) +
        "</small></div>" +
        '<div class="exit-metric"><span>绑定节点</span><strong>' +
        ibs.length +
        ' 个</strong><small class="metric-note">出口 ' +
        slot +
        "</small></div></div>" +
        (e.err ? '<div class="errline">' + esc(e.err) + "</div>" : "") +
        '<div class="exit-bottom"><span class="exit-health ' +
        (h.tone ? "warn" : "") +
        '"><i></i>' +
        esc(h.detail) +
        '</span><div class="exit-actions">' +
        '<button class="primary" data-copy-exit="' +
        slot +
        '"' +
        (noLinks ? " disabled" : "") +
        ' title="' +
        (isXCL()
          ? "请使用节点后端提供的订阅"
          : noLinks
            ? "此出口尚未绑定节点"
            : "复制此出口绑定的节点链接") +
        '">' +
        ICON.copy +
        "复制节点链接</button>" +
        '<button data-diag="' +
        slot +
        '"' +
        (e.status !== "up" ? " disabled" : "") +
        ">" +
        ICON.ok +
        "检查连接</button>" +
        '<button data-swap="' +
        slot +
        '"' +
        (p.mode === "strict" || e.status === "starting" ? " disabled" : "") +
        ' title="' +
        (p.mode === "strict"
          ? "严格锁定模式下不自动换 IP"
          : "预检同国候选，合格后再切换") +
        '">' +
        ICON.redo +
        "更换 IP</button>" +
        '<details class="exit-menu" data-exit-menu="' +
        slot +
        '"' +
        (menus.has(String(slot)) ? " open" : "") +
        '><summary>更多 <span aria-hidden="true">⌄</span></summary><div class="menu-items"><button data-ip-open="' +
        slot +
        '">选择出口 IP</button><button data-show-settings="' +
        slot +
        '">轮换与节点详情</button><button data-cred="' +
        slot +
        '">SOCKS5 访问凭据</button><button data-ip-events="' +
        slot +
        '">查看切换记录</button><button class="danger" data-stop="' +
        slot +
        '"' +
        disabled +
        ">停止此出口</button></div></details></div></div>" +
        '<div class="exit-settings" data-settings-slot="' +
        slot +
        '"' +
        (expanded.has(String(slot)) ? "" : " hidden") +
        "><p>" +
        esc(ipModeLabel(p)) +
        " · ProxyCheck：" +
        esc(ipProxycheckLabel(e.proxycheck)) +
        '（仅供参考）</p><div class="ip-mini"><label for="rotation-' +
        slot +
        '">定时轮换</label><select id="rotation-' +
        slot +
        '" aria-label="出口 ' +
        slot +
        ' 的定时轮换" data-ip-rotate="' +
        slot +
        '"' +
        (p.mode === "strict" ? " disabled" : "") +
        ">" +
        uiRotationOptions(e) +
        "</select>" +
        (p.country === "JP" && p.mode === "auto"
          ? '<button data-ip-tokyo="' +
            slot +
            '"' +
            (p.region_code === "13" ? " disabled" : "") +
            ">东京优先</button>"
          : "") +
        '</div><div class="chips">' +
        chips +
        "</div></div></article>"
      );
    })
    .join("");
  uiRenderRotationSettings();
}
function uiRenderRotationSettings() {
  if (
    document.activeElement?.closest("#rotation-settings") &&
    document.activeElement.matches("select")
  )
    return;
  $("#rotation-settings").innerHTML =
    '<h2>每个出口的轮换周期</h2><p class="ip-note">轮换不会更改节点端口和客户端凭据。严格锁定模式不自动更换 IP。</p>' +
    (view.exits || [])
      .map(
        (e) =>
          '<div class="rotation-row"><span>' +
          esc(uiPlace(e)) +
          '</span><select aria-label="出口 ' +
          e.slot +
          ' 的轮换周期" data-ip-rotate="' +
          e.slot +
          '"' +
          (e.selection?.mode === "strict" ? " disabled" : "") +
          ">" +
          uiRotationOptions(e) +
          '</select><button data-ip-open="' +
          e.slot +
          '">选择 IP / 模式</button><button data-ip-events="' +
          e.slot +
          '">切换记录</button></div>',
      )
      .join("") +
    (!view.exits?.length
      ? '<p class="muted">创建出口后可设置轮换周期。</p>'
      : "");
}
async function uiLoadEvents(force = false) {
  if (uiEventsLoading || (!force && Date.now() - uiLastEvents < 30000)) return;
  uiEventsLoading = true;
  try {
    const rows = await api("/api/ip/events");
    uiEvents = Array.isArray(rows) ? rows : [];
    uiLastEvents = Date.now();
    const meaningful = uiEvents.filter(
      (r) => !["preparing", "prepare_wait", "risk_checked"].includes(r.kind),
    );
    $("#recent-events").innerHTML =
      (meaningful.length ? meaningful : uiEvents)
        .slice(0, 3)
        .map(
          (r) =>
            '<div class="activity-row"><span class="activity-icon ' +
            (["connected", "switched"].includes(r.kind) ? "good" : "") +
            '" aria-hidden="true">' +
            (["connected", "switched"].includes(r.kind) ? "✓" : "↻") +
            "</span><p>" +
            esc(r.reason || r.kind || "出口状态已更新") +
            '<small class="activity-detail">' +
            esc(uiCountryNames[r.country] || r.country || "出口事件") +
            (r.slot ? " · 出口 " + r.slot : "") +
            "</small></p><time>" +
            esc(uiShortTime(r.time)) +
            "</time></div>",
        )
        .join("") ||
      '<p class="muted">暂无动态。出口连接、轮换和检测结果会显示在这里。</p>';
  } catch (err) {
    $("#recent-events").innerHTML =
      '<p class="muted">动态暂时无法读取，可稍后点击“查看全部”重试。</p>';
  } finally {
    uiEventsLoading = false;
  }
}
function uiShortTime(value) {
  const d = new Date(value);
  return Number.isFinite(d.getTime())
    ? d.toLocaleString("zh-CN", {
        month: "numeric",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })
    : "时间待确认";
}
function uiNavigate(page) {
  if (!uiPages[page]) return;
  uiPage = page;
  const ids = {
    overview: "overview",
    ips: "ip-browser",
    automation: "automation",
    notifications: "notifications",
    settings: "settings",
  };
  Object.entries(ids).forEach(([name, id]) =>
    $("#" + id)?.classList.toggle("open", name === page),
  );
  document.querySelectorAll(".side-nav .nav-item").forEach((b) => {
    const active = (b.dataset.page || "settings") === page;
    b.classList.toggle("active", active);
    if (active) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  });
  $("#page-title").textContent = uiPages[page][0];
  $("#page-description").textContent = uiPages[page][1];
  document
    .querySelectorAll(".exit-menu[open]")
    .forEach((d) => (d.open = false));
  if (page === "overview") uiLoadEvents();
  if (page === "automation" || page === "notifications") uiLoadConfig();
}
async function uiLoadConfig() {
  if (uiConfigLoading) return;
  uiConfigLoading = true;
  try {
    const tasks =
      uiPage === "notifications"
        ? [["telegram", () => api("/api/telegram"), (s) => ipTelegramRender(s)]]
        : [
            [
              "risk",
              () => api("/api/ip/risk"),
              (s) => {
                $("#ip-risk").checked = !!s.enabled;
              },
            ],
            [
              "verification",
              () => api("/api/ip/verification"),
              ipVerificationRender,
            ],
            ["proxycheck", () => api("/api/ip/proxycheck"), ipProxycheckRender],
          ];
    await Promise.all(
      tasks.map(async ([id, load, render]) => {
        const parent = $(
          "#" +
            {
              telegram: "telegram-settings",
              risk: "risk-settings",
              verification: "verification-settings",
              proxycheck: "proxycheck-settings",
            }[id],
        );
        parent.querySelector(".config-error")?.remove();
        try {
          render(await load());
        } catch (err) {
          const note = document.createElement("div");
          note.className = "config-error";
          note.setAttribute("role", "status");
          note.textContent = "此项设置暂时无法读取：" + err.message;
          parent.prepend(note);
        }
      }),
    );
  } finally {
    uiConfigLoading = false;
  }
}

// Keep existing controls and their handlers; only relocate them into clear pages.
for (const [input, target, title] of [
  ["ip-verify-hourly", "verification-settings", "候选连接核验"],
  ["ip-risk", "risk-settings", "IPPure 风险筛选"],
  ["ip-pc-enabled", "proxycheck-settings", "ProxyCheck 参考查询"],
  ["ip-tg-enabled", "telegram-settings", "Telegram 通知设置"],
]) {
  const card = $("#" + input).closest(".ip-info");
  const heading = document.createElement("h3");
  heading.textContent = title;
  card.prepend(heading);
  $("#" + target).append(card);
  const explanations = [...card.querySelectorAll(".ip-note:not([id])")];
  if (explanations.length) {
    const details = document.createElement("details");
    details.className = "config-explanation";
    const summary = document.createElement("summary");
    summary.textContent = "查看完整规则说明";
    details.append(summary);
    explanations.forEach((note) => details.append(note));
    card.append(details);
  }
  const brief = document.createElement("p");
  brief.className = "ip-note";
  brief.textContent = {
    "ip-verify-hourly":
      "按队列检查候选连接，支持每小时自动核验或手动全部核验，不会更换现有出口。",
    "ip-risk":
      "新出口风险分须不高于 20；当前 IP 每小时复查，高于 20 才提前轮换。",
    "ip-pc-enabled":
      "只在出口 IP 变化后查询，仅作参考，不参与接入或换 IP 判断。",
    "ip-tg-enabled":
      "通知原 IP、新 IP、IPPure 风险分与下次轮换时间。Token 保存后不回显。",
  }[input];
  heading.after(brief);
}
for (const id of ["ip-browser", "settings"]) {
  const section = $("#" + id);
  section.classList.remove("modal");
  section.classList.add("workspace-page");
  $("#workspace").insertBefore(section, $(".app-footer"));
  section.removeAttribute("aria-modal");
}
$("#ip-close").onclick = () => uiNavigate("overview");
const uiOriginalOpen = openModal,
  uiOriginalClose = closeModal;
openModal = function (id) {
  if (id === "settings") {
    uiNavigate("settings");
    return;
  }
  uiLastFocus = document.activeElement;
  uiOriginalOpen(id);
  const box = $("#" + id);
  box.setAttribute("role", "dialog");
  box.setAttribute("aria-modal", "true");
  box.querySelector("button,input,select,textarea")?.focus();
};
closeModal = function (id) {
  if (id === "settings") {
    uiNavigate("overview");
    return;
  }
  uiOriginalClose(id);
  if (uiLastFocus?.isConnected) uiLastFocus.focus();
};
document.addEventListener("click", async (event) => {
  const nav = event.target.closest("[data-page]");
  if (nav) {
    event.preventDefault();
    if (nav.dataset.page === "ips") ipOpen(0);
    else uiNavigate(nav.dataset.page);
    return;
  }
  const copyButton = event.target.closest("[data-copy-exit]");
  if (copyButton) {
    const exit = view.exits.find(
      (e) => e.slot === Number(copyButton.dataset.copyExit),
    );
    const ids = (exit?.inbounds || []).map((i) => i.id);
    if (!ids.length) return;
    copyButton.disabled = true;
    try {
      const result = await api("/api/xui/links?ids=" + ids.join(","));
      if (!result.links?.length) throw new Error("此出口暂时没有可复制的节点");
      await copy(result.links.join("\n"));
    } catch (err) {
      toast(err.message, true);
    } finally {
      copyButton.disabled = false;
    }
    return;
  }
  const expand = event.target.closest("[data-show-settings]");
  if (expand) {
    const block = $(
      '[data-settings-slot="' + expand.dataset.showSettings + '"]',
    );
    block.hidden = !block.hidden;
    expand.closest("details").open = false;
    return;
  }
  if (event.target.closest("[data-all-events]")) {
    openModal("ip-history");
    $("#ip-history-body").textContent = "读取中…";
    try {
      const rows = await api("/api/ip/events");
      uiEvents = Array.isArray(rows) ? rows : [];
      $("#ip-history-body").innerHTML =
        uiEvents
          .map(
            (r) =>
              '<div class="ip-info"><b>' +
              esc(uiShortTime(r.time)) +
              "</b><p>" +
              esc(r.reason || r.kind) +
              "</p><small>" +
              esc(r.old_ip || "—") +
              " → " +
              esc(r.new_ip || "—") +
              "</small></div>",
          )
          .join("") || "暂无记录";
    } catch (err) {
      $("#ip-history-body").textContent = err.message;
    }
    return;
  }
  if (!event.target.closest(".exit-menu"))
    document
      .querySelectorAll(".exit-menu[open]")
      .forEach((d) => (d.open = false));
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape")
    document
      .querySelectorAll(".exit-menu[open]")
      .forEach((d) => (d.open = false));
  if (event.key !== "Tab") return;
  const modal = [...document.querySelectorAll(".modal.open")].at(-1);
  if (!modal) return;
  const items = [
    ...modal.querySelectorAll(
      'button:not(:disabled),a[href],input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"]',
    ),
  ].filter((x) => x.getClientRects().length);
  if (!items.length) return;
  const first = items[0],
    last = items.at(-1);
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
});
const results = document.createElement("div");
results.className = "modal";
results.id = "connection-results";
results.innerHTML =
  '<div class="sheet"><div class="head"><h2>连接检查</h2><span class="spacer"></span><button data-close="connection-results">关闭</button></div><div class="body" id="connection-results-body"></div></div>';
document.body.append(results);
results.addEventListener("click", (event) => {
  if (event.target === results) closeModal("connection-results");
});
function uiRenderDiagnostics(report) {
  $("#connection-results-body").innerHTML =
    '<div class="dialog-results">' +
    [
      ["socks", "出口连接"],
      ["https", "HTTPS 访问"],
      ["node", "节点连接"],
    ]
      .map(([key, label]) => {
        const row = report[key] || {};
        return (
          '<div class="diagnostic-row"><div><strong>' +
          label +
          "</strong><p>" +
          esc(row.detail || "暂无结果") +
          '</p></div><span class="status-pill ' +
          (row.ok ? "" : "warn") +
          '">' +
          (row.ok ? "通过" : "未通过") +
          "</span></div>"
        );
      })
      .join("") +
    '</div><p class="ip-note">以上为服务器本机检查；手机到服务器的网络情况需要从手机另测。</p>';
}
uiNavigate("overview");
uiLoadEvents();
setInterval(() => {
  if (uiPage === "overview") uiLoadEvents();
  else if (uiPage === "automation" || uiPage === "notifications")
    uiLoadConfig();
}, 30000);
