// Command gonasd 是 GoNAS 的核心 daemon:REST API、內嵌的 Web 管理介面,
// 以及儲存/Docker/檔案共享/帳號等各個 Manager 的啟動進入點。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/bng147/gonas/internal/api"
	"github.com/bng147/gonas/internal/config"
	"github.com/bng147/gonas/internal/doctor"
	"github.com/bng147/gonas/internal/version"
)

func main() {
	// -check-deps 跟 -version 都是「印完東西就結束,不啟動 daemon」的
	// 一次性指令 ——install.sh 安裝完會自動跑一次 -check-deps,讓使用者
	// 立刻知道這台機器還缺哪些選用的外部工具(見 internal/doctor 套件
	// 說明);-version 是給打包/除錯時快速確認「裝到的到底是哪個版本」
	// 用的,不用啟動整個 daemon 再呼叫 /api/v1/version。
	checkDeps := flag.Bool("check-deps", false, "檢查選用的外部工具(mergerfs、snapraid、samba、nfs、wireguard-tools、rsync 等)是否已安裝,不啟動 daemon")
	showVersion := flag.Bool("version", false, "印出版本資訊,不啟動 daemon")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gonasd %s (commit %s, built %s, %s/%s)\n", version.Version, version.Commit, version.BuildDate, runtime.GOOS, runtime.GOARCH)
		return
	}
	if *checkDeps {
		doctor.Report(os.Stdout, doctor.Run())
		return
	}

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
		// ReadTimeout 涵蓋整個請求(標頭 + body)讀取的時間上限,擋
		// slowloris 這類「故意用極慢的速度一點一點送資料撐住連線」的
		// 攻擊。GoNAS 絕大多數的請求 body 都是小型 JSON(見
		// internal/api.maxRequestBodyBytes),20 秒對正常區網使用者已經
		// 非常寬裕。
		//
		// 例外是 POST /api/v1/files/upload:檔案上傳的 body 大小取決於
		// 使用者要傳多大的檔案,20 秒對傳一顆幾 GB 的影片檔毫無意義。
		// 這支端點自己呼叫 http.NewResponseController(w).SetReadDeadline
		// 把「這一個請求」的讀取逾時整個關掉(Go 1.20 起的功能,見
		// internal/api/files_handlers.go 的 handleFilesUpload 說明),
		// 不需要為了這一支端點放寬全站的 slowloris 防護。
		//
		// 刻意不設 WriteTimeout:net/http 的 WriteTimeout 是從「讀完
		// 請求標頭」那一刻開始算的整段回應時間,不是「單次寫入」的
		// timeout —— 而 POST /api/v1/appstore/apps 會同步等 Docker
		// 把映像檔拉完才回應(見 internal/api/appstore_handlers.go),
		// 第一次安裝一個沒快取過的大型映像檔在慢速網路下可能要好幾
		// 分鐘。設一個「看起來合理」的 WriteTimeout(例如 30 秒)在這台
		// 開發沙盒裡完全測不出問題(沒有真的 Docker daemon 可以拉映像
		// 檔驗證),但在真正裝了 Docker、拉真實映像檔的機器上會讓這支
		// 端點在映像檔還沒拉完時就被伺服器自己掐斷連線 —— 這種「測試
		// 環境看不出來、只有在實機上跑長時間操作才會炸」的坑,與其現在
		// 猜一個數字冒這個風險,不如先不設,留到 Phase 9 的實機測試
		// 清單裡明確列成待驗證項目(見 docs/REAL_HARDWARE_TESTING.md)。
		ReadTimeout: 20 * time.Second,
		IdleTimeout: 120 * time.Second,
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
