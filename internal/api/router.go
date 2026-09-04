// Package api 提供 GoNAS 對外的 REST API,以及(Phase 4 起)內嵌的 Web
// 管理介面靜態檔案。
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/version"
)

// Server 持有建立路由所需的共用依賴。
type Server struct {
	logger    *slog.Logger
	startedAt time.Time
	runner    storage.Runner
	docker    *docker.Client
	store     *state.Store

	// array 是目前載入的儲存陣列。啟動時如果 store 裡已經有 pool 設定,
	// 會在這裡建立對應的 *storage.Array(但不會自動 Start —— 掛載陣列
	// 是使用者的明確動作,不該在 daemon 重啟時靜默發生)。
	array *storage.Array
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
	}

	if pool := store.Snapshot().Pool; pool != nil {
		s.array = storage.NewArray(*pool)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

	mux.HandleFunc("GET /api/v1/storage/disks", s.handleStorageDisks)
	mux.HandleFunc("GET /api/v1/storage/array", s.handleStorageArrayStatus)
	mux.HandleFunc("PUT /api/v1/storage/pool", s.handleStoragePoolSet)
	mux.HandleFunc("POST /api/v1/storage/array/start", s.handleStorageArrayStart)
	mux.HandleFunc("POST /api/v1/storage/array/stop", s.handleStorageArrayStop)

	mux.HandleFunc("GET /api/v1/docker/ping", s.handleDockerPing)
	mux.HandleFunc("GET /api/v1/docker/containers", s.handleDockerContainers)
	mux.HandleFunc("GET /api/v1/docker/images", s.handleDockerImages)
	mux.HandleFunc("GET /api/v1/docker/networks", s.handleDockerNetworks)

	mux.HandleFunc("GET /api/v1/appstore/catalog", s.handleAppstoreCatalog)
	mux.HandleFunc("GET /api/v1/appstore/apps", s.handleAppstoreListInstalled)
	mux.HandleFunc("POST /api/v1/appstore/apps", s.handleAppstoreInstall)
	mux.HandleFunc("DELETE /api/v1/appstore/apps/{id}", s.handleAppstoreUninstall)

	mux.HandleFunc("GET /api/v1/share/shares", s.handleSharesList)
	mux.HandleFunc("POST /api/v1/share/shares", s.handleSharesCreate)
	mux.HandleFunc("DELETE /api/v1/share/shares/{name}", s.handleSharesDelete)
	mux.HandleFunc("GET /api/v1/share/exports", s.handleExportsList)
	mux.HandleFunc("POST /api/v1/share/exports", s.handleExportsCreate)
	mux.HandleFunc("GET /api/v1/share/users", s.handleUsersList)
	mux.HandleFunc("POST /api/v1/share/users", s.handleUsersCreate)
	mux.HandleFunc("DELETE /api/v1/share/users/{username}", s.handleUsersDelete)

	mux.Handle("/", webUIHandler())

	return s, withLogging(logger, mux), nil
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
