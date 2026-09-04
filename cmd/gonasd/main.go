// Command gonasd 是 GoNAS 的核心 daemon。
//
// Phase 0 的範圍:啟動一個會回應健康檢查與版本資訊的 HTTP server,
// 並且能被 systemd 正常啟動、停止(收到 SIGTERM 時優雅關閉)。
// 之後每個 Phase 都會往這裡掛上新的 Manager(Storage/Docker/Share/...)。
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

	_, handler := api.New(logger)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	logger.Info("gonasd ready", "listenAddr", cfg.ListenAddr)

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
