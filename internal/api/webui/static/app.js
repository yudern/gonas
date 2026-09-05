import { api, setUnauthorizedHandler } from "/api.js";
import { t, getLocale, setLocale, translateNotice } from "/i18n.js";

const content = document.getElementById("content");
const navLinks = document.querySelectorAll(".nav-list a");
const shell = document.getElementById("shell");
const authGate = document.getElementById("auth-gate");
const authGateContent = document.getElementById("auth-gate-content");

const routes = {
  dashboard: renderDashboard,
  storage: renderStorage,
  files: renderFiles,
  apps: renderApps,
  shares: renderShares,
  users: renderUsers,
  monitor: renderMonitor,
  security: renderSecurity,
  backup: renderBackup,
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
    if (route) a.textContent = t(`nav.${route}`);
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

async function boot() {
  applyStaticI18n();
  wireLangSwitcher();
  setUnauthorizedHandler(showLoginGate);

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
    await api.me();
    showApp();
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
      <div class="field"><label>${esc(t("auth.totpCode"))}</label><input type="text" name="totpCode" inputmode="numeric" pattern="[0-9]*" placeholder="123456" autocomplete="one-time-code"></div>
      <div class="btn-row"><button type="submit">${esc(t("auth.loginBtn"))}</button></div>
    </form>
  `;
  authGateContent.querySelector("#login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = authGateContent.querySelector("#login-msg");
    try {
      await api.authLogin(f.get("username").trim(), f.get("password"), f.get("totpCode").trim());
      showApp();
    } catch (err) {
      box.innerHTML = msg("error", err.message);
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
      showApp();
    } catch (err) {
      box.innerHTML = msg("error", err.message);
    }
  });
}

// ---------- 儀表板 ----------

async function renderDashboard(el) {
  const [health, version, dockerStatus, disks, arrayStatus] = await Promise.all([
    api.health(), api.version(), api.dockerPing().catch((e) => ({ available: false, error: e.message })),
    api.disks().catch(() => []), api.arrayStatus().catch(() => ({ state: "unknown" })),
  ]);

  el.innerHTML = `
    <h1>${esc(t("dashboard.title"))}</h1>
    <p class="page-subtitle">${esc(t("dashboard.subtitle", { version: version.version, os: version.goos, arch: version.goarch, uptime: formatUptime(health.uptimeSeconds) }))}</p>
    <div class="grid">
      ${statTile(t("dashboard.systemStatus"), t("dashboard.running"), "ok")}
      ${statTile("Docker", dockerStatus.available ? t("dashboard.dockerAvailable") : t("dashboard.dockerUnavailable"), dockerStatus.available ? "ok" : "danger")}
      ${statTile(t("dashboard.storageArray"), arrayLabel(arrayStatus.state), arrayPillClass(arrayStatus.state))}
      ${statTile(t("dashboard.disksDetected"), String(disks.length), "")}
    </div>
    ${!dockerStatus.available ? msg("warn", t("dashboard.dockerWarn", { reason: dockerStatus.error || t("dashboard.unknownReason") })) : ""}
    <div class="card">
      <h2>${esc(t("dashboard.quickLinks"))}</h2>
      <p style="color:var(--text-dim);font-size:13px;margin:0">${t("dashboard.quickLinksBody")}</p>
    </div>
  `;
}

function statTile(label, value, cls) {
  return `<div class="stat-tile"><div class="label">${esc(label)}</div><div class="value ${cls}">${esc(value)}</div></div>`;
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

// ---------- 儲存 ----------

async function renderStorage(el) {
  const [disks, arrayStatus] = await Promise.all([
    api.disks().catch(() => []), api.arrayStatus().catch(() => ({ state: "unconfigured" })),
  ]);

  el.innerHTML = `
    <h1>${esc(t("storage.title"))}</h1>
    <p class="page-subtitle">${esc(t("storage.subtitle"))}</p>

    <div class="card">
      <h2>${esc(t("storage.currentStatus"))}</h2>
      <p style="margin:0 0 12px">
        <span class="pill ${arrayPillClass(arrayStatus.state)}">${esc(arrayLabel(arrayStatus.state))}</span>
        ${arrayStatus.mountPoint ? ` · ${esc(t("storage.mountPoint"))} <code>${esc(arrayStatus.mountPoint)}</code>` : ""}
      </p>
      ${arrayStatus.error ? msg("error", arrayStatus.error) : ""}
      <div class="btn-row">
        <button id="start-array" ${arrayStatus.state === "unconfigured" ? "disabled" : ""}>${esc(t("storage.startArray"))}</button>
        <button id="stop-array" class="secondary" ${arrayStatus.state === "unconfigured" ? "disabled" : ""}>${esc(t("storage.stopArray"))}</button>
      </div>
    </div>

    <div class="card">
      <h2>${esc(t("storage.disksDetected"))}</h2>
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
      <h2>${esc(t("storage.poolSetup"))}</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${t("storage.poolSetupHint")}</p>
      <div id="pool-msg"></div>
      <form class="stacked" id="pool-form">
        <div class="field"><label>${esc(t("storage.poolName"))}</label><input type="text" name="name" value="tank" required></div>
        <div class="field"><label>${esc(t("storage.poolMountPoint"))}</label><input type="text" name="mountPoint" value="/mnt/tank" required></div>
        <div class="field"><label>${esc(t("storage.dataDisks"))}</label><textarea name="dataDisks" rows="3" placeholder="/mnt/disk1&#10;/mnt/disk2"></textarea></div>
        <div class="field"><label>${esc(t("storage.parityDisks"))}</label><textarea name="parityDisks" rows="2" placeholder="/mnt/parity1"></textarea></div>
        <div class="field"><label>${esc(t("storage.contentFiles"))}</label><textarea name="contentFiles" rows="2" placeholder="/mnt/disk1&#10;/boot/config/snapraid"></textarea></div>
        <div class="btn-row"><button type="submit">${esc(t("storage.savePool"))}</button></div>
      </form>
    </div>
  `;

  el.querySelector("#start-array").addEventListener("click", () => runAction(api.startArray, renderStorage, el));
  el.querySelector("#stop-array").addEventListener("click", () => runAction(api.stopArray, renderStorage, el));

  if (disks.length) loadSmartData(el);

  el.querySelector("#pool-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const pool = {
      name: f.get("name").trim(),
      mountPoint: f.get("mountPoint").trim(),
      dataDisks: linesOf(f.get("dataDisks")),
      parityDisks: linesOf(f.get("parityDisks")),
      contentFiles: linesOf(f.get("contentFiles")),
    };
    const box = el.querySelector("#pool-msg");
    try {
      await api.setPool(pool);
      box.innerHTML = msg("ok", t("storage.poolSaved"));
      await renderStorage(el);
    } catch (err) {
      box.innerHTML = msg("error", err.message);
    }
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
    el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
      ${msg("warn", t("files.unavailable", { reason: status.reason || "" }))}
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
        <table class="file-table">
          <thead><tr><th></th><th>${esc(t("files.colName"))}</th><th>${esc(t("files.colSize"))}</th><th>${esc(t("files.colModified"))}</th><th></th></tr></thead>
          <tbody id="files-tbody"></tbody>
        </table>
      </div>
      <div id="files-panel"></div>
    </div>
    <div class="card">
      <h2>${esc(t("files.trash"))}</h2>
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
      root.querySelector("#trash-msg").innerHTML = msg("error", err.message);
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
      showFilesMsg(root, "error", err.message);
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
      showFilesMsg(root, "error", err.message);
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
    showFilesMsg(root, "error", err.message);
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
        panel.querySelector("#file-editor-msg").innerHTML = msg("error", err.message);
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
    showFilesMsg(root, "error", err.message);
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
      errors.push(`${basename(path)}: ${err.message}`);
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
      errors.push(`${basename(path)}: ${err.message}`);
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
          root.querySelector("#trash-msg").innerHTML = msg("error", err.message);
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
          root.querySelector("#trash-msg").innerHTML = msg("error", err.message);
        }
      });
    });
  } catch (err) {
    list.innerHTML = msg("error", err.message);
  }
}

// ---------- 應用程式 ----------

async function renderApps(el) {
  const [installed, catalog, dockerStatus] = await Promise.all([
    api.installedApps().catch(() => []), api.catalog().catch(() => []),
    api.dockerPing().catch((e) => ({ available: false, error: e.message })),
  ]);

  el.innerHTML = `
    <h1>${esc(t("apps.title"))}</h1>
    <p class="page-subtitle">${esc(t("apps.subtitle"))}</p>
    ${!dockerStatus.available ? msg("warn", t("apps.dockerWarn", { reason: dockerStatus.error || "" })) : ""}

    <div class="card">
      <h2>${esc(t("apps.installed", { n: installed.length }))}</h2>
      ${installed.length ? installed.map((app) => `
        <div class="app-card">
          <div class="app-card-main">
            <div>
              <h3>${esc(app.template.name)}</h3>
              <p>${esc(translateNotice(app.template.description || ""))}</p>
            </div>
            <button class="danger" data-uninstall="${esc(app.template.id)}">${esc(t("apps.uninstall"))}</button>
          </div>
          <div class="services">
            ${Object.entries(app.result.containerIds || {}).map(([svc, id]) => `
              <div class="service-row">
                <span>${esc(svc)}: ${esc(id.slice(0, 12))}</span>
                <button type="button" data-logs-toggle="${esc(id)}">${esc(t("apps.viewLogs"))}</button>
                <button type="button" data-exec-toggle="${esc(id)}">${esc(t("apps.execCmd"))}</button>
              </div>
              <div class="service-panel" id="panel-${esc(id)}" hidden></div>
            `).join("")}
          </div>
        </div>
      `).join("") : `<p class="empty-state">${esc(t("apps.noneInstalled"))}</p>`}
    </div>

    <div class="card">
      <h2>${esc(t("apps.catalog"))}</h2>
      ${catalog.map((tmpl) => renderCatalogEntry(tmpl)).join("")}
    </div>

    <div class="card">
      <h2>${esc(t("apps.customInstall"))}</h2>
      <p class="hint">${esc(t("apps.customInstallHint"))}</p>
      <div id="custom-install-msg"></div>
      <form class="stacked" id="custom-install-form">
        <div class="field"><label>${esc(t("apps.appId"))}</label><input type="text" name="id" pattern="[a-z0-9][a-z0-9\\-]*" placeholder="my-app" required></div>
        <div class="field"><label>${esc(t("apps.name"))}</label><input type="text" name="name" required></div>
        <div class="field"><label>${esc(t("apps.description"))}</label><input type="text" name="description"></div>
        <div class="field"><label>${esc(t("apps.image"))}</label><input type="text" name="image" placeholder="nginx:latest" required></div>
        <div class="field">
          <label>${esc(t("apps.ports"))}</label>
          <textarea name="ports" rows="2" placeholder="8080:80"></textarea>
        </div>
        <div class="field">
          <label>${esc(t("apps.volumes"))}</label>
          <textarea name="volumes" rows="2" placeholder="/mnt/tank/appdata/my-app:/data"></textarea>
        </div>
        <div class="field">
          <label>${esc(t("apps.env"))}</label>
          <textarea name="env" rows="2" placeholder="TZ=Asia/Taipei"></textarea>
        </div>
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
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
      }
    });
  });

  el.querySelectorAll("[data-logs-toggle]").forEach((btn) => {
    btn.addEventListener("click", () => showContainerLogsPanel(el, btn.dataset.logsToggle));
  });
  el.querySelectorAll("[data-exec-toggle]").forEach((btn) => {
    btn.addEventListener("click", () => showContainerExecPanel(el, btn.dataset.execToggle));
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
      for (const [key, value] of f.entries()) {
        const m = key.match(/^(.+?)\.(env|volume|port)\.(.+)$/);
        if (!m) continue;
        const [, svc, kind, name] = m;
        overrides[svc] = overrides[svc] || { env: {}, volumeHostPaths: {}, portHostOverrides: {} };
        if (kind === "env" && value) overrides[svc].env[name] = value;
        if (kind === "volume" && value) overrides[svc].volumeHostPaths[name] = value;
        if (kind === "port" && value) overrides[svc].portHostOverrides[Number(name)] = Number(value);
      }
      const box = form.querySelector(".install-msg");
      try {
        await api.installApp(templateId, overrides);
        box.innerHTML = msg("ok", t("apps.installSuccess"));
        setTimeout(() => renderApps(el), 600);
      } catch (err) {
        box.innerHTML = msg("error", err.message);
      }
    });
  });

  const customForm = el.querySelector("#custom-install-form");
  if (customForm) {
    customForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const f = new FormData(customForm);
      const box = el.querySelector("#custom-install-msg");
      let template;
      try {
        template = {
          id: f.get("id").trim(),
          name: f.get("name").trim(),
          description: f.get("description").trim(),
          services: [{
            name: "app",
            image: f.get("image").trim(),
            ports: parsePortLines(f.get("ports")),
            volumes: parseVolumeLines(f.get("volumes")),
            env: parseEnvLines(f.get("env")),
          }],
        };
      } catch (err) {
        box.innerHTML = msg("error", err.message);
        return;
      }
      try {
        await api.installCustomApp(template);
        box.innerHTML = msg("ok", t("apps.installSuccess"));
        setTimeout(() => renderApps(el), 600);
      } catch (err) {
        box.innerHTML = msg("error", err.message);
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
  panel.innerHTML = `<div class="panel-header"><strong>${esc(t("apps.logsTitle"))}</strong><button type="button" data-panel-close>${esc(t("common.close"))}</button></div><pre class="log-output">${esc(t("common.loading"))}</pre>`;
  wirePanelClose(panel);
  try {
    const { logs } = await api.containerLogs(containerID, "200");
    panel.querySelector(".log-output").textContent = logs || t("apps.logsEmpty");
  } catch (err) {
    panel.querySelector(".log-output").textContent = t("apps.logsFailed", { msg: err.message });
  }
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
      const result = await api.containerExec(containerID, raw.split(/\s+/));
      output.textContent = t("apps.execResult", { code: result.exitCode, output: result.output || t("apps.execEmptyOutput") });
    } catch (err) {
      output.textContent = t("apps.execFailed", { msg: err.message });
    }
  });
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

function renderCatalogEntry(tmpl) {
  const fields = tmpl.services.flatMap((svc) => {
    const env = (svc.env || []).map((e) => `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(e.key)}${e.required ? esc(t("apps.required")) : ""}</label>
        <input type="${/pass/i.test(e.key) ? "password" : "text"}" name="${esc(svc.name)}.env.${esc(e.key)}" placeholder="${esc(e.default || "")}" ${e.required ? "required" : ""}>
        ${e.description ? `<div class="hint">${esc(translateNotice(e.description))}</div>` : ""}
      </div>`);
    const vol = (svc.volumes || []).map((v) => `
      <div class="field">
        <label>${t("apps.volumeLabel", { svc: esc(svc.name), path: esc(v.containerPath) })}</label>
        <input type="text" name="${esc(svc.name)}.volume.${esc(v.containerPath)}" placeholder="/mnt/tank/appdata/${esc(tmpl.id)}" required>
      </div>`);
    return [...env, ...vol];
  });

  return `
    <div class="app-card">
      <div>
        <h3>${esc(tmpl.name)}</h3>
        <p>${esc(translateNotice(tmpl.description || ""))}</p>
        <div class="services">${tmpl.services.map((s) => esc(s.image)).join(" · ")}</div>
      </div>
      <button class="secondary" data-toggle-install="${esc(tmpl.id)}">${esc(t("apps.install"))}</button>
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
      <h2>${esc(t("shares.smbShares"))}</h2>
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("shares.colName"))}</th><th>${esc(t("shares.colPath"))}</th><th>${esc(t("shares.colReadOnly"))}</th><th>${esc(t("shares.colGuest"))}</th><th></th></tr></thead>
          <tbody>
            ${shares.length ? shares.map((s) => `
              <tr>
                <td>${esc(s.name)}</td><td><code>${esc(s.path)}</code></td>
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
        <div class="btn-row"><button type="submit">${esc(t("shares.addShare"))}</button></div>
      </form>
    </div>

    <div class="card">
      <h2>${esc(t("shares.nfsExports"))}</h2>
      <div class="table-wrap">
        <table>
          <thead><tr><th>${esc(t("shares.colPath"))}</th><th>${esc(t("shares.colClientRules"))}</th></tr></thead>
          <tbody>
            ${exportsList.length ? exportsList.map((e) => `
              <tr><td><code>${esc(e.path)}</code></td><td>${(e.clients || []).map((c) => `${esc(c.cidr || "*")}(${(c.options || []).join(",")})`).join(", ")}</td></tr>
            `).join("") : `<tr><td colspan="2" class="empty-state">${esc(t("shares.noExports"))}</td></tr>`}
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
      try { await api.deleteShare(btn.dataset.delShare); await renderShares(el); }
      catch (err) { el.insertAdjacentHTML("afterbegin", msg("error", err.message)); }
    });
  });

  el.querySelector("#share-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const share = {
      name: f.get("name").trim(),
      path: f.get("path").trim(),
      comment: f.get("comment").trim(),
      readOnly: f.get("readOnly") === "on",
      guestOk: f.get("guestOk") === "on",
      validUsers: (f.get("validUsers") || "").split(",").map((s) => s.trim()).filter(Boolean),
    };
    const box = el.querySelector("#share-msg");
    try {
      const res = await api.createShare(share);
      box.innerHTML = res.applied ? msg("ok", t("shares.shareAdded")) : msg("warn", t("shares.shareAddedWarn", { warn: res.warning || "" }));
      await renderShares(el);
    } catch (err) { box.innerHTML = msg("error", err.message); }
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
    } catch (err) { box.innerHTML = msg("error", err.message); }
  });
}

// ---------- 使用者 ----------

async function renderUsers(el) {
  const users = await api.users().catch(() => []);

  el.innerHTML = `
    <h1>${esc(t("users.title"))}</h1>
    <p class="page-subtitle">${esc(t("users.subtitle"))}</p>

    <div class="card">
      <h2>${esc(t("users.accounts", { n: users.length }))}</h2>
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
      catch (err) { el.insertAdjacentHTML("afterbegin", msg("error", err.message)); }
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
    } catch (err) { box.innerHTML = msg("error", err.message); }
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

async function renderMonitor(el) {
  const [system, history, rules, notifiers] = await Promise.all([
    api.monitorSystem().catch(() => null),
    api.monitorHistory().catch(() => []),
    api.alertRules().catch(() => []),
    api.notifiers().catch(() => []),
  ]);

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
      <h2>${esc(t("monitor.recentTrend"))}</h2>
      ${history.length < 2 ? `<p class="empty-state">${esc(t("monitor.notEnoughData"))}</p>` : `<canvas id="monitor-chart"></canvas>`}
      <div class="chart-legend">
        <span><span class="swatch" style="background:var(--accent)"></span>${esc(t("monitor.legendCpu"))}</span>
        <span><span class="swatch" style="background:var(--warn)"></span>${esc(t("monitor.legendMem"))}</span>
        <span><span class="swatch" style="background:var(--ok)"></span>${esc(t("monitor.legendDisk", { path: system ? `(${system.diskPath})` : "" }))}</span>
      </div>
    </div>

    <div class="card">
      <h2>${esc(t("monitor.alertRules", { n: rules.length }))}</h2>
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
    </div>

    <div class="card">
      <h2>${esc(t("monitor.notifiers", { n: notifiers.length }))}</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">${esc(t("monitor.notifiersHint"))}</p>
      ${notifiers.length ? notifiers.map((n) => renderNotifierRow(n)).join("") : `<p class="empty-state">${esc(t("monitor.noNotifiers"))}</p>`}
      <div id="notifier-msg"></div>
      <form class="stacked" id="notifier-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("monitor.notifierName"))}</label><input type="text" name="name" placeholder="Slack" required></div>
        <div class="field"><label>${esc(t("monitor.webhookUrl"))}</label><input type="text" name="url" placeholder="https://example.com/hook" required></div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> ${esc(t("monitor.enabled"))}</label></div>
        <div class="btn-row"><button type="submit">${esc(t("monitor.addNotifier"))}</button></div>
      </form>
    </div>
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
      try {
        await api.deleteAlertRule(btn.dataset.delRule);
        await renderMonitor(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
      box.innerHTML = msg("error", err.message);
    }
  });

  el.querySelectorAll("[data-del-notifier]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      try {
        await api.deleteNotifier(btn.dataset.delNotifier);
        await renderMonitor(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
      box.innerHTML = msg("error", err.message);
    }
  });
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

// ---------- 安全 ----------

async function renderSecurity(el) {
  const [me, https, vpnStatus, peers] = await Promise.all([
    api.me().catch(() => ({ username: "", totpEnabled: false })),
    api.httpsSettings().catch(() => ({ enabled: false })),
    api.vpnStatus().catch(() => ({ configured: false })),
    api.vpnPeers().catch(() => []),
  ]);

  el.innerHTML = `
    <h1>${esc(t("security.title"))}</h1>
    <p class="page-subtitle">${esc(t("security.subtitle"))}</p>

    <div class="card">
      <h2>${esc(t("security.changePassword"))}</h2>
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

    <div class="card">
      <h2>${esc(t("security.https"))}</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">
        ${esc(t("security.currentStatus"))}<span class="pill ${https.enabled ? "ok" : "neutral"}">${https.enabled ? esc(t("security.enabledLabel")) : esc(t("security.disabledLabel"))}</span>
        ${https.certPath ? ` · ${esc(t("security.certFile"))} <code>${esc(https.certPath)}</code>` : ""}
      </p>
      ${https.restartRequiredNotice ? msg("warn", translateNotice(https.restartRequiredNotice)) : ""}
      <div id="https-msg"></div>
      <form class="stacked" id="https-form">
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" ${https.enabled ? "checked" : ""}> ${esc(t("security.enableHttps"))}</label></div>
        <div class="field">
          <label>${esc(t("security.certHosts"))}</label>
          <textarea name="hosts" rows="2" placeholder="nas.local&#10;192.168.1.10"></textarea>
          <div class="hint">${esc(t("security.certHostsHint"))}</div>
        </div>
        <div class="btn-row"><button type="submit">${esc(t("security.saveHttps"))}</button></div>
      </form>
    </div>

    <div class="card">
      <h2>${esc(t("security.vpn"))}</h2>
      ${renderVPNSection(vpnStatus, peers)}
    </div>
  `;

  attachPasswordFormHandlers(el);
  attachTOTPHandlers(el);
  attachHTTPSFormHandlers(el);
  attachVPNHandlers(el);
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
      box.innerHTML = msg("error", err.message);
    }
  });
}

function renderTOTPSection(enabled) {
  if (enabled) {
    return `
      <h2>${esc(t("security.totp"))}</h2>
      <p style="margin:0 0 12px"><span class="pill ok">${esc(t("security.totpEnabledPill"))}</span></p>
      <div id="totp-msg"></div>
      <form class="stacked" id="totp-disable-form">
        <div class="field"><label>${esc(t("security.totpCurrentPassword"))}</label><input type="password" name="password" autocomplete="current-password" required></div>
        <div class="btn-row"><button type="submit" class="danger">${esc(t("security.totpDisable"))}</button></div>
      </form>
    `;
  }
  return `
    <h2>${esc(t("security.totp"))}</h2>
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
        const { secret, provisioningUri } = await api.totpSetup();
        el.querySelector("#totp-setup-area").innerHTML = `
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
            await api.totpEnable(f.get("code").trim());
            box.innerHTML = msg("ok", t("security.totpEnabled"));
            await renderSecurity(el);
          } catch (err) {
            box.innerHTML = msg("error", err.message);
          }
        });
      } catch (err) {
        box.innerHTML = msg("error", err.message);
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
        box.innerHTML = msg("error", err.message);
      }
    });
  }
}

function attachHTTPSFormHandlers(el) {
  el.querySelector("#https-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#https-msg");
    try {
      await api.setHTTPSSettings({ enabled: f.get("enabled") === "on", hosts: linesOf(f.get("hosts")) });
      box.innerHTML = msg("ok", t("security.httpsSaved"));
    } catch (err) {
      box.innerHTML = msg("error", err.message);
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
        <div class="field"><label>${esc(t("security.vpnEndpoint"))}</label><input type="text" name="endpoint" placeholder="mynas.example.com:51820"></div>
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
      box.innerHTML = msg("error", err.message);
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
        box.innerHTML = msg("error", err.message);
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
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
      <h2>${esc(t("backup.jobs", { n: jobs.length }))}</h2>
      <div id="backup-jobs-list">${renderBackupJobRows(jobs)}</div>
      <div id="backup-msg"></div>
      <form class="stacked" id="backup-form" style="margin-top:16px">
        <div class="field"><label>${esc(t("backup.jobName"))}</label><input type="text" name="name" placeholder="${esc(t("backup.jobNamePlaceholder"))}" required></div>
        <div class="field"><label>${esc(t("backup.sourcePath"))}</label><input type="text" name="sourcePath" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>${esc(t("backup.destPath"))}</label><input type="text" name="destPath" placeholder="/mnt/backup" required></div>
        <div class="field"><label>${esc(t("backup.retention"))}</label><input type="number" name="retentionCount" value="7" min="1" required></div>
        <div class="field"><label>${esc(t("backup.everyHours"))}</label><input type="number" name="everyHours" value="24" min="1" required></div>
        <div class="field"><label>${esc(t("backup.startTime"))}</label>
          <div style="display:flex;gap:8px">
            <input type="number" name="hourOfDay" value="3" min="0" max="23" style="width:90px" required>
            <input type="number" name="minuteOfHour" value="0" min="0" max="59" style="width:90px" required>
          </div>
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
      if (j.lastRun.error) errorMsg = msg("warn", j.lastRun.error);
    } else {
      statusPill = `<span class="pill danger">${esc(t("backup.lastFailed", { time: formatDateTime(j.lastRun.finishedAt) }))}</span>`;
      errorMsg = msg("error", j.lastRun.error || t("backup.unknownError"));
    }
  }
  return `
    <div class="rule-row" data-job-row="${esc(j.id)}">
      <div class="rule-main">
        <span class="pill ${j.enabled ? "ok" : "neutral"}">${j.enabled ? esc(t("backup.jobEnabled")) : esc(t("backup.jobDisabled"))}</span>
        <div>
          <div class="rule-name">${esc(j.name)}</div>
          <div class="rule-cond">${esc(j.sourcePath)} → ${esc(j.destPath)} · ${esc(t("backup.retention"))} ${j.retentionCount} · ${esc(describeSchedule(j.schedule))}</div>
        </div>
      </div>
      <div class="btn-row" style="margin:0">
        ${statusPill}
        <button class="secondary" data-run-job="${esc(j.id)}">${esc(t("backup.runNow"))}</button>
        <button class="secondary" data-view-snapshots="${esc(j.id)}">${esc(t("backup.snapshots"))}</button>
        <button class="secondary" data-del-job="${esc(j.id)}">${esc(t("backup.deleteJob"))}</button>
      </div>
    </div>
    ${errorMsg}
    <div id="snapshots-${esc(j.id)}" class="snapshots-panel" hidden></div>
  `;
}

function attachBackupHandlers(el) {
  el.querySelector("#backup-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = el.querySelector("#backup-msg");
    const job = {
      name: f.get("name").trim(),
      sourcePath: f.get("sourcePath").trim(),
      destPath: f.get("destPath").trim(),
      retentionCount: Number(f.get("retentionCount")),
      enabled: f.get("enabled") === "on",
      schedule: {
        everyHours: Number(f.get("everyHours")),
        hourOfDay: Number(f.get("hourOfDay")),
        minuteOfHour: Number(f.get("minuteOfHour")),
      },
    };
    try {
      await api.createBackupJob(job);
      box.innerHTML = msg("ok", t("backup.jobAdded"));
      await renderBackup(el);
    } catch (err) {
      box.innerHTML = msg("error", err.message);
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
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
          ? `<ul class="snapshot-list">${snapshots.map((s) => `<li><code>${esc(s.name)}</code> · ${esc(formatDateTime(s.createdAt))}</li>`).join("")}</ul>`
          : `<p class="empty-state">${esc(t("backup.noSnapshots"))}</p>`;
      } catch (err) {
        panel.innerHTML = msg("error", err.message);
      }
    });
  });
}
