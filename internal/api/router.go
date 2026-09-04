// Package api 提供 GoNAS 對外的 REST API。
//
// Phase 0 只掛載最基本的健康檢查與版本資訊端點,之後每個 Phase
// (儲存、Docker、共享、使用者……) 會在這裡各自掛上一組 /api/v1/xxx 路由。
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/version"
)

// Server 持有建立路由所需的共用依賴(logger、啟動時間……)。
// 之後 Storage/Docker/Share 等 Manager 會以欄位形式加進來,
// 讓 handler 可以呼叫它們,而不是散落各處的全域變數。
type Server struct {
	logger    *slog.Logger
	startedAt time.Time
	runner    storage.Runner
	// array 在還沒有人透過 Web UI / 設定檔建立 pool 之前是 nil。
	// Phase 4 接上設定持久化後,這裡會在啟動時從設定檔載入既有的 pool。
	array *storage.Array
	// docker 指向本機 Docker daemon。這裡先只掛唯讀端點(ping/containers/
	// images/networks)—— 安裝/解除安裝需要使用者在 Web UI 上選陣列路徑、
	// 填環境變數,那是 Phase 4 的範圍,現在貿然開放 POST 端點沒有介面把關,
	// 容易被誤用。
	docker *docker.Client
}

// New 建立一個 Server,並回傳已掛好所有路由的 http.Handler。
func New(logger *slog.Logger) (*Server, http.Handler) {
	s := &Server{
		logger:    logger,
		startedAt: time.Now(),
		runner:    storage.NewExecRunner(),
		docker:    docker.NewClient(""),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/storage/disks", s.handleStorageDisks)
	mux.HandleFunc("GET /api/v1/storage/array", s.handleStorageArrayStatus)
	mux.HandleFunc("GET /api/v1/docker/ping", s.handleDockerPing)
	mux.HandleFunc("GET /api/v1/docker/containers", s.handleDockerContainers)
	mux.HandleFunc("GET /api/v1/docker/images", s.handleDockerImages)
	mux.HandleFunc("GET /api/v1/docker/networks", s.handleDockerNetworks)

	return s, withLogging(logger, mux)
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

// handleStorageDisks 探測系統上目前有哪些區塊裝置。這是唯讀操作,
// 所以不需要陣列先被設定好才能呼叫 —— 使用者第一次設定 pool 之前,
// 就是靠這支 API 看到「有哪些硬碟可以選」。
func (s *Server) handleStorageDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := storage.DiscoverDisks(r.Context(), s.runner)
	if err != nil {
		s.logger.Error("disk discovery failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, disks)
}

// handleStorageArrayStatus 回傳目前陣列的狀態。在使用者還沒建立任何
// pool 設定之前(Phase 1 還沒有設定持久化,Phase 4 會補上),回傳
// "unconfigured" 而不是錯誤 —— 這是合法的初始狀態,不是異常。
func (s *Server) handleStorageArrayStatus(w http.ResponseWriter, r *http.Request) {
	if s.array == nil {
		writeJSON(w, http.StatusOK, storage.Status{State: "unconfigured"})
		return
	}
	writeJSON(w, http.StatusOK, s.array.Status())
}

// dockerStatusResponse 讓 Web UI 能區分「Docker 沒裝/沒啟動」跟其他錯誤,
// 這是安裝任何 App 之前第一件要確認的事。
type dockerStatusResponse struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleDockerPing(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, dockerStatusResponse{Available: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, dockerStatusResponse{Available: true})
}

func (s *Server) handleDockerContainers(w http.ResponseWriter, r *http.Request) {
	containers, err := s.docker.ListContainers(r.Context(), true)
	if err != nil {
		s.logger.Error("listing docker containers failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, containers)
}

func (s *Server) handleDockerImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.docker.ListImages(r.Context())
	if err != nil {
		s.logger.Error("listing docker images failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, images)
}

func (s *Server) handleDockerNetworks(w http.ResponseWriter, r *http.Request) {
	networks, err := s.docker.ListNetworks(r.Context())
	if err != nil {
		s.logger.Error("listing docker networks failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, networks)
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// withLogging 是一個最小的存取記錄中介層,之後可以擴充成結構化的
// request-id / 延遲統計等。先求「看得到系統在做什麼」。
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
