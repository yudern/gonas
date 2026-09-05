import { api, setUnauthorizedHandler } from "/api.js";

const content = document.getElementById("content");
const navLinks = document.querySelectorAll(".nav-list a");
const shell = document.getElementById("shell");
const authGate = document.getElementById("auth-gate");
const authGateContent = document.getElementById("auth-gate-content");

const routes = {
  dashboard: renderDashboard,
  storage: renderStorage,
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

async function router() {
  const hash = location.hash.replace(/^#\//, "") || "dashboard";
  const route = routes[hash] ? hash : "dashboard";

  navLinks.forEach((a) => a.classList.toggle("active", a.dataset.route === route));

  content.innerHTML = `<p class="loading">載入中…</p>`;
  try {
    await routes[route](content);
  } catch (err) {
    content.innerHTML = msg("error", "載入失敗:" + err.message);
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
    authGateContent.innerHTML = msg("error", "無法連線到 gonasd:" + err.message);
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
    <h1>登入</h1>
    <p class="page-subtitle">請輸入管理者帳號密碼。已啟用兩步驟驗證的話,一併填入目前的驗證碼。</p>
    <div id="login-msg"></div>
    <form class="stacked" id="login-form">
      <div class="field"><label>使用者名稱</label><input type="text" name="username" autocomplete="username" required></div>
      <div class="field"><label>密碼</label><input type="password" name="password" autocomplete="current-password" required></div>
      <div class="field"><label>兩步驟驗證碼(如已啟用)</label><input type="text" name="totpCode" inputmode="numeric" pattern="[0-9]*" placeholder="123456" autocomplete="one-time-code"></div>
      <div class="btn-row"><button type="submit">登入</button></div>
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
    <h1>初始設定</h1>
    <p class="page-subtitle">第一次執行 GoNAS,請先建立唯一的管理者帳號。</p>
    <div id="setup-msg"></div>
    <form class="stacked" id="setup-form">
      <div class="field"><label>使用者名稱</label><input type="text" name="username" autocomplete="username" required></div>
      <div class="field"><label>密碼(至少 8 個字元)</label><input type="password" name="password" minlength="8" autocomplete="new-password" required></div>
      <div class="field"><label>確認密碼</label><input type="password" name="confirm" minlength="8" autocomplete="new-password" required></div>
      <div class="btn-row"><button type="submit">建立管理者帳號</button></div>
    </form>
  `;
  authGateContent.querySelector("#setup-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const box = authGateContent.querySelector("#setup-msg");
    if (f.get("password") !== f.get("confirm")) {
      box.innerHTML = msg("error", "兩次輸入的密碼不一致。");
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
    <h1>儀表板</h1>
    <p class="page-subtitle">GoNAS ${esc(version.version)} · ${esc(version.goos)}/${esc(version.goarch)} · 已執行 ${formatUptime(health.uptimeSeconds)}</p>
    <div class="grid">
      ${statTile("系統狀態", "運作中", "ok")}
      ${statTile("Docker", dockerStatus.available ? "可用" : "不可用", dockerStatus.available ? "ok" : "danger")}
      ${statTile("儲存陣列", arrayLabel(arrayStatus.state), arrayPillClass(arrayStatus.state))}
      ${statTile("偵測到的硬碟", String(disks.length), "")}
    </div>
    ${!dockerStatus.available ? msg("warn", "Docker 無法連線:" + (dockerStatus.error || "未知原因") + "。安裝應用程式前需要先確認 Docker 已安裝並啟動。") : ""}
    <div class="card">
      <h2>快速連結</h2>
      <p style="color:var(--text-dim);font-size:13px;margin:0">前往「<a href="#/storage">儲存</a>」設定並啟動陣列、「<a href="#/apps">應用程式</a>」安裝服務、「<a href="#/shares">共享</a>」設定 SMB/NFS、「<a href="#/users">使用者</a>」管理帳號,或「<a href="#/monitor">監控</a>」查看資源使用率與設定告警。</p>
    </div>
  `;
}

function statTile(label, value, cls) {
  return `<div class="stat-tile"><div class="label">${esc(label)}</div><div class="value ${cls}">${esc(value)}</div></div>`;
}

function formatUptime(sec) {
  sec = sec || 0;
  if (sec < 60) return `${sec} 秒`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m} 分鐘`;
  const h = Math.floor(m / 60);
  return `${h} 小時 ${m % 60} 分鐘`;
}

function arrayLabel(state) {
  return { unconfigured: "尚未設定", stopped: "已停止", starting: "啟動中", started: "運作中", stopping: "停止中", failed: "失敗", unknown: "未知" }[state] || state;
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
    <h1>儲存</h1>
    <p class="page-subtitle">陣列採 SnapRAID + mergerFS:每顆資料碟各自獨立掛載、資料不打散,由 GoNAS 統一聯合掛載並提供同位校驗。</p>

    <div class="card">
      <h2>目前陣列狀態</h2>
      <p style="margin:0 0 12px">
        <span class="pill ${arrayPillClass(arrayStatus.state)}">${esc(arrayLabel(arrayStatus.state))}</span>
        ${arrayStatus.mountPoint ? ` · 掛載點 <code>${esc(arrayStatus.mountPoint)}</code>` : ""}
      </p>
      ${arrayStatus.error ? msg("error", arrayStatus.error) : ""}
      <div class="btn-row">
        <button id="start-array" ${arrayStatus.state === "unconfigured" ? "disabled" : ""}>啟動陣列</button>
        <button id="stop-array" class="secondary" ${arrayStatus.state === "unconfigured" ? "disabled" : ""}>停止陣列</button>
      </div>
    </div>

    <div class="card">
      <h2>偵測到的硬碟</h2>
      <div class="table-wrap">
        <table>
          <thead><tr><th>裝置</th><th>型號</th><th>容量</th><th>類型</th><th>掛載點</th></tr></thead>
          <tbody>
            ${disks.length ? disks.map((d) => `
              <tr>
                <td><code>${esc(d.path)}</code></td>
                <td>${esc(d.model || "—")}</td>
                <td>${formatBytes(d.sizeBytes)}</td>
                <td>${d.rotational ? "HDD" : "SSD/NVMe"}</td>
                <td>${esc(d.mountpoint || "—")}</td>
              </tr>`).join("") : `<tr><td colspan="5" class="empty-state">沒有偵測到硬碟</td></tr>`}
          </tbody>
        </table>
      </div>
    </div>

    <div class="card">
      <h2>設定儲存池</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">
        資料碟/同位碟請填已經格式化並掛載好的路徑(例如 <code>/mnt/disk1</code>),每行一個。
        GoNAS 目前不會幫你格式化硬碟 —— 這是刻意的:自動格式化是會清空資料的危險操作,交給使用者在系統層面自己確認過再做。
      </p>
      <div id="pool-msg"></div>
      <form class="stacked" id="pool-form">
        <div class="field"><label>池名稱</label><input type="text" name="name" value="tank" required></div>
        <div class="field"><label>聯合掛載點</label><input type="text" name="mountPoint" value="/mnt/tank" required></div>
        <div class="field"><label>資料碟路徑(每行一個)</label><textarea name="dataDisks" rows="3" placeholder="/mnt/disk1&#10;/mnt/disk2"></textarea></div>
        <div class="field"><label>同位碟路徑(每行一個)</label><textarea name="parityDisks" rows="2" placeholder="/mnt/parity1"></textarea></div>
        <div class="field"><label>SnapRAID 索引檔位置(每行一個,建議至少 2 份)</label><textarea name="contentFiles" rows="2" placeholder="/mnt/disk1&#10;/boot/config/snapraid"></textarea></div>
        <div class="btn-row"><button type="submit">儲存設定</button></div>
      </form>
    </div>
  `;

  el.querySelector("#start-array").addEventListener("click", () => runAction(api.startArray, "陣列已啟動", renderStorage, el));
  el.querySelector("#stop-array").addEventListener("click", () => runAction(api.stopArray, "陣列已停止", renderStorage, el));

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
      box.innerHTML = msg("ok", "儲存池設定已儲存。");
      await renderStorage(el);
    } catch (err) {
      box.innerHTML = msg("error", err.message);
    }
  });
}

function linesOf(text) {
  return (text || "").split("\n").map((s) => s.trim()).filter(Boolean);
}

async function runAction(fn, okText, rerender, el) {
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

// ---------- 應用程式 ----------

async function renderApps(el) {
  const [installed, catalog, dockerStatus] = await Promise.all([
    api.installedApps().catch(() => []), api.catalog().catch(() => []),
    api.dockerPing().catch((e) => ({ available: false, error: e.message })),
  ]);

  el.innerHTML = `
    <h1>應用程式</h1>
    <p class="page-subtitle">用 Docker 容器安裝與管理服務。多容器的 App 會自動建立專屬的共用網路。</p>
    ${!dockerStatus.available ? msg("warn", "Docker 無法連線,無法安裝或管理應用程式:" + (dockerStatus.error || "")) : ""}

    <div class="card">
      <h2>已安裝(${installed.length})</h2>
      ${installed.length ? installed.map((app) => `
        <div class="app-card">
          <div>
            <h3>${esc(app.template.name)}</h3>
            <p>${esc(app.template.description || "")}</p>
            <div class="services">${Object.entries(app.result.containerIds || {}).map(([svc, id]) => `${esc(svc)}: ${esc(id.slice(0, 12))}`).join(" · ")}</div>
          </div>
          <button class="danger" data-uninstall="${esc(app.template.id)}">解除安裝</button>
        </div>
      `).join("") : `<p class="empty-state">還沒有安裝任何應用程式。</p>`}
    </div>

    <div class="card">
      <h2>商店目錄</h2>
      ${catalog.map((tmpl) => renderCatalogEntry(tmpl)).join("")}
    </div>

    <div class="card">
      <h2>自訂安裝</h2>
      <p class="hint">不透過上面的商店目錄,直接指定任意 image 安裝一個容器(對應 Docker Hub 或其他 registry 上的任何映像檔,或這台機器上已經存在的本機映像檔)。</p>
      <div id="custom-install-msg"></div>
      <form class="stacked" id="custom-install-form">
        <div class="field"><label>App ID(英數字/連字號,用於容器與網路命名,安裝後不能改)</label><input type="text" name="id" pattern="[a-z0-9][a-z0-9\\-]*" placeholder="my-app" required></div>
        <div class="field"><label>名稱</label><input type="text" name="name" required></div>
        <div class="field"><label>說明(選填)</label><input type="text" name="description"></div>
        <div class="field"><label>Image</label><input type="text" name="image" placeholder="nginx:latest" required></div>
        <div class="field">
          <label>埠對應(選填,每行一個,格式 host:container,可加 /udp,例如 8080:80)</label>
          <textarea name="ports" rows="2" placeholder="8080:80"></textarea>
        </div>
        <div class="field">
          <label>掛載路徑(選填,每行一個,格式 host路徑:容器路徑,可加 :ro 唯讀)</label>
          <textarea name="volumes" rows="2" placeholder="/mnt/tank/appdata/my-app:/data"></textarea>
        </div>
        <div class="field">
          <label>環境變數(選填,每行一個,格式 KEY=VALUE)</label>
          <textarea name="env" rows="2" placeholder="TZ=Asia/Taipei"></textarea>
        </div>
        <div class="btn-row"><button type="submit">安裝</button></div>
      </form>
    </div>
  `;

  el.querySelectorAll("[data-uninstall]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(`確定要解除安裝「${btn.closest(".app-card").querySelector("h3").textContent}」嗎?`)) return;
      try {
        await api.uninstallApp(btn.dataset.uninstall);
        await renderApps(el);
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
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
        box.innerHTML = msg("ok", "安裝成功!");
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
        box.innerHTML = msg("ok", "安裝成功!");
        setTimeout(() => renderApps(el), 600);
      } catch (err) {
        box.innerHTML = msg("error", err.message);
      }
    });
  }
}

// parsePortLines/parseVolumeLines/parseEnvLines 把自訂安裝表單裡的簡易
// 文字格式(每行一個)翻譯成後端 appstore.AppTemplate 期待的結構化欄位
// ——刻意不做成一排一排可以動態新增/刪除的欄位(常見的 Docker 管理介面
// 作法),用純文字多行輸入换取實作/維護成本低很多,對「一次裝一個
// 容器、填個幾條就好」的情境已經足夠,之後真的有需要再改成動態表單。
function parsePortLines(text) {
  return (text || "").split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
    const m = line.match(/^(\d+):(\d+)(\/(tcp|udp))?$/i);
    if (!m) throw new Error(`埠對應格式錯誤:「${line}」,應為 host:container 或 host:container/udp`);
    return { hostPort: Number(m[1]), containerPort: Number(m[2]), protocol: (m[4] || "").toLowerCase() || undefined };
  });
}

function parseVolumeLines(text) {
  return (text || "").split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
    const parts = line.split(":");
    if (parts.length < 2 || parts.length > 3) {
      throw new Error(`掛載路徑格式錯誤:「${line}」,應為 host路徑:容器路徑 或 host路徑:容器路徑:ro`);
    }
    const readOnly = parts[2] === "ro";
    if (parts.length === 3 && !readOnly) {
      throw new Error(`掛載路徑格式錯誤:「${line}」,第三段只接受 ro`);
    }
    return { hostPath: parts[0], containerPath: parts[1], readOnly };
  });
}

function parseEnvLines(text) {
  return (text || "").split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
    const idx = line.indexOf("=");
    if (idx <= 0) throw new Error(`環境變數格式錯誤:「${line}」,應為 KEY=VALUE`);
    return { key: line.slice(0, idx), default: line.slice(idx + 1) };
  });
}

function renderCatalogEntry(tmpl) {
  const fields = tmpl.services.flatMap((svc) => {
    const env = (svc.env || []).map((e) => `
      <div class="field">
        <label>${esc(svc.name)} · ${esc(e.key)}${e.required ? " (必填)" : ""}</label>
        <input type="${/pass/i.test(e.key) ? "password" : "text"}" name="${esc(svc.name)}.env.${esc(e.key)}" placeholder="${esc(e.default || "")}" ${e.required ? "required" : ""}>
        ${e.description ? `<div class="hint">${esc(e.description)}</div>` : ""}
      </div>`);
    const vol = (svc.volumes || []).map((v) => `
      <div class="field">
        <label>${esc(svc.name)} · 掛載路徑(對應容器內 ${esc(v.containerPath)})</label>
        <input type="text" name="${esc(svc.name)}.volume.${esc(v.containerPath)}" placeholder="/mnt/tank/appdata/${esc(tmpl.id)}" required>
      </div>`);
    return [...env, ...vol];
  });

  return `
    <div class="app-card">
      <div>
        <h3>${esc(tmpl.name)}</h3>
        <p>${esc(tmpl.description || "")}</p>
        <div class="services">${tmpl.services.map((s) => esc(s.image)).join(" · ")}</div>
      </div>
      <button class="secondary" data-toggle-install="${esc(tmpl.id)}">安裝</button>
    </div>
    <form class="install-form" id="install-form-${esc(tmpl.id)}" data-install="${esc(tmpl.id)}">
      <div class="install-msg"></div>
      ${fields.join("")}
      <div class="btn-row"><button type="submit">確認安裝</button></div>
    </form>
  `;
}

// ---------- 共享 ----------

async function renderShares(el) {
  const [shares, exports] = await Promise.all([api.shares().catch(() => []), api.exports().catch(() => [])]);

  el.innerHTML = `
    <h1>共享</h1>
    <p class="page-subtitle">SMB(Windows/macOS)與 NFS(Linux)檔案共享。</p>

    <div class="card">
      <h2>SMB 共享</h2>
      <div class="table-wrap">
        <table>
          <thead><tr><th>名稱</th><th>路徑</th><th>唯讀</th><th>允許訪客</th><th></th></tr></thead>
          <tbody>
            ${shares.length ? shares.map((s) => `
              <tr>
                <td>${esc(s.name)}</td><td><code>${esc(s.path)}</code></td>
                <td>${s.readOnly ? "是" : "否"}</td><td>${s.guestOk ? "是" : "否"}</td>
                <td><button class="secondary" data-del-share="${esc(s.name)}">刪除</button></td>
              </tr>`).join("") : `<tr><td colspan="5" class="empty-state">還沒有設定共享</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="share-msg"></div>
      <form class="stacked" id="share-form" style="margin-top:16px">
        <div class="field"><label>名稱</label><input type="text" name="name" required></div>
        <div class="field"><label>路徑</label><input type="text" name="path" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>備註</label><input type="text" name="comment"></div>
        <div class="checkbox-row"><label><input type="checkbox" name="readOnly"> 唯讀</label></div>
        <div class="checkbox-row"><label><input type="checkbox" name="guestOk"> 允許訪客(匿名)存取</label></div>
        <div class="field"><label>允許的使用者(逗號分隔,留空代表沿用全域設定)</label><input type="text" name="validUsers" placeholder="alice, bob"></div>
        <div class="btn-row"><button type="submit">新增共享</button></div>
      </form>
    </div>

    <div class="card">
      <h2>NFS 匯出</h2>
      <div class="table-wrap">
        <table>
          <thead><tr><th>路徑</th><th>用戶端規則</th></tr></thead>
          <tbody>
            ${exports.length ? exports.map((e) => `
              <tr><td><code>${esc(e.path)}</code></td><td>${(e.clients || []).map((c) => `${esc(c.cidr || "*")}(${(c.options || []).join(",")})`).join(", ")}</td></tr>
            `).join("") : `<tr><td colspan="2" class="empty-state">還沒有設定 NFS 匯出</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="export-msg"></div>
      <form class="stacked" id="export-form" style="margin-top:16px">
        <div class="field"><label>路徑</label><input type="text" name="path" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>允許的網段(CIDR)</label><input type="text" name="cidr" placeholder="192.168.1.0/24" required></div>
        <div class="field"><label>選項(逗號分隔)</label><input type="text" name="options" value="rw,sync,no_subtree_check" required></div>
        <div class="btn-row"><button type="submit">新增匯出</button></div>
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
      box.innerHTML = res.applied ? msg("ok", "共享已新增並套用。") : msg("warn", "共享已存起來,但套用失敗:" + (res.warning || ""));
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
      box.innerHTML = res.applied ? msg("ok", "匯出已新增並套用。") : msg("warn", "匯出已存起來,但套用失敗:" + (res.warning || ""));
      await renderShares(el);
    } catch (err) { box.innerHTML = msg("error", err.message); }
  });
}

// ---------- 使用者 ----------

async function renderUsers(el) {
  const users = await api.users().catch(() => []);

  el.innerHTML = `
    <h1>使用者</h1>
    <p class="page-subtitle">帳號同時是系統帳號與 Samba 帳號,沒有互動式登入殼層 —— 純粹是檔案共享的身份。</p>

    <div class="card">
      <h2>帳號(${users.length})</h2>
      <div class="table-wrap">
        <table>
          <thead><tr><th>使用者名稱</th><th>備註</th><th></th></tr></thead>
          <tbody>
            ${users.length ? users.map((u) => `
              <tr><td>${esc(u.username)}</td><td>${esc(u.comment || "—")}</td>
              <td><button class="secondary" data-del-user="${esc(u.username)}">刪除</button></td></tr>
            `).join("") : `<tr><td colspan="3" class="empty-state">還沒有建立使用者</td></tr>`}
          </tbody>
        </table>
      </div>
      <div id="user-msg"></div>
      <form class="stacked" id="user-form" style="margin-top:16px">
        <div class="field"><label>使用者名稱</label><input type="text" name="username" placeholder="alice" required></div>
        <div class="field"><label>備註</label><input type="text" name="comment" placeholder="Alice Chen"></div>
        <div class="field"><label>密碼</label><input type="password" name="password" required></div>
        <div class="btn-row"><button type="submit">建立使用者</button></div>
      </form>
    </div>
  `;

  el.querySelectorAll("[data-del-user]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(`確定要刪除使用者「${btn.dataset.delUser}」嗎?`)) return;
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
      box.innerHTML = res.sambaWarning ? msg("warn", "使用者已建立,但 " + res.sambaWarning) : msg("ok", "使用者已建立。");
      await renderUsers(el);
    } catch (err) { box.innerHTML = msg("error", err.message); }
  });
}

// ---------- 監控 ----------

const METRIC_LABELS = {
  cpuPercent: "CPU 使用率",
  memPercent: "記憶體使用率",
  diskPercent: "磁碟使用率",
  arrayFailed: "陣列狀態變成 failed",
  smartFailed: "任一顆碟 SMART 檢查沒過",
};
const COMPARATOR_LABELS = { ">": ">", ">=": "≥", "<": "<", "<=": "≤" };

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
    <h1>監控</h1>
    <p class="page-subtitle">每 ${MONITOR_POLL_SECONDS} 秒在背景取樣一次系統資源;告警規則觸發或解除時,會寫進 daemon 的 log,也會送到下面設定的通知管道。</p>

    <div class="grid">
      ${statTile("CPU 使用率", formatPercent(system && system.cpuPercent), percentClass(system && system.cpuPercent))}
      ${statTile("記憶體使用率", formatPercent(system && system.memPercent), percentClass(system && system.memPercent))}
      ${statTile("磁碟使用率", formatPercent(system && system.diskPercent), percentClass(system && system.diskPercent))}
      ${statTile("執行時間", system ? formatUptime(system.uptimeSeconds) : "—", "")}
    </div>

    <div class="card chart-card">
      <h2>最近趨勢</h2>
      ${history.length < 2 ? `<p class="empty-state">取樣資料還不夠畫圖,daemon 剛啟動時需要等一小段時間累積。</p>` : `<canvas id="monitor-chart"></canvas>`}
      <div class="chart-legend">
        <span><span class="swatch" style="background:var(--accent)"></span>CPU</span>
        <span><span class="swatch" style="background:var(--warn)"></span>記憶體</span>
        <span><span class="swatch" style="background:var(--ok)"></span>磁碟${system ? `(${esc(system.diskPath)})` : ""}</span>
      </div>
    </div>

    <div class="card">
      <h2>告警規則(${rules.length})</h2>
      ${rules.length ? rules.map((r) => renderRuleRow(r)).join("") : `<p class="empty-state">還沒有設定告警規則。</p>`}
      <div id="rule-msg"></div>
      <form class="stacked" id="rule-form" style="margin-top:16px">
        <div class="field"><label>名稱</label><input type="text" name="name" placeholder="CPU 過載" required></div>
        <div class="field">
          <label>指標</label>
          <select name="metric" id="rule-metric">
            <option value="cpuPercent">CPU 使用率(%)</option>
            <option value="memPercent">記憶體使用率(%)</option>
            <option value="diskPercent">磁碟使用率(%)</option>
            <option value="arrayFailed">陣列狀態變成 failed</option>
            <option value="smartFailed">任一顆碟 SMART 檢查沒過</option>
          </select>
        </div>
        <div class="field" id="rule-comparator-field">
          <label>比較方式</label>
          <select name="comparator">
            <option value=">">大於</option>
            <option value=">=">大於等於</option>
            <option value="<">小於</option>
            <option value="<=">小於等於</option>
          </select>
        </div>
        <div class="field" id="rule-threshold-field"><label>門檻值</label><input type="number" name="threshold" value="90" step="0.1"></div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> 啟用</label></div>
        <div class="btn-row"><button type="submit">新增規則</button></div>
      </form>
    </div>

    <div class="card">
      <h2>通知管道(${notifiers.length})</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">告警一律會寫進 daemon 的 log;下面可以額外加 webhook 端點,規則觸發或解除時會 POST 一份 JSON 過去。</p>
      ${notifiers.length ? notifiers.map((n) => renderNotifierRow(n)).join("") : `<p class="empty-state">還沒有設定額外的通知管道。</p>`}
      <div id="notifier-msg"></div>
      <form class="stacked" id="notifier-form" style="margin-top:16px">
        <div class="field"><label>名稱</label><input type="text" name="name" placeholder="Slack" required></div>
        <div class="field"><label>Webhook URL</label><input type="text" name="url" placeholder="https://example.com/hook" required></div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> 啟用</label></div>
        <div class="btn-row"><button type="submit">新增通知管道</button></div>
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
      box.innerHTML = msg("ok", "告警規則已新增。");
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
      box.innerHTML = msg("ok", "通知管道已新增。");
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
  const metricLabel = METRIC_LABELS[r.metric] || r.metric;
  const cond = boolean ? metricLabel : `${metricLabel} ${COMPARATOR_LABELS[r.comparator] || r.comparator} ${r.threshold}`;
  const statusCls = !r.enabled ? "neutral" : r.firing ? "danger" : "ok";
  const statusText = !r.enabled ? "已停用" : r.firing ? "觸發中" : "監控中";
  return `
    <div class="rule-row">
      <div class="rule-main">
        <span class="pill ${statusCls}">${statusText}</span>
        <div>
          <div class="rule-name">${esc(r.name)}</div>
          <div class="rule-cond">${esc(cond)}</div>
        </div>
      </div>
      <button class="secondary" data-del-rule="${esc(r.id)}">刪除</button>
    </div>`;
}

function renderNotifierRow(n) {
  return `
    <div class="rule-row">
      <div class="rule-main">
        <span class="pill ${n.enabled ? "ok" : "neutral"}">${n.enabled ? "啟用" : "停用"}</span>
        <div>
          <div class="rule-name">${esc(n.name)}</div>
          <div class="rule-cond">${esc(n.url)}</div>
        </div>
      </div>
      <button class="secondary" data-del-notifier="${esc(n.id)}">刪除</button>
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
    <h1>安全</h1>
    <p class="page-subtitle">管理登入密碼、兩步驟驗證、Web 介面的 HTTPS,以及 WireGuard VPN 遠端連線。</p>

    <div class="card">
      <h2>修改密碼</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">目前登入身分:<strong>${esc(me.username)}</strong>。修改成功後,其他裝置上已登入的 session 會全部失效。</p>
      <div id="password-msg"></div>
      <form class="stacked" id="password-form">
        <div class="field"><label>目前密碼</label><input type="password" name="oldPassword" autocomplete="current-password" required></div>
        <div class="field"><label>新密碼(至少 8 個字元)</label><input type="password" name="newPassword" minlength="8" autocomplete="new-password" required></div>
        <div class="field"><label>確認新密碼</label><input type="password" name="confirmPassword" minlength="8" autocomplete="new-password" required></div>
        <div class="btn-row"><button type="submit">更新密碼</button></div>
      </form>
    </div>

    <div class="card" id="totp-card">
      ${renderTOTPSection(me.totpEnabled)}
    </div>

    <div class="card">
      <h2>HTTPS</h2>
      <p style="color:var(--text-dim);font-size:12.5px;margin:0 0 12px">
        目前狀態:<span class="pill ${https.enabled ? "ok" : "neutral"}">${https.enabled ? "已啟用" : "未啟用"}</span>
        ${https.certPath ? ` · 憑證檔案 <code>${esc(https.certPath)}</code>` : ""}
      </p>
      ${https.restartRequiredNotice ? msg("warn", https.restartRequiredNotice) : ""}
      <div id="https-msg"></div>
      <form class="stacked" id="https-form">
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" ${https.enabled ? "checked" : ""}> 啟用 HTTPS</label></div>
        <div class="field">
          <label>憑證主機名稱/IP(每行一個,留空預設 localhost/127.0.0.1)</label>
          <textarea name="hosts" rows="2" placeholder="nas.local&#10;192.168.1.10"></textarea>
          <div class="hint">自簽憑證,瀏覽器第一次連線會顯示不受信任的警告,需要手動選擇繼續/信任。</div>
        </div>
        <div class="btn-row"><button type="submit">儲存 HTTPS 設定</button></div>
      </form>
    </div>

    <div class="card">
      <h2>WireGuard VPN</h2>
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
      box.innerHTML = msg("error", "兩次輸入的新密碼不一致。");
      return;
    }
    try {
      await api.changePassword(f.get("oldPassword"), f.get("newPassword"));
      box.innerHTML = msg("ok", "密碼已更新。");
      ev.target.reset();
    } catch (err) {
      box.innerHTML = msg("error", err.message);
    }
  });
}

function renderTOTPSection(enabled) {
  if (enabled) {
    return `
      <h2>兩步驟驗證(TOTP)</h2>
      <p style="margin:0 0 12px"><span class="pill ok">已啟用</span></p>
      <div id="totp-msg"></div>
      <form class="stacked" id="totp-disable-form">
        <div class="field"><label>目前密碼(停用前需要重新確認)</label><input type="password" name="password" autocomplete="current-password" required></div>
        <div class="btn-row"><button type="submit" class="danger">停用兩步驟驗證</button></div>
      </form>
    `;
  }
  return `
    <h2>兩步驟驗證(TOTP)</h2>
    <p style="margin:0 0 12px"><span class="pill neutral">未啟用</span></p>
    <div id="totp-msg"></div>
    <div id="totp-setup-area">
      <button class="secondary" id="totp-begin-setup">設定兩步驟驗證</button>
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
          <p style="color:var(--text-dim);font-size:12.5px">用驗證器 App(Google Authenticator、Authy 等)手動輸入下面的密鑰,或直接貼上 Provisioning URI(部分 App 支援用文字加入帳號,GoNAS 沒有內建 QR code 產生器)。</p>
          <p><code style="word-break:break-all">${esc(secret)}</code></p>
          <p style="font-size:11.5px;color:var(--text-faint);word-break:break-all">${esc(provisioningUri)}</p>
          <form class="stacked" id="totp-enable-form">
            <div class="field"><label>輸入目前的驗證碼以完成設定</label><input type="text" name="code" inputmode="numeric" pattern="[0-9]*" placeholder="123456" required></div>
            <div class="btn-row"><button type="submit">啟用兩步驟驗證</button></div>
          </form>
        `;
        el.querySelector("#totp-enable-form").addEventListener("submit", async (ev) => {
          ev.preventDefault();
          const f = new FormData(ev.target);
          try {
            await api.totpEnable(f.get("code").trim());
            box.innerHTML = msg("ok", "兩步驟驗證已啟用。");
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
        box.innerHTML = msg("ok", "兩步驟驗證已停用。");
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
      box.innerHTML = msg("ok", "HTTPS 設定已儲存,請重新啟動 gonasd 讓設定生效。");
    } catch (err) {
      box.innerHTML = msg("error", err.message);
    }
  });
}

function renderVPNSection(status, peers) {
  const statusBlock = status.configured ? `
    <p style="margin:0 0 12px">
      <span class="pill ${status.running ? "ok" : "neutral"}">${status.running ? "介面運作中" : "介面未啟用"}</span>
      · 監聽埠 <code>${esc(status.listenPort)}</code>
      · 位址 <code>${esc((status.address || []).join(", "))}</code>
      ${status.publicKey ? ` · 公鑰 <code style="word-break:break-all">${esc(status.publicKey)}</code>` : ""}
    </p>
    ${status.warning ? msg("warn", status.warning) : ""}
  ` : `<p class="empty-state">還沒有設定 WireGuard 介面。</p>`;

  return `
    ${statusBlock}
    <div id="vpn-iface-msg"></div>
    <form class="stacked" id="vpn-iface-form">
      <div class="field"><label>介面位址(CIDR,每行一個)</label><textarea name="address" rows="1" placeholder="10.10.0.1/24">${esc((status.address || []).join("\n"))}</textarea></div>
      <div class="field"><label>監聽埠</label><input type="number" name="listenPort" value="${status.listenPort || 51820}"></div>
      <div class="btn-row"><button type="submit">${status.configured ? "更新介面設定" : "建立 WireGuard 介面"}</button></div>
    </form>

    ${status.configured ? `
      <h2 style="margin-top:24px" id="vpn-peer-count">用戶端(${peers.length})</h2>
      <div id="vpn-peers-list">${renderPeerRows(peers)}</div>
      <div id="vpn-peer-msg"></div>
      <form class="stacked" id="vpn-peer-form" style="margin-top:16px">
        <div class="field"><label>裝置名稱</label><input type="text" name="name" placeholder="我的手機" required></div>
        <div class="field"><label>分配的位址(CIDR,通常是介面網段裡的一個 /32)</label><input type="text" name="allowedIPs" placeholder="10.10.0.2/32" required></div>
        <div class="field"><label>GoNAS 對外位址(選填,寫進產生的用戶端設定檔)</label><input type="text" name="endpoint" placeholder="mynas.example.com:51820"></div>
        <div class="btn-row"><button type="submit">新增用戶端並產生設定檔</button></div>
      </form>
      <div id="vpn-client-config"></div>
    ` : ""}
  `;
}

function renderPeerRows(peers) {
  if (!peers.length) return `<p class="empty-state">還沒有加入任何用戶端裝置。</p>`;
  return peers.map((p) => `
    <div class="rule-row">
      <div class="rule-main">
        <div>
          <div class="rule-name">${esc(p.name)}</div>
          <div class="rule-cond">${esc((p.allowedIPs || []).join(", "))} · <span style="word-break:break-all">${esc(p.publicKey)}</span></div>
        </div>
      </div>
      <button class="secondary" data-del-peer="${esc(p.id)}">刪除</button>
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
      box.innerHTML = msg("ok", "介面設定已儲存。實際套用/停用連線請透過 SSH 手動執行 wg-quick,或等待之後版本補上一鍵套用。");
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
        box.innerHTML = msg("ok", `已新增用戶端「${esc(res.peer.name)}」,下面是它的設定檔內容 —— 只會顯示這一次,請立刻複製或匯入用戶端裝置。`);
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
      if (!confirm("確定要刪除這個用戶端嗎?刪除後該裝置會立刻無法再連線,且無法復原。")) return;
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
  el.querySelector("#vpn-peer-count").textContent = `用戶端(${peers.length})`;
  el.querySelector("#vpn-peers-list").innerHTML = renderPeerRows(peers);
  attachPeerDeleteHandlers(el);
}

// ---------- 備份 ----------

function pad2(n) {
  return String(n).padStart(2, "0");
}

function describeSchedule(sched) {
  return `每 ${sched.everyHours} 小時,從 ${pad2(sched.hourOfDay)}:${pad2(sched.minuteOfHour)} 開始`;
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
    <h1>備份</h1>
    <p class="page-subtitle">用 rsync 加硬連結輪替(跟 rsnapshot、Time Machine 是同一套技巧)把來源目錄備份到另一個位置,保留最近幾份快照;沒有變更的檔案在磁碟上只佔一份空間。需要主機上已安裝 <code>rsync</code>。</p>

    <div class="card">
      <h2>備份工作(${jobs.length})</h2>
      <div id="backup-jobs-list">${renderBackupJobRows(jobs)}</div>
      <div id="backup-msg"></div>
      <form class="stacked" id="backup-form" style="margin-top:16px">
        <div class="field"><label>名稱</label><input type="text" name="name" placeholder="每日備份" required></div>
        <div class="field"><label>來源路徑</label><input type="text" name="sourcePath" placeholder="/mnt/tank/media" required></div>
        <div class="field"><label>目的地路徑</label><input type="text" name="destPath" placeholder="/mnt/backup" required></div>
        <div class="field"><label>保留份數</label><input type="number" name="retentionCount" value="7" min="1" required></div>
        <div class="field"><label>執行間隔(小時)</label><input type="number" name="everyHours" value="24" min="1" required></div>
        <div class="field"><label>起始時刻(小時:分鐘)</label>
          <div style="display:flex;gap:8px">
            <input type="number" name="hourOfDay" value="3" min="0" max="23" style="width:90px" required>
            <input type="number" name="minuteOfHour" value="0" min="0" max="59" style="width:90px" required>
          </div>
        </div>
        <div class="checkbox-row"><label><input type="checkbox" name="enabled" checked> 啟用排程</label></div>
        <div class="btn-row"><button type="submit">新增備份工作</button></div>
      </form>
    </div>
  `;

  attachBackupHandlers(el);
}

function renderBackupJobRows(jobs) {
  if (!jobs.length) return `<p class="empty-state">還沒有設定備份工作。</p>`;
  return jobs.map((j) => renderBackupJobRow(j)).join("");
}

function renderBackupJobRow(j) {
  let statusPill = `<span class="pill neutral">尚未執行</span>`;
  let errorMsg = "";
  if (j.lastRun) {
    if (j.lastRun.success) {
      statusPill = `<span class="pill ok">上次成功 · ${esc(formatDateTime(j.lastRun.finishedAt))}</span>`;
      if (j.lastRun.error) errorMsg = msg("warn", j.lastRun.error);
    } else {
      statusPill = `<span class="pill danger">上次失敗 · ${esc(formatDateTime(j.lastRun.finishedAt))}</span>`;
      errorMsg = msg("error", j.lastRun.error || "未知錯誤");
    }
  }
  return `
    <div class="rule-row" data-job-row="${esc(j.id)}">
      <div class="rule-main">
        <span class="pill ${j.enabled ? "ok" : "neutral"}">${j.enabled ? "已啟用" : "已停用"}</span>
        <div>
          <div class="rule-name">${esc(j.name)}</div>
          <div class="rule-cond">${esc(j.sourcePath)} → ${esc(j.destPath)} · 保留 ${j.retentionCount} 份 · ${esc(describeSchedule(j.schedule))}</div>
        </div>
      </div>
      <div class="btn-row" style="margin:0">
        ${statusPill}
        <button class="secondary" data-run-job="${esc(j.id)}">立即執行</button>
        <button class="secondary" data-view-snapshots="${esc(j.id)}">快照</button>
        <button class="secondary" data-del-job="${esc(j.id)}">刪除</button>
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
      box.innerHTML = msg("ok", "備份工作已新增。");
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
      btn.textContent = "執行中…";
      try {
        const res = await api.runBackupJob(btn.dataset.runJob);
        el.insertAdjacentHTML("afterbegin", msg("ok", res.message));
      } catch (err) {
        el.insertAdjacentHTML("afterbegin", msg("error", err.message));
        btn.disabled = false;
        btn.textContent = "立即執行";
      }
    });
  });

  el.querySelectorAll("[data-del-job]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm("確定要刪除這個備份工作嗎?已經備份好的快照不會被刪除,但排程會停止。")) return;
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
      panel.innerHTML = `<p class="loading">載入中…</p>`;
      try {
        const snapshots = await api.backupJobSnapshots(id);
        panel.innerHTML = snapshots.length
          ? `<ul class="snapshot-list">${snapshots.map((s) => `<li><code>${esc(s.name)}</code> · ${esc(formatDateTime(s.createdAt))}</li>`).join("")}</ul>`
          : `<p class="empty-state">還沒有任何成功的快照。</p>`;
      } catch (err) {
        panel.innerHTML = msg("error", err.message);
      }
    });
  });
}
