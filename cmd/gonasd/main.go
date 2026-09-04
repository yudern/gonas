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

	_, handler, err := api.New(logger, cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize API server", "err", err)
		os.Exit(1)
	}

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
