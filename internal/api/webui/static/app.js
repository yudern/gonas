import { api } from "/api.js";

const content = document.getElementById("content");
const navLinks = document.querySelectorAll(".nav-list a");

const routes = {
  dashboard: renderDashboard,
  storage: renderStorage,
  apps: renderApps,
  shares: renderShares,
  users: renderUsers,
};

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
window.addEventListener("DOMContentLoaded", () => {
  router();
  api.version().then((v) => {
    document.getElementById("sidebar-version").textContent = `gonasd ${v.version} (${v.goos}/${v.goarch})`;
  }).catch(() => {});
});

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
      <p style="color:var(--text-dim);font-size:13px;margin:0">前往「<a href="#/storage">儲存</a>」設定並啟動陣列、「<a href="#/apps">應用程式</a>」安裝服務、「<a href="#/shares">共享</a>」設定 SMB/NFS,或「<a href="#/users">使用者</a>」管理帳號。</p>
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
