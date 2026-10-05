// 對 GoNAS REST API 的薄封裝。刻意不用任何框架（fetch 是瀏覽器內建的),
// 因為這個開發環境沒有辦法拉 npm 套件建置前端(見 gonasd 專案 README「已知
// 取捨」),而且對一個要在使用者自己 NAS 上跑的管理介面來說,不依賴建置
// 工具鏈、瀏覽器打開就能動,其實才是正確的方向。

// onUnauthorized 是全域的「session 失效」回呼:app.js 在啟動時註冊一個
// 會顯示登入畫面的函式。放在 api.js 這一層統一攔截 401,而不是要求每個
// 呼叫端(render 函式)各自判斷「這個錯誤是不是代表要登入」,是因為
// session 過期可能發生在任何一支 API 呼叫上,不該讓每支呼叫端都重複這段
// 邏輯。/api/v1/auth/* 本身的 401(例如密碼打錯)刻意不觸發這個回呼 ——
// 那是登入表單自己要處理、顯示在表單上的錯誤訊息,不是「session 失效,
// 帶使用者回登入畫面」。
import { translateError, t } from "/i18n.js";

let onUnauthorized = null;
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

async function request(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);

  if (res.status === 401 && onUnauthorized && !path.startsWith("/api/v1/auth/")) {
    onUnauthorized();
  }

  if (res.status === 204) return null;

  let data = null;
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch { data = text; }
  }

  if (!res.ok) {
    // 後端固定回英文錯誤訊息(見 internal/api/errors.go 的說明),
    // translateError 把裡面「乾淨、可枚舉」的固定訊息翻成目前介面語言,
    // 查不到的動態/技術性訊息就原樣顯示英文,見 i18n.js 的說明。
    const message = (data && data.error) ? translateError(data.error) : `${res.status} ${res.statusText}`;
    throw new Error(message);
  }
  return data;
}

export const api = {
  health: () => request("GET", "/api/v1/health"),
  version: () => request("GET", "/api/v1/version"),

  disks: () => request("GET", "/api/v1/storage/disks"),
  disksSmart: () => request("GET", "/api/v1/storage/disks/smart"),
  prepareDisk: (device, mountPoint) => request("POST", "/api/v1/storage/disks/prepare", { device, mountPoint }),

  powerShutdown: () => request("POST", "/api/v1/system/power/shutdown"),
  powerReboot: () => request("POST", "/api/v1/system/power/reboot"),

  doctorStatus: () => request("GET", "/api/v1/system/doctor"),
  doctorInstall: (apt) => request("POST", "/api/v1/system/doctor/install", { apt }),

  upsStatus: () => request("GET", "/api/v1/ups/status"),
  upsList: () => request("GET", "/api/v1/ups/list"),
  upsConfig: () => request("GET", "/api/v1/ups/config"),
  setUpsConfig: (cfg) => request("PUT", "/api/v1/ups/config", cfg),
  arrayStatus: () => request("GET", "/api/v1/storage/array"),
  setPool: (pool) => request("PUT", "/api/v1/storage/pool", pool),
  startArray: () => request("POST", "/api/v1/storage/array/start"),
  stopArray: () => request("POST", "/api/v1/storage/array/stop"),
  syncArray: () => request("POST", "/api/v1/storage/array/sync"),
  scrubArray: () => request("POST", "/api/v1/storage/array/scrub"),
  fixDisk: (mountPoint) => request("POST", "/api/v1/storage/array/fix", { mountPoint }),
  paritySchedule: () => request("GET", "/api/v1/storage/parity/schedule"),
  setParitySchedule: (cfg) => request("PUT", "/api/v1/storage/parity/schedule", cfg),
  smartSchedule: () => request("GET", "/api/v1/storage/smart/schedule"),
  setSmartSchedule: (cfg) => request("PUT", "/api/v1/storage/smart/schedule", cfg),
  runSmartTest: (kind) => request("POST", "/api/v1/storage/smart/test", { kind }),
  smartSelfTestLog: (device) => request("GET", "/api/v1/storage/smart/selftest-log?device=" + encodeURIComponent(device)),

  dockerPing: () => request("GET", "/api/v1/docker/ping"),
  registryConfig: () => request("GET", "/api/v1/docker/registry-config"),
  setRegistryConfig: (registryMirrors, insecureRegistries) => request("PUT", "/api/v1/docker/registry-config", { registryMirrors, insecureRegistries }),
  containers: () => request("GET", "/api/v1/docker/containers"),
  images: () => request("GET", "/api/v1/docker/images"),
  removeImage: (id) => request("DELETE", `/api/v1/docker/images/${encodeURIComponent(id)}`),
  pruneImages: () => request("POST", "/api/v1/docker/images/prune"),
  networks: () => request("GET", "/api/v1/docker/networks"),
  containerLogs: (id, tail) => request("GET", `/api/v1/docker/containers/${encodeURIComponent(id)}/logs${tail ? `?tail=${encodeURIComponent(tail)}` : ""}`),
  containerExec: (id, cmd) => request("POST", `/api/v1/docker/containers/${encodeURIComponent(id)}/exec`, { cmd }),
  containerStart: (id) => request("POST", `/api/v1/docker/containers/${encodeURIComponent(id)}/start`),
  containerStop: (id) => request("POST", `/api/v1/docker/containers/${encodeURIComponent(id)}/stop`),
  containerRestart: (id) => request("POST", `/api/v1/docker/containers/${encodeURIComponent(id)}/restart`),
  containerStats: (id) => request("GET", `/api/v1/docker/containers/${encodeURIComponent(id)}/stats`),
  containerRemove: (id) => request("DELETE", `/api/v1/docker/containers/${encodeURIComponent(id)}`),

  catalog: () => request("GET", "/api/v1/appstore/catalog"),
  catalogSource: () => request("GET", "/api/v1/appstore/catalog/source"),
  setCatalogSource: (url) => request("PUT", "/api/v1/appstore/catalog/source", { url }),
  refreshCatalog: () => request("POST", "/api/v1/appstore/catalog/refresh"),
  installedApps: () => request("GET", "/api/v1/appstore/apps"),
  installApp: (templateId, overrides) => request("POST", "/api/v1/appstore/apps", { templateId, overrides }),
  installCustomApp: (template) => request("POST", "/api/v1/appstore/apps", { template }),
  uninstallApp: (id) => request("DELETE", `/api/v1/appstore/apps/${encodeURIComponent(id)}`),
  updateApp: (id) => request("POST", `/api/v1/appstore/apps/${encodeURIComponent(id)}/update`),
  editApp: (id, overrides) => request("PUT", `/api/v1/appstore/apps/${encodeURIComponent(id)}`, { overrides }),
  appOpStatus: () => request("GET", "/api/v1/appstore/op-status"),

  shares: () => request("GET", "/api/v1/share/shares"),
  createShare: (share) => request("POST", "/api/v1/share/shares", share),
  deleteShare: (name) => request("DELETE", `/api/v1/share/shares/${encodeURIComponent(name)}`),

  exports: () => request("GET", "/api/v1/share/exports"),
  createExport: (exp) => request("POST", "/api/v1/share/exports", exp),
  deleteExport: (path) => request("DELETE", "/api/v1/share/exports?path=" + encodeURIComponent(path)),

  users: () => request("GET", "/api/v1/share/users"),
  createUser: (user) => request("POST", "/api/v1/share/users", user),
  deleteUser: (username) => request("DELETE", `/api/v1/share/users/${encodeURIComponent(username)}`),

  monitorSystem: () => request("GET", "/api/v1/monitor/system"),
  monitorHistory: () => request("GET", "/api/v1/monitor/history"),

  alertRules: () => request("GET", "/api/v1/monitor/alerts"),
  createAlertRule: (rule) => request("POST", "/api/v1/monitor/alerts", rule),
  deleteAlertRule: (id) => request("DELETE", `/api/v1/monitor/alerts/${encodeURIComponent(id)}`),

  notifiers: () => request("GET", "/api/v1/monitor/notifiers"),
  createNotifier: (notifier) => request("POST", "/api/v1/monitor/notifiers", notifier),
  deleteNotifier: (id) => request("DELETE", `/api/v1/monitor/notifiers/${encodeURIComponent(id)}`),
  emailNotifiers: () => request("GET", "/api/v1/monitor/email-notifiers"),
  createEmailNotifier: (notifier) => request("POST", "/api/v1/monitor/email-notifiers", notifier),
  deleteEmailNotifier: (id) => request("DELETE", `/api/v1/monitor/email-notifiers/${encodeURIComponent(id)}`),

  digest: () => request("GET", "/api/v1/monitor/digest"),
  setDigest: (cfg) => request("PUT", "/api/v1/monitor/digest", cfg),
  sendDigestNow: () => request("POST", "/api/v1/monitor/digest/send"),

  authStatus: () => request("GET", "/api/v1/auth/status"),
  authSetup: (username, password) => request("POST", "/api/v1/auth/setup", { username, password }),
  authLogin: (username, password, totpCode) => request("POST", "/api/v1/auth/login", { username, password, totpCode }),
  authLogout: () => request("POST", "/api/v1/auth/logout"),
  me: () => request("GET", "/api/v1/auth/me"),
  changePassword: (oldPassword, newPassword) => request("POST", "/api/v1/auth/password", { oldPassword, newPassword }),
  totpSetup: () => request("POST", "/api/v1/auth/totp/setup"),
  totpEnable: (code) => request("POST", "/api/v1/auth/totp/enable", { code }),
  totpDisable: (password) => request("POST", "/api/v1/auth/totp/disable", { password }),
  totpRegenerateRecoveryCodes: (password) => request("POST", "/api/v1/auth/totp/recovery-codes", { password }),
  resetAccountTOTP: (username) => request("POST", `/api/v1/auth/accounts/${encodeURIComponent(username)}/reset-totp`),
  authAccounts: () => request("GET", "/api/v1/auth/accounts"),
  createAuthAccount: (account) => request("POST", "/api/v1/auth/accounts", account),
  deleteAuthAccount: (username) => request("DELETE", `/api/v1/auth/accounts/${encodeURIComponent(username)}`),
  auditLog: () => request("GET", "/api/v1/audit/log"),

  httpsSettings: () => request("GET", "/api/v1/security/https"),
  setHTTPSSettings: (settings) => request("PUT", "/api/v1/security/https", settings),

  vpnStatus: () => request("GET", "/api/v1/vpn/status"),
  setVPNInterface: (iface) => request("PUT", "/api/v1/vpn/interface", iface),
  vpnPeers: () => request("GET", "/api/v1/vpn/peers"),
  addVPNPeer: (peer) => request("POST", "/api/v1/vpn/peers", peer),
  deleteVPNPeer: (id) => request("DELETE", `/api/v1/vpn/peers/${encodeURIComponent(id)}`),

  backupJobs: () => request("GET", "/api/v1/backup/jobs"),
  createBackupJob: (job) => request("POST", "/api/v1/backup/jobs", job),
  deleteBackupJob: (id) => request("DELETE", `/api/v1/backup/jobs/${encodeURIComponent(id)}`),
  runBackupJob: (id) => request("POST", `/api/v1/backup/jobs/${encodeURIComponent(id)}/run`),
  backupJobSnapshots: (id) => request("GET", `/api/v1/backup/jobs/${encodeURIComponent(id)}/snapshots`),
  backupJobRestore: (id, snapshot, targetPath) => request("POST", `/api/v1/backup/jobs/${encodeURIComponent(id)}/restore`, { snapshot, targetPath }),

  filesStatus: () => request("GET", "/api/v1/files/status"),
  filesList: (path) => request("GET", `/api/v1/files/list?path=${encodeURIComponent(path)}`),
  filesMkdir: (path) => request("POST", "/api/v1/files/mkdir", { path }),
  filesMove: (from, to) => request("POST", "/api/v1/files/move", { from, to }),
  filesCopy: (from, to) => request("POST", "/api/v1/files/copy", { from, to }),
  filesDelete: (path, permanent) => request("DELETE", `/api/v1/files/item?path=${encodeURIComponent(path)}${permanent ? "&permanent=true" : ""}`),
  filesDownloadURL: (path) => `/api/v1/files/download?path=${encodeURIComponent(path)}`,
  filesDownloadZipURL: (path) => `/api/v1/files/download-zip?path=${encodeURIComponent(path)}`,
  filesSearch: (path, q) => request("GET", `/api/v1/files/search?path=${encodeURIComponent(path)}&q=${encodeURIComponent(q)}`),
  filesReadText: (path) => request("GET", `/api/v1/files/text?path=${encodeURIComponent(path)}`),
  filesWriteText: (path, content) => request("PUT", `/api/v1/files/text?path=${encodeURIComponent(path)}`, { content }),
  filesUpload: (dirPath, formData, onProgress) => uploadWithProgress(`/api/v1/files/upload?path=${encodeURIComponent(dirPath)}`, formData, onProgress),
  filesTrashList: () => request("GET", "/api/v1/files/trash"),
  filesTrashRestore: (id) => request("POST", `/api/v1/files/trash/${encodeURIComponent(id)}/restore`),
  filesTrashDeleteItem: (id) => request("DELETE", `/api/v1/files/trash/${encodeURIComponent(id)}`),
  filesTrashEmpty: () => request("POST", "/api/v1/files/trash/empty"),

  systemUpdate: () => request("GET", "/api/v1/system/update"),
  setSystemUpdateSettings: (manifestUrl) => request("PUT", "/api/v1/system/update/settings", { manifestUrl }),
  checkSystemUpdate: () => request("POST", "/api/v1/system/update/check"),
  applySystemUpdate: () => request("POST", "/api/v1/system/update/apply"),
  rollbackSystemUpdate: () => request("POST", "/api/v1/system/update/rollback"),
  // 離線上傳更新:直接把一份 gonasd 執行檔 POST 上去(multipart),後端驗證
  // 後替換自己並重啟。沿用 uploadWithProgress(XHR + 進度),執行檔十幾 MB,
  // 讓使用者看得到上傳進度。
  uploadSystemUpdate: (formData, onProgress) => uploadWithProgress("/api/v1/system/update/upload", formData, onProgress),
};

// uploadWithProgress 用 XMLHttpRequest 而不是 fetch 送出上傳請求——這是
// 目前瀏覽器原生 API 裡唯一能拿到「已經傳了多少 byte」進度事件的方式
// (fetch 的 body 上傳目前還沒有標準化的進度回呼),對「上傳一個幾百 MB
// 的檔案」這種可能要等好一段時間的操作，讓使用者看得到進度條而不是
// 對著一個沒有任何回饋的畫面乾等，是基本的可用性要求。
function uploadWithProgress(path, formData, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", path);
    if (onProgress) {
      xhr.upload.addEventListener("progress", (ev) => {
        if (ev.lengthComputable) onProgress(ev.loaded / ev.total);
      });
    }
    xhr.onload = () => {
      let data = null;
      try { data = JSON.parse(xhr.responseText); } catch { /* 非 JSON 回應忽略 */ }
      if (xhr.status === 401 && onUnauthorized) onUnauthorized();
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data);
      } else {
        reject(new Error((data && data.error) ? data.error : `${xhr.status} ${xhr.statusText}`));
      }
    };
    xhr.onerror = () => reject(new Error(t("files.uploadNetworkError")));
    xhr.send(formData);
  });
}
