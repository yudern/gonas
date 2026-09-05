// Package api 提供 GoNAS 對外的 REST API,以及(Phase 4 起)內嵌的 Web
// 管理介面靜態檔案。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/version"
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

	// array 是目前載入的儲存陣列。啟動時如果 store 裡已經有 pool 設定,
	// 會在這裡建立對應的 *storage.Array(但不會自動 Start —— 掛載陣列
	// 是使用者的明確動作,不該在 daemon 重啟時靜默發生)。
	array *storage.Array

	monitorCollector *monitor.Collector
	monitorHistory   *monitor.History
	alertEngine      *monitor.AlertEngine
	monitorPoller    *monitor.Poller
}

// New 建立一個 Server,從 dataDir/state.json 載入既有狀態,並回傳已掛好
// 所有路由的 http.Handler。
func New(logger *slog.Logger, dataDir string) (*Server, http.Handler, error) {
	store, err := state.Open(dataDir + "/state.json")
	if err != nil {
		return nil, nil, err
	}

	s := &Server{
		logger:    logger,
		startedAt: time.Now(),
		runner:    storage.NewExecRunner(),
		docker:    docker.NewClient(""),
		store:     store,
		dataDir:   dataDir,
		sessions:  security.NewSessionManager(sessionTTL),
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
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.handleAuthLogout))
	mux.HandleFunc("GET /api/v1/auth/me", s.requireAuth(s.handleAuthMe))
	mux.HandleFunc("POST /api/v1/auth/password", s.requireAuth(s.handleAuthChangePassword))
	mux.HandleFunc("POST /api/v1/auth/totp/setup", s.requireAuth(s.handleAuthTOTPSetup))
	mux.HandleFunc("POST /api/v1/auth/totp/enable", s.requireAuth(s.handleAuthTOTPEnable))
	mux.HandleFunc("POST /api/v1/auth/totp/disable", s.requireAuth(s.handleAuthTOTPDisable))

	mux.HandleFunc("GET /api/v1/security/https", s.requireAuth(s.handleSecurityHTTPSGet))
	mux.HandleFunc("PUT /api/v1/security/https", s.requireAuth(s.handleSecurityHTTPSSet))

	mux.HandleFunc("GET /api/v1/vpn/status", s.requireAuth(s.handleVPNStatus))
	mux.HandleFunc("PUT /api/v1/vpn/interface", s.requireAuth(s.handleVPNInterfaceSet))
	mux.HandleFunc("GET /api/v1/vpn/peers", s.requireAuth(s.handleVPNPeersList))
	mux.HandleFunc("POST /api/v1/vpn/peers", s.requireAuth(s.handleVPNPeerAdd))
	mux.HandleFunc("DELETE /api/v1/vpn/peers/{id}", s.requireAuth(s.handleVPNPeerDelete))

	mux.HandleFunc("GET /api/v1/storage/disks", s.requireAuth(s.handleStorageDisks))
	mux.HandleFunc("GET /api/v1/storage/array", s.requireAuth(s.handleStorageArrayStatus))
	mux.HandleFunc("PUT /api/v1/storage/pool", s.requireAuth(s.handleStoragePoolSet))
	mux.HandleFunc("POST /api/v1/storage/array/start", s.requireAuth(s.handleStorageArrayStart))
	mux.HandleFunc("POST /api/v1/storage/array/stop", s.requireAuth(s.handleStorageArrayStop))

	mux.HandleFunc("GET /api/v1/docker/ping", s.requireAuth(s.handleDockerPing))
	mux.HandleFunc("GET /api/v1/docker/containers", s.requireAuth(s.handleDockerContainers))
	mux.HandleFunc("GET /api/v1/docker/images", s.requireAuth(s.handleDockerImages))
	mux.HandleFunc("GET /api/v1/docker/networks", s.requireAuth(s.handleDockerNetworks))

	mux.HandleFunc("GET /api/v1/appstore/catalog", s.requireAuth(s.handleAppstoreCatalog))
	mux.HandleFunc("GET /api/v1/appstore/apps", s.requireAuth(s.handleAppstoreListInstalled))
	mux.HandleFunc("POST /api/v1/appstore/apps", s.requireAuth(s.handleAppstoreInstall))
	mux.HandleFunc("DELETE /api/v1/appstore/apps/{id}", s.requireAuth(s.handleAppstoreUninstall))

	mux.HandleFunc("GET /api/v1/share/shares", s.requireAuth(s.handleSharesList))
	mux.HandleFunc("POST /api/v1/share/shares", s.requireAuth(s.handleSharesCreate))
	mux.HandleFunc("DELETE /api/v1/share/shares/{name}", s.requireAuth(s.handleSharesDelete))
	mux.HandleFunc("GET /api/v1/share/exports", s.requireAuth(s.handleExportsList))
	mux.HandleFunc("POST /api/v1/share/exports", s.requireAuth(s.handleExportsCreate))
	mux.HandleFunc("GET /api/v1/share/users", s.requireAuth(s.handleUsersList))
	mux.HandleFunc("POST /api/v1/share/users", s.requireAuth(s.handleUsersCreate))
	mux.HandleFunc("DELETE /api/v1/share/users/{username}", s.requireAuth(s.handleUsersDelete))

	mux.HandleFunc("GET /api/v1/monitor/system", s.requireAuth(s.handleMonitorSystem))
	mux.HandleFunc("GET /api/v1/monitor/history", s.requireAuth(s.handleMonitorHistory))
	mux.HandleFunc("GET /api/v1/monitor/alerts", s.requireAuth(s.handleMonitorAlertsList))
	mux.HandleFunc("POST /api/v1/monitor/alerts", s.requireAuth(s.handleMonitorAlertsCreate))
	mux.HandleFunc("DELETE /api/v1/monitor/alerts/{id}", s.requireAuth(s.handleMonitorAlertsDelete))
	mux.HandleFunc("GET /api/v1/monitor/notifiers", s.requireAuth(s.handleMonitorNotifiersList))
	mux.HandleFunc("POST /api/v1/monitor/notifiers", s.requireAuth(s.handleMonitorNotifiersCreate))
	mux.HandleFunc("DELETE /api/v1/monitor/notifiers/{id}", s.requireAuth(s.handleMonitorNotifiersDelete))

	mux.Handle("/", webUIHandler())

	return s, withLogging(logger, mux), nil
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
// 呼叫端只需要判斷 ok 就好。
func readJSON(w http.ResponseWriter, r *http.Request, dst any) (ok bool) {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// withLogging 是一個最小的存取記錄中介層。
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
