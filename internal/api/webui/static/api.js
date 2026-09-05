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
    const message = (data && data.error) ? data.error : `${res.status} ${res.statusText}`;
    throw new Error(message);
  }
  return data;
}

export const api = {
  health: () => request("GET", "/api/v1/health"),
  version: () => request("GET", "/api/v1/version"),

  disks: () => request("GET", "/api/v1/storage/disks"),
  arrayStatus: () => request("GET", "/api/v1/storage/array"),
  setPool: (pool) => request("PUT", "/api/v1/storage/pool", pool),
  startArray: () => request("POST", "/api/v1/storage/array/start"),
  stopArray: () => request("POST", "/api/v1/storage/array/stop"),

  dockerPing: () => request("GET", "/api/v1/docker/ping"),
  containers: () => request("GET", "/api/v1/docker/containers"),
  images: () => request("GET", "/api/v1/docker/images"),
  networks: () => request("GET", "/api/v1/docker/networks"),

  catalog: () => request("GET", "/api/v1/appstore/catalog"),
  installedApps: () => request("GET", "/api/v1/appstore/apps"),
  installApp: (templateId, overrides) => request("POST", "/api/v1/appstore/apps", { templateId, overrides }),
  uninstallApp: (id) => request("DELETE", `/api/v1/appstore/apps/${encodeURIComponent(id)}`),

  shares: () => request("GET", "/api/v1/share/shares"),
  createShare: (share) => request("POST", "/api/v1/share/shares", share),
  deleteShare: (name) => request("DELETE", `/api/v1/share/shares/${encodeURIComponent(name)}`),

  exports: () => request("GET", "/api/v1/share/exports"),
  createExport: (exp) => request("POST", "/api/v1/share/exports", exp),

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

  authStatus: () => request("GET", "/api/v1/auth/status"),
  authSetup: (username, password) => request("POST", "/api/v1/auth/setup", { username, password }),
  authLogin: (username, password, totpCode) => request("POST", "/api/v1/auth/login", { username, password, totpCode }),
  authLogout: () => request("POST", "/api/v1/auth/logout"),
  me: () => request("GET", "/api/v1/auth/me"),
  changePassword: (oldPassword, newPassword) => request("POST", "/api/v1/auth/password", { oldPassword, newPassword }),
  totpSetup: () => request("POST", "/api/v1/auth/totp/setup"),
  totpEnable: (code) => request("POST", "/api/v1/auth/totp/enable", { code }),
  totpDisable: (password) => request("POST", "/api/v1/auth/totp/disable", { password }),

  httpsSettings: () => request("GET", "/api/v1/security/https"),
  setHTTPSSettings: (settings) => request("PUT", "/api/v1/security/https", settings),

  vpnStatus: () => request("GET", "/api/v1/vpn/status"),
  setVPNInterface: (iface) => request("PUT", "/api/v1/vpn/interface", iface),
  vpnPeers: () => request("GET", "/api/v1/vpn/peers"),
  addVPNPeer: (peer) => request("POST", "/api/v1/vpn/peers", peer),
  deleteVPNPeer: (id) => request("DELETE", `/api/v1/vpn/peers/${encodeURIComponent(id)}`),
};
