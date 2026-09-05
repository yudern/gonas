// Command gonasd 是 GoNAS 的核心 daemon:REST API、內嵌的 Web 管理介面,
// 以及儲存/Docker/檔案共享/帳號等各個 Manager 的啟動進入點。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bng147/gonas/internal/api"
	"github.com/bng147/gonas/internal/config"
	"github.com/bng147/gonas/internal/version"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := config.Load()

	logger.Info("starting gonasd",
		"version", version.Version,
		"commit", version.Commit,
		"listenAddr", cfg.ListenAddr,
		"dataDir", cfg.DataDir,
	)

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		// Phase 0 只警告不致命:很多開發環境下 /var/lib/gonas 建不出來也無妨,
		// 之後接上真正的狀態儲存時這裡要改成 fatal。
		logger.Warn("failed to ensure data dir, continuing without it", "dataDir", cfg.DataDir, "err", err)
	}

	apiServer, handler, err := api.New(logger, cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize API server", "err", err)
		os.Exit(1)
	}
	defer apiServer.Close() // 停掉監控輪詢的背景 goroutine，見 internal/api.Server.Close 的說明

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// HTTPS 是不是要開啟只在這裡、程序啟動的當下讀一次 state.json 決定
	// ——中途透過 Web UI 呼叫 PUT /api/v1/security/https 改設定,只會寫回
	// state.json 跟(如果是第一次開啟)產生憑證檔案,不會去動已經在跑的
	// http.Server,所以 API 回應會提醒使用者需要重新啟動 gonasd 才會生效
	// (見 internal/api/security_handlers.go)。這個取捨換來的是不需要在
	// 執行期支援「把一個正在監聽 HTTP 的 listener 換成 HTTPS」這種
	// 比較少見、容易出錯的操作。
	httpsCfg := apiServer.HTTPSConfig()
	if httpsCfg.Enabled {
		if _, certErr := os.Stat(httpsCfg.CertPath); certErr != nil {
			// 使用者透過 Web UI 開啟過 HTTPS,但憑證檔案不見了(例如被
			// 手動刪除,或 GONAS_DATA_DIR 換了位置)——寧可退回 HTTP 讓
			// daemon 正常啟動、留一條路讓使用者還能登入管理介面重新
			// 開啟 HTTPS(那個流程會重新產生憑證),也不要讓 daemon
			// 直接啟動失敗、進入誰都連不上的狀態。
			logger.Warn("https is enabled but certificate files are missing, falling back to http", "certPath", httpsCfg.CertPath, "err", certErr)
			httpsCfg.Enabled = false
		}
	}

	go func() {
		var err error
		if httpsCfg.Enabled {
			logger.Info("starting gonasd with https enabled", "certPath", httpsCfg.CertPath)
			err = srv.ListenAndServeTLS(httpsCfg.CertPath, httpsCfg.KeyPath)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	logger.Info("gonasd ready", "listenAddr", cfg.ListenAddr, "https", httpsCfg.Enabled)

	<-ctx.Done()
	logger.Info("shutting down gonasd")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}

	logger.Info("gonasd stopped cleanly")
}
