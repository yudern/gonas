// Package api 提供 GoNAS 對外的 REST API,以及(Phase 4 起)內嵌的 Web
// 管理介面靜態檔案。
package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/selfupdate"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/ups"
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
// 時間跨度太短。
//
// monitorHistoryCapacity 搭配上面的間隔,保留最近 30 分鐘的走勢
// (180 * 10s = 1800s)給 Web UI 畫圖表 —— 這個時間長度足夠讓使用者看出
// 「剛剛发生了什麼」，daemon 重啟後從頭累積是可接受的(見 monitor.History
// 的套件註解)。
const (
	monitorPollInterval    = 10 * time.Second
	monitorHistoryCapacity = 180
)

// smartCheckInterval 是背景重新檢查各硬碟 SMART 健康狀態的最短間隔。
// SMART 的整體健康(PASSED/FAILED)是以小時計才會變的東西,而每次檢查都要
// 對每顆碟 fork 一次 `smartctl -a`、還會把休眠中的碟喚醒。所以告警評估
// 不再每 10 秒(monitorPollInterval)都去查,而是讀快取值,快取超過這個
// 間隔才在背景重跑一次。15 分鐘足以及時抓到「碟開始回報 FAILED」這種
// 事件(SMART 預警通常在真正故障前數小時~數天就出現),同時讓孱弱的
// ARM CPU 與想休眠的硬碟不再被每 10 秒的輪詢騷擾。
const smartCheckInterval = 15 * time.Minute

// upsPollInterval 是「市電中斷自動關機」背景監控查 UPS 的週期。停電時要夠
// 即時反應(20 秒足以在電量見底前抓到 LB/續航過低),但又不必更密——UPS
// 狀態不是每秒都在變。監控本身會依設定自我開關(見 ups.Monitor)。
const upsPollInterval = 20 * time.Second

// auditLogCapacity 是 Phase 18b 稽核紀錄(state.State.AuditLog)保留的
// 最大筆數,超過就從最舊的開始丟——理由跟 monitorHistoryCapacity 一樣:
// 這是一份持續寫進單一 JSON 檔案的紀錄(見 internal/state 套件的
// 「單一檔案、整份覆寫」設計),沒有上限的話檔案會隨著時間無限長大。
// 500 筆對一台家用/小型辦公室 NAS 來說,通常已經涵蓋好幾週到幾個月的
// 管理操作歷史,真的需要更長期的稽核紀錄時,使用者可以自行定期把
// GET /api/v1/audit/log 的結果匯出保存。
const auditLogCapacity = 500

// updateCheckInterval 是背景自我更新檢查的週期。版本更新不是分秒必爭
// 的事(不像 HTTPS 憑證快過期那樣有明確的截止日),6 小時一次已經能讓
// 使用者在合理時間內在 Web UI 看到「有新版本」的提示,又不會對使用者
// 自己架設的 Manifest 伺服器造成有意義的負擔——況且完全沒設定
// ManifestURL 之前,這個週期根本不會發出任何請求,見
// internal/selfupdate 套件註解。
//
// updateHTTPTimeout 是背景檢查、下載更新檔共用的 HTTP client 逾時。
// 檢查 Manifest 是小型 JSON,下載執行檔可能是十幾 MB 到快 200 MiB
// (見 internal/selfupdate.maxDownloadBytes)——5 分鐘對正常網路環境
// 綽綽有餘,同時還是擋得住「伺服器沒回應、連線掛住不放」的情況,不會讓
// 背景 goroutine 無限期卡住。
const (
	updateCheckInterval = 6 * time.Hour
	updateHTTPTimeout   = 5 * time.Minute
)

// Server 持有建立路由所需的共用依賴。
type Server struct {
	logger    *slog.Logger
	startedAt time.Time
	runner    storage.Runner
	// fstabPath 是「準備硬碟」寫入開機自動掛載設定的檔案,正式環境是
	// /etc/fstab;抽成欄位是為了讓測試指到暫存檔,不去動真的 /etc/fstab。
	fstabPath string
	// snapraidCfgPath 是 GoNAS 管理的 snapraid.conf 位置;抽成欄位(跟
	// fstabPath 同理)是為了讓測試指到暫存檔,不去動真的 /etc/gonas/snapraid.conf。
	snapraidCfgPath string
	docker          *docker.Client
	store           *state.Store

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
	//
	// arrayMu 保護 array 這個「指標欄位」本身的讀寫。第三十二輪(全鏈路
	// 覆核)抓到的 data race:設定儲存池的 HTTP handler 會
	// `s.array = NewArray(...)` 重新賦值,而背景監控輪詢 goroutine
	// (onMonitorSample)同時在讀 s.array —— 兩者沒有同步。Array 本身
	// 的方法(Status/Start/Stop)有自己的 mu 保護其內部狀態,所以這裡
	// 只需要一把 RWMutex 保護「換掉/讀取這個指標」這件事;透過
	// getArray()/setArray() 存取,長時間的 Start/Stop 只在區域變數上呼叫、
	// 不佔著 arrayMu。
	arrayMu sync.RWMutex
	array   *storage.Array

	monitorCollector *monitor.Collector
	monitorHistory   *monitor.History
	alertEngine      *monitor.AlertEngine
	monitorPoller    *monitor.Poller

	// smartMu 保護底下這組 SMART 健康檢查的快取欄位。第三十四輪(效能/
	// ARM 稽核)修法:原本監控輪詢每 10 秒的告警評估,會對每一顆資料碟+
	// 同位碟逐一 fork 出 `smartctl -a`。一台 6 碟的 NAS 就是每 10 秒 6 次
	// 行程,一天約 5 萬次——在孱弱的 ARM CPU 上是持續的無謂負載,而且
	// `smartctl -a` 會喚醒硬碟,等於讓碟永遠無法休眠(spin-down),對家用
	// ARM NAS 是耗電/發熱/磨損的大忌。SMART 健康是「以小時計」才會變的
	// 東西,不需要每 10 秒查。改成:告警評估只讀 smartFailed 這個快取值
	// (非阻塞),快取過期(smartCheckInterval)時才在背景 goroutine 重跑
	// 一次真正的 smartctl,把行程 spawn 砍掉約兩個數量級、讓硬碟能休眠。
	smartMu       sync.Mutex
	smartFailed   bool
	smartChecked  time.Time
	smartChecking bool

	// digestScheduler 是 Phase 18c 新增的週期性健康摘要背景排程,
	// state.State.Digest.Enabled 且 CronExpr 合法時才會真的啟動——跟
	// certRenewer 一樣是「看設定決定要不要啟動」的背景工作,不像
	// monitorPoller/updateChecker 那樣一律啟動。digestMu 保護
	// 「讀取/替換這個欄位」本身(PUT /api/v1/monitor/digest 修改設定後
	// 需要停掉舊的、視情況啟動新的排程),不能沿用 state.Store 內部的鎖,
	// 理由跟 backupMu 一樣。
	digestMu        sync.Mutex
	digestScheduler *monitor.DigestScheduler

	// backupMu 保護 backupSchedulers —— 這個 map 本身不是持久化狀態的一
	// 部分(排程 goroutine 的控制代碼不能序列化進 state.json),所以需要
	// 自己的鎖,不能沿用 state.Store 內部的鎖。
	backupMu         sync.Mutex
	backupSchedulers map[string]*backup.JobScheduler

	// certRenewer 是 HTTPS 啟用時,背景週期性檢查/續簽自簽 TLS 憑證的
	// goroutine(見 internal/security.CertRenewer)。HTTPS 沒有啟用時
	// 維持 nil,New()/Close() 都要檢查 nil 再動作。
	certRenewer *security.CertRenewer

	// updateChecker 是背景週期性檢查是否有新版 gonasd 的 goroutine(見
	// internal/selfupdate.Checker)。跟 certRenewer 不同,這裡不管
	// ManifestURL 有沒有設定都會啟動——Checker.Start 每次檢查前都會透過
	// getManifestURL 重新讀一次 state.json,URL 是空字串(預設值)時
	// 直接跳過、不發任何網路請求,所以「啟動」不等於「開始連網」,細節
	// 見 internal/selfupdate 套件註解的隱私設計說明。
	updateChecker *selfupdate.Checker

	// updateHTTPClient 是 updateChecker 背景檢查、以及套用更新時下載
	// 執行檔共用的 HTTP client。獨立成一個欄位(而不是每次臨時
	// new 一個)方便測試替換,正式執行時就是一個帶合理逾時的 client。
	updateHTTPClient *http.Client

	// upsMonitor 是「市電中斷且電量過低就安全關機」的背景守護。一律啟動,
	// 每一輪自己讀設定決定要不要動作(見 ups.Monitor 與 upsPollInterval)。
	upsMonitor *ups.Monitor

	// restartRequested 是 self-update 實際把新執行檔換上去之後,通知
	// cmd/gonasd/main.go「該重啟程序了」的訊號,內容是重啟要用的執行檔
	// 路徑。buffered size 1 是因為送訊號那個 goroutine
	// (runApplyUpdate)不該被「main.go 還沒讀走上一個訊號」卡住,用
	// 非阻塞送出(select+default)就好,細節見 RestartRequested 的方法
	// 註解。
	//
	// 這裡刻意傳遞路徑字串,而不是單純的 struct{}{} 訊號,是因為一個
	// 真實踩到的坑:main.go 收到訊號之後如果自己重新呼叫一次
	// os.Executable() 找路徑,在 Linux 上會讀到錯的答案——
	// os.Executable() 底層是讀 /proc/self/exe,這是一個會跟著「目前這個
	// 執行檔的 inode」被改名而變動的 magic symlink;ApplyUpdate
	// 已經把「目前正在跑的這個執行檔」(也就是 runApplyUpdate 呼叫
	// os.Executable() 當下拿到的那個 inode)重新命名成
	// "<execPath>.previous" 了,所以事後在同一個程序裡再呼叫一次
	// os.Executable(),读到的會是改名後的 ".previous" 路徑,不是新版本
	// 執行檔實際所在的路徑——這樣 Reexec 會很荒謬地重新載入舊版本的
	// 內容。正確做法是在 ApplyUpdate 置換之前就先把路徑記下來(見
	// runApplyUpdate 裡呼叫 resolveExecPath() 的那一行),透過這個
	// channel 原封不動地交給 main.go 使用,不要事後重新查詢。
	restartRequested chan string

	// applyMu 保護 applyStatus——self-update 套用動作是背景 goroutine
	// 執行的(見 handleSystemUpdateApply),GET /api/v1/system/update
	// 需要能隨時安全地讀取目前的套用進度。
	applyMu     sync.Mutex
	applyStatus applyUpdateStatus

	// updateExecPathFunc 是 runApplyUpdate 用來找出「目前這個執行檔在
	// 磁碟上的路徑」的函式,預設(nil)時等同 os.Executable。獨立成一個
	// 可替換的欄位,單純是為了讓測試能注入一個 t.TempDir() 底下的假
	// 執行檔路徑,不會讓測試不小心去覆寫真正在跑測試的那個二進位檔——
	// 跟 s.runner/s.docker 這些欄位可以在測試裡被替換成假實作是同樣的
	// 考量。
	updateExecPathFunc func() (string, error)

	// doctorInstalling 是「系統診斷一鍵補裝」的 single-flight 旗標(第五十二
	// 輪覆核 S-3)。apt/dpkg 有系統層的獨佔鎖(/var/lib/dpkg/lock),兩個
	// 安裝請求同時打進來會撞鎖、第二個以難懂的錯誤失敗。用一個 atomic 旗標
	// 讓同一時間只跑一個安裝,後到的請求直接回 409,而不是讓它去撞 dpkg 鎖。
	doctorInstalling atomic.Bool

	// paritySyncing 是「SnapRAID 同位同步/校驗」的 single-flight 旗標(第五十八
	// 輪全鏈路覆核 P1)。snapraid sync 在大陣列上可能跑很久,而且會寫同位碟,
	// 同一時間只能有一個在跑;用一個 atomic 旗標讓同步在背景 goroutine 執行,
	// 後到的請求直接回 409。狀態(進行中/上次結果)給 array 狀態端點回報。
	paritySyncing atomic.Bool
	paritySyncErr atomic.Pointer[string] // 上次同步的錯誤訊息(nil=上次成功或還沒跑過)
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
		fstabPath:        storage.DefaultFstabPath,
		snapraidCfgPath:  defaultSnapraidConfigPath,
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
		// 第五十五輪覆核(產品 P1):開機時若已經設定過 pool,就自動把陣列掛回來。
		// mergerfs 是 FUSE 掛載,沒有寫進 fstab(也不適合),所以「重開機後儲存
		// 會不會自己回來」完全靠這裡 —— gonasd 由 systemd 開機啟動,啟動時把
		// union 重新掛上。best-effort:掛不起來(例如 mergerfs 還沒裝、或某顆資料
		// 碟還沒掛)只記 log、把狀態留在 failed,儀表板會顯示「啟動陣列」讓使用者
		// 手動處理,絕不擋住 daemon 啟動。
		if err := s.array.Start(context.Background(), s.runner); err != nil {
			logger.Warn("auto-starting the storage array on boot failed (leaving it stopped; start it from the dashboard)", "err", err)
		} else {
			logger.Info("storage array auto-started on boot", "mountPoint", pool.MountPoint)
		}
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

	// HTTPS 是不是要開啟只在 cmd/gonasd/main.go 啟動當下決定要不要用
	// TLS 監聽(見那邊的說明,改設定需要重啟才生效),但「憑證快過期時
	// 背景自動續簽」是完全獨立的另一件事,不需要等重啟這個機制本身
	// 存在與否——只要這次啟動當下 HTTPS 是啟用的、憑證檔案路徑也有記
	// 下來,就把 CertRenewer 一起啟動,讓它跟 monitorPoller、
	// backupSchedulers 一樣是 New() 啟動、Close() 停止的背景工作。
	if httpsCfg := store.Snapshot().HTTPS; httpsCfg.Enabled && httpsCfg.CertPath != "" && httpsCfg.KeyPath != "" {
		validFor, renewBefore, checkInterval := s.httpsCertRenewalPolicy()
		s.certRenewer = security.NewCertRenewer(logger)
		s.certRenewer.Start(context.Background(), checkInterval, httpsCfg.CertPath, httpsCfg.KeyPath, httpsCfg.Hosts, validFor, renewBefore)
	}

	// 自我更新的背景檢查器一律啟動,不像 certRenewer 那樣要看設定決定
	// 要不要啟動——理由見 Server.updateChecker 欄位的說明:沒設定
	// ManifestURL 之前,啟動這個 goroutine 本身不會產生任何網路流量,
	// 使用者之後透過 Web UI 設定/清空更新來源網址也不需要重啟 gonasd
	// 才會生效。
	// 用 selfupdate.NewHTTPClient(而不是自己 new 一個 &http.Client{}),
	// 才會帶上 secureRedirect —— 擋掉「https 起手、302 降級到 http」的
	// 重導向 MITM,requireHTTPS 只驗初始 URL 不夠(見 secureRedirect)。
	s.updateHTTPClient = selfupdate.NewHTTPClient(updateHTTPTimeout)
	s.restartRequested = make(chan string, 1)
	s.updateChecker = selfupdate.NewChecker(logger)
	s.updateChecker.Start(context.Background(), updateCheckInterval, version.Version, func() string {
		return s.store.Snapshot().Update.ManifestURL
	}, s.updateHTTPClient)

	// 跟 certRenewer 一樣「看設定決定要不要啟動」——digest 沒設定
	// (CronExpr 空字串,DigestConfig 的預設零值)或使用者主動停用之前,
	// 不該憑空多一個背景 goroutine 在跑,細節見 startDigestScheduler
	// 的說明。
	if snap := store.Snapshot(); snap.Digest.Enabled && snap.Digest.CronExpr != "" {
		s.startDigestScheduler(snap.Digest.CronExpr)
	}

	// UPS 監控一律啟動,但每輪自己讀設定決定要不要動作(沒啟用/沒開自動
	// 關機時等於空轉,不查也不動)。getConfig 每輪讀當下持久化設定,所以
	// 使用者改設定存檔後不必重啟就生效;onShutdown 走跟網頁電源按鈕同一條
	// systemctl poweroff。
	s.upsMonitor = ups.NewMonitor(logger, s.runner, upsPollInterval,
		func() ups.MonitorConfig {
			c := s.store.Snapshot().UPS
			return ups.MonitorConfig{
				Enabled:                 c.Enabled,
				ShutdownOnLowBattery:    c.ShutdownOnLowBattery,
				UPSName:                 c.UPSName,
				RuntimeThresholdSeconds: c.RuntimeThresholdSeconds,
			}
		},
		func() {
			if _, err := s.runner.Run(context.Background(), "systemctl", "poweroff"); err != nil {
				s.logger.Error("ups-monitor: safe shutdown command failed", "err", err)
			}
		},
	)
	s.upsMonitor.Start(context.Background())

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
	mux.HandleFunc("POST /api/v1/auth/totp/recovery-codes", s.requireAuth(s.handleAuthTOTPRecoveryCodes))
	// 帳號管理(新增/刪除「其他」帳號、列出所有帳號)才是真正「管理 NAS」
	// 的動作,一律要求 RoleAdmin。
	mux.HandleFunc("GET /api/v1/auth/accounts", s.requireAdmin(s.handleAuthAccountsList))
	// Phase 18b:管理者動作稽核紀錄,見 audit_handlers.go 的說明。
	mux.HandleFunc("GET /api/v1/audit/log", s.requireAdmin(s.handleAuditLogGet))
	mux.HandleFunc("POST /api/v1/auth/accounts", s.requireAdmin(s.handleAuthAccountsCreate))
	mux.HandleFunc("DELETE /api/v1/auth/accounts/{username}", s.requireAdmin(s.handleAuthAccountsDelete))
	mux.HandleFunc("POST /api/v1/auth/accounts/{username}/reset-totp", s.requireAdmin(s.handleAuthAccountsResetTOTP))

	mux.HandleFunc("GET /api/v1/security/https", s.requireAuth(s.handleSecurityHTTPSGet))
	mux.HandleFunc("PUT /api/v1/security/https", s.requireAdmin(s.handleSecurityHTTPSSet))

	// 跟 HTTPS 設定同樣的 requireAuth/requireAdmin 分法:查詢版本/檢查
	// 結果只是讀資訊,RoleViewer 也該看得到;設定更新來源網址、觸發
	// 檢查、真正套用更新都是「管理 NAS」的動作,一律 requireAdmin——尤其
	// apply 會直接置換掉正在執行的 gonasd 執行檔,絕對不能是 RoleViewer
	// 就能觸發的動作。
	mux.HandleFunc("GET /api/v1/system/update", s.requireAuth(s.handleSystemUpdateGet))
	mux.HandleFunc("PUT /api/v1/system/update/settings", s.requireAdmin(s.handleSystemUpdateSettingsSet))
	mux.HandleFunc("POST /api/v1/system/update/check", s.requireAdmin(s.handleSystemUpdateCheck))
	mux.HandleFunc("POST /api/v1/system/power/shutdown", s.requireAdmin(s.handleSystemPowerShutdown))
	mux.HandleFunc("POST /api/v1/system/power/reboot", s.requireAdmin(s.handleSystemPowerReboot))
	mux.HandleFunc("GET /api/v1/system/doctor", s.requireAuth(s.handleDoctorStatus))
	mux.HandleFunc("POST /api/v1/system/doctor/install", s.requireAdmin(s.handleDoctorInstall))

	mux.HandleFunc("GET /api/v1/ups/status", s.requireAuth(s.handleUPSStatus))
	mux.HandleFunc("GET /api/v1/ups/list", s.requireAuth(s.handleUPSList))
	mux.HandleFunc("GET /api/v1/ups/config", s.requireAuth(s.handleUPSConfigGet))
	mux.HandleFunc("PUT /api/v1/ups/config", s.requireAdmin(s.handleUPSConfigSet))
	mux.HandleFunc("POST /api/v1/system/update/apply", s.requireAdmin(s.handleSystemUpdateApply))
	mux.HandleFunc("POST /api/v1/system/update/rollback", s.requireAdmin(s.handleSystemUpdateRollback))

	mux.HandleFunc("GET /api/v1/vpn/status", s.requireAuth(s.handleVPNStatus))
	mux.HandleFunc("PUT /api/v1/vpn/interface", s.requireAdmin(s.handleVPNInterfaceSet))
	mux.HandleFunc("GET /api/v1/vpn/peers", s.requireAuth(s.handleVPNPeersList))
	mux.HandleFunc("POST /api/v1/vpn/peers", s.requireAdmin(s.handleVPNPeerAdd))
	mux.HandleFunc("DELETE /api/v1/vpn/peers/{id}", s.requireAdmin(s.handleVPNPeerDelete))

	mux.HandleFunc("GET /api/v1/storage/disks", s.requireAuth(s.handleStorageDisks))
	mux.HandleFunc("GET /api/v1/storage/disks/smart", s.requireAuth(s.handleStorageDisksSmart))
	mux.HandleFunc("GET /api/v1/storage/array", s.requireAuth(s.handleStorageArrayStatus))
	mux.HandleFunc("POST /api/v1/storage/disks/prepare", s.requireAdmin(s.handleStoragePrepareDisk))
	mux.HandleFunc("PUT /api/v1/storage/pool", s.requireAdmin(s.handleStoragePoolSet))
	mux.HandleFunc("POST /api/v1/storage/array/start", s.requireAdmin(s.handleStorageArrayStart))
	mux.HandleFunc("POST /api/v1/storage/array/stop", s.requireAdmin(s.handleStorageArrayStop))
	mux.HandleFunc("POST /api/v1/storage/array/sync", s.requireAdmin(s.handleStorageArraySync))

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
	mux.HandleFunc("DELETE /api/v1/share/exports", s.requireAdmin(s.handleExportsDelete))
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
	mux.HandleFunc("GET /api/v1/monitor/email-notifiers", s.requireAuth(s.handleMonitorEmailNotifiersList))
	mux.HandleFunc("POST /api/v1/monitor/email-notifiers", s.requireAdmin(s.handleMonitorEmailNotifiersCreate))
	mux.HandleFunc("DELETE /api/v1/monitor/email-notifiers/{id}", s.requireAdmin(s.handleMonitorEmailNotifiersDelete))
	// Phase 18c:週期性健康摘要,見 monitor_digest_handlers.go 的說明。
	mux.HandleFunc("GET /api/v1/monitor/digest", s.requireAuth(s.handleMonitorDigestGet))
	mux.HandleFunc("PUT /api/v1/monitor/digest", s.requireAdmin(s.handleMonitorDigestSet))
	mux.HandleFunc("POST /api/v1/monitor/digest/send", s.requireAdmin(s.handleMonitorDigestSend))

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
	if s.upsMonitor != nil {
		s.upsMonitor.Stop()
	}

	s.backupMu.Lock()
	schedulers := s.backupSchedulers
	s.backupSchedulers = make(map[string]*backup.JobScheduler)
	s.backupMu.Unlock()
	for _, sched := range schedulers {
		sched.Stop()
	}

	if s.certRenewer != nil {
		s.certRenewer.Stop()
	}

	if s.updateChecker != nil {
		s.updateChecker.Stop()
	}

	s.stopDigestScheduler()
}

// RestartRequested 回傳一個訊號 channel,self-update 成功把新版執行檔
// 換上去之後(見 runApplyUpdate)會往裡面送一個值——內容是重啟時該用
// 的執行檔路徑,不是空的 struct{}{},理由見 restartRequested 欄位的
// 說明(main.go 事後自己重新呼叫 os.Executable() 會因為 /proc/self/exe
// 跟著 rename 的坑而拿到錯誤答案)。cmd/gonasd/main.go 在自己的主迴圈裡
// 跟 SIGTERM/SIGINT 的 ctx.Done() 一起 select 這個 channel——收到訊號
// 代表「該優雅關閉目前的 http.Server、然後用 internal/selfupdate.Reexec
// 搭配這個路徑重新啟動這個程序」,而不是像收到訊號那樣直接結束。獨立
// 成一個方法(而不是直接讓 main.go 拿 channel 欄位)是為了維持
// 「main.go 只依賴 api.Server 這一層抽象」的既有慣例,見
// HTTPSConfig/Close 的方法註解。
func (s *Server) RestartRequested() <-chan string {
	return s.restartRequested
}

// HTTPSCertificateLoader 回傳一個可以指定給 tls.Config.GetCertificate
// 的函式,讓 cmd/gonasd/main.go 啟動 TLS 監聽時不是傳靜態的
// certFile/keyFile 路徑給 ListenAndServeTLS,而是每次 TLS 交握都透過
// internal/security.CertStore 動態載入「目前檔案系統上最新」的憑證。
// 這樣 certRenewer 在背景重新簽發憑證之後,不需要重啟 gonasd、下一次
// 有人連進來的 TLS 交握就會自動拿到新憑證,徹底做到「憑證續期不用
// 重啟」——跟「開關 HTTPS 本身需要重啟」是兩件獨立的事,見本檔案
// New() 裡 certRenewer 啟動邏輯旁的說明。
func (s *Server) HTTPSCertificateLoader(certPath, keyPath string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return security.NewCertStore(certPath, keyPath).GetCertificate
}

// httpsCertRenewalPolicy 回傳自簽憑證的效期、續期門檻、續期檢查頻率
// 這三個政策數字(定義在 internal/api/security_handlers.go),讓 New()
// 啟動 certRenewer 時跟 handleSecurityHTTPSSet 簽發新憑證時,永遠用
// 同一組數字,不會兩處各自維護一份、之後改一個忘了改另一個。
func (s *Server) httpsCertRenewalPolicy() (validFor, renewBefore, checkInterval time.Duration) {
	return httpsCertValidity, httpsCertRenewBefore, httpsCertRenewCheckInterval
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
// getArray 讀取目前的陣列指標(在 arrayMu 的讀鎖下),回傳的是指標本身;
// 呼叫端拿到後可以安心呼叫它的 Start/Stop/Status(那些方法自己有鎖)。
func (s *Server) getArray() *storage.Array {
	s.arrayMu.RLock()
	defer s.arrayMu.RUnlock()
	return s.array
}

// setArray 換掉目前的陣列指標(在 arrayMu 的寫鎖下)。
func (s *Server) setArray(a *storage.Array) {
	s.arrayMu.Lock()
	defer s.arrayMu.Unlock()
	s.array = a
}

func (s *Server) fileManagerRoot() (string, error) {
	pool := s.store.Snapshot().Pool
	array := s.getArray()
	if pool == nil || array == nil {
		return "", errNoPoolConfigured
	}
	if array.Status().State != storage.StateStarted {
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
