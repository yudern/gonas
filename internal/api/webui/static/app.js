import { api, setUnauthorizedHandler } from "/api.js";
import { t, getLocale, setLocale, translateNotice, translateError } from "/i18n.js";

const content = document.getElementById("content");
const navLinks = document.querySelectorAll(".nav-list a");
const shell = document.getElementById("shell");
const authGate = document.getElementById("auth-gate");
const authGateContent = document.getElementById("auth-gate-content");

const routes = {
  dashboard: renderDashboard,
  setup: renderSetupWizard,
  storage: renderStorage,
  files: renderFiles,
  apps: renderApps,
  shares: renderShares,
  users: renderUsers,
  monitor: renderMonitor,
  security: renderSecurity,
  backup: renderBackup,
  system: renderSystem,
  doctor: renderDoctor,
};

// 跟後端 internal/api.monitorPollInterval 一致，純粹用來在頁面文字上
// 告訴使用者「多久取樣一次」，不影響任何實際輪詢行為 —— 真正的輪詢是
// 後端的 Poller 在背景做的,前端只是每次切頁/重新整理時讀最新資料。
const MONITOR_POLL_SECONDS = 10;

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function msg(kind, text) {
  return `<div class="msg ${kind}">${esc(text)}</div>`;
}

// applyStaticI18n 翻譯 index.html 裡固定存在、跟目前路由無關的文字
// (側邊欄導覽、登出按鈕)——這些元素在 app.js 載入前就已經在 DOM 裡,
// 不屬於任何一支 render 函式,所以獨立處理。
function applyStaticI18n() {
  navLinks.forEach((a) => {
    const route = a.dataset.route;
    // 只更新標籤文字的那個 span,不要動整個 <a>——裡面還有 SVG 圖示,
    // 用 a.textContent 會把圖示一起清掉(第三十四輪 UI 精品化加了圖示)。
    const label = a.querySelector(".nav-label");
    if (route && label) label.textContent = t(`nav.${route}`);
    else if (route) a.textContent = t(`nav.${route}`);
  });
  const logoutBtn = document.getElementById("logout-btn");
  if (logoutBtn) logoutBtn.textContent = t("nav.logout");
}

// wireLangSwitcher 讓側邊欄的語言選單生效。切換語言後直接重新整理整個
// 頁面而不是嘗試就地重新渲染目前畫面——這支介面沒有任何一個地方的
// 狀態貴到值得为了省一次整頁重新整理去換取更複雜的「重新渲染目前這頁,
// 且不要弄丟使用者手上正在填的表單」邏輯,重新整理在本機 NAS 管理介面
// 上不到一秒就完成,使用者也不會太在意。
function wireLangSwitcher() {
  const sel = document.getElementById("lang-switcher");
  if (!sel) return;
  sel.value = getLocale();
  sel.addEventListener("change", () => {
    setLocale(sel.value);
    location.reload();
  });
}

// associateLabels 把每個 .field 裡的 <label> 用 for= 連到它的輸入控制項
// (第五十八輪 UI 覆核 #3)。原本 91 個 <label> 沒有一個有 for=,點文字不會
// 聚焦欄位、螢幕閱讀器也唸不出欄位名。各頁是動態 innerHTML 產生的,所以由
// boot() 裡的 MutationObserver 在每次重繪後重跑;已配對過的(label 已有 for)
// 直接略過,冪等。回傳這次新配對的數量。
function associateLabels(root) {
  let n = 0;
  root.querySelectorAll(".field").forEach((field) => {
    const label = field.querySelector("label");
    const ctrl = field.querySelector("input, select, textarea");
    if (!label || !ctrl || label.htmlFor) return;
    if (!ctrl.id) ctrl.id = "f_" + Math.random().toString(36).slice(2, 9);
    label.htmlFor = ctrl.id;
    n++;
  });
  return n;
}

async function router() {
  const hash = location.hash.replace(/^#\//, "") || "dashboard";
  const route = routes[hash] ? hash : "dashboard";

  navLinks.forEach((a) => a.classList.toggle("active", a.dataset.route === route));

  content.innerHTML = `<p class="loading">${esc(t("common.loading"))}</p>`;
  try {
    await routes[route](content);
  } catch (err) {
    content.innerHTML = msg("error", t("common.loadFailed", { msg: err.message }));
  }
}

window.addEventListener("hashchange", router);
window.addEventListener("DOMContentLoaded", boot);

// ---------- 登入/初始設定 ----------
//
// GoNAS 的登入狀態是「整個 Web UI 能不能用」的前提,所以不能像其他頁面
// 一樣走 routes{} 那套 hash router —— 在使用者通過驗證之前,連 sidebar
// 都不該顯示(裡面全是需要登入才能呼叫的功能入口)。boot() 是頁面載入時
// 唯一的進入點,決定顯示登入畫面、初始設定畫面,還是真正的管理介面。

let showingApp = false;

// wireNavToggle 讓窄螢幕(手機)的漢堡鈕生效:點一下展開/收起側邊欄導覽,
// 點任何一個導覽連結後自動收起(不然選單會一直蓋著內容)。桌面版這顆鈕
// 用 CSS 藏起來,所以這段在桌面上等於沒作用。第三十三輪響應式修法。
function wireNavToggle() {
  const sidebar = document.getElementById("sidebar");
  const toggle = document.getElementById("nav-toggle");
  if (!sidebar || !toggle) return;
  toggle.addEventListener("click", () => {
    const open = sidebar.classList.toggle("nav-open");
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
  });
  navLinks.forEach((a) => a.addEventListener("click", () => {
    sidebar.classList.remove("nav-open");
    toggle.setAttribute("aria-expanded", "false");
  }));
}

async function boot() {
  // 第五十八輪 UI 覆核(#7):讓 <html lang> 跟著實際語言走,否則不管切到
  // 英文/簡中,輔助技術與瀏覽器都以為整頁是繁中(index.html 寫死 zh-Hant)。
  document.documentElement.lang = getLocale();
  applyStaticI18n();
  wireLangSwitcher();
  wireNavToggle();
  setUnauthorizedHandler(showLoginGate);

  // 第五十八輪 UI 覆核:兩個全站層級的無障礙/防呆機制。
  // (a) 表單 label 關聯(#3):MutationObserver 在每次畫面重繪後自動重跑
  //     associateLabels(頁面內容是動態產生的)。重跑前先 disconnect、跑完
  //     再 observe,避免自己設定 for=/id 造成的變動又觸發自己形成迴圈。
  const labelObserver = new MutationObserver(() => {
    labelObserver.disconnect();
    try { associateLabels(document.body); } finally {
      labelObserver.observe(document.body, { childList: true, subtree: true });
    }
  });
  associateLabels(document.body);
  labelObserver.observe(document.body, { childList: true, subtree: true });
  // (b) 送出中防重複點擊(#8):表單送出時暫時停用觸發的送出鈕。用捕獲階段
  //     在表單自己的 handler 之前先停用。3 秒安全網自動恢復——成功的操作
  //     通常會重繪(按鈕本來就消失),失敗留在原畫面的表單則能再送一次。
  document.body.addEventListener("submit", (ev) => {
    const btn = ev.submitter;
    if (btn && btn.tagName === "BUTTON" && !btn.disabled) {
      btn.disabled = true;
      setTimeout(() => { btn.disabled = false; }, 3000);
    }
  }, true);

  document.getElementById("logout-btn").addEventListener("click", async () => {
    try { await api.authLogout(); } catch { /* 就算 logout 呼叫本身失敗,也還是要讓使用者回到登入畫面 */ }
    showLoginGate();
  });

  let status;
  try {
    status = await api.authStatus();
  } catch (err) {
    authGate.hidden = false;
    authGateContent.innerHTML = msg("error", t("common.networkError", { msg: err.message }));
    return;
  }

  if (status.setupRequired) {
    showSetupGate();
    return;
  }

  try {
    const me = await api.me();
    if (me && me.mustChangePassword) {
      showForcedPasswordChange();
    } else {
      showApp();
    }
  } catch {
    showLoginGate();
  }
}

function showApp() {
  showingApp = true;
  authGate.hidden = true;
  shell.hidden = false;
  router();
  api.version().then((v) => {
    document.getElementById("sidebar-version").textContent = `gonasd ${v.version} (${v.goos}/${v.goarch})`;
  }).catch(() => {});
  api.me().then(updateSidebarUser).catch(() => {});
}

// updateSidebarUser 更新側邊欄最下面「目前登入身分」的小字。獨立成一個
// 函式而不是散在 showApp/renderSecurity 裡各自組字串,是因為 Phase 13
// 多帳號之後,兩個地方都需要顯示同一份資訊(登入當下、之後每次打開
// 安全頁面刷新一次)——renderSecurity 本來就要呼叫 api.me() 取得
// changePasswordHint 需要的 username,順手也更新這裡,不用多打一次 API。
function updateSidebarUser(me) {
  const box = document.getElementById("sidebar-user");
  if (!box || !me || !me.username) return;
  const roleLabel = me.role === "admin" ? t("auth.roleAdmin") : t("auth.roleViewer");
  box.innerHTML = `${esc(me.username)}<span class="role-badge">${esc(roleLabel)}</span>`;
  box.hidden = false;
}

function showLoginGate() {
  if (!showingApp && authGate.hidden === false && authGateContent.querySelector("#login-form")) return; // 已經顯示登入表單,不要蓋掉使用者正在打的字
  showingApp = false;
  shell.hidden = true;
  authGate.hidden = false;
  authGateContent.innerHTML = `
    <h1>${esc(t("auth.loginTitle"))}</h1>
    <p class="page-subtitle">${esc(t("auth.loginSubtitle"))}</p>
    <div id="login-msg"></div>
    <form class="stacked" id="login-form">
      <div class="field"><label>${esc(t("auth.username"))}</label><input type="text" name="username" autocomplete="username" required></div>
      <div class="field"><label>${esc(t("auth.password"))}</label><input type="password" name="password" autocomplete="current-password" required></div>
      <div class="field"><label>${esc(t("auth.totpCode"))}</label><input type="text" name="totpCode" placeholder="123456" autocomplete="one-time-code"><div class="hint">${esc(t("auth.totpOrRecoveryHint"))}</div></div>
      <div class="btn-row"><button type="submit">${esc(t("auth.loginBtn"))}</button></div>
    </form>
  `;
  authGateContent.querySelector("#login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = authGateContent.querySelector("#login-msg");
    try {
      const me = await api.authLogin(f.get("username").trim(), f.get("password"), f.get("totpCode").trim());
      if (me && me.mustChangePassword) {
        showForcedPasswordChange();
      } else {
        showApp();
      }
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

// showForcedPasswordChange 是「預設 admin(gonas/gonas)第一次登入」時
// 強制先改密碼的畫面——伺服器端的 requireAdmin 也會擋掉所有 admin 操作
// 直到密碼改掉(見 internal/api),所以這個畫面不是唯一防線,但它讓
// 使用者有一個清楚、擋不過去的地方去改掉預設密碼,而不是進到主畫面卻
// 到處點了都 403。改成功之後直接 showApp()。
function showForcedPasswordChange() {
  showingApp = false;
  shell.hidden = true;
  authGate.hidden = false;
  authGateContent.innerHTML = `
    <h1>${esc(t("auth.forcedChangeTitle"))}</h1>
    <p class="page-subtitle">${esc(t("auth.forcedChangeIntro"))}</p>
    <div id="forced-change-msg"></div>
    <form class="stacked" id="forced-change-form">
      <div class="field"><label>${esc(t("security.oldPassword"))}</label><input type="password" name="oldPassword" autocomplete="current-password" required></div>
      <div class="field"><label>${esc(t("security.newPassword"))}</label><input type="password" name="newPassword" minlength="8" autocomplete="new-password" required></div>
      <div class="field"><label>${esc(t("security.confirmNewPassword"))}</label><input type="password" name="confirm" minlength="8" autocomplete="new-password" required></div>
      <div class="btn-row"><button type="submit">${esc(t("auth.forcedChangeBtn"))}</button></div>
    </form>
  `;
  authGateContent.querySelector("#forced-change-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = authGateContent.querySelector("#forced-change-msg");
    if (f.get("newPassword") !== f.get("confirm")) {
      box.innerHTML = msg("error", t("security.newPasswordMismatch"));
      return;
    }
    try {
      await api.changePassword(f.get("oldPassword"), f.get("newPassword"));
      showApp();
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

function showSetupGate() {
  showingApp = false;
  shell.hidden = true;
  authGate.hidden = false;
  authGateContent.innerHTML = `
    <h1>${esc(t("auth.setupTitle"))}</h1>
    <p class="page-subtitle">${esc(t("auth.setupSubtitle"))}</p>
    <div id="setup-msg"></div>
    <form class="stacked" id="setup-form">
      <div class="field"><label>${esc(t("auth.username"))}</label><input type="text" name="username" autocomplete="username" required></div>
      <div class="field"><label>${esc(t("auth.passwordMin"))}</label><input type="password" name="password" minlength="8" autocomplete="new-password" required></div>
      <div class="field"><label>${esc(t("auth.confirmPassword"))}</label><input type="password" name="confirm" minlength="8" autocomplete="new-password" required></div>
      <div class="btn-row"><button type="submit">${esc(t("auth.setupBtn"))}</button></div>
    </form>
  `;
  authGateContent.querySelector("#setup-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = authGateContent.querySelector("#setup-msg");
    if (f.get("password") !== f.get("confirm")) {
      box.innerHTML = msg("error", t("auth.passwordMismatch"));
      return;
    }
    try {
      await api.authSetup(f.get("username").trim(), f.get("password"));
      // 剛建好管理帳號 = 全新系統的第一刻,直接帶進新手設定精靈
      // (歡迎→準備硬碟→建池→建共享→完成)。使用者可在任一步略過。
      wizardStep = 0;
      location.hash = "#/setup";
      showApp();
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

// ---------- 儀表板 ----------

async function renderDashboard(el) {
  const [health, version, dockerStatus, disks, arrayStatus, me, deps, containers] = await Promise.all([
    api.health(), api.version(), api.dockerPing().catch((e) => ({ available: false, error: e.message })),
    api.disks().catch(() => []), api.arrayStatus().catch(() => ({ state: "unknown" })),
    api.me().catch(() => ({ role: "" })),
    api.doctorStatus().catch(() => []),
    api.containers().catch(() => []),
  ]);
  const isAdmin = me.role === "admin";
  const missingDeps = (deps || []).filter((d) => !d.installed);
  // 容器运行概览(第六十轮产品复审):仪表盘一眼看出有没有容器挂了。
  const running = (containers || []).filter((c) => c.State === "running").length;
  const stopped = (containers || []).length - running;
  const dockerValue = dockerStatus.available
    ? ((containers && containers.length)
        ? t("dashboard.dockerRunningStopped", { running, stopped })
        : t("dashboard.dockerAvailable"))
    : t("dashboard.dockerUnavailable");

  el.innerHTML = `
    <h1>${esc(t("dashboard.title"))}</h1>
    <p class="page-subtitle">${esc(t("dashboard.subtitle", { version: version.version, os: version.goos, arch: version.goarch, uptime: formatUptime(health.uptimeSeconds) }))}</p>
    <div class="grid">
      ${statTile(t("dashboard.systemStatus"), t("dashboard.running"), "ok", "server")}
      ${statTile("Docker", dockerValue, dockerStatus.available ? "ok" : "danger", "docker")}
      ${statTile(t("dashboard.storageArray"), arrayLabel(arrayStatus.state), arrayPillClass(arrayStatus.state), "array")}
      ${statTile(t("dashboard.disksDetected"), String(disks.length), "", "disks")}
    </div>
    ${!dockerStatus.available ? msg("warn", t("dashboard.dockerWarn", { reason: translateError(dockerStatus.error) || t("dashboard.unknownReason") })) + `<div class="btn-row" style="margin:-4px 0 10px"><a href="#/doctor" class="secondary">${esc(t("doctor.dashButton"))}</a></div>` : ""}
    ${missingDeps.length ? `
    <div class="card" style="border-color:var(--warn);background:var(--warn-soft)">
      ${h2i("stethoscope", esc(t("doctor.dashTitle")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0 0 12px">${esc(t("doctor.dashBody", { n: missingDeps.length, names: missingDeps.map((d) => t("doctor.pkg." + d.key + ".name")).join("、") }))}</p>
      <div class="btn-row"><a href="#/doctor" class="btnlink">${esc(t("doctor.dashButton"))}</a></div>
    </div>` : ""}
    ${arrayStatus.state === "unconfigured" ? `
    <div class="card" style="border-color:var(--accent);background:var(--accent-soft)">
      ${h2i("sliders", esc(t("setup.ctaTitle")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0 0 12px">${esc(t("setup.ctaBody"))}</p>
      <div class="btn-row"><a href="#/setup" class="btnlink">${esc(t("setup.ctaButton"))}</a></div>
    </div>` : ""}
    ${isAdmin && (arrayStatus.state === "stopped" || arrayStatus.state === "failed") ? `
    <div class="card" style="border-color:var(--warn);background:var(--warn-soft)">
      ${h2i("array", esc(t("dashboard.arrayDownTitle")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0 0 12px">${esc(t("dashboard.arrayDownBody"))}${arrayStatus.error ? " " + esc(translateError(arrayStatus.error)) : ""}</p>
      <div id="dash-array-msg"></div>
      <div class="btn-row"><button type="button" id="dash-start-array">${esc(t("storage.startArray"))}</button></div>
    </div>` : ""}
    <div class="card">
      ${h2i("link", esc(t("dashboard.quickLinks")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0">${t("dashboard.quickLinksBody")}</p>
    </div>
  `;

  const dashStart = el.querySelector("#dash-start-array");
  if (dashStart) {
    dashStart.addEventListener("click", async () => {
      const box = el.querySelector("#dash-array-msg");
      dashStart.disabled = true;
      try {
        await api.startArray();
        await renderDashboard(el);
      } catch (err) {
        if (box) box.innerHTML = msg("error", translateError(err.message) || err.message);
        dashStart.disabled = false;
      }
    });
  }
}

// renderSystemUpdateCard 顯示 Phase 17 自我更新功能的狀態:目前版本、
// 有沒有設定更新來源、背景檢查器最近一次的結果。RoleViewer 也看得到
// 這張卡片(「現在是不是最新版本」不算敏感資訊),但只有 isAdmin 才會
// 拿到設定更新來源/立即檢查/套用更新這些操作按鈕——跟後端
// requireAuth/requireAdmin 的分法完全對應,見
// internal/api/router.go 對 /api/v1/system/update* 路由的註冊說明。
function renderSystemUpdateCard(update, currentVersion, isAdmin) {
  if (!update) {
    return `
    <div class="card">
      ${h2i("download", esc(t("update.title")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0">${esc(t("update.loadError"))}</p>
    </div>`;
  }

  const statusPill = update.updateAvailable
    ? `<span class="pill warn">${esc(t("update.pillAvailable", { version: update.latestVersion }))}</span>`
    : (update.configured ? `<span class="pill ok">${esc(t("update.pillUpToDate"))}</span>` : `<span class="pill neutral">${esc(t("update.pillNotConfigured"))}</span>`);

  const checkedLine = update.checkedAt
    ? `<p style="color:var(--text-dim);font-size:12.5px;margin:6px 0 0">${esc(t("update.lastChecked", { date: formatDateTime(update.checkedAt) }))}</p>`
    : "";
  const checkErrorMsg = update.checkError ? msg("error", translateError(update.checkError)) : "";
  const notesBlock = update.updateAvailable && update.notes
    ? `<p style="color:var(--text-dim);font-size:12.5px;margin:8px 0 0;white-space:pre-wrap">${esc(update.notes)}</p>`
    : "";
  const applyErrorMsg = update.applyError ? msg("error", t("update.applyFailed", { reason: translateError(update.applyError) })) : "";
  const applyInProgressMsg = update.applyInProgress ? msg("warn", t("update.applyInProgress")) : "";

  // 「立即檢查」「套用更新」都是 requireAdmin 的動作,RoleViewer 只看得
  // 到上面那些純資訊區塊,不會看到任何按鈕——不是隱藏起來、按了會被
  // 後端拒絕,而是根本不渲染,跟 renderSecurity 的 isAdmin 判斷是同一個
  // 慣例。
  const actions = isAdmin && update.configured ? `
    <div class="btn-row" style="margin-top:12px">
      <button type="button" class="secondary" id="update-check-btn" ${update.applyInProgress ? "disabled" : ""}>${esc(t("update.checkNow"))}</button>
      ${update.updateAvailable ? `<button type="button" class="danger" id="update-apply-btn" ${update.applyInProgress ? "disabled" : ""}>${update.applyInProgress ? esc(t("update.applying")) : esc(t("update.applyNow"))}</button>` : ""}
    </div>
  ` : "";

  // 復原按鈕刻意不跟著 update.configured 一起判斷是否顯示——備份檔案
  // (執行檔旁邊的 .previous)是否存在跟「現在有沒有設定更新來源網址」
  // 是兩件獨立的事:使用者完全可能先套用過一次更新、之後又把更新來源
  // 清空,這種情況下備份依然在,復原功能也應該依然可用,見
  // internal/api/system_update_handlers.go 的 buildSystemUpdateResponse
  // 對 BackupAvailable 的說明。
  const rollbackAction = isAdmin && update.backupAvailable ? `
    <div class="btn-row" style="margin-top:8px">
      <button type="button" class="secondary" id="update-rollback-btn" ${update.applyInProgress ? "disabled" : ""}>${update.applyInProgress ? esc(t("update.rollingBack")) : esc(t("update.rollback"))}</button>
    </div>
  ` : "";

  const settingsFormInner = `
    <form class="stacked" id="update-settings-form" style="margin-top:12px">
      <div class="field">
        <label>${esc(t("update.manifestUrl"))}</label>
        <input type="url" name="manifestUrl" placeholder="https://example.com/gonas-manifest.json" value="${esc(update.manifestUrl || "")}">
        <div class="hint">${esc(t("update.manifestUrlHint"))}</div>
      </div>
      <div class="btn-row"><button type="submit">${esc(t("update.saveSettings"))}</button></div>
    </form>`;
  // 第五十八輪產品覆核(#5):GoNAS 沒有官方更新來源,這個功能是給「自架更新
  // 伺服器」的進階使用者用的。未設定時,把設定表單收進 <details> 折疊起來、
  // 並明講沒有官方來源,避免一般使用者看到一個指向 example.com 的欄位以為
  // 「更新壞了」。已設定的話照常顯示。
  let settingsForm;
  if (isAdmin) {
    settingsForm = update.configured
      ? settingsFormInner
      : `<details style="margin-top:12px">
          <summary style="cursor:pointer;color:var(--text-dim);font-size:12.5px">${esc(t("update.advancedSource"))}</summary>
          <p class="hint" style="margin:8px 0 0">${esc(t("update.noOfficialSource"))}</p>
          ${settingsFormInner}
        </details>`;
  } else {
    settingsForm = !update.configured ? `<p style="color:var(--text-dim);font-size:12.5px;margin:8px 0 0">${esc(t("update.adminOnlyHint"))}</p>` : "";
  }

  // 第六十輪:離線上傳更新。對「離線 NAS、沒有更新伺服器」的使用者來說,
  // 這才是主要的更新方式——直接把新的 gonasd 執行檔上傳上來替換,不用架
  // manifest 伺服器、也不用 SSH 進機器。所以不藏在折疊區,isAdmin 就直接顯示。
  const uploadBlock = isAdmin ? `
    <div style="margin-top:14px;padding-top:14px;border-top:1px solid var(--border)">
      <p style="margin:0 0 4px;font-weight:600;font-size:13.5px">${esc(t("update.offlineTitle"))}</p>
      <p class="hint" style="margin:0 0 10px">${esc(t("update.offlineHint"))}</p>
      <form id="update-upload-form">
        <input type="file" id="update-upload-file" accept="" style="font-size:13px">
        <div class="btn-row" style="margin-top:10px">
          <button type="submit" id="update-upload-btn" ${update.applyInProgress ? "disabled" : ""}>${esc(t("update.offlineUpload"))}</button>
        </div>
      </form>
      <div id="update-upload-progress" style="margin-top:8px"></div>
    </div>` : "";

  return `
    <div class="card">
      ${h2i("download", esc(t("update.title")))}
      <p style="margin:0">${esc(t("update.currentVersion", { version: currentVersion }))} ${statusPill}</p>
      ${checkedLine}
      ${checkErrorMsg}
      ${notesBlock}
      <div id="update-msg"></div>
      ${applyInProgressMsg}
      ${applyErrorMsg}
      ${actions}
      ${rollbackAction}
      ${uploadBlock}
      ${settingsForm}
    </div>
  `;
}

function attachSystemUpdateHandlers(el, isAdmin) {
  if (!isAdmin) return;

  const settingsForm = el.querySelector("#update-settings-form");
  if (settingsForm) {
    settingsForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      try {
        await api.setSystemUpdateSettings(f.get("manifestUrl").trim());
        await renderSystem(el);
      } catch (err) {
        const box = el.querySelector("#update-msg");
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const checkBtn = el.querySelector("#update-check-btn");
  if (checkBtn) {
    checkBtn.addEventListener("click", async () => {
      checkBtn.disabled = true;
      try {
        await api.checkSystemUpdate();
        await renderSystem(el);
      } catch (err) {
        checkBtn.disabled = false;
        const box = el.querySelector("#update-msg");
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const applyBtn = el.querySelector("#update-apply-btn");
  if (applyBtn) {
    applyBtn.addEventListener("click", async () => {
      // 這是整個 Web UI 裡數一數二危險的按鈕:按下去之後 gonasd 會
      // 下載、驗證、置換自己的執行檔,然後整個程序重新啟動——過程中
      // 陣列跟 Docker 容器本身不受影響(它們是獨立的系統服務/程序,
      // 不會因為 gonasd 重啟而跟著斷線),但管理介面會有幾秒鐘連不上,
      // 值得用一個明確的確認對話框攔一次,而不是跟「刪除一個備份工作」
      // 用一樣輕量的確認方式。
      if (!confirm(t("update.applyConfirm"))) return;
      applyBtn.disabled = true;
      applyBtn.textContent = t("update.applying");
      const checkBtn2 = el.querySelector("#update-check-btn");
      if (checkBtn2) checkBtn2.disabled = true;
      try {
        const res = await api.applySystemUpdate();
        const box = el.querySelector("#update-msg");
        if (box) box.innerHTML = msg("ok", translateNotice(res.message));
        pollForRestartThenReload();
      } catch (err) {
        applyBtn.disabled = false;
        applyBtn.textContent = t("update.applyNow");
        if (checkBtn2) checkBtn2.disabled = false;
        const box = el.querySelector("#update-msg");
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const uploadForm = el.querySelector("#update-upload-form");
  if (uploadForm) {
    uploadForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const fileInput = el.querySelector("#update-upload-file");
      const progressBox = el.querySelector("#update-upload-progress");
      const uploadBtn = el.querySelector("#update-upload-btn");
      const file = fileInput && fileInput.files && fileInput.files[0];
      if (!file) {
        if (progressBox) progressBox.innerHTML = msg("error", t("update.offlineNoFile"));
        return;
      }
      // 跟「套用更新」同等級的高風險確認:上傳的執行檔會取代正在跑的
      // gonasd 並重啟整個程序,管理介面會短暫連不上。
      if (!confirm(t("update.offlineConfirm"))) return;
      if (uploadBtn) uploadBtn.disabled = true;
      const fd = new FormData();
      fd.append("file", file);
      try {
        if (progressBox) progressBox.innerHTML = msg("warn", t("update.offlineUploading", { pct: 0 }));
        const res = await api.uploadSystemUpdate(fd, (frac) => {
          if (progressBox) progressBox.innerHTML = msg("warn", t("update.offlineUploading", { pct: Math.round(frac * 100) }));
        });
        if (progressBox) progressBox.innerHTML = msg("ok", translateNotice(res.message));
        pollForRestartThenReload();
      } catch (err) {
        if (uploadBtn) uploadBtn.disabled = false;
        if (progressBox) progressBox.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const rollbackBtn = el.querySelector("#update-rollback-btn");
  if (rollbackBtn) {
    rollbackBtn.addEventListener("click", async () => {
      // 跟「套用更新」一樣是高風險操作——會置換執行檔並重啟整個
      // gonasd 程序——所以一樣用明確的確認對話框攔一次。
      if (!confirm(t("update.rollbackConfirm"))) return;
      rollbackBtn.disabled = true;
      rollbackBtn.textContent = t("update.rollingBack");
      const applyBtn2 = el.querySelector("#update-apply-btn");
      const checkBtn3 = el.querySelector("#update-check-btn");
      if (applyBtn2) applyBtn2.disabled = true;
      if (checkBtn3) checkBtn3.disabled = true;
      try {
        const res = await api.rollbackSystemUpdate();
        const box = el.querySelector("#update-msg");
        if (box) box.innerHTML = msg("ok", translateNotice(res.message));
        pollForRestartThenReload();
      } catch (err) {
        rollbackBtn.disabled = false;
        rollbackBtn.textContent = t("update.rollback");
        if (applyBtn2) applyBtn2.disabled = false;
        if (checkBtn3) checkBtn3.disabled = false;
        const box = el.querySelector("#update-msg");
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }
}

// pollForRestartThenReload 在使用者觸發「套用更新」之後,定期戳
// GET /api/v1/health,等 gonasd 真正重新啟動、API 又能回應之後自動重新
// 整理整個頁面——比起讓使用者自己盯著畫面手動按重新整理,體驗更順暢。
// 套用更新本身通常只需要幾秒鐘(下載完成之後的重啟是 exec(2) 換掉同一
// 個 PID 的程式映像檔,幾乎是瞬間的事,見
// internal/selfupdate.Reexec 的函式註解),中間會有一段 API 完全連不上
// 的空窗期,這裡的錯誤(fetch 失敗)在這段期間是預期中的過渡狀態,
// 不需要顯示成錯誤訊息——只有真的等了 1 分鐘還連不上,才放棄輪詢、讓
// 使用者自己判斷是不是更新失敗了。
function pollForRestartThenReload() {
  let attempts = 0;
  const maxAttempts = 30;
  const timer = setInterval(async () => {
    attempts += 1;
    try {
      await api.health();
      clearInterval(timer);
      location.reload();
    } catch {
      if (attempts >= maxAttempts) clearInterval(timer);
    }
  }, 2000);
}

// STAT_ICONS 是儀表板統計磚用的小圖示(inline SVG,不依賴任何外部圖示庫,
// 維持這個介面「零 CDN、離線可用」的原則)。第三十四輪 UI 精品化新增。
const STAT_ICONS = {
  server: '<path d="M3 4.5h14v4H3zM3 11.5h14v4H3z"/><path d="M6 6.5h.01M6 13.5h.01"/>',
  docker: '<path d="M3 11h14v1.5c0 2-1.8 3.5-4 3.5H7c-2.2 0-4-1.5-4-3.5z"/><rect x="5" y="7.5" width="2.4" height="2.4"/><rect x="8.3" y="7.5" width="2.4" height="2.4"/><rect x="11.6" y="7.5" width="2.4" height="2.4"/>',
  array: '<ellipse cx="10" cy="5" rx="6.5" ry="2.3"/><path d="M3.5 5v10c0 1.3 2.9 2.3 6.5 2.3s6.5-1 6.5-2.3V5"/><path d="M3.5 10c0 1.3 2.9 2.3 6.5 2.3s6.5-1 6.5-2.3"/>',
  disks: '<circle cx="10" cy="10" r="7.5"/><circle cx="10" cy="10" r="2"/>',
};

function statTile(label, value, cls, icon) {
  const ic = icon && STAT_ICONS[icon]
    ? `<svg class="stat-icon" viewBox="0 0 20 20" aria-hidden="true">${STAT_ICONS[icon]}</svg>`
    : "";
  return `<div class="stat-tile"><div class="stat-tile-head"><span class="label">${esc(label)}</span>${ic}</div><div class="value ${cls}">${esc(value)}</div></div>`;
}

// SECTION_ICONS 是各頁區塊標題(卡片 h2)前的小圖示。跟 STAT_ICONS 與
// 導覽列圖示同一套線條風格,inline SVG、零 CDN、離線可用。第三十四輪
// UI 精品化:給每個區塊一枚低調的識別圖示,讓八個頁面看起來成一套系統,
// 又不搶戲(圖示用 --text-faint、細描邊)。
const SECTION_ICONS = {
  link: '<path d="M8 12l4-4M7.5 5.5l1-1a3.5 3.5 0 0 1 5 5l-1 1M12.5 14.5l-1 1a3.5 3.5 0 0 1-5-5l1-1"/>',
  download: '<path d="M10 3v9M6.5 8.5 10 12l3.5-3.5M4 15.5h12"/>',
  array: '<ellipse cx="10" cy="5" rx="6.5" ry="2.3"/><path d="M3.5 5v10c0 1.3 2.9 2.3 6.5 2.3s6.5-1 6.5-2.3V5"/><path d="M3.5 10c0 1.3 2.9 2.3 6.5 2.3s6.5-1 6.5-2.3"/>',
  disks: '<circle cx="10" cy="10" r="7.5"/><circle cx="10" cy="10" r="2"/>',
  sliders: '<path d="M4 6.5h9M15 6.5h1M4 13.5h1M7 13.5h9"/><circle cx="13" cy="6.5" r="1.6"/><circle cx="6" cy="13.5" r="1.6"/>',
  trash: '<path d="M4.5 6h11M8 6V4.5h4V6M6 6l.8 9.5c.05.6.55 1 1.15 1h4.1c.6 0 1.1-.4 1.15-1L15 6"/>',
  box: '<path d="M10 3 3.5 6.2v7.6L10 17l6.5-3.2V6.2z"/><path d="M3.5 6.2 10 9.4l6.5-3.2M10 9.4V17"/>',
  grid: '<rect x="3" y="3" width="5.5" height="5.5" rx="1.2"/><rect x="11.5" y="3" width="5.5" height="5.5" rx="1.2"/><rect x="3" y="11.5" width="5.5" height="5.5" rx="1.2"/><rect x="11.5" y="11.5" width="5.5" height="5.5" rx="1.2"/>',
  plus: '<path d="M10 4.5v11M4.5 10h11"/>',
  share: '<circle cx="5" cy="10" r="2.3"/><circle cx="15" cy="5" r="2.3"/><circle cx="15" cy="15" r="2.3"/><path d="M6.95 8.9 13.05 5.8M6.95 11.1 13.05 14.2"/>',
  users: '<circle cx="10" cy="6.5" r="3"/><path d="M4.2 16.5c0-3.1 2.6-5.2 5.8-5.2s5.8 2.1 5.8 5.2"/>',
  chart: '<path d="M2.5 10.5h3l2-5.5 3 10 2-4.5h4.5"/>',
  bell: '<path d="M10 3.5a4 4 0 0 0-4 4c0 4.5-1.5 5.5-1.5 5.5h11S14 12 14 7.5a4 4 0 0 0-4-4zM8.5 16a1.5 1.5 0 0 0 3 0"/>',
  mail: '<rect x="3" y="5" width="14" height="10" rx="1.6"/><path d="M3.5 6l6.5 5 6.5-5"/>',
  calendar: '<rect x="3.5" y="4.5" width="13" height="12" rx="1.6"/><path d="M3.5 8h13M7 3v3M13 3v3"/>',
  key: '<circle cx="7" cy="7" r="3.2"/><path d="M9.3 9.3 16 16M13.5 13.5l1.5-1.5"/>',
  lock: '<rect x="4.5" y="9" width="11" height="7.5" rx="1.6"/><path d="M7 9V7a3 3 0 0 1 6 0v2"/>',
  shield: '<path d="M10 2.5 4 5v4.6c0 3.6 2.5 6.8 6 7.9 3.5-1.1 6-4.3 6-7.9V5z"/>',
  log: '<rect x="3.5" y="3" width="13" height="14" rx="1.6"/><path d="M6.5 7h7M6.5 10h7M6.5 13h4"/>',
  otp: '<rect x="5.5" y="2.5" width="9" height="15" rx="2"/><path d="M8.5 15h3"/>',
  backup: '<path d="M3 4.5h14v3H3z"/><path d="M4.5 7.5v8.5h11V7.5"/><path d="M8 11h4"/>',
  power: '<path d="M10 2.5v7"/><path d="M6 5.2a6 6 0 1 0 8 0"/>',
  battery: '<rect x="2.5" y="6.5" width="13" height="7" rx="1.5"/><path d="M17 9v2"/><rect x="4" y="8" width="7" height="4" rx="0.6" fill="currentColor" stroke="none"/>',
  stethoscope: '<path d="M5 3v4a3 3 0 0 0 6 0V3"/><path d="M8 13v-2"/><path d="M8 13a4.5 4.5 0 0 0 4.5 4.5c2.2 0 3.5-1.6 3.5-3.7"/><circle cx="16" cy="11.5" r="2"/>',
};

// h2i:帶圖示的區塊標題。label 必須是「已跳脫」的字串(呼叫端照舊傳
// esc(t(...))),圖示只是視覺裝飾(aria-hidden),不影響螢幕報讀。
function h2i(iconKey, label) {
  const ic = SECTION_ICONS[iconKey]
    ? `<svg class="card-icon" viewBox="0 0 20 20" aria-hidden="true">${SECTION_ICONS[iconKey]}</svg>`
    : "";
  return `<h2 class="card-h2">${ic}<span>${label}</span></h2>`;
}

function formatUptime(sec) {
  sec = sec || 0;
  if (sec < 60) return t("dashboard.uptimeSeconds", { n: sec });
  const m = Math.floor(sec / 60);
  if (m < 60) return t("dashboard.uptimeMinutes", { n: m });
  const h = Math.floor(m / 60);
  return t("dashboard.uptimeHours", { h, m: m % 60 });
}

function arrayLabel(state) {
  return t(`array.${state}`) !== `array.${state}` ? t(`array.${state}`) : state;
}
function arrayPillClass(state) {
  if (state === "started") return "ok";
  if (state === "failed") return "danger";
  if (state === "starting" || state === "stopping") return "warn";
  return "";
}

// renderParityStatus 誠實顯示同位保護狀態(第五十八輪產品 P1):沒有同位碟就
// 不顯示;有同位碟但從沒成功 sync 過 → 明確警告「尚未受保護」;同步中 → 顯示
// 進行中;已同步過 → 顯示「已於 X 受保護」。上次同步失敗也一併顯示。
function renderParityStatus(a) {
  if (!a || !a.hasParity) return "";
  let out = "";
  if (a.paritySyncing) {
    out += `<p style="margin:0 0 8px"><span class="pill warn">${esc(t("storage.paritySyncing"))}</span> <span style="color:var(--text-dim);font-size:12.5px">${esc(t("storage.paritySyncingHint"))}</span></p>`;
  } else if (a.protected) {
    out += `<p style="margin:0 0 8px"><span class="pill ok">${esc(t("storage.parityProtected", { time: formatDateTime(a.parityLastSync) }))}</span></p>`;
  } else {
    out += msg("warn", t("storage.parityNeverSynced"));
  }
  if (a.paritySyncError) out += msg("error", t("storage.paritySyncFailed", { msg: translateError(a.paritySyncError) }));
  return out;
}

// ---------- 儲存 ----------

// ---------- 新手設定精靈 ----------
// 一步步帶新使用者從「剛裝好」走到「可用」:歡迎 → 準備硬碟 → 建立儲存池
// → 建立第一個共享 → 完成。刻意全用既有的 API,不另造後端狀態:首次建好
// 管理帳號後自動帶進來(見 showSetupGate 成功後導向 #/setup),之後只要
// 還沒設定儲存池,儀表板上有卡片可隨時再進來;設定好了卡片就消失。每步
// 都能略過。步驟索引放模組層級變數,切步驟只是重畫。
const WIZARD_STEPS = ["welcome", "disks", "pool", "share", "done"];
let wizardStep = 0;

function wizardGo(el, i) {
  wizardStep = Math.max(0, Math.min(WIZARD_STEPS.length - 1, i));
  renderSetupWizard(el);
}

function wizardProgress() {
  const labels = [t("setup.stepWelcome"), t("setup.stepDisks"), t("setup.stepPool"), t("setup.stepShare"), t("setup.stepDone")];
  return `<div class="wizard-steps">` + labels.map((lbl, i) =>
    `<span class="wizard-step ${i === wizardStep ? "active" : (i < wizardStep ? "done" : "")}">${i + 1}. ${esc(lbl)}</span>`
  ).join("") + `</div>`;
}

// wizardShell 包一致的外框:標題、步驟指示、內容卡片、底部按鈕列。
// btns 是要顯示哪些按鈕(back/skip/next),各步驟自己綁事件。
function wizardShell(inner, btns) {
  const b = btns || {};
  const parts = [];
  if (b.back) parts.push(`<button class="secondary" data-wz-back>${esc(t("setup.back"))}</button>`);
  const right = [];
  if (b.skip) right.push(`<button class="secondary" data-wz-skip>${esc(b.skipLabel || t("setup.skip"))}</button>`);
  if (b.next) right.push(`<button data-wz-next>${esc(b.nextLabel || t("setup.next"))}</button>`);
  return `
    <h1>${esc(t("setup.title"))}</h1>
    ${wizardProgress()}
    <div class="card">${inner}</div>
    <div class="wizard-nav"><div>${parts.join("")}</div><div class="btn-row">${right.join("")}</div></div>
  `;
}

async function renderSetupWizard(el) {
  const step = WIZARD_STEPS[wizardStep];
  if (step === "welcome") return wizardWelcome(el);
  if (step === "disks") return wizardDisks(el);
  if (step === "pool") return wizardPool(el);
  if (step === "share") return wizardShare(el);
  return wizardDone(el);
}

async function wizardWelcome(el) {
  // 第五十二輪(二次覆核 P-2):嚮導要「知道」缺不缺套件。原廠映像可能還沒裝
  // mergerfs/snapraid(建池要)、samba(共享要),不先提醒就往下走,會在後面
  // 「看起來成功、其實沒生效」。這裡開場就檢查,缺的話指路到系統診斷一鍵補裝。
  const deps = await api.doctorStatus().catch(() => []);
  const wanted = ["mergerfs", "snapraid", "samba"];
  const missing = (deps || []).filter((d) => wanted.includes(d.key) && !d.installed);
  const depNote = missing.length ? `
    <div class="card" style="border-color:var(--warn);background:var(--warn-soft);margin-top:12px">
      <p style="margin:0 0 10px;font-size:12.5px">${esc(t("setup.depsMissing", { names: missing.map((d) => t("doctor.pkg." + d.key + ".name")).join("、") }))}</p>
      <div class="btn-row"><a href="#/doctor" class="btnlink">${esc(t("doctor.dashButton"))}</a></div>
    </div>` : "";
  el.innerHTML = wizardShell(
    `<h2>${esc(t("setup.welcomeTitle"))}</h2><p style="color:var(--text-dim)">${t("setup.welcomeBody")}</p>${depNote}`,
    { next: true, nextLabel: t("setup.start"), skip: true, skipLabel: t("setup.skipAll") }
  );
  el.querySelector("[data-wz-next]").addEventListener("click", () => wizardGo(el, 1));
  el.querySelector("[data-wz-skip]").addEventListener("click", () => { location.hash = "#/dashboard"; });
}

async function wizardDisks(el) {
  const disks = await api.disks().catch(() => []);
  el.innerHTML = wizardShell(`
    <h2>${esc(t("setup.disksTitle"))}</h2>
    <p style="color:var(--text-dim)">${t("setup.disksBody")}</p>
    <div id="prepare-msg"></div>
    <div id="prepare-list">${renderPrepareList(disks)}</div>
  `, { back: true, next: true, skip: true });
  wirePrepareDisk(el, wizardDisks); // 成功後重畫本步驟(讓剛備好的碟從候選消失)
  el.querySelector("[data-wz-back]").addEventListener("click", () => wizardGo(el, 0));
  el.querySelector("[data-wz-next]").addEventListener("click", () => wizardGo(el, 2));
  el.querySelector("[data-wz-skip]").addEventListener("click", () => wizardGo(el, 2));
}

// poolCandidates 挑出「已經備好、掛在 /mnt 底下」的碟——這些才是能加進儲存池
// 的對象(系統碟掛在 / 或 /boot,不會是 /mnt 開頭,自然被排除)。第三十輪
// 覆核(資深產品經理)把嚮導這一步從「手打路徑」改成勾選,靠的就是這份清單。
function poolCandidates(disks) {
  return (disks || []).filter((d) => d.mountpoint && d.mountpoint.indexOf("/mnt/") === 0);
}

// poolDiskPicker 把候選碟畫成一排「勾選 + 資料/同位切換」。預設把「最大的
// 一顆」設成同位碟 —— SnapRAID 要求同位碟不小於最大的資料碟,把最大的當同位
// 是最不會出錯的預設,使用者要改也行。
function poolDiskPicker(candidates) {
  let maxIdx = 0;
  candidates.forEach((d, i) => { if (d.sizeBytes > candidates[maxIdx].sizeBytes) maxIdx = i; });
  return candidates.map((d, i) => {
    const nm = "role-" + d.path.replace(/[^a-zA-Z0-9]/g, "_");
    const parityDefault = i === maxIdx && candidates.length >= 2;
    return `<div class="pool-disk-row">
      <label class="pool-disk-pick"><input type="checkbox" class="pool-pick" data-mount="${esc(d.mountpoint)}" data-role-name="${esc(nm)}" checked>
        <span class="pool-disk-info"><code>${esc(d.path)}</code><span class="prepare-meta">${formatBytes(d.sizeBytes)} · ${d.rotational ? "HDD" : "SSD/NVMe"} · ${esc(d.mountpoint)}</span></span>
      </label>
      <span class="pool-role-toggle">
        <label><input type="radio" name="${nm}" value="data" ${parityDefault ? "" : "checked"}>${esc(t("storage.roleData"))}</label>
        <label><input type="radio" name="${nm}" value="parity" ${parityDefault ? "checked" : ""}>${esc(t("storage.roleParity"))}</label>
      </span>
    </div>`;
  }).join("");
}

// collectPool 從勾選狀態組出要送給後端的 pool 設定,並做「至少一顆資料碟、
// 至少一顆同位碟」的前端檢查。content 檔位置自動推導(放在每顆資料碟上,不足
// 兩份就補到同位碟),使用者完全不用碰 SnapRAID 的內部細節。回傳 {pool} 或
// {error}。
function collectPool(el, name, mountPoint) {
  const data = [], parity = [];
  el.querySelectorAll(".pool-pick").forEach((cb) => {
    if (!cb.checked) return;
    const role = el.querySelector(`input[name="${cb.dataset.roleName}"]:checked`);
    (role && role.value === "parity" ? parity : data).push(cb.dataset.mount);
  });
  if (data.length === 0) return { error: t("storage.needData") };
  if (parity.length === 0) return { error: t("storage.needParity") };
  const content = data.slice();
  for (const p of parity) { if (content.length >= 2) break; content.push(p); }
  return { pool: { name: name.trim(), mountPoint: mountPoint.trim(), dataDisks: data, parityDisks: parity, contentFiles: content } };
}

// poolParityNote:一句話 + 小圖解釋同位碟在做什麼(第三十輪覆核要求)。
function poolParityNote() {
  return `<div class="pool-parity-note">
    <svg viewBox="0 0 120 40" aria-hidden="true" class="pool-parity-diagram">
      <rect x="2" y="10" width="20" height="20" rx="3"/><rect x="26" y="10" width="20" height="20" rx="3"/><rect x="50" y="10" width="20" height="20" rx="3"/>
      <rect x="90" y="10" width="20" height="20" rx="3" class="parity"/>
      <path d="M74 20h12" /><path d="M82 16l4 4-4 4"/>
    </svg>
    <p>${esc(t("storage.parityExplain"))}</p>
  </div>`;
}

async function wizardPool(el) {
  const [arr, disks] = await Promise.all([
    api.arrayStatus().catch(() => ({ state: "unconfigured" })),
    api.disks().catch(() => []),
  ]);
  if (arr.state !== "unconfigured") {
    el.innerHTML = wizardShell(
      `<h2>${esc(t("setup.poolTitle"))}</h2>${msg("ok", t("setup.poolAlready"))}`,
      { back: true, next: true }
    );
    el.querySelector("[data-wz-back]").addEventListener("click", () => wizardGo(el, 1));
    el.querySelector("[data-wz-next]").addEventListener("click", () => wizardGo(el, 3));
    return;
  }
  const candidates = poolCandidates(disks);
  if (candidates.length === 0) {
    el.innerHTML = wizardShell(`
      <h2>${esc(t("setup.poolTitle"))}</h2>
      ${msg("warn", t("storage.poolNoPrepared"))}
    `, { back: true, skip: true });
    el.querySelector("[data-wz-back]").addEventListener("click", () => wizardGo(el, 1));
    el.querySelector("[data-wz-skip]").addEventListener("click", () => wizardGo(el, 3));
    return;
  }
  el.innerHTML = wizardShell(`
    <h2>${esc(t("setup.poolTitle"))}</h2>
    <p style="color:var(--text-dim)">${t("setup.poolBody")}</p>
    ${poolParityNote()}
    <div id="wz-pool-msg"></div>
    <form class="stacked" id="wz-pool-form">
      <div class="field"><label>${esc(t("storage.poolName"))}</label><input type="text" name="name" value="tank" required></div>
      <div class="field"><label>${esc(t("storage.poolMountPoint"))}</label><input type="text" name="mountPoint" value="/mnt/tank" required></div>
      <div class="field"><label>${esc(t("storage.choosePoolDisks"))}</label>
        <div class="pool-disk-list">${poolDiskPicker(candidates)}</div>
      </div>
      <div class="btn-row"><button type="submit">${esc(t("storage.savePool"))}</button></div>
    </form>
  `, { back: true, skip: true });
  el.querySelector("[data-wz-back]").addEventListener("click", () => wizardGo(el, 1));
  el.querySelector("[data-wz-skip]").addEventListener("click", () => wizardGo(el, 3));
  el.querySelector("#wz-pool-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#wz-pool-msg");
    const res = collectPool(el, f.get("name"), f.get("mountPoint"));
    if (res.error) { box.innerHTML = msg("error", translateError(res.error)); return; }
    try {
      await api.setPool(res.pool);
      wizardGo(el, 3);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

async function wizardShare(el) {
  const arr = await api.arrayStatus().catch(() => ({ mountPoint: "" }));
  const base = arr.mountPoint || "/mnt/tank";
  el.innerHTML = wizardShell(`
    <h2>${esc(t("setup.shareTitle"))}</h2>
    <p style="color:var(--text-dim)">${t("setup.shareBody")}</p>
    <div id="wz-share-msg"></div>
    <form class="stacked" id="wz-share-form">
      <div class="field"><label>${esc(t("shares.name"))}</label><input type="text" name="name" placeholder="media" required></div>
      <div class="field"><label>${esc(t("shares.path"))}</label><input type="text" name="path" value="${esc(base)}/media" required></div>
      <div class="checkbox-row"><label><input type="checkbox" name="guestOk"> ${esc(t("shares.guestOk"))}</label></div>
      <div class="btn-row"><button type="submit">${esc(t("setup.createShare"))}</button></div>
    </form>
  `, { back: true, skip: true, skipLabel: t("setup.skip") });
  el.querySelector("[data-wz-back]").addEventListener("click", () => wizardGo(el, 2));
  el.querySelector("[data-wz-skip]").addEventListener("click", () => wizardGo(el, 4));
  el.querySelector("#wz-share-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const share = {
      name: f.get("name").trim(), path: f.get("path").trim(),
      guestOk: f.get("guestOk") === "on", readOnly: false, validUsers: [],
    };
    const box = el.querySelector("#wz-share-msg");
    try {
      const res = await api.createShare(share);
      // 第五十二輪(二次覆核 P-2):不要在「其實沒套用成功」時假裝成功。後端
      // 在 samba 沒裝時會回 {applied:false, warning},共享設定有存、但 Windows
      // 還連不上。這時顯示警告 + 指路到系統診斷裝 samba,不自動衝到「完成」。
      if (res && res.applied === false) {
        box.innerHTML = msg("warn", t("setup.shareSavedNotApplied") + " " + t("setup.installViaDoctor"));
      } else {
        wizardGo(el, 4);
      }
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

function wizardDone(el) {
  el.innerHTML = wizardShell(
    `<h2>${esc(t("setup.doneTitle"))}</h2><p style="color:var(--text-dim)">${t("setup.doneBody")}</p>`,
    { next: true, nextLabel: t("setup.finish") }
  );
  el.querySelector("[data-wz-next]").addEventListener("click", () => { wizardStep = 0; location.hash = "#/dashboard"; });
}

async function renderStorage(el) {
  const [disks, arrayStatus, scrubSchedule, smartSchedule] = await Promise.all([
    api.disks().catch(() => []), api.arrayStatus().catch(() => ({ state: "unconfigured" })),
    api.paritySchedule().catch(() => ({ enabled: false, everyDays: 7, hour: 3, minute: 0 })),
    api.smartSchedule().catch(() => ({ enabled: false, everyDays: 7, hour: 4, minute: 0, kind: "short" })),
  ]);

  el.innerHTML = `
    <h1>${esc(t("storage.title"))}</h1>
    <p class="page-subtitle">${esc(t("storage.subtitle"))}</p>

    <div class="card">
      ${h2i("array", esc(t("storage.currentStatus")))}
      <p style="margin:0 0 12px">
        <span class="pill ${arrayPillClass(arrayStatus.state)}">${esc(arrayLabel(arrayStatus.state))}</span>
        ${arrayStatus.mountPoint ? ` · ${esc(t("storage.mountPoint"))} <code>${esc(arrayStatus.mountPoint)}</code>` : ""}
      </p>
      ${arrayStatus.error ? msg("error", translateError(arrayStatus.error)) : ""}
      ${renderParityStatus(arrayStatus)}
      <div class="btn-row">
        <button id="start-array" ${arrayStatus.state === "unconfigured" ? "disabled" : ""}>${esc(t("storage.startArray"))}</button>
        <button id="stop-array" class="secondary" ${arrayStatus.state === "unconfigured" ? "disabled" : ""}>${esc(t("storage.stopArray"))}</button>
        ${arrayStatus.hasParity ? `<button id="sync-parity" class="secondary" ${arrayStatus.paritySyncing ? "disabled" : ""}>${esc(arrayStatus.paritySyncing ? t("storage.paritySyncing") : t("storage.syncParity"))}</button>` : ""}
        ${arrayStatus.hasParity ? `<button id="scrub-parity" class="secondary" ${arrayStatus.paritySyncing ? "disabled" : ""}>${esc(t("storage.scrubParity"))}</button>` : ""}
      </div>
    </div>

    ${arrayStatus.hasParity ? `
    <div class="card">
      ${h2i("calendar", esc(t("storage.scrubScheduleTitle")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("storage.scrubScheduleHint"))}</p>
      <div id="scrub-sched-msg"></div>
      <form class="stacked" id="scrub-sched-form">
        <label class="checkbox-row"><input type="checkbox" name="enabled" ${scrubSchedule.enabled ? "checked" : ""}> ${esc(t("storage.scrubScheduleEnable"))}</label>
        <div class="field-row">
          <div class="field"><label>${esc(t("storage.scrubEveryDays"))}</label><input type="number" name="everyDays" min="1" max="365" value="${esc(String(scrubSchedule.everyDays || 7))}"></div>
          <div class="field"><label>${esc(t("storage.scrubHour"))}</label><input type="number" name="hour" min="0" max="23" value="${esc(String(scrubSchedule.hour || 0))}"></div>
          <div class="field"><label>${esc(t("storage.scrubMinute"))}</label><input type="number" name="minute" min="0" max="59" value="${esc(String(scrubSchedule.minute || 0))}"></div>
        </div>
        <div class="btn-row"><button type="submit">${esc(t("common.save"))}</button></div>
      </form>
    </div>` : ""}

    ${arrayStatus.state !== "unconfigured" ? `
    <div class="card">
      ${h2i("stethoscope", esc(t("storage.smartTitle")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("storage.smartHint"))}</p>
      ${smartSchedule.lastRunAt ? `<p style="margin:0 0 10px"><span class="pill neutral">${esc(t("storage.smartLastRun", { time: formatDateTime(smartSchedule.lastRunAt) }))}</span></p>` : ""}
      <div id="smart-sched-msg"></div>
      <form class="stacked" id="smart-sched-form">
        <label class="checkbox-row"><input type="checkbox" name="enabled" ${smartSchedule.enabled ? "checked" : ""}> ${esc(t("storage.smartScheduleEnable"))}</label>
        <div class="field-row">
          <div class="field"><label>${esc(t("storage.smartKind"))}</label>
            <select name="kind">
              <option value="short" ${smartSchedule.kind !== "long" ? "selected" : ""}>${esc(t("storage.smartKindShort"))}</option>
              <option value="long" ${smartSchedule.kind === "long" ? "selected" : ""}>${esc(t("storage.smartKindLong"))}</option>
            </select>
          </div>
          <div class="field"><label>${esc(t("storage.scrubEveryDays"))}</label><input type="number" name="everyDays" min="1" max="365" value="${esc(String(smartSchedule.everyDays || 7))}"></div>
          <div class="field"><label>${esc(t("storage.scrubHour"))}</label><input type="number" name="hour" min="0" max="23" value="${esc(String(smartSchedule.hour || 0))}"></div>
          <div class="field"><label>${esc(t("storage.scrubMinute"))}</label><input type="number" name="minute" min="0" max="59" value="${esc(String(smartSchedule.minute || 0))}"></div>
        </div>
        <div class="btn-row">
          <button type="submit">${esc(t("common.save"))}</button>
          <button type="button" id="smart-run-now" class="secondary">${esc(t("storage.smartRunNow"))}</button>
        </div>
      </form>
    </div>` : ""}

    ${arrayStatus.hasParity && (arrayStatus.dataDisks && arrayStatus.dataDisks.length) ? `
    <div class="card" style="border-color:var(--warn-border, var(--border))">
      ${h2i("array", esc(t("storage.recoveryTitle")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("storage.recoveryHint"))}</p>
      <div id="recovery-msg"></div>
      ${arrayStatus.dataDisks.map((d) => `
        <div class="service-row">
          <span><code>${esc(d)}</code></span>
          <button type="button" class="danger" data-fix-disk="${esc(d)}" ${arrayStatus.paritySyncing ? "disabled" : ""}>${esc(t("storage.rebuildDisk"))}</button>
        </div>`).join("")}
    </div>` : ""}

    <div class="card">
      ${h2i("disks", esc(t("storage.disksDetected")))}
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("storage.colDevice"))}</th><th>${esc(t("storage.colModel"))}</th><th>${esc(t("storage.colCapacity"))}</th><th>${esc(t("storage.colType"))}</th><th>${esc(t("storage.colMountPoint"))}</th><th>${esc(t("storage.colSmart"))}</th></tr></thead>
          <tbody>
            ${disks.length ? disks.map((d) => `
              <tr>
                <td><code>${esc(d.path)}</code></td>
                <td>${esc(d.model || "—")}</td>
                <td>${formatBytes(d.sizeBytes)}</td>
                <td>${d.rotational ? "HDD" : "SSD/NVMe"}</td>
                <td>${esc(d.mountpoint || "—")}</td>
                <td data-smart-cell="${esc(d.path)}"><span class="pill neutral">${esc(t("common.loading"))}</span></td>
              </tr>`).join("") : `<tr><td colspan="6" class="empty-state">${esc(t("storage.noDisksDetected"))}</td></tr>`}
          </tbody>
        </table>
      </div>
    </div>

    <div class="card">
      ${h2i("disks", esc(t("storage.prepareTitle")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${t("storage.prepareHint")}</p>
      <div id="prepare-msg"></div>
      <div id="prepare-list">${renderPrepareList(disks)}</div>
    </div>

    <div class="card">
      ${h2i("sliders", esc(t("storage.poolSetup")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${t("storage.poolSetupHint")}</p>
      ${poolCandidates(disks).length === 0
        ? msg("warn", t("storage.poolNoPrepared"))
        : `${poolParityNote()}
      <div id="pool-msg"></div>
      <form class="stacked" id="pool-form">
        <div class="field"><label>${esc(t("storage.poolName"))}</label><input type="text" name="name" value="${esc(arrayStatus.name || "tank")}" required></div>
        <div class="field"><label>${esc(t("storage.poolMountPoint"))}</label><input type="text" name="mountPoint" value="${esc(arrayStatus.mountPoint || "/mnt/tank")}" required></div>
        <div class="field"><label>${esc(t("storage.choosePoolDisks"))}</label>
          <div class="pool-disk-list">${poolDiskPicker(poolCandidates(disks))}</div>
        </div>
        <div class="btn-row"><button type="submit">${esc(t("storage.savePool"))}</button></div>
      </form>`}
    </div>
  `;

  el.querySelector("#start-array").addEventListener("click", () => runAction(api.startArray, renderStorage, el));
  el.querySelector("#stop-array").addEventListener("click", () => {
    // 停止阵列会卸载存储池,所有共享和运行中的应用都会失去存储 —— 高风险,
    // 跟其他破坏性操作一样先确认(第六十轮 UI 复审)。
    if (!confirm(t("storage.stopArrayConfirm"))) return;
    runAction(api.stopArray, renderStorage, el);
  });
  const syncBtn = el.querySelector("#sync-parity");
  if (syncBtn) {
    syncBtn.addEventListener("click", async () => {
      syncBtn.disabled = true;
      try {
        await api.syncArray();
        // sync 在背景跑,重新載入頁面會顯示「同步中…」狀態。
        await renderStorage(el);
      } catch (err) {
        el.querySelector("#pool-msg")?.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
        syncBtn.disabled = false;
      }
    });
  }

  const scrubBtn = el.querySelector("#scrub-parity");
  if (scrubBtn) {
    scrubBtn.addEventListener("click", async () => {
      if (!confirm(t("storage.scrubConfirm"))) return;
      scrubBtn.disabled = true;
      try {
        await api.scrubArray();
        await renderStorage(el);
      } catch (err) {
        el.querySelector("#pool-msg")?.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
        scrubBtn.disabled = false;
      }
    });
  }

  const scrubSchedForm = el.querySelector("#scrub-sched-form");
  if (scrubSchedForm) {
    scrubSchedForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const box = el.querySelector("#scrub-sched-msg");
      const cfg = {
        enabled: f.get("enabled") === "on",
        everyDays: Number(f.get("everyDays")) || 7,
        hour: Number(f.get("hour")) || 0,
        minute: Number(f.get("minute")) || 0,
      };
      try {
        await api.setParitySchedule(cfg);
        if (box) box.innerHTML = msg("ok", t("storage.scrubScheduleSaved"));
      } catch (err) {
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const smartSchedForm = el.querySelector("#smart-sched-form");
  if (smartSchedForm) {
    const box = el.querySelector("#smart-sched-msg");
    smartSchedForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const cfg = {
        enabled: f.get("enabled") === "on",
        kind: f.get("kind") || "short",
        everyDays: Number(f.get("everyDays")) || 7,
        hour: Number(f.get("hour")) || 0,
        minute: Number(f.get("minute")) || 0,
      };
      try {
        await api.setSmartSchedule(cfg);
        if (box) box.innerHTML = msg("ok", t("storage.scrubScheduleSaved"));
      } catch (err) {
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
    const runBtn = el.querySelector("#smart-run-now");
    if (runBtn) {
      runBtn.addEventListener("click", async () => {
        const kind = smartSchedForm.querySelector('select[name="kind"]').value || "short";
        runBtn.disabled = true;
        runBtn.textContent = t("storage.smartRunning");
        try {
          const res = await api.runSmartTest(kind);
          if (box) box.innerHTML = msg("ok", t("storage.smartRunStarted", { n: res.started }));
        } catch (err) {
          if (box) box.innerHTML = msg("error", translateError(err.message));
        } finally {
          runBtn.disabled = false;
          runBtn.textContent = t("storage.smartRunNow");
        }
      });
    }
  }

  el.querySelectorAll("[data-fix-disk]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const mount = btn.dataset.fixDisk;
      // 重建是破坏性操作:会用校验数据覆写这块盘的内容。强确认 + 二次输入掛载点。
      if (!confirm(t("storage.rebuildConfirm", { disk: mount }))) return;
      const typed = prompt(t("storage.rebuildTypePrompt", { disk: mount }), "");
      if (typed !== mount) {
        if (typed !== null) el.querySelector("#recovery-msg").innerHTML = msg("error", t("storage.rebuildMismatch"));
        return;
      }
      btn.disabled = true;
      const box = el.querySelector("#recovery-msg");
      try {
        await api.fixDisk(mount);
        if (box) box.innerHTML = msg("ok", t("storage.rebuildStarted"));
        await renderStorage(el);
      } catch (err) {
        btn.disabled = false;
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  if (disks.length) loadSmartData(el);

  wirePrepareDisk(el);

  // 第五十二輪(二次覆核 P-1/UI-1):儲存頁的建立儲存池從「手打 SnapRAID
  // 路徑」的 textarea 改成跟新手嚮導同一套視覺化硬碟選擇器(勾選 + 資料/同位
  // 切換 + 自動 content 檔),不再讓使用者回到這頁又撞上一套完全不同、嚇人的
  // 舊介面。共用 poolDiskPicker/collectPool(見上面嚮導那段)。
  const poolForm = el.querySelector("#pool-form");
  if (poolForm) {
    poolForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const box = el.querySelector("#pool-msg");
      const res = collectPool(el, f.get("name"), f.get("mountPoint"));
      if (res.error) { box.innerHTML = msg("error", translateError(res.error)); return; }
      try {
        await api.setPool(res.pool);
        box.innerHTML = msg("ok", t("storage.poolSaved"));
        await renderStorage(el);
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }
}

// renderPrepareList 畫出「可以拿來準備」的候選硬碟:只列未掛載的整碟
// (d.inUse === false)。系統碟與使用中的碟因為 inUse 被過濾掉,不會出現,
// 避免使用者誤選(後端 PrepareDisk 也會再擋一次,不倚賴前端過濾)。
// 另外把 0 位元組的虛擬區塊裝置(zram、空的 loop 等)也濾掉 —— 它們不是
// 真的可以拿來做儲存的硬碟,列出來只會讓使用者困惑。
function renderPrepareList(disks) {
  const all = disks || [];
  const candidates = all.filter((d) => !d.inUse && d.sizeBytes > 0);
  if (!candidates.length) {
    return `<p class="empty-state">${esc(t("storage.prepareNoCandidates"))}</p>`;
  }
  // 第五十三輪(真機測試抓到):建議掛載點原本用候選清單的 index+1,但一顆
  // 一顆準備時,每準備好一顆它就從候選清單消失,下一顆的 index 又回到 0,
  // 於是每顆都被建議成 /mnt/disk1 —— 使用者照建議按下去,好幾顆全掛到
  // /mnt/disk1,最後在建立儲存池時撞成「同一顆碟被列了不止一次」。改成掃描
  // 「已經被任何碟(含已掛載的)佔用的 /mnt/diskN 編號」,每顆都配一個真正
  // 還沒被用到的編號。
  const usedNums = new Set();
  all.forEach((d) => {
    const m = /^\/mnt\/disk(\d+)$/.exec(d.mountpoint || "");
    if (m) usedNums.add(Number(m[1]));
  });
  let nextNum = 0;
  const nextFreeMount = () => {
    do { nextNum += 1; } while (usedNums.has(nextNum));
    usedNums.add(nextNum);
    return `/mnt/disk${nextNum}`;
  };
  return candidates.map((d) => {
    const suggested = nextFreeMount();
    return `<div class="prepare-row" data-device="${esc(d.path)}">
      <div class="prepare-info">
        <code>${esc(d.path)}</code>
        <span class="prepare-meta">${formatBytes(d.sizeBytes)} · ${d.rotational ? "HDD" : "SSD/NVMe"}${d.model ? " · " + esc(d.model) : ""}</span>
      </div>
      <div class="prepare-action">
        <input type="text" class="prepare-mount" value="${esc(suggested)}" aria-label="${esc(t("storage.prepareMountLabel"))}">
        <button type="button" class="prepare-btn secondary">${esc(t("storage.prepareBtn"))}</button>
      </div>
    </div>`;
  }).join("");
}

// wirePrepareDisk 綁定每一列「格式化並掛載」按鈕:先跳確認(破壞性操作),
// 使用者確認後呼叫 API,成功就重畫整頁(讓新掛載的碟出現在硬碟表格、也從
// 候選清單消失)。
function wirePrepareDisk(el, rerender) {
  const rerenderFn = rerender || renderStorage; // 儲存頁用預設;精靈傳自己的
  const box = el.querySelector("#prepare-msg");
  el.querySelectorAll(".prepare-row").forEach((row) => {
    const btn = row.querySelector(".prepare-btn");
    if (!btn) return;
    btn.addEventListener("click", async () => {
      const device = row.getAttribute("data-device");
      const mount = row.querySelector(".prepare-mount").value.trim();
      if (!window.confirm(t("storage.prepareConfirm", { device }))) return;
      btn.disabled = true;
      try {
        const res = await api.prepareDisk(device, mount);
        box.innerHTML = msg("ok", t("storage.prepareDone", { device: res.device, mount: res.mountpoint }));
        await rerenderFn(el);
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
        btn.disabled = false;
      }
    });
  });
}

function linesOf(text) {
  return (text || "").split("\n").map((s) => s.trim()).filter(Boolean);
}

async function runAction(fn, rerender, el) {
  try {
    await fn();
    await rerender(el);
  } catch (err) {
    el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
  }
}

function formatBytes(n) {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

// loadSmartData 在硬碟表格已經先用偵測到的基本資訊(型號/容量之類)畫出來
// 之後,才另外非同步抓 SMART 健康狀態填進「SMART」那一欄——刻意分兩階段
// 而不是等 SMART 查完才一次畫出整張表:對每顆硬碟跑一次 `smartctl -a`
// 可能要一段時間(尤其是還沒喚醒的傳統硬碟,或根本沒裝 smartctl 導致
// 逐一逾時),不該讓使用者盯著空白頁面等,基本資訊應該先看得到。單顆
// 硬碟查詢失敗(見 internal/api/storage_handlers.go 的說明——沒裝
// smartctl、裝置不支援 SMART 都算)顯示「無法讀取」而不是讓整欄空著,
// 並把原始錯誤放進 title 屬性,滑鼠移過去可以看到細節。
async function loadSmartData(el) {
  let results;
  try {
    results = await api.disksSmart();
  } catch (err) {
    el.querySelectorAll("[data-smart-cell]").forEach((cell) => {
      cell.innerHTML = `<span class="pill neutral" title="${esc(err.message)}">${esc(t("storage.smartUnavailable"))}</span>`;
    });
    return;
  }
  const cellsByPath = new Map();
  el.querySelectorAll("[data-smart-cell]").forEach((cell) => cellsByPath.set(cell.dataset.smartCell, cell));
  for (const r of results) {
    const cell = cellsByPath.get(r.path);
    if (!cell) continue;
    if (r.error) {
      cell.innerHTML = `<span class="pill neutral" title="${esc(r.error)}">${esc(t("storage.smartUnavailable"))}</span>`;
    } else if (!r.passed) {
      cell.innerHTML = `<span class="pill danger">${esc(t("storage.smartFailed"))}</span>`;
    } else {
      const temp = r.tempCelsius != null ? ` · ${r.tempCelsius}°C` : "";
      cell.innerHTML = `<span class="pill ok">${esc(t("storage.smartHealthy"))}${esc(temp)}</span>`;
    }
  }
}

// ---------- 檔案 ----------
//
// 檔案管理員讓使用者直接在瀏覽器裡瀏覽、上傳、下載、整理陣列裡的檔案，
// 不需要另外掛載 SMB/NFS 或安裝任何用戶端軟體。畫面狀態(目前瀏覽到
// 哪個目錄、勾選了哪些項目)刻意放在模組層級的 filesState,而不是每次
// 重新 render 就重置——使用者切去別頁再切回來，理應還停留在原本瀏覽的
// 目錄，這才是符合直覺的行為。

const filesState = {
  path: "",
  selected: new Set(),
  searching: false,
};

function joinPath(dir, name) {
  return dir ? `${dir}/${name}` : name;
}

function dirname(p) {
  const i = p.lastIndexOf("/");
  return i === -1 ? "" : p.slice(0, i);
}

function basename(p) {
  const i = p.lastIndexOf("/");
  return i === -1 ? p : p.slice(i + 1);
}

async function renderFiles(el) {
  const status = await api.filesStatus().catch((e) => ({ available: false, reason: e.message }));

  el.innerHTML = `
    <h1>${esc(t("files.title"))}</h1>
    <p class="page-subtitle">${esc(t("files.subtitle"))}</p>
    <div id="files-root"></div>
  `;
  const root = el.querySelector("#files-root");

  if (!status.available) {
    root.innerHTML = `
      ${msg("warn", t("files.unavailable", { reason: translateError(status.reason) || "" }))}
      <p class="hint">${esc(t("files.unavailableHint"))}</p>
    `;
    return;
  }

  root.innerHTML = `
    <div class="card">
      <div id="files-toolbar" class="files-toolbar"></div>
      <div id="files-breadcrumb" class="breadcrumb"></div>
      <div id="files-msg"></div>
      <div id="files-drop-zone" class="files-drop-zone">
        <div class="table-wrap">
          <table class="file-table">
            <thead><tr><th></th><th>${esc(t("files.colName"))}</th><th>${esc(t("files.colSize"))}</th><th>${esc(t("files.colModified"))}</th><th></th></tr></thead>
            <tbody id="files-tbody"></tbody>
          </table>
        </div>
      </div>
      <div id="files-panel"></div>
    </div>
    <div class="card">
      ${h2i("trash", esc(t("files.trash")))}
      <p class="hint">${esc(t("files.trashHint"))}</p>
      <div id="trash-msg"></div>
      <div id="trash-list"></div>
      <div class="btn-row"><button class="secondary" id="trash-empty-btn">${esc(t("files.emptyTrash"))}</button></div>
    </div>
  `;

  wireFilesToolbar(root);
  wireFilesDropZone(root);
  await loadFilesList(root);
  await loadTrash(root);

  root.querySelector("#trash-empty-btn").addEventListener("click", async () => {
    if (!confirm(t("files.emptyTrashConfirm"))) return;
    try {
      await api.filesTrashEmpty();
      await loadTrash(root);
    } catch (err) {
      root.querySelector("#trash-msg").innerHTML = msg("error", translateError(err.message));
    }
  });
}

function wireFilesToolbar(root) {
  const toolbar = root.querySelector("#files-toolbar");
  toolbar.innerHTML = `
    <div class="btn-row">
      <button type="button" id="files-upload-btn">${esc(t("files.upload"))}</button>
      <input type="file" id="files-upload-input" multiple hidden>
      <button type="button" class="secondary" id="files-mkdir-btn">${esc(t("files.newFolder"))}</button>
      <button type="button" class="secondary" id="files-move-btn">${esc(t("files.moveSelected"))}</button>
      <button type="button" class="secondary" id="files-copy-btn">${esc(t("files.copySelected"))}</button>
      <button type="button" class="secondary" id="files-download-btn">${esc(t("files.downloadSelected"))}</button>
      <button type="button" class="danger" id="files-delete-btn">${esc(t("files.deleteSelected"))}</button>
    </div>
    <form class="btn-row" id="files-search-form" style="margin-top:8px">
      <input type="text" id="files-search-input" placeholder="${esc(t("files.searchPlaceholder"))}" style="flex:1;min-width:200px">
      <button type="submit" class="secondary">${esc(t("common.search"))}</button>
      <button type="button" class="secondary" id="files-search-clear" hidden>${esc(t("files.clearSearch"))}</button>
    </form>
  `;

  const fileInput = toolbar.querySelector("#files-upload-input");
  toolbar.querySelector("#files-upload-btn").addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", async () => {
    if (fileInput.files.length) await uploadFiles(root, fileInput.files);
    fileInput.value = "";
  });

  toolbar.querySelector("#files-mkdir-btn").addEventListener("click", async () => {
    const name = prompt(t("files.newFolderPrompt"));
    if (!name) return;
    try {
      await api.filesMkdir(joinPath(filesState.path, name));
      await loadFilesList(root);
    } catch (err) {
      showFilesMsg(root, "error", translateError(err.message));
    }
  });

  toolbar.querySelector("#files-move-btn").addEventListener("click", () => moveOrCopySelected(root, "move"));
  toolbar.querySelector("#files-copy-btn").addEventListener("click", () => moveOrCopySelected(root, "copy"));
  toolbar.querySelector("#files-download-btn").addEventListener("click", () => downloadSelected(root));
  toolbar.querySelector("#files-delete-btn").addEventListener("click", () => deleteSelected(root));

  toolbar.querySelector("#files-search-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const q = toolbar.querySelector("#files-search-input").value.trim();
    if (!q) return;
    filesState.searching = true;
    toolbar.querySelector("#files-search-clear").hidden = false;
    try {
      const result = await api.filesSearch(filesState.path, q);
      renderFileRows(root, result.entries, { flatPaths: true, truncated: result.truncated });
    } catch (err) {
      showFilesMsg(root, "error", translateError(err.message));
    }
  });
  toolbar.querySelector("#files-search-clear").addEventListener("click", async () => {
    filesState.searching = false;
    toolbar.querySelector("#files-search-input").value = "";
    toolbar.querySelector("#files-search-clear").hidden = true;
    await loadFilesList(root);
  });
}

function wireFilesDropZone(root) {
  const zone = root.querySelector("#files-drop-zone");
  ["dragenter", "dragover"].forEach((evt) => {
    zone.addEventListener(evt, (ev) => {
      ev.preventDefault();
      zone.classList.add("drag-over");
    });
  });
  ["dragleave", "drop"].forEach((evt) => {
    zone.addEventListener(evt, (ev) => {
      ev.preventDefault();
      zone.classList.remove("drag-over");
    });
  });
  zone.addEventListener("drop", async (ev) => {
    const files = ev.dataTransfer && ev.dataTransfer.files;
    if (files && files.length) await uploadFiles(root, files);
  });
}

function showFilesMsg(root, kind, text) {
  root.querySelector("#files-msg").innerHTML = msg(kind, text);
}

async function loadFilesList(root) {
  filesState.selected.clear();
  root.querySelector("#files-msg").innerHTML = "";
  renderBreadcrumb(root);
  try {
    const entries = await api.filesList(filesState.path);
    entries.sort((a, b) => (a.isDir === b.isDir ? a.name.localeCompare(b.name) : a.isDir ? -1 : 1));
    renderFileRows(root, entries, { flatPaths: false });
  } catch (err) {
    showFilesMsg(root, "error", translateError(err.message));
    root.querySelector("#files-tbody").innerHTML = "";
  }
}

function renderBreadcrumb(root) {
  const parts = filesState.path ? filesState.path.split("/") : [];
  let acc = "";
  const crumbs = [`<a href="#" data-goto="">${esc(t("files.root"))}</a>`];
  for (const part of parts) {
    acc = joinPath(acc, part);
    crumbs.push(`<a href="#" data-goto="${esc(acc)}">${esc(part)}</a>`);
  }
  const bc = root.querySelector("#files-breadcrumb");
  bc.innerHTML = crumbs.join(`<span class="sep">/</span>`);
  bc.querySelectorAll("[data-goto]").forEach((a) => {
    a.addEventListener("click", async (ev) => {
      ev.preventDefault();
      filesState.path = a.dataset.goto;
      filesState.searching = false;
      root.querySelector("#files-search-clear").hidden = true;
      root.querySelector("#files-search-input").value = "";
      root.querySelector("#files-panel").innerHTML = "";
      await loadFilesList(root);
    });
  });
}

function renderFileRows(root, entries, { flatPaths, truncated }) {
  const tbody = root.querySelector("#files-tbody");
  if (!entries.length) {
    tbody.innerHTML = `<tr><td colspan="5" class="empty-state">${esc(flatPaths ? t("files.noSearchResults") : t("files.emptyFolder"))}</td></tr>`;
    return;
  }
  tbody.innerHTML = entries.map((e) => `
    <tr data-path="${esc(e.path)}" data-isdir="${e.isDir}">
      <td><input type="checkbox" data-select="${esc(e.path)}"></td>
      <td>
        <a href="#" class="file-name ${e.isDir ? "is-dir" : ""}" data-open="${esc(e.path)}">${esc(flatPaths ? e.path : e.name)}${e.isDir ? "/" : ""}</a>
      </td>
      <td>${e.isDir ? "—" : formatBytes(e.size)}</td>
      <td>${esc(formatDateTime(e.modTime))}</td>
      <td class="btn-row" style="margin:0">
        ${e.isDir ? "" : `<a class="secondary" href="${api.filesDownloadURL(e.path)}">${esc(t("common.download"))}</a>`}
        <button type="button" class="secondary" data-rename="${esc(e.path)}" data-isdir="${e.isDir}">${esc(t("common.rename"))}</button>
      </td>
    </tr>
  `).join("") + (truncated ? `<tr><td colspan="5" class="empty-state">${esc(t("files.tooManyResults"))}</td></tr>` : "");

  tbody.querySelectorAll("[data-select]").forEach((cb) => {
    cb.addEventListener("change", () => {
      if (cb.checked) filesState.selected.add(cb.dataset.select);
      else filesState.selected.delete(cb.dataset.select);
    });
  });

  tbody.querySelectorAll("[data-open]").forEach((a) => {
    a.addEventListener("click", async (ev) => {
      ev.preventDefault();
      const row = a.closest("tr");
      const path = a.dataset.open;
      if (row.dataset.isdir === "true") {
        filesState.path = path;
        filesState.searching = false;
        root.querySelector("#files-search-clear").hidden = true;
        root.querySelector("#files-search-input").value = "";
        await loadFilesList(root);
      } else {
        await openFilePreview(root, path);
      }
    });
  });

  tbody.querySelectorAll("[data-rename]").forEach((btn) => {
    btn.addEventListener("click", () => renameItem(root, btn.dataset.rename));
  });
}

async function openFilePreview(root, path) {
  const panel = root.querySelector("#files-panel");
  panel.innerHTML = `<div class="service-panel"><p class="loading">${esc(t("common.loading"))}</p></div>`;
  try {
    const { content } = await api.filesReadText(path);
    panel.innerHTML = `
      <div class="service-panel">
        <div class="panel-header"><strong>${esc(basename(path))}</strong><button type="button" data-panel-close>${esc(t("files.close"))}</button></div>
        <textarea class="file-editor">${esc(content)}</textarea>
        <div class="btn-row" style="margin-top:8px">
          <button type="button" data-save-text>${esc(t("files.saveText"))}</button>
          <a class="secondary" href="${api.filesDownloadURL(path)}">${esc(t("files.downloadOriginal"))}</a>
        </div>
        <div id="file-editor-msg"></div>
      </div>
    `;
    panel.querySelector("[data-panel-close]").addEventListener("click", () => { panel.innerHTML = ""; });
    panel.querySelector("[data-save-text]").addEventListener("click", async () => {
      const newContent = panel.querySelector(".file-editor").value;
      try {
        await api.filesWriteText(path, newContent);
        panel.querySelector("#file-editor-msg").innerHTML = msg("ok", t("files.saved"));
      } catch (err) {
        panel.querySelector("#file-editor-msg").innerHTML = msg("error", translateError(err.message));
      }
    });
  } catch (err) {
    panel.innerHTML = `
      <div class="service-panel">
        <div class="panel-header"><strong>${esc(basename(path))}</strong><button type="button" data-panel-close>${esc(t("files.close"))}</button></div>
        <p class="hint">${esc(t("files.previewUnavailable", { msg: err.message }))}</p>
        <div class="btn-row"><a href="${api.filesDownloadURL(path)}">${esc(t("files.downloadFile"))}</a></div>
      </div>
    `;
    panel.querySelector("[data-panel-close]").addEventListener("click", () => { panel.innerHTML = ""; });
  }
}

async function renameItem(root, path) {
  const oldName = basename(path);
  const newName = prompt(t("files.renamePrompt"), oldName);
  if (!newName || newName === oldName) return;
  try {
    await api.filesMove(path, joinPath(dirname(path), newName));
    await loadFilesList(root);
  } catch (err) {
    showFilesMsg(root, "error", translateError(err.message));
  }
}

async function moveOrCopySelected(root, kind) {
  if (filesState.selected.size === 0) {
    showFilesMsg(root, "warn", t("files.selectItemsFirst"));
    return;
  }
  const dest = prompt(kind === "move" ? t("files.moveDestPrompt") : t("files.copyDestPrompt"), filesState.path);
  if (dest === null) return;
  const op = kind === "move" ? api.filesMove : api.filesCopy;
  const errors = [];
  for (const path of filesState.selected) {
    try {
      await op(path, joinPath(dest, basename(path)));
    } catch (err) {
      errors.push(`${basename(path)}: ${translateError(err.message)}`);
    }
  }
  if (errors.length) showFilesMsg(root, "error", errors.join("；"));
  else showFilesMsg(root, "ok", kind === "move" ? t("files.moveDone") : t("files.copyDone"));
  await loadFilesList(root);
}

function downloadSelected(root) {
  if (filesState.selected.size === 0) {
    showFilesMsg(root, "warn", t("files.selectItemsToDownload"));
    return;
  }
  const rows = root.querySelectorAll("#files-tbody tr[data-path]");
  const isDir = new Map();
  rows.forEach((r) => isDir.set(r.dataset.path, r.dataset.isdir === "true"));

  let delay = 0;
  for (const path of filesState.selected) {
    const url = isDir.get(path) ? api.filesDownloadZipURL(path) : api.filesDownloadURL(path);
    setTimeout(() => {
      const a = document.createElement("a");
      a.href = url;
      document.body.appendChild(a);
      a.click();
      a.remove();
    }, delay);
    delay += 400; // 瀏覽器對「一次觸發好幾個下載」通常有防護，錯開觸發時間比較不會被擋。
  }
}

async function deleteSelected(root) {
  if (filesState.selected.size === 0) {
    showFilesMsg(root, "warn", t("files.selectItemsToDelete"));
    return;
  }
  if (!confirm(t("files.deleteConfirm", { n: filesState.selected.size }))) return;
  const errors = [];
  for (const path of filesState.selected) {
    try {
      await api.filesDelete(path, false);
    } catch (err) {
      errors.push(`${basename(path)}: ${translateError(err.message)}`);
    }
  }
  if (errors.length) showFilesMsg(root, "error", errors.join("；"));
  else showFilesMsg(root, "ok", t("files.deleteDone"));
  await loadFilesList(root);
  await loadTrash(root);
}

async function uploadFiles(root, fileList) {
  const formData = new FormData();
  for (const f of fileList) formData.append("file", f);

  showFilesMsg(root, "ok", t("files.uploading", { pct: 0 }));
  try {
    await api.filesUpload(filesState.path, formData, (fraction) => {
      showFilesMsg(root, "ok", t("files.uploading", { pct: Math.round(fraction * 100) }));
    });
    showFilesMsg(root, "ok", t("files.uploadDone"));
    await loadFilesList(root);
  } catch (err) {
    showFilesMsg(root, "error", t("files.uploadFailed", { msg: err.message }));
  }
}

async function loadTrash(root) {
  const list = root.querySelector("#trash-list");
  try {
    const trash = await api.filesTrashList();
    if (!trash.length) {
      list.innerHTML = `<p class="empty-state">${esc(t("files.trashEmpty"))}</p>`;
      return;
    }
    list.innerHTML = trash.map((t2) => `
      <div class="rule-row" data-trash-id="${esc(t2.id)}">
        <div class="rule-main">
          <div>
            <div class="rule-name">${esc(t2.name)}${t2.isDir ? "/" : ""}</div>
            <div class="rule-cond">${esc(t("files.trashOriginalPath", { path: t2.originalPath, time: formatDateTime(t2.deletedAt) }))}${t2.isDir ? "" : " · " + esc(formatBytes(t2.size))}</div>
          </div>
        </div>
        <div class="btn-row" style="margin:0">
          <button type="button" class="secondary" data-restore="${esc(t2.id)}">${esc(t("files.restore"))}</button>
          <button type="button" class="danger" data-purge="${esc(t2.id)}">${esc(t("files.purge"))}</button>
        </div>
      </div>
    `).join("");

    list.querySelectorAll("[data-restore]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        try {
          await api.filesTrashRestore(btn.dataset.restore);
          await loadTrash(root);
          await loadFilesList(root);
        } catch (err) {
          root.querySelector("#trash-msg").innerHTML = msg("error", translateError(err.message));
        }
      });
    });
    list.querySelectorAll("[data-purge]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        if (!confirm(t("files.purgeConfirm"))) return;
        try {
          await api.filesTrashDeleteItem(btn.dataset.purge);
          await loadTrash(root);
        } catch (err) {
          root.querySelector("#trash-msg").innerHTML = msg("error", translateError(err.message));
        }
      });
    });
  } catch (err) {
    list.innerHTML = msg("error", translateError(err.message));
  }
}

// ---------- 應用程式 ----------

// reRenderAppsKeepingScroll 重畫應用頁,但保留捲動位置——第六十輪 UI 複審:
// 容器開關/移除/鏡像清理等動作都會整頁重畫,原本每次都把長頁面跳回最上面。
// 無法完全避免重畫(面板也會被重建),但至少不要讓捲動位置亂跳。
async function reRenderAppsKeepingScroll(el) {
  const y = window.scrollY;
  await renderApps(el);
  window.scrollTo(0, y);
}

// containerStatePill 把 docker 容器状态渲染成状态灯。running=绿;exited/dead/
// created=红(已停止);paused/restarting 等过渡态=黄(不当成"已停止");未知=灰。
function containerStatePill(st) {
  if (st === "running") return `<span class="pill ok">${esc(t("apps.stateRunning"))}</span>`;
  if (!st) return `<span class="pill neutral">${esc(t("apps.stateUnknown"))}</span>`;
  if (st === "exited" || st === "dead" || st === "created") return `<span class="pill danger">${esc(t("apps.stateStopped"))}</span>`;
  // paused / restarting / removing 等:黄灯 + 显示原始状态词。
  return `<span class="pill warn">${esc(st)}</span>`;
}

// pollAppOp 輪詢安裝/更新進度,把 docker 拉取進度顯示到 box,直到完成或失敗。
// 完成回 true、失敗回 false(呼叫端據此顯示成功/失敗並重整)。第六十輪產品
// 覆核:安裝/更新改成背景執行,這裡讓使用者看到「正在拉取 xxx」而不是空等。
async function pollAppOp(box, labelKey) {
  for (;;) {
    await new Promise((r) => setTimeout(r, 1000));
    let st;
    try {
      st = await api.appOpStatus();
    } catch {
      continue; // 暫時讀不到就再試
    }
    if (st.stage === "running") {
      const detail = st.service ? `${st.service}: ${st.progress || ""}` : (st.progress || "");
      if (box) box.innerHTML = msg("warn", t(labelKey) + (detail ? " — " + esc(detail) : ""));
      continue;
    }
    return st; // done / failed / idle — 呼叫端據此在重整後顯示結果
  }
}

async function renderApps(el) {
  const [installed, catalog, dockerStatus, containers, arrayStatus, images, catalogSource, registryConfig] = await Promise.all([
    api.installedApps().catch(() => []), api.catalog().catch(() => []),
    api.dockerPing().catch((e) => ({ available: false, error: e.message })),
    api.containers().catch(() => []),
    api.arrayStatus().catch(() => ({})),
    api.images().catch(() => []),
    api.catalogSource().catch(() => ({ url: "", remoteCount: 0 })),
    api.registryConfig().catch(() => ({ registryMirrors: [], insecureRegistries: [] })),
  ]);
  // 容器目前狀態(id -> "running"/"exited"/…),給每個服務顯示狀態燈與決定
  // 啟動/停止按鈕怎麼呈現。ListContainers 回的是完整 64 字元 id,跟安裝時
  // 存下的 containerIds 一致,直接對得起來。
  // 注意:容器清单来自 docker.Container,JSON 字段是大写开头(Id/State/Ports/
  // Labels/Names/Image),与 InstallResult.containerIds(小写)不同,别搞混。
  const stateById = {};
  const portById = {};
  (containers || []).forEach((c) => {
    stateById[c.Id] = c.State;
    // 找出这个容器对外发布的第一个 TCP 端口,用来生成「打开」链接。
    const pub = (c.Ports || []).find((p) => p.PublicPort && (p.Type === "tcp" || !p.Type));
    if (pub) portById[c.Id] = pub.PublicPort;
  });
  // 非 GoNAS 应用商店安装的独立容器(CLI/compose/Portainer 建的):没有
  // com.gonas.app 标签。单独列出让用户也能管理(第六十轮产品复审)。
  const otherContainers = (containers || []).filter((c) => !(c.Labels && c.Labels["com.gonas.app"]));
  // 預填 appdata 路徑用的基準:優先用陣列掛載點,沒有就退回 /mnt/tank。
  const appdataBase = (arrayStatus && arrayStatus.mountPoint) || "/mnt/tank";

  el.innerHTML = `
    <h1>${esc(t("apps.title"))}</h1>
    <p class="page-subtitle">${esc(t("apps.subtitle"))}</p>
    ${!dockerStatus.available ? msg("warn", t("apps.dockerWarn", { reason: translateError(dockerStatus.error) || "" })) + `<div class="btn-row" style="margin:-4px 0 10px"><a href="#/doctor" class="secondary">${esc(t("doctor.dashButton"))}</a></div>` : ""}

    <div class="card">
      ${h2i("box", esc(t("apps.installed", { n: installed.length })))}
      ${installed.length ? installed.map((app) => `
        <div class="app-card">
          <div class="app-card-main">
            <div>
              <h3>${esc(app.template.name)}</h3>
              <p>${esc(translateNotice(app.template.description || ""))}</p>
            </div>
            <div class="btn-row" style="margin:0">
              <button class="secondary" data-edit-app="${esc(app.template.id)}">${esc(t("apps.edit"))}</button>
              <button class="secondary" data-update="${esc(app.template.id)}">${esc(t("apps.update"))}</button>
              <button class="danger" data-uninstall="${esc(app.template.id)}">${esc(t("apps.uninstall"))}</button>
            </div>
          </div>
          <form class="install-form" id="edit-form-${esc(app.template.id)}" data-edit="${esc(app.template.id)}">
            <div class="install-msg"></div>
            ${renderEditFields(app)}
            <div class="btn-row"><button type="submit">${esc(t("apps.saveChanges"))}</button></div>
          </form>
          <div class="services">
            ${Object.entries(app.result.containerIds || {}).map(([svc, id]) => {
              const st = stateById[id];
              const running = st === "running";
              const statePill = containerStatePill(st);
              const port = portById[id];
              const openLink = running && port
                ? `<a class="btnlink-sm" href="http://${location.hostname}:${port}" target="_blank" rel="noopener">${esc(t("apps.open"))}</a>`
                : "";
              return `
              <div class="service-row">
                <span>${esc(svc)}: ${esc(id.slice(0, 12))} ${statePill}</span>
                ${openLink}
                ${running
                  ? `<button type="button" data-ctr-stop="${esc(id)}">${esc(t("apps.stop"))}</button>
                     <button type="button" data-ctr-restart="${esc(id)}">${esc(t("apps.restart"))}</button>`
                  : `<button type="button" data-ctr-start="${esc(id)}">${esc(t("apps.start"))}</button>`}
                <button type="button" data-logs-toggle="${esc(id)}">${esc(t("apps.viewLogs"))}</button>
                <button type="button" data-stats-toggle="${esc(id)}">${esc(t("apps.stats"))}</button>
                <button type="button" data-exec-toggle="${esc(id)}">${esc(t("apps.execCmd"))}</button>
              </div>
              <div class="service-panel" id="panel-${esc(id)}" hidden></div>
            `;}).join("")}
          </div>
        </div>
      `).join("") : `<p class="empty-state">${esc(t("apps.noneInstalled"))}</p>`}
    </div>

    ${dockerStatus.available && otherContainers.length ? `
    <div class="card">
      ${h2i("box", esc(t("apps.otherContainers", { n: otherContainers.length })))}
      <p class="hint">${esc(t("apps.otherContainersHint"))}</p>
      ${otherContainers.map((c) => {
        const id = c.Id;
        const running = c.State === "running";
        const name = (c.Names && c.Names[0] ? c.Names[0].replace(/^\//, "") : id.slice(0, 12));
        const port = portById[id];
        const statePill = containerStatePill(c.State);
        const openLink = running && port ? `<a class="btnlink-sm" href="http://${location.hostname}:${port}" target="_blank" rel="noopener">${esc(t("apps.open"))}</a>` : "";
        return `
          <div class="service-row">
            <span>${esc(name)} <span style="color:var(--text-faint)">${esc(c.Image || "")}</span> ${statePill}</span>
            ${openLink}
            ${running
              ? `<button type="button" data-ctr-stop="${esc(id)}">${esc(t("apps.stop"))}</button>
                 <button type="button" data-ctr-restart="${esc(id)}">${esc(t("apps.restart"))}</button>`
              : `<button type="button" data-ctr-start="${esc(id)}">${esc(t("apps.start"))}</button>`}
            <button type="button" data-logs-toggle="${esc(id)}">${esc(t("apps.viewLogs"))}</button>
            <button type="button" data-stats-toggle="${esc(id)}">${esc(t("apps.stats"))}</button>
            <button type="button" data-ctr-remove="${esc(id)}" class="danger">${esc(t("apps.removeContainer"))}</button>
          </div>
          <div class="service-panel" id="panel-${esc(id)}" hidden></div>`;
      }).join("")}
    </div>` : ""}

    ${dockerStatus.available && (images && images.length) ? `
    <div class="card">
      ${h2i("box", esc(t("apps.images", { n: images.length })))}
      <p class="hint">${esc(t("apps.imagesHint"))}</p>
      <div id="images-msg"></div>
      <div class="btn-row" style="margin:0 0 10px"><button type="button" id="prune-images" class="secondary">${esc(t("apps.pruneImages"))}</button></div>
      ${images.map((im) => {
        const tag = (im.RepoTags && im.RepoTags.length && im.RepoTags[0] !== "<none>:<none>") ? im.RepoTags[0] : (im.Id || "").replace(/^sha256:/, "").slice(0, 12);
        return `
          <div class="service-row">
            <span><code>${esc(tag)}</code> <span style="color:var(--text-faint)">${esc(formatBytes(im.Size || 0))}</span></span>
            <button type="button" data-img-remove="${esc(im.Id)}" class="danger">${esc(t("apps.removeImage"))}</button>
          </div>`;
      }).join("")}
    </div>` : ""}

    ${renderRegistryConfigCard(registryConfig)}

    <div class="card">
      ${h2i("grid", esc(t("apps.catalog")))}
      ${renderCatalogSource(catalogSource)}
      ${catalog.map((tmpl) => renderCatalogEntry(tmpl, appdataBase)).join("")}
    </div>

    <div class="card">
      ${h2i("plus", esc(t("apps.customInstall")))}
      <p class="hint">${esc(t("apps.customInstallHint"))}</p>
      <div id="custom-install-msg"></div>
      <form class="stacked" id="custom-install-form">
        <div class="field"><label>${esc(t("apps.appId"))}</label><input type="text" name="id" pattern="[a-z0-9][a-z0-9\\-]*" placeholder="my-app" required></div>
        <div class="field"><label>${esc(t("apps.name"))}</label><input type="text" name="name" required></div>
        <div class="field"><label>${esc(t("apps.description"))}</label><input type="text" name="description"></div>
        <div id="custom-services">${customServiceBlock(0)}</div>
        <div class="btn-row" style="margin-top:4px"><button type="button" id="add-service" class="secondary">${esc(t("apps.addService"))}</button></div>
        <div class="btn-row"><button type="submit">${esc(t("apps.install"))}</button></div>
      </form>
    </div>
  `;

  el.querySelectorAll("[data-uninstall]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("apps.uninstallConfirm", { name: btn.closest(".app-card").querySelector("h3").textContent }))) return;
      try {
        await api.uninstallApp(btn.dataset.uninstall);
        await renderApps(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });

  el.querySelectorAll("[data-update]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const name = btn.closest(".app-card").querySelector("h3").textContent;
      if (!confirm(t("apps.updateConfirm", { name }))) return;
      btn.disabled = true;
      btn.textContent = t("apps.updating");
      // 顶部放一个会随进度更新的提示框;后台拉镜像时显示拉取进度。
      el.insertAdjacentHTML("afterbegin", `<div id="app-op-msg">${msg("warn", t("apps.updatingLong", { name }))}</div>`);
      const box = el.querySelector("#app-op-msg");
      try {
        await api.updateApp(btn.dataset.update); // 202,立即返回
        const st = await pollAppOp(box, "apps.updatingLong");
        await reRenderAppsKeepingScroll(el);
        const done = st.stage !== "failed";
        el.insertAdjacentHTML("afterbegin", done
          ? msg("ok", t("apps.updateOk", { name }))
          : msg("error", translateError(st.error || "")));
      } catch (err) {
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  // 「编辑」:展开/收起该应用的编辑表单。
  el.querySelectorAll("[data-edit-app]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const form = el.querySelector(`#edit-form-${CSS.escape(btn.dataset.editApp)}`);
      if (form) form.classList.toggle("open");
    });
  });
  // 编辑表单提交:用新 overrides 重建(与安装同一套字段解析 + 进度轮询)。
  el.querySelectorAll("form[data-edit]").forEach((form) => {
    form.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const id = form.dataset.edit;
      const f = new FormData(form);
      const overrides = {};
      const ensure = (svc) => (overrides[svc] = overrides[svc] || { env: {}, volumeHostPaths: {}, portHostOverrides: {} });
      for (const [key, value] of f.entries()) {
        const im = key.match(/^(.+?)\.image$/);
        if (im) { ensure(im[1]); if (value && value.trim()) overrides[im[1]].image = value.trim(); continue; }
        const m = key.match(/^(.+?)\.(env|volume|port)\.(.+)$/);
        if (!m) continue;
        const [, svc, kind, name] = m;
        ensure(svc);
        if (kind === "env" && value) overrides[svc].env[name] = value;
        if (kind === "volume" && value) overrides[svc].volumeHostPaths[name] = value;
        if (kind === "port" && value) overrides[svc].portHostOverrides[Number(name)] = Number(value);
      }
      const box = form.querySelector(".install-msg");
      if (!confirm(t("apps.editConfirm"))) return;
      try {
        box.innerHTML = msg("warn", t("apps.updating"));
        await api.editApp(id, overrides); // 202,背景重建
        const st = await pollAppOp(box, "apps.updating");
        await reRenderAppsKeepingScroll(el);
        el.insertAdjacentHTML("afterbegin", st.stage === "failed"
          ? msg("error", translateError(st.error || ""))
          : msg("ok", t("apps.editOk")));
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  el.querySelectorAll("[data-logs-toggle]").forEach((btn) => {
    btn.addEventListener("click", () => showContainerLogsPanel(el, btn.dataset.logsToggle));
  });
  el.querySelectorAll("[data-exec-toggle]").forEach((btn) => {
    btn.addEventListener("click", () => showContainerExecPanel(el, btn.dataset.execToggle));
  });
  el.querySelectorAll("[data-stats-toggle]").forEach((btn) => {
    btn.addEventListener("click", () => showContainerStatsPanel(el, btn.dataset.statsToggle));
  });

  // 第六十輪:容器啟動/停止/重啟。按完重畫整頁,狀態燈與按鈕跟著更新。
  const wireCtrAction = (attr, call, busyKey) => {
    el.querySelectorAll(`[${attr}]`).forEach((btn) => {
      btn.addEventListener("click", async () => {
        const label = btn.textContent; // 失敗時要還原,否則按鈕卡在「…中…」
        btn.disabled = true;
        btn.textContent = t(busyKey);
        try {
          await call(btn.getAttribute(attr));
          await reRenderAppsKeepingScroll(el);
        } catch (err) {
          btn.disabled = false;
          btn.textContent = label;
          el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
        }
      });
    });
  };
  wireCtrAction("data-ctr-start", (id) => api.containerStart(id), "apps.starting");
  wireCtrAction("data-ctr-stop", (id) => api.containerStop(id), "apps.stopping");
  wireCtrAction("data-ctr-restart", (id) => api.containerRestart(id), "apps.restarting");
  // 镜像:清理未使用 + 单个移除。
  const pruneBtn = el.querySelector("#prune-images");
  if (pruneBtn) {
    pruneBtn.addEventListener("click", async () => {
      if (!confirm(t("apps.pruneImagesConfirm"))) return;
      pruneBtn.disabled = true;
      try {
        const res = await api.pruneImages();
        // 先重畫(會清掉整頁),再把成功訊息放上去——否則訊息會被緊接著的
        // 重畫一起抹掉(第六十輪 UI 複審)。
        await reRenderAppsKeepingScroll(el);
        const box2 = el.querySelector("#images-msg");
        if (box2) box2.innerHTML = msg("ok", t("apps.pruneImagesOk", { size: formatBytes(res.spaceReclaimed || 0) }));
      } catch (err) {
        pruneBtn.disabled = false;
        const box = el.querySelector("#images-msg");
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }
  el.querySelectorAll("[data-img-remove]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("apps.removeImageConfirm"))) return;
      btn.disabled = true;
      try {
        await api.removeImage(btn.dataset.imgRemove);
        await reRenderAppsKeepingScroll(el);
      } catch (err) {
        btn.disabled = false;
        const box = el.querySelector("#images-msg");
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  // 移除独立容器(带确认,破坏性)。
  el.querySelectorAll("[data-ctr-remove]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("apps.removeContainerConfirm"))) return;
      btn.disabled = true;
      try {
        await api.containerRemove(btn.dataset.ctrRemove);
        await reRenderAppsKeepingScroll(el);
      } catch (err) {
        btn.disabled = false;
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });

  el.querySelectorAll("[data-toggle-install]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const form = el.querySelector(`#install-form-${btn.dataset.toggleInstall}`);
      form.classList.toggle("open");
    });
  });

  el.querySelectorAll("form[data-install]").forEach((form) => {
    form.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(form);
      const templateId = form.dataset.install;
      const overrides = {};
      const ensure = (svc) => (overrides[svc] = overrides[svc] || { env: {}, volumeHostPaths: {}, portHostOverrides: {} });
      for (const [key, value] of f.entries()) {
        const im = key.match(/^(.+?)\.image$/);
        if (im) { ensure(im[1]); if (value && value.trim()) overrides[im[1]].image = value.trim(); continue; }
        const m = key.match(/^(.+?)\.(env|volume|port)\.(.+)$/);
        if (!m) continue;
        const [, svc, kind, name] = m;
        ensure(svc);
        if (kind === "env" && value) overrides[svc].env[name] = value;
        if (kind === "volume" && value) overrides[svc].volumeHostPaths[name] = value;
        if (kind === "port" && value) overrides[svc].portHostOverrides[Number(name)] = Number(value);
      }
      const box = form.querySelector(".install-msg");
      try {
        box.innerHTML = msg("warn", t("apps.installing"));
        await api.installApp(templateId, overrides); // 202,背景安裝
        const st = await pollAppOp(box, "apps.installing");
        if (st.stage === "failed") {
          box.innerHTML = msg("error", translateError(st.error || ""));
          return;
        }
        box.innerHTML = msg("ok", t("apps.installSuccess"));
        setTimeout(() => reRenderAppsKeepingScroll(el), 600);
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  // 遠端 App 目錄:儲存網址(同步抓一次)與手動重新整理。兩者都用整頁重畫
  // 來反映新的目錄內容(抓回來的範本會直接出現在下方清單)。
  const registryForm = el.querySelector("#registry-cfg-form");
  if (registryForm) {
    const rBox = el.querySelector("#registry-cfg-msg");
    registryForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const split = (s) => (s || "").split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
      const btn = ev.target.querySelector('button[type="submit"]');
      btn.disabled = true;
      rBox.innerHTML = msg("warn", t("apps.registrySaving"));
      try {
        const res = await api.setRegistryConfig(split(f.get("mirrors")), split(f.get("insecure")));
        rBox.innerHTML = res.applied
          ? msg("ok", t("apps.registrySaved"))
          : msg("warn", translateNotice(res.warning) || t("apps.registrySavedNoReload"));
      } catch (err) {
        rBox.innerHTML = msg("error", translateError(err.message));
      } finally {
        btn.disabled = false;
      }
    });
  }

  const sourceForm = el.querySelector("#catalog-source-form");
  if (sourceForm) {
    const srcBox = el.querySelector("#catalog-source-msg");
    sourceForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const url = new FormData(ev.target).get("url").trim();
      const btn = ev.target.querySelector('button[type="submit"]');
      btn.disabled = true;
      srcBox.innerHTML = msg("warn", t("apps.catalogSaving"));
      try {
        await api.setCatalogSource(url);
        await renderApps(el);
      } catch (err) {
        btn.disabled = false;
        srcBox.innerHTML = msg("error", translateError(err.message));
      }
    });
    const refreshBtn = el.querySelector("#catalog-refresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", async () => {
        refreshBtn.disabled = true;
        refreshBtn.textContent = t("apps.catalogRefreshing");
        try {
          await api.refreshCatalog();
          await renderApps(el);
        } catch (err) {
          refreshBtn.disabled = false;
          refreshBtn.textContent = t("apps.catalogRefresh");
          srcBox.innerHTML = msg("error", translateError(err.message));
        }
      });
    }
  }

  const customForm = el.querySelector("#custom-install-form");
  if (customForm) {
    // 「+ 新增服務」:再加一個服務欄位塊(多服務自定义安装)。
    let svcCount = 1;
    const addBtn = el.querySelector("#add-service");
    const servicesBox = el.querySelector("#custom-services");
    if (addBtn && servicesBox) {
      addBtn.addEventListener("click", () => {
        servicesBox.insertAdjacentHTML("beforeend", customServiceBlock(svcCount));
        svcCount++;
        associateLabels(servicesBox);
      });
      servicesBox.addEventListener("click", (ev) => {
        const rm = ev.target.closest(".remove-service");
        if (rm) rm.closest(".custom-service").remove();
      });
    }
    customForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(customForm);
      const box = el.querySelector("#custom-install-msg");
      let template;
      try {
        const services = [...customForm.querySelectorAll(".custom-service")].map((blk) => ({
          name: blk.querySelector(".svc-name").value.trim(),
          image: blk.querySelector(".svc-image").value.trim(),
          ports: parsePortLines(blk.querySelector(".svc-ports").value),
          volumes: parseVolumeLines(blk.querySelector(".svc-volumes").value),
          env: parseEnvLines(blk.querySelector(".svc-env").value),
        }));
        template = {
          id: f.get("id").trim(),
          name: f.get("name").trim(),
          description: f.get("description").trim(),
          services,
        };
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
        return;
      }
      try {
        box.innerHTML = msg("warn", t("apps.installing"));
        await api.installCustomApp(template); // 202,背景安裝
        const st = await pollAppOp(box, "apps.installing");
        if (st.stage === "failed") {
          box.innerHTML = msg("error", translateError(st.error || ""));
          return;
        }
        box.innerHTML = msg("ok", t("apps.installSuccess"));
        setTimeout(() => reRenderAppsKeepingScroll(el), 600);
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }
}

// showContainerLogsPanel/showContainerExecPanel 把「查看 Log」/「執行指令」
// 兩個按鈕的內容渲染進同一個 <div class="service-panel"> 裡 —— 每個容器
// 共用一個面板,再按哪個按鈕就切換面板顯示的內容,而不是同時開兩個面板
// 佔掉版面。面板本身刻意做成「按需載入」而不是列表一渲染就全部抓一輪 log
// ——已安裝的服務可能同時有好幾個容器,沒必要每次進到應用程式頁面就對
// 全部容器各打一次 log API。
async function showContainerLogsPanel(el, containerID) {
  const panel = el.querySelector(`#panel-${cssEscape(containerID)}`);
  if (!panel) return;
  panel.hidden = false;
  // 第六十轮:加 tail 选择 + 刷新,不用关掉再打开才能看新日志(产品/UI 复审)。
  panel.innerHTML = `
    <div class="panel-header">
      <strong>${esc(t("apps.logsTitle"))}</strong>
      <span class="panel-tools">
        <select data-log-tail>
          <option value="200">200</option>
          <option value="500">500</option>
          <option value="1000">1000</option>
          <option value="2000">2000</option>
        </select>
        <button type="button" data-log-refresh>${esc(t("apps.refresh"))}</button>
        <button type="button" data-panel-close>${esc(t("common.close"))}</button>
      </span>
    </div>
    <pre class="log-output">${esc(t("common.loading"))}</pre>`;
  wirePanelClose(panel);
  const out = panel.querySelector(".log-output");
  const tailSel = panel.querySelector("[data-log-tail]");
  const load = async () => {
    out.textContent = t("common.loading");
    try {
      const { logs } = await api.containerLogs(containerID, tailSel.value);
      out.textContent = logs || t("apps.logsEmpty");
    } catch (err) {
      out.textContent = t("apps.logsFailed", { msg: translateError(err.message) });
    }
  };
  panel.querySelector("[data-log-refresh]").addEventListener("click", load);
  tailSel.addEventListener("change", load);
  await load();
}

// showContainerStatsPanel 显示单个容器的一次性资源用量(CPU%/内存),带刷新。
async function showContainerStatsPanel(el, containerID) {
  const panel = el.querySelector(`#panel-${cssEscape(containerID)}`);
  if (!panel) return;
  panel.hidden = false;
  panel.innerHTML = `
    <div class="panel-header">
      <strong>${esc(t("apps.statsTitle"))}</strong>
      <span class="panel-tools">
        <button type="button" data-stats-refresh>${esc(t("apps.refresh"))}</button>
        <button type="button" data-panel-close>${esc(t("common.close"))}</button>
      </span>
    </div>
    <div class="stats-body">${esc(t("common.loading"))}</div>`;
  wirePanelClose(panel);
  const body = panel.querySelector(".stats-body");
  const load = async () => {
    body.textContent = t("common.loading");
    try {
      const s = await api.containerStats(containerID);
      const memLine = s.memoryLimit
        ? `${formatBytes(s.memoryBytes)} / ${formatBytes(s.memoryLimit)} (${s.memPercent.toFixed(1)}%)`
        : formatBytes(s.memoryBytes);
      body.innerHTML = `
        <div class="stat-line"><span>${esc(t("apps.statCpu"))}</span><strong>${s.cpuPercent.toFixed(1)}%</strong></div>
        <div class="stat-line"><span>${esc(t("apps.statMem"))}</span><strong>${esc(memLine)}</strong></div>`;
    } catch (err) {
      body.textContent = t("apps.statsFailed", { msg: translateError(err.message) });
    }
  };
  panel.querySelector("[data-stats-refresh]").addEventListener("click", load);
  await load();
}

function showContainerExecPanel(el, containerID) {
  const panel = el.querySelector(`#panel-${cssEscape(containerID)}`);
  if (!panel) return;
  panel.hidden = false;
  panel.innerHTML = `
    <div class="panel-header"><strong>${esc(t("apps.execTitle"))}</strong><button type="button" data-panel-close>${esc(t("common.close"))}</button></div>
    <p class="hint">${t("apps.execHint")}</p>
    <form class="exec-form">
      <input type="text" name="cmd" placeholder="ls -la /data" required>
      <button type="submit">${esc(t("apps.execRun"))}</button>
    </form>
    <pre class="log-output" hidden></pre>
  `;
  wirePanelClose(panel);
  const form = panel.querySelector(".exec-form");
  const output = panel.querySelector(".log-output");
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const raw = new FormData(form).get("cmd").trim();
    if (!raw) return;
    output.hidden = false;
    output.textContent = t("apps.execRunning");
    try {
      const result = await api.containerExec(containerID, tokenizeCommand(raw));
      output.textContent = t("apps.execResult", { code: result.exitCode, output: result.output || t("apps.execEmptyOutput") });
    } catch (err) {
      output.textContent = t("apps.execFailed", { msg: translateError(err.message) });
    }
  });
}

// tokenizeCommand 把一行指令拆成 argv,支援单/双引号,让 `sh -c "echo hi"`
// 这类带引号的参数不会被空白硬拆坏(第六十轮复审)。
function tokenizeCommand(raw) {
  const out = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let m;
  while ((m = re.exec(raw)) !== null) {
    out.push(m[1] !== undefined ? m[1] : m[2] !== undefined ? m[2] : m[3]);
  }
  return out;
}

function wirePanelClose(panel) {
  panel.querySelector("[data-panel-close]").addEventListener("click", () => {
    panel.hidden = true;
    panel.innerHTML = "";
  });
}

// cssEscape 讓容器 ID(64 碼十六進位字串,理論上不會有特殊字元,但這裡
// 保守處理)可以安全地當成 CSS ID selector 使用。
function cssEscape(id) {
  return (window.CSS && CSS.escape) ? CSS.escape(id) : id.replace(/[^a-zA-Z0-9_-]/g, "");
}

// parsePortLines/parseVolumeLines/parseEnvLines 把自訂安裝表單裡的簡易
// 文字格式(每行一個)翻譯成後端 appstore.AppTemplate 期待的結構化欄位
// ——刻意不做成一排一排可以動態新增/刪除的欄位(常見的 Docker 管理介面
// 作法),用純文字多行輸入换取實作/維護成本低很多,對「一次裝一個
// 容器、填個幾條就好」的情境已經足夠,之後真的有需要再改成動態表單。
function parsePortLines(text) {
  return (text || "").split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
    const m = line.match(/^(\d+):(\d+)(\/(tcp|udp))?$/i);
    if (!m) throw new Error(t("apps.portFormatError", { line }));
    return { hostPort: Number(m[1]), containerPort: Number(m[2]), protocol: (m[4] || "").toLowerCase() || undefined };
  });
}

function parseVolumeLines(text) {
  return (text || "").split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
    const parts = line.split(":");
    if (parts.length < 2 || parts.length > 3) {
      throw new Error(t("apps.volumeFormatError", { line }));
    }
    const readOnly = parts[2] === "ro";
    if (parts.length === 3 && !readOnly) {
      throw new Error(t("apps.volumeFormatErrorRO", { line }));
    }
    return { hostPath: parts[0], containerPath: parts[1], readOnly };
  });
}

function parseEnvLines(text) {
  return (text || "").split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
    const idx = line.indexOf("=");
    if (idx <= 0) throw new Error(t("apps.envFormatError", { line }));
    return { key: line.slice(0, idx), default: line.slice(idx + 1) };
  });
}

// customServiceBlock 画出自定义安装里「一个服务」的字段块(第六十轮:支持
// 多服务自定义安装,例如 app + db)。第 0 个不给移除按钮(至少要有一个)。
function customServiceBlock(idx) {
  const removable = idx > 0;
  return `
    <div class="custom-service" style="border:1px solid var(--border);border-radius:8px;padding:10px;margin:8px 0">
      <div style="display:flex;justify-content:space-between;align-items:center">
        <strong style="font-size:12.5px">${esc(t("apps.service"))} ${idx + 1}</strong>
        ${removable ? `<button type="button" class="secondary remove-service" style="padding:2px 8px">${esc(t("apps.removeService"))}</button>` : ""}
      </div>
      <div class="field"><label>${esc(t("apps.serviceName"))}</label><input type="text" class="svc-name" placeholder="app" value="${idx === 0 ? "app" : ""}" required></div>
      <div class="field"><label>${esc(t("apps.image"))}</label><input type="text" class="svc-image" placeholder="nginx:latest" required></div>
      <div class="field"><label>${esc(t("apps.ports"))}</label><textarea class="svc-ports" rows="2" placeholder="8080:80"></textarea></div>
      <div class="field"><label>${esc(t("apps.volumes"))}</label><textarea class="svc-volumes" rows="2" placeholder="/mnt/tank/appdata/my-app:/data"></textarea></div>
      <div class="field"><label>${esc(t("apps.env"))}</label><textarea class="svc-env" rows="2" placeholder="TZ=Asia/Taipei"></textarea></div>
    </div>`;
}

// renderEditFields 为「编辑已安装应用」生成预填当前值的字段(env/挂载/端口),
// 字段命名与 renderCatalogEntry 一致,这样提交解析可以共用同一套逻辑。预填值
// 来自 app.overrides(用户安装时填的),没有就退回模板默认。
function renderEditFields(app) {
  const tmpl = app.template;
  const ov = app.overrides || {};
  return (tmpl.services || []).map((svc) => {
    const so = ov[svc.name] || {};
    const envVals = so.env || {};
    const volVals = so.volumeHostPaths || {};
    const portVals = so.portHostOverrides || {};
    const imageField = `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(t("apps.image"))}</label>
        <input type="text" name="${esc(svc.name)}.image" value="${esc(so.image || svc.image)}">
        <div class="hint">${esc(t("apps.imageHint"))}</div>
      </div>`;
    const env = (svc.env || []).map((e) => {
      const cur = envVals[e.key] !== undefined ? envVals[e.key] : "";
      return `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(e.key)}${e.required ? esc(t("apps.required")) : ""}</label>
        <input type="${/pass/i.test(e.key) ? "password" : "text"}" name="${esc(svc.name)}.env.${esc(e.key)}" value="${esc(cur)}" placeholder="${esc(e.default || "")}" ${e.required ? "required" : ""}>
      </div>`;
    });
    const vol = (svc.volumes || []).map((v) => {
      const cur = volVals[v.containerPath] !== undefined ? volVals[v.containerPath] : "";
      return `
      <div class="field">
        <label>${t("apps.volumeLabel", { svc: esc(svc.name), path: esc(v.containerPath) })}</label>
        <input type="text" name="${esc(svc.name)}.volume.${esc(v.containerPath)}" value="${esc(cur)}" required>
      </div>`;
    });
    const port = (svc.ports || []).map((p) => {
      const cur = portVals[p.containerPort] !== undefined ? portVals[p.containerPort] : (p.hostPort || "");
      return `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(t("apps.ports"))} (${esc(String(p.containerPort))})</label>
        <input type="number" name="${esc(svc.name)}.port.${esc(String(p.containerPort))}" value="${esc(String(cur))}">
      </div>`;
    });
    return [imageField, ...env, ...vol, ...port].join("");
  }).join("");
}

// renderRegistryConfigCard 畫出「Docker 鏡像加速」設定(可摺疊)。一行一個加速
// 地址 / insecure registry。給 Docker Hub 連不上/很慢的網路(中國)用。
function renderRegistryConfigCard(cfg) {
  cfg = cfg || { registryMirrors: [], insecureRegistries: [] };
  const mirrors = (cfg.registryMirrors || []).join("\n");
  const insecure = (cfg.insecureRegistries || []).join("\n");
  const hasAny = (cfg.registryMirrors || []).length || (cfg.insecureRegistries || []).length;
  return `
    <div class="card">
      <details class="catalog-source" ${hasAny ? "open" : ""}>
        <summary>${esc(t("apps.registryTitle"))}</summary>
        <p class="hint">${esc(t("apps.registryHint"))}</p>
        <div id="registry-cfg-msg"></div>
        <form class="stacked" id="registry-cfg-form" style="margin-top:8px">
          <div class="field">
            <label>${esc(t("apps.registryMirrors"))}</label>
            <textarea name="mirrors" rows="2" placeholder="https://docker.m.daocloud.io">${esc(mirrors)}</textarea>
            <div class="hint">${esc(t("apps.registryMirrorsHint"))}</div>
          </div>
          <div class="field">
            <label>${esc(t("apps.insecureRegistries"))}</label>
            <textarea name="insecure" rows="1" placeholder="192.168.1.10:5000">${esc(insecure)}</textarea>
            <div class="hint">${esc(t("apps.insecureRegistriesHint"))}</div>
          </div>
          <div class="btn-row"><button type="submit">${esc(t("common.save"))}</button></div>
        </form>
      </details>
    </div>`;
}

// renderCatalogSource 畫出「遠端 App 目錄」的設定區:網址輸入 + 儲存 + 重新整理,
// 以及上次抓取的狀態(時間、抓到幾個、錯誤、被跳過的範本數)。摺疊在一個
// <details> 裡,預設只在已設定網址時展開,避免佔據一般使用者的視線。
function renderCatalogSource(src) {
  src = src || { url: "", remoteCount: 0 };
  let status = "";
  if (src.error) {
    status = msg("error", t("apps.catalogSourceError", { err: translateError(src.error) }));
  } else if (src.fetchedAt) {
    status = msg("ok", t("apps.catalogSourceOk", { n: src.remoteCount, time: formatDateTime(src.fetchedAt) }));
  }
  let skipped = "";
  if (src.skipped && src.skipped.length) {
    skipped = msg("warn", t("apps.catalogSkipped", { n: src.skipped.length }) + " " + src.skipped.slice(0, 3).join("; "));
  }
  return `
    <details class="catalog-source" ${src.url ? "open" : ""}>
      <summary>${esc(t("apps.catalogSourceTitle"))}</summary>
      <p class="hint">${esc(t("apps.catalogSourceHint"))}</p>
      <div id="catalog-source-msg">${status}${skipped}</div>
      <form class="stacked" id="catalog-source-form" style="margin-top:8px">
        <div class="field">
          <label>${esc(t("apps.catalogSourceUrl"))}</label>
          <input type="text" name="url" value="${esc(src.url || "")}" placeholder="https://example.com/gonas-catalog.json">
        </div>
        <div class="btn-row">
          <button type="submit">${esc(t("common.save"))}</button>
          <button type="button" id="catalog-refresh" class="secondary"${src.url ? "" : " disabled"}>${esc(t("apps.catalogRefresh"))}</button>
        </div>
      </form>
    </details>
  `;
}

function renderCatalogEntry(tmpl, appdataBase) {
  const base = appdataBase || "/mnt/tank";
  const fields = tmpl.services.flatMap((svc) => {
    // 鏡像位址預填成範本預設,使用者可改成國內鏡像源(例如換掉 registry 前綴)。
    const imageField = `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(t("apps.image"))}</label>
        <input type="text" name="${esc(svc.name)}.image" value="${esc(svc.image)}">
        <div class="hint">${esc(t("apps.imageHint"))}</div>
      </div>`;
    const env = (svc.env || []).map((e) => `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(e.key)}${e.required ? esc(t("apps.required")) : ""}</label>
        <input type="${/pass/i.test(e.key) ? "password" : "text"}" name="${esc(svc.name)}.env.${esc(e.key)}" placeholder="${esc(e.default || "")}" ${e.required ? "required" : ""}>
        ${e.description ? `<div class="hint">${esc(translateNotice(e.description))}</div>` : ""}
      </div>`);
    // 第六十輪:掛載路徑改成「預填實際值」(不是 placeholder),使用者直接
    // 按確認即可裝,不用自己想路徑。多服務 App 每個服務各給一個子目錄,避免
    // 兩個服務的資料互相覆蓋(例如 wordpress 的 db 與 app)。後端安裝時若這個
    // 路徑還不存在會自動建立。
    const subdir = tmpl.services.length > 1 ? tmpl.id + "/" + svc.name : tmpl.id;
    const vol = (svc.volumes || []).map((v) => `
      <div class="field">
        <label>${t("apps.volumeLabel", { svc: esc(svc.name), path: esc(v.containerPath) })}</label>
        <input type="text" name="${esc(svc.name)}.volume.${esc(v.containerPath)}" value="${esc(base + "/appdata/" + subdir)}" required>
        <div class="hint">${esc(t("apps.volumeAutocreateHint"))}</div>
      </div>`);
    return [imageField, ...env, ...vol];
  });

  const sourceBadge = tmpl.source === "remote" ? ` <span class="pill neutral">${esc(t("apps.catalogRemoteBadge"))}</span>` : "";
  return `
    <div class="app-card">
      <div>
        <h3>${esc(tmpl.name)}${sourceBadge}</h3>
        <p>${esc(translateNotice(tmpl.description || ""))}</p>
        <div class="services">${tmpl.services.map((s) => esc(s.image)).join(" · ")}</div>
      </div>
      <button data-toggle-install="${esc(tmpl.id)}">${esc(t("apps.install"))}</button>
    </div>
    <form class="install-form" id="install-form-${esc(tmpl.id)}" data-install="${esc(tmpl.id)}">
      <div class="install-msg"></div>
      ${fields.join("")}
      <div class="btn-row"><button type="submit">${esc(t("apps.confirmInstall"))}</button></div>
    </form>
  `;
}

// ---------- 共享 ----------

async function renderShares(el) {
  const [shares, exportsList] = await Promise.all([api.shares().catch(() => []), api.exports().catch(() => [])]);

  el.innerHTML = `
    <h1>${esc(t("shares.title"))}</h1>
    <p class="page-subtitle">${esc(t("shares.subtitle"))}</p>

    <div class="card">
      ${h2i("share", esc(t("shares.smbShares")))}
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("shares.colName"))}</th><th>${esc(t("shares.colPath"))}</th><th>${esc(t("shares.colReadOnly"))}</th><th>${esc(t("shares.colGuest"))}</th><th></th></tr></thead>
          <tbody>
            ${shares.length ? shares.map((s) => `
              <tr>
                <td>${esc(s.name)}${s.recycle ? ` <span class="pill ok" title="${esc(s.recycleMaxDays ? t("shares.recycleBadgeDays", { n: s.recycleMaxDays }) : t("shares.recycleBadgeForever"))}">${esc(t("shares.recycleBadge"))}</span>` : ""}</td><td><code>${esc(s.path)}</code></td>
                <td>${s.readOnly ? esc(t("shares.yes")) : esc(t("shares.no"))}</td><td>${s.guestOk ? esc(t("shares.yes")) : esc(t("shares.no"))}</td>
                <td><button class="secondary" data-del-share="${esc(s.name)}">${esc(t("common.delete"))}</button></td>
              </tr>`).join("") : `<tr><td colspan="5" class="empty-state">${esc(t("shares.noShares"))}</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="share-msg"></div>
      <form class="stacked" id="share-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("shares.name"))}</label><input type="text" name="name" required></div>
        <div class="field"><label>${esc(t("shares.path"))}</label><input type="text" name="path" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>${esc(t("shares.comment"))}</label><input type="text" name="comment"></div>
        <div class="checkbox-row"><label><input type="checkbox" name="readOnly"> ${esc(t("shares.readOnly"))}</label></div>
        <div class="checkbox-row"><label><input type="checkbox" name="guestOk"> ${esc(t("shares.guestOk"))}</label></div>
        <div class="field"><label>${esc(t("shares.validUsers"))}</label><input type="text" name="validUsers" placeholder="alice, bob"></div>
        <div class="field"><label>${esc(t("shares.writeList"))}</label><input type="text" name="writeList" placeholder="alice"><div class="hint">${esc(t("shares.writeListHint"))}</div></div>
        <div class="field"><label>${esc(t("shares.readList"))}</label><input type="text" name="readList" placeholder="guest"><div class="hint">${esc(t("shares.readListHint"))}</div></div>
        <div class="checkbox-row"><label><input type="checkbox" name="recycle" id="share-recycle"> ${esc(t("shares.recycle"))}</label></div>
        <div class="field" id="share-recycle-days-field" style="display:none"><label>${esc(t("shares.recycleMaxDays"))}</label><input type="number" name="recycleMaxDays" min="0" max="3650" value="30"><div class="hint">${esc(t("shares.recycleMaxDaysHint"))}</div></div>
        <div class="btn-row"><button type="submit">${esc(t("shares.addShare"))}</button></div>
      </form>
    </div>

    <div class="card">
      ${h2i("share", esc(t("shares.nfsExports")))}
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("shares.colPath"))}</th><th>${esc(t("shares.colClientRules"))}</th><th></th></tr></thead>
          <tbody>
            ${exportsList.length ? exportsList.map((e) => `
              <tr><td><code>${esc(e.path)}</code></td><td>${(e.clients || []).map((c) => `${esc(c.cidr || "*")}(${esc((c.options || []).join(","))})`).join(", ")}</td>
              <td><button class="secondary" data-del-export="${esc(e.path)}">${esc(t("common.delete"))}</button></td></tr>
            `).join("") : `<tr><td colspan="3" class="empty-state">${esc(t("shares.noExports"))}</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="export-msg"></div>
      <form class="stacked" id="export-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("shares.path"))}</label><input type="text" name="path" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>${esc(t("shares.cidr"))}</label><input type="text" name="cidr" placeholder="192.168.1.0/24" required></div>
        <div class="field"><label>${esc(t("shares.options"))}</label><input type="text" name="options" value="rw,sync,no_subtree_check" required></div>
        <div class="btn-row"><button type="submit">${esc(t("shares.addExport"))}</button></div>
      </form>
    </div>
  `;

  el.querySelectorAll("[data-del-share]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      // 第五十六輪覆核(產品 P5):刪共享前先確認(跟刪使用者/工作/App 一致)。
      if (!confirm(t("shares.deleteShareConfirm", { name: btn.dataset.delShare }))) return;
      try { await api.deleteShare(btn.dataset.delShare); await renderShares(el); }
      catch (err) { el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message))); }
    });
  });

  el.querySelectorAll("[data-del-export]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("shares.deleteExportConfirm", { path: btn.dataset.delExport }))) return;
      try { await api.deleteExport(btn.dataset.delExport); await renderShares(el); }
      catch (err) { el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message))); }
    });
  });

  // 勾了「回收筒」才顯示保留天數欄位。
  const recycleChk = el.querySelector("#share-recycle");
  if (recycleChk) {
    const daysField = el.querySelector("#share-recycle-days-field");
    const sync = () => { daysField.style.display = recycleChk.checked ? "" : "none"; };
    recycleChk.addEventListener("change", sync);
    sync();
  }

  el.querySelector("#share-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const recycle = f.get("recycle") === "on";
    const share = {
      name: f.get("name").trim(),
      path: f.get("path").trim(),
      comment: f.get("comment").trim(),
      readOnly: f.get("readOnly") === "on",
      guestOk: f.get("guestOk") === "on",
      validUsers: (f.get("validUsers") || "").split(",").map((s) => s.trim()).filter(Boolean),
      writeList: (f.get("writeList") || "").split(",").map((s) => s.trim()).filter(Boolean),
      readList: (f.get("readList") || "").split(",").map((s) => s.trim()).filter(Boolean),
      recycle,
      recycleMaxDays: recycle ? (Number(f.get("recycleMaxDays")) || 0) : 0,
    };
    const box = el.querySelector("#share-msg");
    try {
      const res = await api.createShare(share);
      box.innerHTML = res.applied ? msg("ok", t("shares.shareAdded")) : msg("warn", t("shares.shareAddedWarn", { warn: res.warning || "" }));
      await renderShares(el);
    } catch (err) { box.innerHTML = msg("error", translateError(err.message)); }
  });

  el.querySelector("#export-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const exp = {
      path: f.get("path").trim(),
      clients: [{ cidr: f.get("cidr").trim(), options: f.get("options").split(",").map((s) => s.trim()).filter(Boolean) }],
    };
    const box = el.querySelector("#export-msg");
    try {
      const res = await api.createExport(exp);
      box.innerHTML = res.applied ? msg("ok", t("shares.exportAdded")) : msg("warn", t("shares.exportAddedWarn", { warn: res.warning || "" }));
      await renderShares(el);
    } catch (err) { box.innerHTML = msg("error", translateError(err.message)); }
  });
}

// ---------- 使用者 ----------

async function renderUsers(el) {
  const users = await api.users().catch(() => []);

  el.innerHTML = `
    <h1>${esc(t("users.title"))}</h1>
    <p class="page-subtitle">${esc(t("users.subtitle"))}</p>
    <p class="page-subtitle" style="margin-top:-6px">${t("users.loginAccountsHint")}</p>

    <div class="card">
      ${h2i("users", esc(t("users.accounts", { n: users.length })))}
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("users.colUsername"))}</th><th>${esc(t("users.colComment"))}</th><th></th></tr></thead>
          <tbody>
            ${users.length ? users.map((u) => `
              <tr><td>${esc(u.username)}</td><td>${esc(u.comment || "—")}</td>
              <td><button class="secondary" data-del-user="${esc(u.username)}">${esc(t("common.delete"))}</button></td></tr>
            `).join("") : `<tr><td colspan="3" class="empty-state">${esc(t("users.noUsers"))}</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="user-msg"></div>
      <form class="stacked" id="user-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("users.username"))}</label><input type="text" name="username" placeholder="alice" required></div>
        <div class="field"><label>${esc(t("users.comment"))}</label><input type="text" name="comment" placeholder="Alice Chen"></div>
        <div class="field"><label>${esc(t("users.password"))}</label><input type="password" name="password" required></div>
        <div class="btn-row"><button type="submit">${esc(t("users.createUser"))}</button></div>
      </form>
    </div>
  `;

  el.querySelectorAll("[data-del-user]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("users.deleteConfirm", { name: btn.dataset.delUser }))) return;
      try { await api.deleteUser(btn.dataset.delUser); await renderUsers(el); }
      catch (err) { el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message))); }
    });
  });

  el.querySelector("#user-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#user-msg");
    try {
      const res = await api.createUser({ username: f.get("username").trim(), comment: f.get("comment").trim(), password: f.get("password") });
      box.innerHTML = res.sambaWarning ? msg("warn", t("users.userCreatedWarn", { warn: res.sambaWarning })) : msg("ok", t("users.userCreated"));
      await renderUsers(el);
    } catch (err) { box.innerHTML = msg("error", translateError(err.message)); }
  });
}

// ---------- 監控 ----------

const METRIC_KEYS = {
  cpuPercent: "monitor.metricCpuPercent",
  memPercent: "monitor.metricMemPercent",
  diskPercent: "monitor.metricDiskPercent",
  arrayFailed: "monitor.metricArrayFailed",
  smartFailed: "monitor.metricSmartFailed",
};
const COMPARATOR_KEYS = { ">": "monitor.gt", ">=": "monitor.gte", "<": "monitor.lt", "<=": "monitor.lte" };
const COMPARATOR_SYMBOLS = { ">": ">", ">=": "≥", "<": "<", "<=": "≤" };

function isBooleanMetric(metric) {
  return metric === "arrayFailed" || metric === "smartFailed";
}

// renderUPSCard 畫 UPS(不斷電系統)狀態 + 設定。狀態:查到就顯示市電/電池、
// 電量、預估續航、負載;查不到(NUT 沒裝/沒設定)顯示提示。設定表單只有
// 管理者看得到(啟用、UPS 名稱、市電中斷自動關機、續航門檻)。
function renderUPSCard(status, cfg, names, isAdmin) {
  status = status || { present: false };
  cfg = cfg || {};
  let statusHTML;
  if (!status.present) {
    statusHTML = `<p style="color:var(--text-dim);font-size:12.5px;margin:0">${esc(t("ups.notDetected"))}</p>`;
  } else {
    const pillCls = status.lowBattery ? "danger" : (status.onBattery ? "warn" : "ok");
    const pillTxt = status.lowBattery ? t("ups.stateLow") : (status.onBattery ? t("ups.stateBattery") : t("ups.stateOnline"));
    const rows = [];
    if (status.model) rows.push([t("ups.model"), esc(status.model)]);
    rows.push([t("ups.state"), `<span class="pill ${pillCls}">${esc(pillTxt)}</span>` + (status.status ? ` <code>${esc(status.status)}</code>` : "")]);
    if (status.batteryCharge != null) rows.push([t("ups.battery"), `${status.batteryCharge}%`]);
    if (status.runtimeSeconds != null) rows.push([t("ups.runtime"), formatUptime(status.runtimeSeconds)]);
    if (status.loadPercent != null) rows.push([t("ups.load"), `${status.loadPercent}%`]);
    statusHTML = `<table><tbody>${rows.map((r) => `<tr><td style="color:var(--text-dim)">${r[0]}</td><td>${r[1]}</td></tr>`).join("")}</tbody></table>`;
  }

  const nameOptions = (names || []).map((n) => `<option value="${esc(n)}" ${cfg.upsName === n ? "selected" : ""}>${esc(n)}</option>`).join("");
  const configHTML = isAdmin ? `
    <div id="ups-msg"></div>
    <form class="stacked" id="ups-form" style="margin-top:14px;border-top:1px dashed var(--border);padding-top:14px">
      <div class="checkbox-row"><label><input type="checkbox" name="enabled" ${cfg.enabled ? "checked" : ""}> ${esc(t("ups.enable"))}</label></div>
      <div class="field">
        <label>${esc(t("ups.name"))}</label>
        ${names && names.length
          ? `<select name="upsName">${nameOptions || `<option value="">—</option>`}</select>`
          : `<input type="text" name="upsName" value="${esc(cfg.upsName || "")}" placeholder="ups"><span class="hint">${esc(t("ups.nameHint"))}</span>`}
      </div>
      <div class="checkbox-row"><label><input type="checkbox" name="shutdownOnLowBattery" ${cfg.shutdownOnLowBattery ? "checked" : ""}> ${esc(t("ups.autoShutdown"))}</label></div>
      <div class="field"><label>${esc(t("ups.runtimeThreshold"))}</label><input type="number" name="runtimeThresholdSeconds" min="0" value="${Number(cfg.runtimeThresholdSeconds) || 0}"><span class="hint">${esc(t("ups.runtimeThresholdHint"))}</span></div>
      <div class="btn-row"><button type="submit">${esc(t("common.save"))}</button></div>
    </form>` : "";

  return `
    <div class="card">
      ${h2i("battery", esc(t("ups.title")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("ups.hint"))}</p>
      ${statusHTML}
      ${configHTML}
    </div>`;
}

function wireUPS(el) {
  const form = el.querySelector("#ups-form");
  if (!form) return;
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const cfg = {
      enabled: f.get("enabled") === "on",
      upsName: (f.get("upsName") || "").trim(),
      shutdownOnLowBattery: f.get("shutdownOnLowBattery") === "on",
      runtimeThresholdSeconds: parseInt(f.get("runtimeThresholdSeconds"), 10) || 0,
    };
    const box = el.querySelector("#ups-msg");
    try {
      await api.setUpsConfig(cfg);
      box.innerHTML = msg("ok", t("ups.saved"));
      // UPS 卡第五十一輪搬到「系統」頁,存檔後要重畫「系統」頁(不是監控頁)。
      await renderSystem(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

async function renderMonitor(el) {
  const [system, history, rules, notifiers, emailNotifiers, digest, me] = await Promise.all([
    api.monitorSystem().catch(() => null),
    api.monitorHistory().catch(() => []),
    api.alertRules().catch(() => []),
    api.notifiers().catch(() => []),
    api.emailNotifiers().catch(() => []),
    api.digest().catch(() => null),
    api.me().catch(() => ({ role: "" })),
  ]);
  const isAdmin = me.role === "admin";

  el.innerHTML = `
    <h1>${esc(t("monitor.title"))}</h1>
    <p class="page-subtitle">${esc(t("monitor.subtitle", { n: MONITOR_POLL_SECONDS }))}</p>

    <div class="grid">
      ${statTile(t("monitor.cpuUsage"), formatPercent(system && system.cpuPercent), percentClass(system && system.cpuPercent))}
      ${statTile(t("monitor.memUsage"), formatPercent(system && system.memPercent), percentClass(system && system.memPercent))}
      ${statTile(t("monitor.diskUsage"), formatPercent(system && system.diskPercent), percentClass(system && system.diskPercent))}
      ${statTile(t("monitor.uptime"), system ? formatUptime(system.uptimeSeconds) : "—", "")}
    </div>

    <div class="card chart-card">
      ${h2i("chart", esc(t("monitor.recentTrend")))}
      ${history.length < 2 ? `<p class="empty-state">${esc(t("monitor.notEnoughData"))}</p>` : `<canvas id="monitor-chart"></canvas>`}
      <div class="chart-legend">
        <span><span class="swatch" style="background:var(--accent)"></span>${esc(t("monitor.legendCpu"))}</span>
        <span><span class="swatch" style="background:var(--warn)"></span>${esc(t("monitor.legendMem"))}</span>
        <span><span class="swatch" style="background:var(--ok)"></span>${esc(t("monitor.legendDisk", { path: system ? `(${system.diskPath})` : "" }))}</span>
      </div>
    </div>

    <details class="card">
      <summary>${h2i("bell", esc(t("monitor.alertRules", { n: rules.length })))}</summary>
      ${rules.length ? rules.map((r) => renderRuleRow(r)).join("") : `<p class="empty-state">${esc(t("monitor.noRules"))}</p>`}
      <div id="rule-msg"></div>
      <form class="stacked" id="rule-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("monitor.ruleName"))}</label><input type="text" name="name" placeholder="${esc(t("monitor.ruleNamePlaceholder"))}" required></div>
        <div class="field">
          <label>${esc(t("monitor.metric"))}</label>
          <select name="metric" id="rule-metric">
            <option value="cpuPercent">${esc(t("monitor.metricCpuPercent"))}</option>
            <option value="memPercent">${esc(t("monitor.metricMemPercent"))}</option>
            <option value="diskPercent">${esc(t("monitor.metricDiskPercent"))}</option>
            <option value="arrayFailed">${esc(t("monitor.metricArrayFailed"))}</option>
            <option value="smartFailed">${esc(t("monitor.metricSmartFailed"))}</option>
          </select>
        </div>
        <div class="field" id="rule-comparator-field">
          <label>${esc(t("monitor.comparator"))}</label>
          <select name="comparator">
            <option value=">">${esc(t("monitor.gt"))}</option>
            <option value=">=">${esc(t("monitor.gte"))}</option>
            <option value="<">${esc(t("monitor.lt"))}</option>
            <option value="<=">${esc(t("monitor.lte"))}</option>
          </select>
        </div>
        <div class="field" id="rule-threshold-field"><label>${esc(t("monitor.threshold"))}</label><input type="number" name="threshold" value="90" step="0.1"></div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> ${esc(t("monitor.enabled"))}</label></div>
        <div class="btn-row"><button type="submit">${esc(t("monitor.addRule"))}</button></div>
      </form>
    </details>

    <details class="card">
      <summary>${h2i("bell", esc(t("monitor.notifiers", { n: notifiers.length })))}</summary>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("monitor.notifiersHint"))}</p>
      ${notifiers.length ? notifiers.map((n) => renderNotifierRow(n)).join("") : `<p class="empty-state">${esc(t("monitor.noNotifiers"))}</p>`}
      <div id="notifier-msg"></div>
      <form class="stacked" id="notifier-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("monitor.notifierName"))}</label><input type="text" name="name" placeholder="Slack" required></div>
        <div class="field"><label>${esc(t("monitor.webhookUrl"))}</label><input type="text" name="url" placeholder="https://example.com/hook" required></div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> ${esc(t("monitor.enabled"))}</label></div>
        <div class="btn-row"><button type="submit">${esc(t("monitor.addNotifier"))}</button></div>
      </form>
    </details>

    <details class="card">
      <summary>${h2i("mail", esc(t("monitor.emailNotifiers", { n: emailNotifiers.length })))}</summary>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("monitor.emailNotifiersHint"))}</p>
      ${emailNotifiers.length ? emailNotifiers.map((n) => renderEmailNotifierRow(n)).join("") : `<p class="empty-state">${esc(t("monitor.noEmailNotifiers"))}</p>`}
      <div id="email-notifier-msg"></div>
      <form class="stacked" id="email-notifier-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("monitor.notifierName"))}</label><input type="text" name="name" placeholder="${esc(t("monitor.emailNamePlaceholder"))}" required></div>
        <div class="field"><label>${esc(t("monitor.smtpHost"))}</label><input type="text" name="smtpHost" placeholder="smtp.gmail.com" required></div>
        <div class="field"><label>${esc(t("monitor.smtpPort"))}</label><input type="number" name="smtpPort" placeholder="587" value="587" required></div>
        <div class="field"><label>${esc(t("monitor.smtpUsername"))}</label><input type="text" name="username" autocomplete="off"></div>
        <div class="field"><label>${esc(t("monitor.smtpPassword"))}</label><input type="password" name="password" autocomplete="off"></div>
        <div class="hint">${esc(t("monitor.smtpPasswordHint"))}</div>
        <div class="field"><label>${esc(t("monitor.emailFrom"))}</label><input type="text" name="from" placeholder="gonas@example.com" required></div>
        <div class="field">
          <label>${esc(t("monitor.emailTo"))}</label>
          <input type="text" name="to" placeholder="alice@example.com, bob@example.com" required>
          <div class="hint">${esc(t("monitor.emailToHint"))}</div>
        </div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> ${esc(t("monitor.enabled"))}</label></div>
        <div class="btn-row"><button type="submit">${esc(t("monitor.addEmailNotifier"))}</button></div>
      </form>
    </details>

    ${renderDigestCard(digest, notifiers, emailNotifiers, isAdmin)}
  `;

  const canvas = el.querySelector("#monitor-chart");
  if (canvas) {
    drawSparklineChart(canvas, {
      cpu: history.map((h) => h.cpuPercent),
      mem: history.map((h) => h.memPercent),
      disk: history.map((h) => h.diskPercent),
    });
  }

  const metricSelect = el.querySelector("#rule-metric");
  const toggleBooleanFields = () => {
    const hide = isBooleanMetric(metricSelect.value);
    el.querySelector("#rule-comparator-field").style.display = hide ? "none" : "";
    el.querySelector("#rule-threshold-field").style.display = hide ? "none" : "";
  };
  metricSelect.addEventListener("change", toggleBooleanFields);
  toggleBooleanFields();

  el.querySelectorAll("[data-del-rule]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("monitor.deleteRuleConfirm"))) return;
      try {
        await api.deleteAlertRule(btn.dataset.delRule);
        await renderMonitor(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });

  el.querySelector("#rule-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const metric = f.get("metric");
    const rule = { name: f.get("name").trim(), metric, enabled: f.get("enabled") === "on" };
    if (!isBooleanMetric(metric)) {
      rule.comparator = f.get("comparator");
      rule.threshold = Number(f.get("threshold"));
    }
    const box = el.querySelector("#rule-msg");
    try {
      await api.createAlertRule(rule);
      box.innerHTML = msg("ok", t("monitor.ruleAdded"));
      await renderMonitor(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });

  el.querySelectorAll("[data-del-notifier]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("monitor.deleteNotifierConfirm"))) return;
      try {
        await api.deleteNotifier(btn.dataset.delNotifier);
        await renderMonitor(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });

  el.querySelector("#notifier-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const notifier = { name: f.get("name").trim(), url: f.get("url").trim(), enabled: f.get("enabled") === "on" };
    const box = el.querySelector("#notifier-msg");
    try {
      await api.createNotifier(notifier);
      box.innerHTML = msg("ok", t("monitor.notifierAdded"));
      await renderMonitor(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });

  el.querySelectorAll("[data-del-email-notifier]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("monitor.deleteEmailConfirm"))) return;
      try {
        await api.deleteEmailNotifier(btn.dataset.delEmailNotifier);
        await renderMonitor(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });

  el.querySelector("#email-notifier-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#email-notifier-msg");
    const to = f.get("to").split(",").map((s) => s.trim()).filter(Boolean);
    const notifier = {
      name: f.get("name").trim(),
      smtpHost: f.get("smtpHost").trim(),
      smtpPort: Number(f.get("smtpPort")),
      username: f.get("username").trim(),
      password: f.get("password"),
      from: f.get("from").trim(),
      to,
      enabled: f.get("enabled") === "on",
    };
    try {
      await api.createEmailNotifier(notifier);
      box.innerHTML = msg("ok", t("monitor.notifierAdded"));
      await renderMonitor(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });

  if (isAdmin) attachDigestHandlers(el);
}

// renderDigestCard 是 Phase 18c 新增的週期性健康摘要卡片。查看目前設定
// (啟用與否、cron 表達式、上次送出時間)所有登入使用者都看得到——跟
// 這個頁面其他「目前狀態」資訊一樣;只有 isAdmin 才會看到設定表單跟
// 「立即送出」按鈕,跟 update/security 頁面對 RoleViewer 的處理是同一個
// 慣例。notifiers/emailNotifiers 是已經設定好的 webhook/email 管道清單
// (跟上面兩張卡片共用同一次 API 呼叫的結果),這裡讓使用者用核取方塊
// 勾選其中哪些要收到 digest——digest 刻意不另外設計一套平行的通知
// 管道表單,見 state.DigestConfig 的套件註解。
function renderDigestCard(digest, notifiers, emailNotifiers, isAdmin) {
  if (!digest) {
    return `
    <div class="card">
      ${h2i("calendar", esc(t("monitor.digestTitle")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0">${esc(t("monitor.digestLoadError"))}</p>
    </div>`;
  }

  const statusPill = digest.enabled
    ? `<span class="pill ok">${esc(t("monitor.digestPillEnabled"))}</span>`
    : `<span class="pill neutral">${esc(t("monitor.digestPillDisabled"))}</span>`;
  const lastSentLine = digest.lastSentAt
    ? `<p style="color:var(--text-dim);font-size:12.5px;margin:6px 0 0">${esc(t("monitor.digestLastSent", { date: formatDateTime(digest.lastSentAt) }))}</p>`
    : `<p style="color:var(--text-dim);font-size:12.5px;margin:6px 0 0">${esc(t("monitor.digestNeverSent"))}</p>`;

  const notifierCheckboxes = (items, selectedIds, dataAttr) => items.length ? items.map((n) => `
    <label class="checkbox-row"><input type="checkbox" ${dataAttr}="${esc(n.id)}" ${selectedIds.includes(n.id) ? "checked" : ""}> ${esc(n.name)}</label>
  `).join("") : `<p class="empty-state">${esc(t("monitor.digestNoChannels"))}</p>`;

  const adminSection = isAdmin ? `
    <div id="digest-msg"></div>
    <form class="stacked" id="digest-form" style="margin-top:12px">
      <div class="checkbox-row"><label><input type="checkbox" name="enabled" ${digest.enabled ? "checked" : ""}> ${esc(t("monitor.digestEnable"))}</label></div>
      <div class="field">
        <label>${esc(t("monitor.digestCronExpr"))}</label>
        <input type="text" name="cronExpr" placeholder="0 8 * * *" value="${esc(digest.cronExpr || "")}">
        <div class="hint">${esc(t("monitor.digestCronExprHint"))}</div>
      </div>
      <div class="field">
        <label>${esc(t("monitor.digestWebhookChannels"))}</label>
        ${notifierCheckboxes(notifiers, digest.notifierIds || [], "data-digest-webhook")}
      </div>
      <div class="field">
        <label>${esc(t("monitor.digestEmailChannels"))}</label>
        ${notifierCheckboxes(emailNotifiers, digest.emailNotifierIds || [], "data-digest-email")}
      </div>
      <div class="btn-row">
        <button type="submit">${esc(t("monitor.digestSaveSettings"))}</button>
        <button type="button" class="secondary" id="digest-send-now-btn">${esc(t("monitor.digestSendNow"))}</button>
      </div>
    </form>
  ` : "";

  return `
    <details class="card">
      <summary>${h2i("calendar", esc(t("monitor.digestTitle")))} ${statusPill}</summary>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("monitor.digestHint"))}</p>
      ${lastSentLine}
      ${adminSection}
    </details>
  `;
}

function attachDigestHandlers(el) {
  const form = el.querySelector("#digest-form");
  if (!form) return;

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const cfg = {
      enabled: f.get("enabled") === "on",
      cronExpr: f.get("cronExpr").trim(),
      notifierIds: Array.from(el.querySelectorAll("[data-digest-webhook]:checked")).map((cb) => cb.dataset.digestWebhook),
      emailNotifierIds: Array.from(el.querySelectorAll("[data-digest-email]:checked")).map((cb) => cb.dataset.digestEmail),
    };
    const box = el.querySelector("#digest-msg");
    try {
      await api.setDigest(cfg);
      box.innerHTML = msg("ok", t("monitor.digestSaved"));
      await renderMonitor(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });

  const sendNowBtn = el.querySelector("#digest-send-now-btn");
  if (sendNowBtn) {
    sendNowBtn.addEventListener("click", async () => {
      sendNowBtn.disabled = true;
      const box = el.querySelector("#digest-msg");
      try {
        const res = await api.sendDigestNow();
        box.innerHTML = msg("ok", translateNotice(res.message));
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      } finally {
        sendNowBtn.disabled = false;
      }
    });
  }
}

function formatPercent(v) {
  return v === undefined || v === null ? "—" : `${v.toFixed(1)}%`;
}

function percentClass(v) {
  if (v === undefined || v === null) return "";
  if (v >= 90) return "danger";
  if (v >= 75) return "warn";
  return "ok";
}

function renderRuleRow(r) {
  const boolean = isBooleanMetric(r.metric);
  const metricLabel = t(METRIC_KEYS[r.metric] || "") || r.metric;
  const cond = boolean ? metricLabel : `${metricLabel} ${COMPARATOR_SYMBOLS[r.comparator] || r.comparator} ${r.threshold}`;
  const statusCls = !r.enabled ? "neutral" : r.firing ? "danger" : "ok";
  const statusText = !r.enabled ? t("monitor.statusDisabled") : r.firing ? t("monitor.statusFiring") : t("monitor.statusMonitoring");
  return `
    <div class="rule-row">
      <div class="rule-main">
        <span class="pill ${statusCls}">${esc(statusText)}</span>
        <div>
          <div class="rule-name">${esc(r.name)}</div>
          <div class="rule-cond">${esc(cond)}</div>
        </div>
      </div>
      <button class="secondary" data-del-rule="${esc(r.id)}">${esc(t("common.delete"))}</button>
    </div>`;
}

function renderNotifierRow(n) {
  return `
    <div class="rule-row">
      <div class="rule-main">
        <span class="pill ${n.enabled ? "ok" : "neutral"}">${n.enabled ? esc(t("monitor.notifierEnabled")) : esc(t("monitor.notifierDisabled"))}</span>
        <div>
          <div class="rule-name">${esc(n.name)}</div>
          <div class="rule-cond">${esc(n.url)}</div>
        </div>
      </div>
      <button class="secondary" data-del-notifier="${esc(n.id)}">${esc(t("common.delete"))}</button>
    </div>`;
}

function renderEmailNotifierRow(n) {
  const to = Array.isArray(n.to) ? n.to.join(", ") : "";
  return `
    <div class="rule-row">
      <div class="rule-main">
        <span class="pill ${n.enabled ? "ok" : "neutral"}">${n.enabled ? esc(t("monitor.notifierEnabled")) : esc(t("monitor.notifierDisabled"))}</span>
        <div>
          <div class="rule-name">${esc(n.name)}</div>
          <div class="rule-cond">${esc(n.smtpHost)}:${esc(n.smtpPort)} → ${esc(to)}</div>
        </div>
      </div>
      <button class="secondary" data-del-email-notifier="${esc(n.id)}">${esc(t("common.delete"))}</button>
    </div>`;
}

// drawSparklineChart 用 Canvas 2D 畫三條(CPU/記憶體/磁碟)百分比折線圖,
// 刻意不用任何圖表函式庫 —— 這個開發環境拉不到 npm 套件(見 README「已知
// 取捨」),而且對「畫三條 0-100% 的折線」這種需求,手寫幾十行 Canvas
// 程式碼遠比引入一整個圖表函式庫合理。顏色直接讀取目前套用的 CSS 變數,
// 深色/淺色主題切換時不需要另外處理。
function drawSparklineChart(canvas, series) {
  const ctx = canvas.getContext("2d");
  const dpr = window.devicePixelRatio || 1;
  const rect = canvas.getBoundingClientRect();
  const w = rect.width || canvas.clientWidth || 600;
  const h = rect.height || 180;
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const styles = getComputedStyle(document.documentElement);
  const gridColor = styles.getPropertyValue("--border").trim();
  const lines = [
    { data: series.cpu, color: styles.getPropertyValue("--accent").trim() },
    { data: series.mem, color: styles.getPropertyValue("--warn").trim() },
    { data: series.disk, color: styles.getPropertyValue("--ok").trim() },
  ];

  ctx.strokeStyle = gridColor;
  ctx.lineWidth = 1;
  [0.25, 0.5, 0.75].forEach((f) => {
    const y = h - f * h;
    ctx.beginPath();
    ctx.moveTo(0, y + 0.5);
    ctx.lineTo(w, y + 0.5);
    ctx.stroke();
  });

  const n = Math.max(series.cpu.length, 2);
  lines.forEach(({ data, color }) => {
    if (!data.length) return;
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.75;
    ctx.beginPath();
    data.forEach((v, i) => {
      const x = (i / (n - 1)) * w;
      const y = h - (Math.min(100, Math.max(0, v || 0)) / 100) * h;
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.stroke();

    const lastIdx = data.length - 1;
    const lastX = (lastIdx / (n - 1)) * w;
    const lastY = h - (Math.min(100, Math.max(0, data[lastIdx] || 0)) / 100) * h;
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(lastX, lastY, 2.75, 0, Math.PI * 2);
    ctx.fill();
  });
}

// ---------- 系統 ----------
// 第三十輪覆核(資深產品經理 P2):安全頁原本是個雜物抽屜(改密碼、2FA、
// 電源、帳號、HTTPS、VPN、稽核全塞一頁),而「電源」擺在安全頁下、「系統
// 更新」擺在儀表板上都很怪。這裡開一個「系統」頁,把偏「維運/設定」性質的
// 電源、系統更新、HTTPS、UPS 收攏在一起;安全頁只留驗證/VPN/稽核。
async function renderSystem(el) {
  const [version, update, https, me, upsStat, upsCfg, upsNames] = await Promise.all([
    api.version().catch(() => ({ version: "?" })),
    api.systemUpdate().catch(() => null),
    api.httpsSettings().catch(() => ({ enabled: false })),
    api.me().catch(() => ({ role: "" })),
    api.upsStatus().catch(() => ({ present: false })),
    api.upsConfig().catch(() => ({})),
    api.upsList().catch(() => []),
  ]);
  const isAdmin = me.role === "admin";

  el.innerHTML = `
    <h1>${esc(t("system.title"))}</h1>
    <p class="page-subtitle">${esc(t("system.subtitle"))}</p>

    ${isAdmin ? `
    <div class="card">
      ${h2i("power", esc(t("power.title")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("power.hint"))}</p>
      <div id="power-msg"></div>
      <div class="btn-row">
        <button class="secondary" id="power-reboot" type="button">${esc(t("power.reboot"))}</button>
        <button class="danger" id="power-shutdown" type="button">${esc(t("power.shutdown"))}</button>
      </div>
    </div>` : ""}

    ${renderSystemUpdateCard(update, version.version, isAdmin)}

    ${renderUPSCard(upsStat, upsCfg, upsNames, isAdmin)}

    ${isAdmin ? `
    <div class="card">
      ${h2i("lock", esc(t("security.https")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">
        ${esc(t("security.currentStatus"))}<span class="pill ${https.enabled ? "ok" : "neutral"}">${https.enabled ? esc(t("security.enabledLabel")) : esc(t("security.disabledLabel"))}</span>
        ${https.certPath ? ` · ${esc(t("security.certFile"))} <code>${esc(https.certPath)}</code>` : ""}
      </p>
      ${https.certExpiresAt ? `<p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("security.certExpiresAt", { date: formatDateTime(https.certExpiresAt) }))} · ${esc(t("security.certAutoRenews"))}</p>` : ""}
      ${https.restartRequiredNotice ? msg("warn", translateNotice(https.restartRequiredNotice)) : ""}
      <div id="https-msg"></div>
      <form class="stacked" id="https-form">
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" ${https.enabled ? "checked" : ""}> ${esc(t("security.enableHttps"))}</label></div>
        <div class="field">
          <label>${esc(t("security.certHosts"))}</label>
          <textarea name="hosts" rows="2" placeholder="nas.local&#10;192.168.1.10">${esc((https.hosts || []).join("\n"))}</textarea>
          <div class="hint">${esc(t("security.certHostsHint"))}</div>
        </div>
        <div class="btn-row"><button type="submit">${esc(t("security.saveHttps"))}</button></div>
      </form>
    </div>` : ""}
  `;

  attachSystemUpdateHandlers(el, isAdmin);
  wireUPS(el);
  if (isAdmin) wirePowerButtons(el);
  attachHTTPSFormHandlers(el);
}

// ---------- 安全 ----------

async function renderSecurity(el) {
  const me = await api.me().catch(() => ({ username: "", totpEnabled: false, role: "" }));
  updateSidebarUser(me);
  const isAdmin = me.role === "admin";

  const [vpnStatus, peers, accounts, auditLog] = await Promise.all([
    api.vpnStatus().catch(() => ({ configured: false })),
    api.vpnPeers().catch(() => []),
    isAdmin ? api.authAccounts().catch(() => []) : Promise.resolve([]),
    isAdmin ? api.auditLog().catch(() => null) : Promise.resolve(null),
  ]);

  el.innerHTML = `
    <h1>${esc(t("security.title"))}</h1>
    <p class="page-subtitle">${esc(t("security.subtitle"))}</p>

    <div class="card">
      ${h2i("key", esc(t("security.changePassword")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${t("security.changePasswordHint", { username: esc(me.username) })}</p>
      <div id="password-msg"></div>
      <form class="stacked" id="password-form">
        <div class="field"><label>${esc(t("security.oldPassword"))}</label><input type="password" name="oldPassword" autocomplete="current-password" required></div>
        <div class="field"><label>${esc(t("security.newPassword"))}</label><input type="password" name="newPassword" minlength="8" autocomplete="new-password" required></div>
        <div class="field"><label>${esc(t("security.confirmNewPassword"))}</label><input type="password" name="confirmPassword" minlength="8" autocomplete="new-password" required></div>
        <div class="btn-row"><button type="submit">${esc(t("security.updatePassword"))}</button></div>
      </form>
    </div>

    <div class="card" id="totp-card">
      ${renderTOTPSection(me.totpEnabled)}
    </div>

    ${isAdmin ? `
    <div class="card">
      ${h2i("users", esc(t("security.accounts", { n: accounts.length })))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("security.accountsHint"))}</p>
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("security.colUsername"))}</th><th>${esc(t("security.colRole"))}</th><th>${esc(t("security.colTOTP"))}</th><th></th></tr></thead>
          <tbody>
            ${accounts.length ? accounts.map((a) => `
              <tr>
                <td>${esc(a.username)}${a.username === me.username ? ` <span class="pill neutral">${esc(t("security.youLabel"))}</span>` : ""}</td>
                <td>${esc(a.role === "admin" ? t("auth.roleAdmin") : t("auth.roleViewer"))}</td>
                <td>${a.totpEnabled ? `<span class="pill ok">${esc(t("security.totpEnabledPill"))}</span>` : `<span class="pill neutral">${esc(t("security.totpDisabledPill"))}</span>`}</td>
                <td>${a.username === me.username ? "" : `
                  ${a.totpEnabled ? `<button class="secondary" data-reset-totp="${esc(a.username)}">${esc(t("security.resetTOTP"))}</button> ` : ""}
                  <button class="secondary" data-del-account="${esc(a.username)}">${esc(t("common.delete"))}</button>`}</td>
              </tr>
            `).join("") : `<tr><td colspan="4" class="empty-state">${esc(t("security.noAccounts"))}</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="account-msg"></div>
      <form class="stacked" id="account-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("security.newAccountUsername"))}</label><input type="text" name="username" required></div>
        <div class="field"><label>${esc(t("security.newAccountPassword"))}</label><input type="password" name="password" minlength="8" required></div>
        <div class="field">
          <label>${esc(t("security.newAccountRole"))}</label>
          <select name="role">
            <option value="viewer">${esc(t("auth.roleViewer"))}</option>
            <option value="admin">${esc(t("auth.roleAdmin"))}</option>
          </select>
        </div>
        <div class="btn-row"><button type="submit">${esc(t("security.createAccount"))}</button></div>
      </form>
    </div>
    ` : ""}

    <div class="card">
      ${h2i("shield", esc(t("security.vpn")))}
      ${renderVPNSection(vpnStatus, peers)}
    </div>

    ${isAdmin ? renderAuditLogCard(auditLog) : ""}
  `;

  attachPasswordFormHandlers(el);
  attachTOTPHandlers(el);
  if (isAdmin) attachAccountsHandlers(el);
  attachVPNHandlers(el);
}

// wirePowerButtons 綁關機/重開按鈕。破壞性/中斷性操作,要「打字確認」:
// 跳出輸入框、必須打出指定的字(依語言:關機/关机/SHUTDOWN…)才會送出,
// 避免誤按。指令送出後系統就會關機/重開,連線會中斷,所以只顯示「已送出」。
function wirePowerButtons(el) {
  const box = el.querySelector("#power-msg");
  const confirmByTyping = (word) => {
    const ans = window.prompt(t("power.confirmPrompt", { word }));
    return ans !== null && ans.trim() === word;
  };
  const reboot = el.querySelector("#power-reboot");
  if (reboot) reboot.addEventListener("click", async () => {
    if (!confirmByTyping(t("power.wordReboot"))) return;
    try { await api.powerReboot(); box.innerHTML = msg("ok", t("power.rebootSent")); }
    catch (err) { box.innerHTML = msg("error", translateError(err.message)); }
  });
  const shutdown = el.querySelector("#power-shutdown");
  if (shutdown) shutdown.addEventListener("click", async () => {
    if (!confirmByTyping(t("power.wordShutdown"))) return;
    try { await api.powerShutdown(); box.innerHTML = msg("ok", t("power.shutdownSent")); }
    catch (err) { box.innerHTML = msg("error", translateError(err.message)); }
  });
}

// renderAuditLogCard 是 Phase 18b 新增的稽核紀錄表格,只有 isAdmin 會
// 被渲染(見上面呼叫端的判斷,跟帳號管理那張卡片是同一個慣例)——
// RoleViewer 不該看得到其他管理者帳號的操作紀錄。auditLog 為 null 代表
// 讀取失敗(例如網路問題),顯示跟 update 卡片一致的錯誤提示,而不是
// 讓整個 Security 頁面因為這一張卡片掛掉。
function renderAuditLogCard(auditLog) {
  if (!auditLog) {
    return `
    <div class="card">
      ${h2i("log", esc(t("security.auditLog")))}
      <p style="color:var(--text-dim);font-size:13px;margin:0">${esc(t("security.auditLogLoadError"))}</p>
    </div>`;
  }

  const entries = auditLog.entries || [];
  return `
    <div class="card">
      ${h2i("log", esc(t("security.auditLog")))}
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("security.auditLogHint"))}</p>
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("security.colTime"))}</th><th>${esc(t("security.colUsername"))}</th><th>${esc(t("security.colAction"))}</th><th>${esc(t("security.colResult"))}</th></tr></thead>
          <tbody>
            ${entries.length ? entries.map((e) => `
              <tr>
                <td>${esc(formatDateTime(e.at))}</td>
                <td>${esc(e.username)}</td>
                <td><code>${esc(e.method)} ${esc(e.path)}</code>${e.detail ? ` — ${esc(e.detail)}` : ""}</td>
                <td><span class="pill ${e.statusCode < 400 ? "ok" : "warn"}">${esc(String(e.statusCode))}</span></td>
              </tr>
            `).join("") : `<tr><td colspan="4" class="empty-state">${esc(t("security.auditLogEmpty"))}</td></tr>`}
          </tbody>
        </table>
      </div>
    </div>
  `;
}

// attachAccountsHandlers 只在 renderSecurity 判斷目前登入帳號是
// RoleAdmin 時才會被呼叫——RoleViewer 的帳號連這個區塊的 HTML 都不會
// 被渲染出來(見上面的 isAdmin 判斷),這裡不需要再重複判斷一次。
function attachAccountsHandlers(el) {
  el.querySelectorAll("[data-del-account]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const username = btn.dataset.delAccount;
      if (!confirm(t("security.deleteAccountConfirm", { name: username }))) return;
      try {
        await api.deleteAuthAccount(username);
        await renderSecurity(el);
      } catch (err) {
        el.querySelector("#account-msg").innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  el.querySelectorAll("[data-reset-totp]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const username = btn.dataset.resetTotp;
      if (!confirm(t("security.resetTOTPConfirm", { name: username }))) return;
      try {
        await api.resetAccountTOTP(username);
        el.querySelector("#account-msg").innerHTML = msg("ok", t("security.resetTOTPOk", { name: username }));
        await renderSecurity(el);
      } catch (err) {
        el.querySelector("#account-msg").innerHTML = msg("error", translateError(err.message));
      }
    });
  });

  const form = el.querySelector("#account-form");
  if (!form) return;
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#account-msg");
    try {
      await api.createAuthAccount({ username: f.get("username").trim(), password: f.get("password"), role: f.get("role") });
      box.innerHTML = msg("ok", t("security.accountCreated"));
      await renderSecurity(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

function attachPasswordFormHandlers(el) {
  el.querySelector("#password-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#password-msg");
    if (f.get("newPassword") !== f.get("confirmPassword")) {
      box.innerHTML = msg("error", t("security.newPasswordMismatch"));
      return;
    }
    try {
      await api.changePassword(f.get("oldPassword"), f.get("newPassword"));
      box.innerHTML = msg("ok", t("security.passwordUpdated"));
      ev.target.reset();
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });
}

// recoveryCodesPanel 產生「只顯示一次」的救援碼面板:一組等寬字體的代碼、
// 抄下收好的提醒,以及一個「我已保存」按鈕(呼叫端把它接去 renderSecurity
// 重新整理)。刻意用 warn 色系強調「這頁關掉就再也看不到」。
function recoveryCodesPanel(codes) {
  return `
    <div class="card" style="border-color:var(--warn);background:var(--warn-soft);margin-top:12px">
      ${h2i("key", esc(t("security.recoveryTitle")))}
      <p style="font-size:12.5px;margin:0 0 10px">${esc(t("security.recoveryIntro"))}</p>
      <div class="recovery-grid">${codes.map((c) => `<code>${esc(c)}</code>`).join("")}</div>
      <div class="btn-row" style="margin-top:12px"><button type="button" id="recovery-done">${esc(t("security.recoveryDone"))}</button></div>
    </div>
  `;
}

function renderTOTPSection(enabled) {
  if (enabled) {
    return `
      ${h2i("otp", esc(t("security.totp")))}
      <p style="margin:0 0 12px"><span class="pill ok">${esc(t("security.totpEnabledPill"))}</span></p>
      <div id="totp-msg"></div>
      <div id="totp-recovery-area"></div>
      <form class="stacked" id="totp-regen-form" style="margin:0 0 16px">
        <div class="field"><label>${esc(t("security.totpCurrentPassword"))}</label><input type="password" name="password" autocomplete="current-password" required></div>
        <div class="btn-row"><button type="submit" class="secondary" id="totp-regen">${esc(t("security.recoveryRegenerate"))}</button></div>
      </form>
      <form class="stacked" id="totp-disable-form">
        <div class="field"><label>${esc(t("security.totpCurrentPassword"))}</label><input type="password" name="password" autocomplete="current-password" required></div>
        <div class="btn-row"><button type="submit" class="danger">${esc(t("security.totpDisable"))}</button></div>
      </form>
    `;
  }
  return `
    ${h2i("otp", esc(t("security.totp")))}
    <p style="margin:0 0 12px"><span class="pill neutral">${esc(t("security.totpDisabledPill"))}</span></p>
    <div id="totp-msg"></div>
    <div id="totp-setup-area">
      <button class="secondary" id="totp-begin-setup">${esc(t("security.totpBeginSetup"))}</button>
    </div>
  `;
}

function attachTOTPHandlers(el) {
  const beginBtn = el.querySelector("#totp-begin-setup");
  if (beginBtn) {
    beginBtn.addEventListener("click", async () => {
      const box = el.querySelector("#totp-msg");
      try {
        const { secret, provisioningUri, qrCodeSvg } = await api.totpSetup();
        // 有 QR 就先顯示「掃描」路徑:手機驗證器 App 直接掃這張圖即可;
        // 掃不了(或想手動)的人再往下看密鑰。qrCodeSvg 是後端用純標準函式庫
        // 產生的自成一體 SVG(離線可用),直接塞進畫面。
        const qrBlock = qrCodeSvg
          ? `<p style="color:var(--text-dim);font-size:12.5px;margin:0 0 8px">${esc(t("security.totpScanHint"))}</p>
             <div class="totp-qr" style="background:#fff;padding:10px;border-radius:10px;display:inline-block;line-height:0">${qrCodeSvg}</div>
             <p style="color:var(--text-faint);font-size:12px;margin:12px 0 4px">${esc(t("security.totpOrManual"))}</p>`
          : "";
        el.querySelector("#totp-setup-area").innerHTML = `
          ${qrBlock}
          <p style="color:var(--text-dim);font-size:12.5px">${esc(t("security.totpSetupHint"))}</p>
          <p><code style="word-break:break-all">${esc(secret)}</code></p>
          <p style="font-size:11.5px;color:var(--text-faint);word-break:break-all">${esc(provisioningUri)}</p>
          <form class="stacked" id="totp-enable-form">
            <div class="field"><label>${esc(t("security.totpEnterCode"))}</label><input type="text" name="code" inputmode="numeric" pattern="[0-9]*" placeholder="123456" required></div>
            <div class="btn-row"><button type="submit">${esc(t("security.totpEnable"))}</button></div>
          </form>
        `;
        el.querySelector("#totp-enable-form").addEventListener("submit", async (ev) => {
          ev.preventDefault();
          const f = new FormData(ev.target);
          try {
            const res = await api.totpEnable(f.get("code").trim());
            box.innerHTML = msg("ok", t("security.totpEnabled"));
            // 顯示救援碼(只有這一次),使用者按「我已保存」後才重新整理。
            const codes = (res && res.recoveryCodes) || [];
            el.querySelector("#totp-setup-area").innerHTML = recoveryCodesPanel(codes);
            const done = el.querySelector("#recovery-done");
            if (done) done.addEventListener("click", () => renderSecurity(el));
          } catch (err) {
            box.innerHTML = msg("error", translateError(err.message));
          }
        });
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const regenForm = el.querySelector("#totp-regen-form");
  if (regenForm) {
    regenForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const box = el.querySelector("#totp-msg");
      const f = new FormData(ev.target);
      if (!confirm(t("security.recoveryRegenConfirm"))) return;
      try {
        // 第五十二輪覆核 S-4:重新產生救援碼跟停用 2FA 同級敏感,後端會要求
        // 重新驗證密碼,所以這裡把密碼一起送出。
        const res = await api.totpRegenerateRecoveryCodes(f.get("password"));
        const codes = (res && res.recoveryCodes) || [];
        const area = el.querySelector("#totp-recovery-area");
        if (area) {
          area.innerHTML = recoveryCodesPanel(codes);
          const done = area.querySelector("#recovery-done");
          if (done) done.addEventListener("click", () => { area.innerHTML = ""; });
        }
        regenForm.reset();
      } catch (err) {
        if (box) box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  const disableForm = el.querySelector("#totp-disable-form");
  if (disableForm) {
    disableForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const box = el.querySelector("#totp-msg");
      try {
        await api.totpDisable(f.get("password"));
        box.innerHTML = msg("ok", t("security.totpDisabled"));
        await renderSecurity(el);
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }
}

function attachHTTPSFormHandlers(el) {
  const httpsForm = el.querySelector("#https-form");
  if (!httpsForm) return; // 非管理者看不到 HTTPS 卡,沒有表單可綁
  httpsForm.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api.setHTTPSSettings({ enabled: f.get("enabled") === "on", hosts: linesOf(f.get("hosts")) });
      // 重畫「系統」頁(HTTPS 卡第五十一輪從安全頁搬到這裡),讓剛簽出來的
      // 憑證到期日(certExpiresAt)、剛填的 hosts 立刻反映在畫面上——尤其是
      // 「第一次開啟 HTTPS」這個時間點,使用者最需要馬上確認憑證真的簽出來了。
      // 第五十二輪修:原本 renderSecurity 會跳到已經沒有這張卡的安全頁、
      // 而且 #https-msg 也不存在導致 null deref。
      await renderSystem(el);
      const hbox = el.querySelector("#https-msg");
      if (hbox) hbox.innerHTML = msg("ok", t("security.httpsSaved"));
    } catch (err) {
      el.querySelector("#https-msg").innerHTML = msg("error", translateError(err.message));
    }
  });
}

function renderVPNSection(status, peers) {
  const statusBlock = status.configured ? `
    <p style="margin:0 0 12px">
      <span class="pill ${status.running ? "ok" : "neutral"}">${status.running ? esc(t("security.vpnRunning")) : esc(t("security.vpnNotRunning"))}</span>
      · ${esc(t("security.vpnListenPort"))} <code>${esc(status.listenPort)}</code>
      · ${esc(t("security.vpnAddress"))} <code>${esc((status.address || []).join(", "))}</code>
      ${status.publicKey ? ` · ${esc(t("security.vpnPublicKey"))} <code style="word-break:break-all">${esc(status.publicKey)}</code>` : ""}
    </p>
    ${status.warning ? msg("warn", translateNotice(status.warning)) : ""}
  ` : `<p class="empty-state">${esc(t("security.vpnNotConfigured"))}</p>`;

  return `
    ${statusBlock}
    <div id="vpn-iface-msg"></div>
    <form class="stacked" id="vpn-iface-form">
      <div class="field"><label>${esc(t("security.vpnAddressField"))}</label><textarea name="address" rows="1" placeholder="10.10.0.1/24">${esc((status.address || []).join("\n"))}</textarea></div>
      <div class="field"><label>${esc(t("security.vpnListenPortField"))}</label><input type="number" name="listenPort" value="${status.listenPort || 51820}"></div>
      <div class="btn-row"><button type="submit">${status.configured ? esc(t("security.vpnUpdateIface")) : esc(t("security.vpnCreateIface"))}</button></div>
    </form>

    ${status.configured ? `
      <h2 style="margin-top:24px" id="vpn-peer-count">${esc(t("security.vpnClients", { n: peers.length }))}</h2>
      <div id="vpn-peers-list">${renderPeerRows(peers)}</div>
      <div id="vpn-peer-msg"></div>
      <form class="stacked" id="vpn-peer-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("security.vpnDeviceName"))}</label><input type="text" name="name" placeholder="${esc(t("security.vpnDeviceNamePlaceholder"))}" required></div>
        <div class="field"><label>${esc(t("security.vpnAllowedIPs"))}</label><input type="text" name="allowedIPs" placeholder="10.10.0.2/32" required></div>
        <div class="field"><label>${esc(t("security.vpnEndpoint"))}</label><input type="text" name="endpoint" placeholder="mynas.example.com:51820"><div class="hint">${esc(t("security.vpnEndpointHint"))}</div></div>
        <div class="btn-row"><button type="submit">${esc(t("security.vpnAddClient"))}</button></div>
      </form>
      <div id="vpn-client-config"></div>
    ` : ""}
  `;
}

function renderPeerRows(peers) {
  if (!peers.length) return `<p class="empty-state">${esc(t("security.vpnNoClients"))}</p>`;
  return peers.map((p) => `
    <div class="rule-row">
      <div class="rule-main">
        <div>
          <div class="rule-name">${esc(p.name)}</div>
          <div class="rule-cond">${esc((p.allowedIPs || []).join(", "))} · <span style="word-break:break-all">${esc(p.publicKey)}</span></div>
        </div>
      </div>
      <button class="secondary" data-del-peer="${esc(p.id)}">${esc(t("common.delete"))}</button>
    </div>
  `).join("");
}

function attachVPNHandlers(el) {
  el.querySelector("#vpn-iface-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#vpn-iface-msg");
    try {
      await api.setVPNInterface({ address: linesOf(f.get("address")), listenPort: Number(f.get("listenPort")) || undefined });
      box.innerHTML = msg("ok", t("security.vpnIfaceSaved"));
      await renderSecurity(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });

  const peerForm = el.querySelector("#vpn-peer-form");
  if (peerForm) {
    peerForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const box = el.querySelector("#vpn-peer-msg");
      try {
        const res = await api.addVPNPeer({
          name: f.get("name").trim(),
          allowedIPs: linesOf(f.get("allowedIPs")),
          endpoint: f.get("endpoint").trim(),
        });
        box.innerHTML = msg("ok", t("security.vpnClientAdded", { name: esc(res.peer.name) }));
        el.querySelector("#vpn-client-config").innerHTML = `<pre class="client-config">${esc(res.clientConfig)}</pre>`;
        // 只重畫 peer 清單那一小塊,不整頁重繪 renderSecurity —— 不然剛顯示
        // 出來、只出現這一次的 clientConfig 內容會立刻被蓋掉,使用者連
        // 複製都來不及。
        await refreshPeerList(el);
        ev.target.reset();
      } catch (err) {
        box.innerHTML = msg("error", translateError(err.message));
      }
    });
  }

  attachPeerDeleteHandlers(el);
}

function attachPeerDeleteHandlers(el) {
  el.querySelectorAll("[data-del-peer]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("security.vpnDeleteConfirm"))) return;
      try {
        await api.deleteVPNPeer(btn.dataset.delPeer);
        await refreshPeerList(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });
}

// refreshPeerList 只重新抓取 peer 清單並換掉清單區塊的 HTML,保留頁面其他
// 部分(尤其是新增用戶端後顯示出來、只出現這一次的 clientConfig 內容)
// 不被動到。
async function refreshPeerList(el) {
  const peers = await api.vpnPeers().catch(() => []);
  el.querySelector("#vpn-peer-count").textContent = t("security.vpnClients", { n: peers.length });
  el.querySelector("#vpn-peers-list").innerHTML = renderPeerRows(peers);
  attachPeerDeleteHandlers(el);
}

// ---------- 備份 ----------

function pad2(n) {
  return String(n).padStart(2, "0");
}

function describeSchedule(sched) {
  if (sched.kind === "cron") {
    return t("backup.scheduleDescCron", { expr: sched.cronExpr });
  }
  return t("backup.scheduleDesc", { hours: sched.everyHours, h: pad2(sched.hourOfDay), m: pad2(sched.minuteOfHour) });
}

function formatDateTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

async function renderBackup(el) {
  const jobs = await api.backupJobs().catch(() => []);

  el.innerHTML = `
    <h1>${esc(t("backup.title"))}</h1>
    <p class="page-subtitle">${t("backup.subtitle")}</p>

    <div class="card">
      ${h2i("backup", esc(t("backup.jobs", { n: jobs.length })))}
      <div id="backup-jobs-list">${renderBackupJobRows(jobs)}</div>
      <div id="backup-msg"></div>
      <form class="stacked" id="backup-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("backup.jobName"))}</label><input type="text" name="name" placeholder="${esc(t("backup.jobNamePlaceholder"))}" required></div>
        <div class="field"><label>${esc(t("backup.sourcePath"))}</label><input type="text" name="sourcePath" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>${esc(t("backup.destType"))}</label>
          <select name="destType" id="backup-dest-type">
            <option value="local">${esc(t("backup.destTypeLocal"))}</option>
            <option value="remote">${esc(t("backup.destTypeRemote"))}</option>
          </select>
        </div>
        <div id="backup-local-fields">
          <div class="field"><label>${esc(t("backup.destPath"))}</label><input type="text" name="destPath" placeholder="/mnt/backup" required><div class="hint">${esc(t("backup.destHint"))}</div></div>
          <div class="field"><label>${esc(t("backup.retention"))}</label><input type="number" name="retentionCount" value="7" min="1" required></div>
        </div>
        <div id="backup-remote-fields" hidden>
          <p class="hint">${esc(t("backup.remoteHint"))}</p>
          <div class="field"><label>${esc(t("backup.remoteHost"))}</label><input type="text" name="remoteHost" placeholder="192.168.1.50" disabled></div>
          <div class="field"><label>${esc(t("backup.remoteUser"))}</label><input type="text" name="remoteUser" placeholder="backup" disabled></div>
          <div class="field"><label>${esc(t("backup.remotePort"))}</label><input type="number" name="remotePort" value="22" min="1" max="65535" disabled></div>
          <div class="field"><label>${esc(t("backup.remotePath"))}</label><input type="text" name="remotePath" placeholder="/volume1/nas-backup" disabled></div>
          <div class="field"><label>${esc(t("backup.remoteKey"))}</label><input type="text" name="remoteKey" placeholder="/root/.ssh/gonas_backup" disabled><div class="hint">${esc(t("backup.remoteKeyHint"))}</div></div>
        </div>
        <div class="field"><label>${esc(t("backup.scheduleKind"))}</label>
          <select name="scheduleKind" id="backup-schedule-kind">
            <option value="interval">${esc(t("backup.scheduleKindInterval"))}</option>
            <option value="cron">${esc(t("backup.scheduleKindCron"))}</option>
          </select>
        </div>
        <div id="backup-interval-fields">
          <div class="field"><label>${esc(t("backup.everyHours"))}</label><input type="number" name="everyHours" value="24" min="1" required></div>
          <div class="field"><label>${esc(t("backup.startTime"))}</label>
            <div style="display:flex;gap:8px">
              <input type="number" name="hourOfDay" value="3" min="0" max="23" style="width:90px" required>
              <input type="number" name="minuteOfHour" value="0" min="0" max="59" style="width:90px" required>
            </div>
          </div>
        </div>
        <div id="backup-cron-fields" hidden>
          <div class="field"><label>${esc(t("backup.cronExpr"))}</label><input type="text" name="cronExpr" placeholder="${esc(t("backup.cronExprPlaceholder"))}"></div>
          <p class="hint">${esc(t("backup.cronHint"))}</p>
        </div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> ${esc(t("backup.scheduleEnabled"))}</label></div>
        <div class="btn-row"><button type="submit">${esc(t("backup.addJob"))}</button></div>
      </form>
    </div>
  `;

  attachBackupHandlers(el);
}

function renderBackupJobRows(jobs) {
  if (!jobs.length) return `<p class="empty-state">${esc(t("backup.noJobs"))}</p>`;
  return jobs.map((j) => renderBackupJobRow(j)).join("");
}

function renderBackupJobRow(j) {
  let statusPill = `<span class="pill neutral">${esc(t("backup.notRunYet"))}</span>`;
  let errorMsg = "";
  if (j.lastRun) {
    if (j.lastRun.success) {
      statusPill = `<span class="pill ok">${esc(t("backup.lastSuccess", { time: formatDateTime(j.lastRun.finishedAt) }))}</span>`;
      if (j.lastRun.error) errorMsg = msg("warn", translateError(j.lastRun.error));
    } else {
      statusPill = `<span class="pill danger">${esc(t("backup.lastFailed", { time: formatDateTime(j.lastRun.finishedAt) }))}</span>`;
      errorMsg = msg("error", translateError(j.lastRun.error) || t("backup.unknownError"));
    }
  }
  // 異地鏡像沒有本機快照輪替 —— 目的地顯示成 user@host:path(含「異地」標記),
  // 也不提供「快照」按鈕(遠端沒有可列舉/還原的本機快照)。
  const isRemote = !!j.remote;
  let destCond;
  if (isRemote) {
    const r = j.remote;
    const port = r.port && r.port !== 22 ? ":" + r.port : "";
    destCond =
      `<span class="pill neutral">${esc(t("backup.remoteBadge"))}</span> ` +
      esc(`${r.user}@${r.host}${port}:${r.path}`) +
      ` · ${esc(describeSchedule(j.schedule))}`;
  } else {
    destCond =
      esc(j.sourcePath) +
      " → " +
      esc(j.destPath) +
      ` · ${esc(t("backup.retention"))} ${j.retentionCount} · ${esc(describeSchedule(j.schedule))}`;
  }
  const sourcePrefix = isRemote ? esc(j.sourcePath) + " → " : "";
  const snapshotsBtn = isRemote
    ? ""
    : `<button class="secondary" data-view-snapshots="${esc(j.id)}">${esc(t("backup.snapshots"))}</button>`;
  return `
    <div class="rule-row" data-job-row="${esc(j.id)}">
      <div class="rule-main">
        <span class="pill ${j.enabled ? "ok" : "neutral"}">${j.enabled ? esc(t("backup.jobEnabled")) : esc(t("backup.jobDisabled"))}</span>
        <div>
          <div class="rule-name">${esc(j.name)}</div>
          <div class="rule-cond">${sourcePrefix}${destCond}</div>
        </div>
      </div>
      <div class="btn-row" style="margin:0">
        ${statusPill}
        <button class="secondary" data-run-job="${esc(j.id)}">${esc(t("backup.runNow"))}</button>
        ${snapshotsBtn}
        <button class="secondary" data-del-job="${esc(j.id)}">${esc(t("backup.deleteJob"))}</button>
      </div>
    </div>
    ${errorMsg}
    <div id="snapshots-${esc(j.id)}" class="snapshots-panel" hidden></div>
  `;
}

// attachBackupScheduleKindToggle 讓「排程方式」下拉選單切換時,同步
// 顯示/隱藏對應的欄位群組,並且把隱藏群組裡的 required 輸入框拿掉
// required(反過來顯示時補回去) —— 不這樣做的話,使用者選了「Cron
// 表達式」之後,瀏覽器內建的表單驗證還是會因為看不見的
// everyHours/hourOfDay/minuteOfHour 欄位「必填但空白」擋下送出,卻沒有
// 任何看得到的欄位可以填。
function attachBackupScheduleKindToggle(el) {
  const select = el.querySelector("#backup-schedule-kind");
  const intervalFields = el.querySelector("#backup-interval-fields");
  const cronFields = el.querySelector("#backup-cron-fields");

  function sync() {
    const isCron = select.value === "cron";
    intervalFields.hidden = isCron;
    cronFields.hidden = !isCron;
    intervalFields.querySelectorAll("input").forEach((input) => {
      input.required = !isCron;
      input.disabled = isCron;
    });
    const cronInput = cronFields.querySelector('input[name="cronExpr"]');
    cronInput.required = isCron;
    cronInput.disabled = !isCron;
  }

  select.addEventListener("change", sync);
  sync();
}

// attachBackupDestTypeToggle 切換「備份目的地」:本機快照 vs 異地 SSH 鏡像。
// 跟排程方式的切換同理 —— 把隱藏那一組的 required/disabled 一起處理,避免瀏覽器
// 對著看不到的 required 欄位擋下送出。異地鏡像沒有本機快照輪替,所以 destPath /
// retentionCount 這兩欄只在本機模式才存在、才必填。
function attachBackupDestTypeToggle(el) {
  const select = el.querySelector("#backup-dest-type");
  const localFields = el.querySelector("#backup-local-fields");
  const remoteFields = el.querySelector("#backup-remote-fields");

  function sync() {
    const isRemote = select.value === "remote";
    localFields.hidden = isRemote;
    remoteFields.hidden = !isRemote;
    localFields.querySelectorAll("input").forEach((input) => {
      input.required = !isRemote;
      input.disabled = isRemote;
    });
    remoteFields.querySelectorAll("input").forEach((input) => {
      // sshKey 與 port 不是必填(port 有預設值);host/user/path 才必填。
      const optional = input.name === "remoteKey" || input.name === "remotePort";
      input.required = isRemote && !optional;
      input.disabled = !isRemote;
    });
  }

  select.addEventListener("change", sync);
  sync();
}

function attachBackupHandlers(el) {
  attachBackupScheduleKindToggle(el);
  attachBackupDestTypeToggle(el);

  el.querySelector("#backup-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#backup-msg");
    const kind = f.get("scheduleKind") || "interval";
    const schedule =
      kind === "cron"
        ? { kind: "cron", cronExpr: (f.get("cronExpr") || "").trim() }
        : {
            kind: "interval",
            everyHours: Number(f.get("everyHours")),
            hourOfDay: Number(f.get("hourOfDay")),
            minuteOfHour: Number(f.get("minuteOfHour")),
          };
    const isRemote = (f.get("destType") || "local") === "remote";
    const job = {
      name: f.get("name").trim(),
      sourcePath: f.get("sourcePath").trim(),
      enabled: f.get("enabled") === "on",
      schedule,
    };
    if (isRemote) {
      // 異地鏡像:沒有本機目的地/保留份數,改帶 remote 物件。port 用預設 22,
      // sshKey 留空則由後端用系統預設金鑰/agent。
      const port = Number(f.get("remotePort"));
      job.remote = {
        host: (f.get("remoteHost") || "").trim(),
        user: (f.get("remoteUser") || "").trim(),
        port: Number.isFinite(port) && port > 0 ? port : 22,
        path: (f.get("remotePath") || "").trim(),
      };
      const key = (f.get("remoteKey") || "").trim();
      if (key) job.remote.sshKey = key;
    } else {
      job.destPath = f.get("destPath").trim();
      job.retentionCount = Number(f.get("retentionCount"));
    }
    try {
      await api.createBackupJob(job);
      box.innerHTML = msg("ok", t("backup.jobAdded"));
      await renderBackup(el);
    } catch (err) {
      box.innerHTML = msg("error", translateError(err.message));
    }
  });

  attachBackupJobRowHandlers(el);
}

function attachBackupJobRowHandlers(el) {
  el.querySelectorAll("[data-run-job]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      btn.textContent = t("backup.running");
      try {
        const res = await api.runBackupJob(btn.dataset.runJob);
        el.insertAdjacentHTML("afterbegin", msg("ok", translateNotice(res.message)));
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
        btn.disabled = false;
        btn.textContent = t("backup.runNow");
      }
    });
  });

  el.querySelectorAll("[data-del-job]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(t("backup.deleteJobConfirm"))) return;
      try {
        await api.deleteBackupJob(btn.dataset.delJob);
        await renderBackup(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", translateError(err.message)));
      }
    });
  });

  el.querySelectorAll("[data-view-snapshots]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const id = btn.dataset.viewSnapshots;
      const panel = el.querySelector(`#snapshots-${CSS.escape(id)}`);
      if (!panel.hidden) {
        panel.hidden = true;
        return;
      }
      panel.hidden = false;
      panel.innerHTML = `<p class="loading">${esc(t("common.loading"))}</p>`;
      try {
        const snapshots = await api.backupJobSnapshots(id);
        panel.innerHTML = snapshots.length
          ? `<div class="snapshot-restore-msg"></div><ul class="snapshot-list">${snapshots.map((s) => `<li><code>${esc(s.name)}</code> · ${esc(formatDateTime(s.createdAt))} <button type="button" class="secondary snap-restore" data-snap="${esc(s.name)}" data-job="${esc(id)}">${esc(t("backup.restore"))}</button></li>`).join("")}</ul>`
          : `<p class="empty-state">${esc(t("backup.noSnapshots"))}</p>`;
        wireSnapshotRestore(panel);
      } catch (err) {
        panel.innerHTML = msg("error", translateError(err.message));
      }
    });
  });
}

// wireSnapshotRestore 接上每份快照旁的「還原」按钮:让用户填还原目标目录
// (预设留空=还原回原来源),确认后调用还原 API。还原是写回数据的操作,
// 用明确的输入+确认,避免误还原到错误位置。
function wireSnapshotRestore(panel) {
  panel.querySelectorAll(".snap-restore").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const box = panel.querySelector(".snapshot-restore-msg");
      const target = prompt(t("backup.restoreTargetPrompt"), "");
      if (target === null) return; // 取消
      if (!confirm(t("backup.restoreConfirm", { snap: btn.dataset.snap, target: target || t("backup.restoreOriginalSource") }))) return;
      btn.disabled = true;
      btn.textContent = t("backup.restoring");
      if (box) box.innerHTML = msg("warn", t("backup.restoringLong"));
      try {
        const res = await api.backupJobRestore(btn.dataset.job, btn.dataset.snap, target);
        if (box) box.innerHTML = msg("ok", t("backup.restoreOk", { target: res.targetPath }));
      } catch (err) {
        if (box) box.innerHTML = msg("error", translateError(err.message));
      } finally {
        btn.disabled = false;
        btn.textContent = t("backup.restore");
      }
    });
  });
}

// renderDoctor 是「系統診斷」頁:列出每個選用外部套件裝了沒,缺的可以由
// 管理者一鍵補裝。這修掉第三十輪覆核抓到的最大產品阻斷 —— 原廠映像沒預裝
// mergerfs/snapraid/samba/docker,新手在嚮導「建立儲存池」那步會卡死,而
// 先前文件叫使用者去的「Doctor 頁面」其實根本不存在。RoleViewer 看得到
// 狀態,但只有管理者拿得到「安裝」按鈕(對應後端 requireAdmin)。
async function renderDoctor(el) {
  const [deps, me] = await Promise.all([
    api.doctorStatus().catch(() => []),
    api.me().catch(() => ({ role: "" })),
  ]);
  const isAdmin = me.role === "admin";
  const missing = (deps || []).filter((d) => !d.installed);

  const rows = (deps || []).map((d) => {
    const name = t("doctor.pkg." + d.key + ".name");
    const desc = t("doctor.pkg." + d.key + ".desc");
    const pill = d.installed
      ? `<span class="pill ok">${esc(t("doctor.installed"))}</span>`
      : `<span class="pill danger">${esc(t("doctor.notInstalled"))}</span>`;
    const action = (!d.installed && isAdmin)
      ? `<button type="button" class="doctor-install" data-apt="${esc(d.apt)}" data-key="${esc(d.key)}">${esc(t("doctor.install"))}</button>`
      : "";
    return `
      <div class="prepare-row">
        <div>
          <div><strong>${esc(name)}</strong> ${pill}</div>
          <div style="color:var(--text-faint);font-size:12px;margin-top:2px">${esc(desc)} · <code>apt: ${esc(d.apt)}</code></div>
        </div>
        <div>${action}</div>
      </div>`;
  }).join("");

  el.innerHTML = `
    <h1>${esc(t("doctor.title"))}</h1>
    <p class="page-subtitle">${esc(t("doctor.subtitle"))}</p>
    <div class="card">
      ${h2i("stethoscope", esc(t("doctor.sectionTitle")))}
      ${missing.length === 0
        ? `<p class="empty-state">${esc(t("doctor.allInstalled"))}</p>`
        : `<p style="color:var(--text-dim);font-size:13px;margin:0 0 12px">${esc(t("doctor.someMissing", { n: missing.length }))}${isAdmin ? "" : " " + esc(t("doctor.adminOnlyNote"))}</p>`}
      ${rows}
      <div id="doctor-msg"></div>
    </div>
  `;

  el.querySelectorAll(".doctor-install").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const apt = btn.dataset.apt;
      const name = t("doctor.pkg." + btn.dataset.key + ".name");
      const box = el.querySelector("#doctor-msg");
      btn.disabled = true;
      const original = btn.textContent;
      btn.textContent = t("doctor.installing");
      if (box) box.innerHTML = msg("warn", t("doctor.installingLong", { name }));
      try {
        await api.doctorInstall(apt);
        if (box) box.innerHTML = msg("ok", t("doctor.installOk", { name }));
        // 重新整理整頁狀態(裝好的會變成「已安裝」、按鈕消失)。
        renderDoctor(el);
      } catch (err) {
        btn.disabled = false;
        btn.textContent = original;
        if (box) box.innerHTML = msg("error", t("doctor.installFailed", { name, reason: err.message }));
      }
    });
  });
}
