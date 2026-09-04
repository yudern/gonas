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

	"github.com/bng147/gonas/internal/version"
)

// Server 持有建立路由所需的共用依賴(logger、啟動時間……)。
// 之後 Storage/Docker/Share 等 Manager 會以欄位形式加進來,
// 讓 handler 可以呼叫它們,而不是散落各處的全域變數。
type Server struct {
	logger    *slog.Logger
	startedAt time.Time
}

// New 建立一個 Server,並回傳已掛好所有路由的 http.Handler。
func New(logger *slog.Logger) (*Server, http.Handler) {
	s := &Server{
		logger:    logger,
		startedAt: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

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
