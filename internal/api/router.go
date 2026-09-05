// Package api 提供 GoNAS 對外的 REST API,以及(Phase 4 起)內嵌的 Web
// 管理介面靜態檔案。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/version"
)

// maxRequestBodyBytes 是任何一支 API 端點願意讀取的請求 body 上限。
// GoNAS 的請求全部是小型 JSON(設定值、表單欄位),1 MiB 已經非常寬裕
// ——真正大量的資料(檔案本身、Docker 映像層)從來不會透過這層 JSON API
// 傳輸,而是分別交給 Samba/NFS/Docker Engine 直接處理。設這個上限主要
// 是擋掉「忘記設 Content-Length、body 送個沒完」或惡意送超大 body 想
// 撐爆記憶體的請求,不是為了限制正常使用情境。
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// loginRateLimitMaxFailures / loginRateLimitLockout 是登入節流的門檻:
// 同一個來源 IP 連續 5 次登入失敗(帳號、密碼、TOTP 驗證碼算同一組
// 「登入失敗」,不細分)之後鎖定 5 分鐘。5 次/5 分鐘對真人打錯密碼
// 的容錯空間足夠(打錯一兩次很常見),但足以讓「每秒嘗試數十次」等級的
// 暴力破解在有意義的時間內幾乎不可能撞出密碼,細節見
// internal/security.LoginLimiter 的套件註解。
const (
	loginRateLimitMaxFailures = 5
	loginRateLimitLockout     = 5 * time.Minute
)

// monitorPollInterval 是系統資源取樣的週期。10 秒對一台 NAS 的監控用途
// 已經夠即時(不是給高頻交易系統用的),又不會因為太頻繁而讓 History 的
// 時間跨度太短、或是不必要地增加 SMART 檢查(見 monitor_handlers.go 的
// anyDiskSmartFailed)對硬碟的存取次數。
//
// monitorHistoryCapacity 搭配上面的間隔,保留最近 30 分鐘的走勢
// (180 * 10s = 1800s)給 Web UI 畫圖表 —— 這個時間長度足夠讓使用者看出
// 「剛剛发生了什麼」，daemon 重啟後從頭累積是可接受的(見 monitor.History
// 的套件註解)。
const (
	monitorPollInterval    = 10 * time.Second
	monitorHistoryCapacity = 180
)

// Server 持有建立路由所需的共用依賴。
type Server struct {
	logger    *slog.Logger
	startedAt time.Time
	runner    storage.Runner
	docker    *docker.Client
	store     *state.Store

	// dataDir 是 state.json 所在的目錄,同時也是 TLS 憑證(tls/)、
	// WireGuard 設定檔(wireguard/)這些「GoNAS 自己產生、不是使用者
	// 手動維護」的檔案的存放位置的共同父目錄 —— 全部集中在一處,方便
	// 備份/搬遷整台 NAS 的設定時只需要打包這一個目錄。
	dataDir string

	// sessions 管理 Web 管理介面的登入 session(不是 SMB/NFS 那種檔案
	// 存取的憑證)。刻意用純記憶體實作(見 internal/security.SessionManager
	// 的套件註解):daemon 重啟後所有人都要重新登入,對一個家用 NAS 來說
	// 是可以接受的代價,換來不用另外設計 session 的持久化/加密儲存。
	sessions *security.SessionManager

	// loginLimiter 節流 /api/v1/auth/login 的失敗嘗試次數,擋暴力猜密碼
	// 攻擊,見 internal/security.LoginLimiter 的套件註解與這個檔案裡
	// loginRateLimit* 常數的說明。
	loginLimiter *security.LoginLimiter

	// array 是目前載入的儲存陣列。啟動時如果 store 裡已經有 pool 設定,
	// 會在這裡建立對應的 *storage.Array(但不會自動 Start —— 掛載陣列
	// 是使用者的明確動作,不該在 daemon 重啟時靜默發生)。
	array *storage.Array

	monitorCollector *monitor.Collector
	monitorHistory   *monitor.History
	alertEngine      *monitor.AlertEngine
	monitorPoller    *monitor.Poller

	// backupMu 保護 backupSchedulers —— 這個 map 本身不是持久化狀態的一
	// 部分(排程 goroutine 的控制代碼不能序列化進 state.json),所以需要
	// 自己的鎖,不能沿用 state.Store 內部的鎖。
	backupMu         sync.Mutex
	backupSchedulers map[string]*backup.JobScheduler
}

// New 建立一個 Server,從 dataDir/state.json 載入既有狀態,並回傳已掛好
// 所有路由的 http.Handler。
func New(logger *slog.Logger, dataDir string) (*Server, http.Handler, error) {
	store, err := state.Open(dataDir + "/state.json")
	if err != nil {
		return nil, nil, err
	}

	s := &Server{
		logger:           logger,
		startedAt:        time.Now(),
		runner:           storage.NewExecRunner(),
		docker:           docker.NewClient(""),
		store:            store,
		dataDir:          dataDir,
		sessions:         security.NewSessionManager(sessionTTL),
		loginLimiter:     security.NewLoginLimiter(loginRateLimitMaxFailures, loginRateLimitLockout),
		backupSchedulers: make(map[string]*backup.JobScheduler),
	}

	// 監控用的磁碟路徑預設是 "/"(還沒設定 pool 前至少能看到系統碟的
	// 使用率);使用者設定/變更 pool 之後,handleStoragePoolSet 會呼叫
	// s.monitorCollector.SetDiskPath 換成陣列的掛載點。
	diskPath := "/"
	if pool := store.Snapshot().Pool; pool != nil {
		s.array = storage.NewArray(*pool)
		diskPath = pool.MountPoint
	}

	s.monitorCollector = monitor.NewCollector(diskPath)
	s.monitorHistory = monitor.NewHistory(monitorHistoryCapacity)
	s.alertEngine = monitor.NewAlertEngine(logger)
	s.rebuildNotifier() // 從 state 讀回既有的 webhook 設定,一併掛上保底的 LogNotifier

	s.monitorPoller = monitor.NewPoller(logger, s.monitorCollector, s.monitorHistory, monitorPollInterval, s.onMonitorSample)
	s.monitorPoller.Start(context.Background())

	// 啟動時把既有的、標記成 Enabled 的備份工作重新掛回排程 —— daemon
	// 重啟不該讓使用者原本設定好的排程默默停擺,得手動重新觸發一次才會
	// 發現。跟 monitor 的 AlertEngine/Notifier 不同,備份排程沒有「重建」
	// 的概念(每個 Job 都是獨立的 goroutine),所以這裡直接逐一 Start。
	for _, job := range store.Snapshot().BackupJobs {
		if job.Enabled {
			s.startBackupScheduler(job)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

	// Phase 6 起,除了健康檢查/版本資訊,以及登入流程本身需要的三支
	// 端點(不然使用者會被鎖在「需要登入才能查詢要不要登入」的雞生蛋
	// 問題裡)之外,其餘所有 API 都包一層 requireAuth。哪支端點公開、
	// 哪支需要登入,從這裡的路由註冊就能一眼看完,不用逐一打開每支
	// handler 檔案確認。
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleAuthLogin)
	// 下面這五支「管理自己帳號」的端點刻意繼續只用 requireAuth,不用
	// requireAdmin——登出、改自己的密碼、設定/啟用/停用自己的 TOTP,
	// RoleViewer 的帳號也該能做,那不算「管理 NAS 設定」,見
	// auth_handlers.go 的 requireAdmin 函式註解。
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.handleAuthLogout))
	mux.HandleFunc("GET /api/v1/auth/me", s.requireAuth(s.handleAuthMe))
	mux.HandleFunc("POST /api/v1/auth/password", s.requireAuth(s.handleAuthChangePassword))
	mux.HandleFunc("POST /api/v1/auth/totp/setup", s.requireAuth(s.handleAuthTOTPSetup))
	mux.HandleFunc("POST /api/v1/auth/totp/enable", s.requireAuth(s.handleAuthTOTPEnable))
	mux.HandleFunc("POST /api/v1/auth/totp/disable", s.requireAuth(s.handleAuthTOTPDisable))
	// 帳號管理(新增/刪除「其他」帳號、列出所有帳號)才是真正「管理 NAS」
	// 的動作,一律要求 RoleAdmin。
	mux.HandleFunc("GET /api/v1/auth/accounts", s.requireAdmin(s.handleAuthAccountsList))
	mux.HandleFunc("POST /api/v1/auth/accounts", s.requireAdmin(s.handleAuthAccountsCreate))
	mux.HandleFunc("DELETE /api/v1/auth/accounts/{username}", s.requireAdmin(s.handleAuthAccountsDelete))

	mux.HandleFunc("GET /api/v1/security/https", s.requireAuth(s.handleSecurityHTTPSGet))
	mux.HandleFunc("PUT /api/v1/security/https", s.requireAdmin(s.handleSecurityHTTPSSet))

	mux.HandleFunc("GET /api/v1/vpn/status", s.requireAuth(s.handleVPNStatus))
	mux.HandleFunc("PUT /api/v1/vpn/interface", s.requireAdmin(s.handleVPNInterfaceSet))
	mux.HandleFunc("GET /api/v1/vpn/peers", s.requireAuth(s.handleVPNPeersList))
	mux.HandleFunc("POST /api/v1/vpn/peers", s.requireAdmin(s.handleVPNPeerAdd))
	mux.HandleFunc("DELETE /api/v1/vpn/peers/{id}", s.requireAdmin(s.handleVPNPeerDelete))

	mux.HandleFunc("GET /api/v1/storage/disks", s.requireAuth(s.handleStorageDisks))
	mux.HandleFunc("GET /api/v1/storage/disks/smart", s.requireAuth(s.handleStorageDisksSmart))
	mux.HandleFunc("GET /api/v1/storage/array", s.requireAuth(s.handleStorageArrayStatus))
	mux.HandleFunc("PUT /api/v1/storage/pool", s.requireAdmin(s.handleStoragePoolSet))
	mux.HandleFunc("POST /api/v1/storage/array/start", s.requireAdmin(s.handleStorageArrayStart))
	mux.HandleFunc("POST /api/v1/storage/array/stop", s.requireAdmin(s.handleStorageArrayStop))

	mux.HandleFunc("GET /api/v1/docker/ping", s.requireAuth(s.handleDockerPing))
	mux.HandleFunc("GET /api/v1/docker/containers", s.requireAuth(s.handleDockerContainers))
	mux.HandleFunc("GET /api/v1/docker/images", s.requireAuth(s.handleDockerImages))
	mux.HandleFunc("GET /api/v1/docker/networks", s.requireAuth(s.handleDockerNetworks))
	mux.HandleFunc("GET /api/v1/docker/containers/{id}/logs", s.requireAuth(s.handleContainerLogs))
	mux.HandleFunc("POST /api/v1/docker/containers/{id}/exec", s.requireAdmin(s.handleContainerExec))

	mux.HandleFunc("GET /api/v1/appstore/catalog", s.requireAuth(s.handleAppstoreCatalog))
	mux.HandleFunc("GET /api/v1/appstore/apps", s.requireAuth(s.handleAppstoreListInstalled))
	mux.HandleFunc("POST /api/v1/appstore/apps", s.requireAdmin(s.handleAppstoreInstall))
	mux.HandleFunc("DELETE /api/v1/appstore/apps/{id}", s.requireAdmin(s.handleAppstoreUninstall))

	mux.HandleFunc("GET /api/v1/share/shares", s.requireAuth(s.handleSharesList))
	mux.HandleFunc("POST /api/v1/share/shares", s.requireAdmin(s.handleSharesCreate))
	mux.HandleFunc("DELETE /api/v1/share/shares/{name}", s.requireAdmin(s.handleSharesDelete))
	mux.HandleFunc("GET /api/v1/share/exports", s.requireAuth(s.handleExportsList))
	mux.HandleFunc("POST /api/v1/share/exports", s.requireAdmin(s.handleExportsCreate))
	mux.HandleFunc("GET /api/v1/share/users", s.requireAuth(s.handleUsersList))
	mux.HandleFunc("POST /api/v1/share/users", s.requireAdmin(s.handleUsersCreate))
	mux.HandleFunc("DELETE /api/v1/share/users/{username}", s.requireAdmin(s.handleUsersDelete))

	mux.HandleFunc("GET /api/v1/monitor/system", s.requireAuth(s.handleMonitorSystem))
	mux.HandleFunc("GET /api/v1/monitor/history", s.requireAuth(s.handleMonitorHistory))
	mux.HandleFunc("GET /api/v1/monitor/alerts", s.requireAuth(s.handleMonitorAlertsList))
	mux.HandleFunc("POST /api/v1/monitor/alerts", s.requireAdmin(s.handleMonitorAlertsCreate))
	mux.HandleFunc("DELETE /api/v1/monitor/alerts/{id}", s.requireAdmin(s.handleMonitorAlertsDelete))
	mux.HandleFunc("GET /api/v1/monitor/notifiers", s.requireAuth(s.handleMonitorNotifiersList))
	mux.HandleFunc("POST /api/v1/monitor/notifiers", s.requireAdmin(s.handleMonitorNotifiersCreate))
	mux.HandleFunc("DELETE /api/v1/monitor/notifiers/{id}", s.requireAdmin(s.handleMonitorNotifiersDelete))

	mux.HandleFunc("GET /api/v1/backup/jobs", s.requireAuth(s.handleBackupJobsList))
	mux.HandleFunc("POST /api/v1/backup/jobs", s.requireAdmin(s.handleBackupJobsCreate))
	mux.HandleFunc("DELETE /api/v1/backup/jobs/{id}", s.requireAdmin(s.handleBackupJobsDelete))
	mux.HandleFunc("POST /api/v1/backup/jobs/{id}/run", s.requireAdmin(s.handleBackupJobsRun))
	mux.HandleFunc("GET /api/v1/backup/jobs/{id}/snapshots", s.requireAuth(s.handleBackupJobsSnapshots))

	mux.HandleFunc("GET /api/v1/files/status", s.requireAuth(s.handleFilesStatus))
	mux.HandleFunc("GET /api/v1/files/list", s.requireAuth(s.handleFilesList))
	mux.HandleFunc("POST /api/v1/files/mkdir", s.requireAdmin(s.handleFilesMkdir))
	mux.HandleFunc("POST /api/v1/files/move", s.requireAdmin(s.handleFilesMove))
	mux.HandleFunc("POST /api/v1/files/copy", s.requireAdmin(s.handleFilesCopy))
	mux.HandleFunc("DELETE /api/v1/files/item", s.requireAdmin(s.handleFilesDelete))
	mux.HandleFunc("GET /api/v1/files/download", s.requireAuth(s.handleFilesDownload))
	mux.HandleFunc("GET /api/v1/files/download-zip", s.requireAuth(s.handleFilesDownloadZip))
	mux.HandleFunc("GET /api/v1/files/search", s.requireAuth(s.handleFilesSearch))
	mux.HandleFunc("GET /api/v1/files/text", s.requireAuth(s.handleFilesReadText))
	mux.HandleFunc("PUT /api/v1/files/text", s.requireAdmin(s.handleFilesWriteText))
	mux.HandleFunc("POST /api/v1/files/upload", s.requireAdmin(s.handleFilesUpload))
	mux.HandleFunc("GET /api/v1/files/trash", s.requireAuth(s.handleFilesTrashList))
	mux.HandleFunc("POST /api/v1/files/trash/{id}/restore", s.requireAdmin(s.handleFilesTrashRestore))
	mux.HandleFunc("DELETE /api/v1/files/trash/{id}", s.requireAdmin(s.handleFilesTrashDeleteItem))
	mux.HandleFunc("POST /api/v1/files/trash/empty", s.requireAdmin(s.handleFilesTrashEmpty))

	mux.Handle("/", webUIHandler())

	// 中介層順序由外而內: withLogging(最外層,不管中間發生什麼都要記錄
	// 這筆請求) -> withSecurityHeaders(連錯誤回應、panic 復原後的 500
	// 都該帶上這些標頭) -> withRecover(包在最裡層、直接包住 mux,任何
	// handler 裡的 panic 都在這裡被攔下來,轉成一個乾淨的 500 回應,
	// 而不是讓整個 daemon 因為一個請求的未預期錯誤而崩潰 —— 見
	// withRecover 的函式註解)。
	handler := withLogging(logger, withSecurityHeaders(withRecover(logger, mux)))

	return s, handler, nil
}

// HTTPSConfig 回傳目前的 HTTPS 設定，讓 cmd/gonasd/main.go 在啟動時決定
// 要監聽 HTTP 還是 HTTPS。獨立成一個方法而不是讓 main.go 直接戳
// s.store.Snapshot().HTTPS，是為了不讓 cmd/gonasd 需要知道 Server 內部
// 是用 state.Store 儲存設定這件事 —— main.go 只依賴 api.Server 這一層
// 抽象,之後如果設定儲存方式改變,main.go 完全不用跟著改。
func (s *Server) HTTPSConfig() state.HTTPSConfig {
	return s.store.Snapshot().HTTPS
}

// Close 釋放 Server 持有的背景資源，目前就是停掉監控輪詢的 goroutine。
// 拆成獨立方法而不是讓呼叫端自己去戳 monitorPoller,是為了讓
// cmd/gonasd/main.go 的優雅關閉流程不需要知道 Server 內部是用 Poller
// 實作監控 —— 之後這裡要多停別的背景工作,呼叫端完全不用改。
func (s *Server) Close() {
	if s.monitorPoller != nil {
		s.monitorPoller.Stop()
	}

	s.backupMu.Lock()
	schedulers := s.backupSchedulers
	s.backupSchedulers = make(map[string]*backup.JobScheduler)
	s.backupMu.Unlock()
	for _, sched := range schedulers {
		sched.Stop()
	}
}

// fileManagerRoot 回傳目前檔案管理員該用的根目錄(陣列的 mergerFS
// 掛載點),或是一個說明原因的錯誤——還沒設定 pool、或設定了但陣列還
// 沒啟動,都是使用者操作順序上的合理狀態，不是伺服器錯誤,所以用
// sentinel 錯誤讓呼叫端對應到 409 Conflict，而不是 500。
//
// 檔案管理員刻意只服務「目前這個陣列的掛載點」這一個根目錄,不是每個
// Samba 分享各自一個根——GoNAS 目前的架構本來就只支援單一個 pool
// (state.State.Pool 是單一指標,不是陣列),分享路徑照慣例都是掛載點
// 底下的子目錄，用掛載點當根已經涵蓋得到所有分享，同時避免「這個根
// 目錄到底對應哪個分享」的額外心智負擔。
func (s *Server) fileManagerRoot() (string, error) {
	pool := s.store.Snapshot().Pool
	if pool == nil || s.array == nil {
		return "", errNoPoolConfigured
	}
	if s.array.Status().State != storage.StateStarted {
		return "", errArrayNotStarted
	}
	return pool.MountPoint, nil
}

type healthResponse struct {
	Status    string `json:"status"`
	UptimeSec int64  `json:"uptimeSeconds"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:    "ok",
		UptimeSec: int64(time.Since(s.startedAt).Seconds()),
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, version.Info{
		Version:   version.Version,
		Commit:    version.Commit,
		BuildDate: version.BuildDate,
		GoOS:      runtime.GOOS,
		GoArch:    runtime.GOARCH,
	})
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

// readJSON 把請求 body 解析進 dst,失敗時回傳一個已經寫好 400 的錯誤,
// 呼叫端只需要判斷 ok 就好。用 http.MaxBytesReader 包住 body 是為了擋掉
// 異常肥大(或忘記帶 Content-Length、body 送個沒完)的請求撐爆記憶體,
// 見 maxRequestBodyBytes 常數的說明 —— 超過上限時 Decode 會回傳一個
// 「http: request body too large」的錯誤,一樣落在下面這個 400 分支,
// 呼叫端不需要特別處理。
func readJSON(w http.ResponseWriter, r *http.Request, dst any) (ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// withLogging 是一個最小的存取記錄中介層。放在中介層鏈最外層,這樣
// 不管請求最後是正常回應、handler 主動回傳的錯誤,還是被 withRecover
// 攔下來的 panic,都會被記到同一行 log 裡,方便事後從 log 追一支請求
// 的完整生命週期,不用比對好幾層不同中介層各自留下的紀錄。
func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

// withSecurityHeaders 幫每一個回應加上幾個標準的瀏覽器安全標頭。GoNAS
// 的內嵌 Web UI 全部是同源請求(fetch 打的都是相對路徑、沒有外部 CDN
// 依賴、也沒有內嵌的 <script>/<style> 或行內 style 屬性 —— 見
// internal/api/webui/static 下的前端程式碼),所以可以直接用比較嚴格
// 的 `default-src 'self'` 而不用另外開白名單:
//
//   - X-Content-Type-Options: nosniff ——擋掉瀏覽器「猜測」回應內容型別
//     這個行為本身可能被拿來做的 MIME 混淆攻擊。
//   - X-Frame-Options: DENY ——GoNAS 的管理介面不應該被嵌進別的網站的
//     <iframe> 裡(防 clickjacking)。
//   - Referrer-Policy: no-referrer ——網址本身可能帶有內部路由資訊,
//     沒有理由外洩給任何第三方(反正也沒有外部連結)。
//   - Content-Security-Policy: default-src 'self' ——多一層瀏覽器端的
//     防護,就算未來哪個頁面不小心被注入了外部腳本/圖片,瀏覽器也會
//     直接擋下不執行/不載入。額外加一條 style-src 'self' 'unsafe-inline'
//     ——前端(internal/api/webui/static/app.js)大量用行內
//     `style="..."` 屬性做版面微調跟顏色(進度條寬度、狀態燈號顏色等
//     動態值),嚴格的 default-src 會連這些行內樣式都一併擋掉,把整個
//     介面的版面弄壞(這是實際用 Playwright 對著真的在跑的 gonasd
//     測出來的,不是憑空猜的 —— 見 Phase 9 的驗證紀錄)。行內樣式
//     跟行內腳本是完全不同等級的風險:CSS 沒有辦法拿來執行任意
//     JavaScript,真正需要擋的「注入腳本」這個攻擊面(script-src)
//     還是繼續套用 default-src 'self' 的嚴格限制,沒有放寬。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

// withRecover 攔截 next 底下任何 handler 的 panic,轉成一個 500 回應,
// 而不是讓 panic 往上炸穿 net/http 的 goroutine-per-request 模型 ——
// Go 的 http.Server 本來就會幫每個請求各自 recover 一次 panic(不會讓
// 一個請求的 panic 弄垮整個 process),但那個內建行為只會直接關閉連線、
// 在 stderr 印一段不太好讀的 stack trace,呼叫端拿到的是一個突然斷掉
// 的連線而不是有意義的錯誤回應,而且不會進到 GoNAS 自己的 structured
// log 裡。這裡自己包一層,好處是:(1) 呼叫端(不管是 curl 還是前端的
// fetch)都會拿到一個正常的 JSON 錯誤回應,不是連線中斷;(2) panic 內容
// 跟 stack trace 會透過 logger 記下來,跟其他請求記錄用同一套格式,
// 方便事後除錯;(3) 這台 daemon 本身除了這個請求以外的其他所有功能
// (已經在跑的排程器、其他請求)完全不受影響,不需要重啟 gonasd。
func withRecover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered while handling request",
					"method", r.Method,
					"path", r.URL.Path,
					"remote", r.RemoteAddr,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				// handler 可能在 panic 之前已經寫出部分回應(例如
				// writeJSON 已經呼叫過 WriteHeader),這裡再呼叫一次
				// WriteHeader 只會在 stderr 留一行「superfluous
				// WriteHeader call」的無害警告,不會影響其他請求,
				// 換來的是「大多數情況下呼叫端能拿到一個結構化的 500
				// 錯誤」這個更重要的保證。
				writeError(w, http.StatusInternalServerError, errInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
