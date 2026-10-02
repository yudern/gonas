package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bng147/gonas/internal/share"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// defaultSnapraidConfigPath 是 GoNAS 產生/管理的 SnapRAID 設定檔預設位置。
// sync/scrub 都用 `-c` 明確指到這裡,不依賴 snapraid 的預設 /etc/snapraid.conf。
// 實際路徑放在 Server.snapraidCfgPath(可被測試覆寫,見 router.go)。
const defaultSnapraidConfigPath = "/etc/gonas/snapraid.conf"

// writeSnapraidConfig 依 pool 產生並原子寫入 snapraid.conf(只有 pool 有同位碟
// 時才有意義)。與 samba/nfs 設定一樣用 share.WriteConfigAtomically(先寫暫存檔
// 再 rename + fsync),避免留下半份設定檔。
func (s *Server) writeSnapraidConfig(pool storage.PoolConfig) error {
	content, err := storage.GenerateSnapraidConfig(pool)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.snapraidCfgPath), 0o755); err != nil {
		return err
	}
	return share.WriteConfigAtomically(s.snapraidCfgPath, content)
}

// handleStorageDisks 探測系統上目前有哪些區塊裝置。這是唯讀操作,
// 所以不需要陣列先被設定好才能呼叫 —— 使用者第一次設定 pool 之前,
// 就是靠這支 API 看到「有哪些硬碟可以選」。
func (s *Server) handleStorageDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := storage.DiscoverDisks(r.Context(), s.runner)
	if err != nil {
		s.logger.Error("disk discovery failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, disks)
}

// diskSmartResult 是「儲存」頁面硬碟表格裡 SMART 那一欄要的資料——每一
// 顆偵測到的硬碟各自一筆,单顆硬碟查詢失敗(這台主機沒裝 smartctl、
// 裝置不支援 SMART、虛擬/迴圈裝置之類)不該讓整支 API 一起回錯,只把
// 失敗原因放在該筆的 Error 欄位,其餘硬碟的結果照常回傳——這是「查詢
// 磁碟健康狀態」這種診斷用途的資訊,單一顆碟查不到本身就是常態
// (尤其是 USB 外接盒、虛擬機器的區塊裝置),不該讓一顆碟查不到就看不到
// 其他顆碟的結果。
type diskSmartResult struct {
	Path        string `json:"path"`
	Passed      bool   `json:"passed"`
	TempCelsius *int   `json:"tempCelsius,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleStorageDisksSmart 對系統上目前偵測到的每一顆硬碟各跑一次
// `smartctl -a` 並回傳簡化的健康狀態(passed/溫度)。刻意重新呼叫
// DiscoverDisks 而不是要求呼叫端先傳裝置清單——這支端點的呼叫情境
// 就是「儲存」頁面已經顯示的硬碟表格要多加一欄,直接用當下偵測到的
// 硬碟清單查一輪最省事,也不會有「表格顯示的硬碟」跟「查詢的硬碟」
// 兜不起來的疑慮。每顆碟的逾時刻意抓短(5 秒,跟 monitor_handlers.go
// 的 probeSmartFailed 用同樣的值)——這是使用者主動點開頁面在等的
// 前景請求,不該因為一顆碟的 smartctl 掛住就讓整個頁面卡住不轉。
// (注意這支是「使用者開頁面時查一次」的前景請求,跟背景告警評估每
// 15 分鐘才刷新一次的 SMART 快取是兩條獨立路徑,互不影響。)
func (s *Server) handleStorageDisksSmart(w http.ResponseWriter, r *http.Request) {
	disks, err := storage.DiscoverDisks(r.Context(), s.runner)
	if err != nil {
		s.logger.Error("disk discovery failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	results := make([]diskSmartResult, 0, len(disks))
	for _, d := range disks {
		checkCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		health, err := storage.CheckSmartHealth(checkCtx, s.runner, d.Path)
		cancel()

		res := diskSmartResult{Path: d.Path}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Passed = health.Passed
			res.TempCelsius = health.TempCelsius
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, results)
}

// handleStorageArrayStatus 回傳目前陣列的狀態。還沒有人設定過 pool 時,
// 回傳 "unconfigured" 而不是錯誤 —— 這是合法的初始狀態,不是異常。
// arrayStatusResponse 在 storage.Status 之外,附上「同位保護」的真實狀態,讓
// 前端能誠實顯示「已於 X 受保護 / 尚未同步(尚未受保護)」而不是讓使用者以為
// 設了同位碟就自動有保護(第五十八輪產品 P1)。
type arrayStatusResponse struct {
	storage.Status
	HasParity       bool       `json:"hasParity"`
	Protected       bool       `json:"protected"` // 有同位碟且至少成功 sync 過一次
	ParitySyncing   bool       `json:"paritySyncing"`
	ParityLastSync  *time.Time `json:"parityLastSync,omitempty"`
	ParitySyncError string     `json:"paritySyncError,omitempty"`
	// DataDisks 是這個 pool 的資料碟掛載點清單,給「從校驗重建某一顆資料碟」的
	// UI 用(第六十輪壞盤重建)。只在有同位碟時才有重建的意義。
	DataDisks []string `json:"dataDisks,omitempty"`
}

func (s *Server) handleStorageArrayStatus(w http.ResponseWriter, r *http.Request) {
	array := s.getArray()
	if array == nil {
		writeJSON(w, http.StatusOK, arrayStatusResponse{Status: storage.Status{State: "unconfigured"}})
		return
	}
	snap := s.store.Snapshot()
	hasParity := snap.Pool != nil && len(snap.Pool.ParityDisks) > 0
	resp := arrayStatusResponse{
		Status:         array.Status(),
		HasParity:      hasParity,
		ParitySyncing:  s.paritySyncing.Load(),
		ParityLastSync: snap.ParityLastSync,
		Protected:      hasParity && snap.ParityLastSync != nil,
	}
	if hasParity && snap.Pool != nil {
		resp.DataDisks = append([]string(nil), snap.Pool.DataDisks...)
	}
	if e := s.paritySyncErr.Load(); e != nil {
		resp.ParitySyncError = *e
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStorageArraySync 觸發一次 SnapRAID sync(把目前資料狀態寫進同位碟,
// 「正式產生保護」的動作)。第五十八輪全鏈路覆核 P1:先前設了同位碟卻永遠
// 不會 sync,等於沒有保護。sync 在大陣列上可能跑很久且會寫同位碟,所以在背景
// goroutine 執行、用 atomic 旗標保證同一時間只有一個,狀態由 array 狀態端點回報。
func (s *Server) handleStorageArraySync(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Snapshot()
	if snap.Pool == nil {
		writeError(w, http.StatusBadRequest, errNoPoolConfigured)
		return
	}
	if len(snap.Pool.ParityDisks) == 0 {
		writeError(w, http.StatusBadRequest, errNoParityDisks)
		return
	}
	if !s.paritySyncing.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errParitySyncInProgress)
		return
	}
	// 第六十輪(使用者實機):清掉上一次同步留下的錯誤。原本開新的一次
	// 同步時沒有重置 paritySyncErr,導致「上次失敗過→這次重試」的自然流程
	// 裡,使用者切到別頁再回到儲存頁,arrayStatus 仍回報那個舊的錯誤字串,
	// 畫面就一直掛著紅字「同步失敗」,即使這次其實正在成功地重跑。開跑的
	// 當下就把錯誤清成乾淨狀態:同步中只顯示「同步中…」,真的又失敗才會
	// 再寫入新的錯誤。
	s.paritySyncErr.Store(nil)
	pool := *snap.Pool
	go func() {
		defer s.paritySyncing.Store(false)
		// 跟 HTTP 請求脫鉤的 context:sync 可能跑數小時,不能被瀏覽器關閉/斷線
		// 中途砍掉。給一個很寬鬆的上限,正常不會觸發。
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		// 確保設定檔是最新的(daemon 可能重啟過、/etc 可能被清過)。
		if err := s.writeSnapraidConfig(pool); err != nil {
			msg := "writing snapraid.conf: " + err.Error()
			s.paritySyncErr.Store(&msg)
			s.logger.Error("parity sync: could not write snapraid.conf", "err", err)
			return
		}
		s.logger.Info("parity sync starting", "pool", pool.Name)
		// 用 RunSnapraidSync(而非通用的 RunSnapraid):它會在遇到「太多磁碟
		// UUID 變了」時自動用 --force-uuid 重試一次。第六十輪使用者實機:磁碟
		// 重掛/換裝置節點後 sync 被 snapraid 的 UUID 檢查擋下,UI 上又沒有強制的
		// 出口,同位保護整個卡住;在 daemon 端自動處理(理由見 looksLikeUUIDChanged)。
		if out, usedForce, err := storage.RunSnapraidSync(ctx, s.runner, s.snapraidCfgPath); err != nil {
			msg := err.Error()
			s.paritySyncErr.Store(&msg)
			s.logger.Error("parity sync failed", "err", err, "out", string(out))
			return
		} else if usedForce {
			s.logger.Warn("parity sync needed --force-uuid (disks were remounted / device nodes changed; this is expected on GoNAS-managed pools)", "pool", pool.Name)
		}
		now := time.Now()
		if err := s.store.Update(func(st *state.State) error {
			// 第六十輪複審:只有在「目前的池還是我們剛剛同步的那個池」時,才把
			// ParityLastSync 標成現在——否則若同步期間使用者換了池
			// (handleStoragePoolSet 已把 ParityLastSync 清成 nil),這裡會把從沒同步過
			// 的新池誤標成「已受保護」。用池名比對身分(換池必然換名或換碟)。
			if st.Pool == nil || st.Pool.Name != pool.Name {
				s.logger.Warn("pool changed during the sync; not marking the new pool as protected", "syncedPool", pool.Name)
				return nil
			}
			st.ParityLastSync = &now
			return nil
		}); err != nil {
			s.logger.Error("parity sync succeeded but recording the last-sync time failed", "err", err)
		}
		s.paritySyncErr.Store(nil)
		s.logger.Info("parity sync completed", "pool", pool.Name)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}

// handleStorageArrayScrub 觸發一次 SnapRAID scrub(重新讀一部分資料、驗證同位
// 是否吻合,用來及早抓到靜默資料損毀 / bit rot)。第六十輪產品覆核:先前
// 只開放 sync,scrub 這個「驗證資料完整性」的動作在程式裡有、卻沒有出口。
// 跟 sync 共用同一個 single-flight 旗標(snapraid 同一時間只能有一個動作在跑),
// 一樣在背景 goroutine 執行,錯誤透過 paritySyncErr 回報。scrub 不寫
// ParityLastSync(那代表「上次把目前狀態寫進同位」的時間,是 sync 的語意)。
func (s *Server) handleStorageArrayScrub(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Snapshot()
	if snap.Pool == nil {
		writeError(w, http.StatusBadRequest, errNoPoolConfigured)
		return
	}
	if len(snap.Pool.ParityDisks) == 0 {
		writeError(w, http.StatusBadRequest, errNoParityDisks)
		return
	}
	if !s.paritySyncing.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errParitySyncInProgress)
		return
	}
	s.paritySyncErr.Store(nil)
	pool := *snap.Pool
	go func() {
		defer s.paritySyncing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		if err := s.writeSnapraidConfig(pool); err != nil {
			msg := "writing snapraid.conf: " + err.Error()
			s.paritySyncErr.Store(&msg)
			s.logger.Error("parity scrub: could not write snapraid.conf", "err", err)
			return
		}
		s.logger.Info("parity scrub starting", "pool", pool.Name)
		if out, err := storage.RunSnapraid(ctx, s.runner, s.snapraidCfgPath, storage.SnapraidScrub); err != nil {
			msg := err.Error()
			s.paritySyncErr.Store(&msg)
			s.logger.Error("parity scrub failed", "err", err, "out", string(out))
			return
		}
		s.paritySyncErr.Store(nil)
		s.logger.Info("parity scrub completed", "pool", pool.Name)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scrubbing"})
}

type fixDiskRequest struct {
	MountPoint string `json:"mountPoint"`
}

// handleStorageArrayFix 從同位資料「重建一顆資料碟」(對應 snapraid fix -d dN)。
// 第六十輪(產品 P0:壞盤更換/校驗重建)。使用者流程:壞盤 → 換上新空碟 →
// 用「準備磁碟」掛到「同一個掛載點」→ 對那個掛載點按「從校驗重建」。這支
// 把掛載點換算成 snapraid 的 dN 再跑 fix。具破壞性(會覆寫該碟內容),所以
// 前端有強確認;requireAdmin;跟 sync/scrub 共用 single-flight(背景執行)。
func (s *Server) handleStorageArrayFix(w http.ResponseWriter, r *http.Request) {
	var req fixDiskRequest
	if !readJSON(w, r, &req) {
		return
	}
	snap := s.store.Snapshot()
	if snap.Pool == nil {
		writeError(w, http.StatusBadRequest, errNoPoolConfigured)
		return
	}
	if len(snap.Pool.ParityDisks) == 0 {
		// 沒有同位碟就沒有東西可以用來重建。
		writeError(w, http.StatusBadRequest, errNoParityDisks)
		return
	}
	diskName, err := storage.DataDiskNameForMount(*snap.Pool, req.MountPoint)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.paritySyncing.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errParitySyncInProgress)
		return
	}
	s.paritySyncErr.Store(nil)
	pool := *snap.Pool
	go func() {
		defer s.paritySyncing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		if err := s.writeSnapraidConfig(pool); err != nil {
			msg := "writing snapraid.conf: " + err.Error()
			s.paritySyncErr.Store(&msg)
			s.logger.Error("disk rebuild: could not write snapraid.conf", "err", err)
			return
		}
		s.logger.Info("disk rebuild starting", "pool", pool.Name, "disk", diskName, "mount", req.MountPoint)
		if out, err := storage.RunSnapraidFix(ctx, s.runner, s.snapraidCfgPath, diskName); err != nil {
			msg := err.Error()
			s.paritySyncErr.Store(&msg)
			s.logger.Error("disk rebuild failed", "err", err, "disk", diskName, "out", string(out))
			return
		}
		s.paritySyncErr.Store(nil)
		s.logger.Info("disk rebuild completed", "pool", pool.Name, "disk", diskName)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rebuilding", "disk": diskName})
}

// --- 定時同位校驗(scrub)排程 ------------------------------------------

// startParityScrubScheduler 依 cfg 啟動(或停掉)定時 scrub 背景排程。停舊啟新,
// 給 PUT 設定後與 New() 啟動時共用。cfg.Enabled 為 false 時只停不啟。
func (s *Server) startParityScrubScheduler(cfg state.ParityScrubConfig) {
	s.parityScrubMu.Lock()
	defer s.parityScrubMu.Unlock()
	if s.parityScrubScheduler != nil {
		s.parityScrubScheduler.Stop()
		s.parityScrubScheduler = nil
	}
	if !cfg.Enabled {
		return
	}
	every := time.Duration(cfg.EveryDays) * 24 * time.Hour
	if every <= 0 {
		every = 7 * 24 * time.Hour
	}
	sched := storage.ParitySchedule{Every: every, HourOfDay: cfg.Hour, MinuteOfHr: cfg.Minute, Action: storage.SnapraidScrub}
	s.parityScrubScheduler = storage.NewScheduler(s.logger)
	s.parityScrubScheduler.Start(context.Background(), sched, s.runScheduledScrub)
	s.logger.Info("scheduled parity scrub enabled", "schedule", sched.Describe())
}

func (s *Server) stopParityScrubScheduler() {
	s.parityScrubMu.Lock()
	defer s.parityScrubMu.Unlock()
	if s.parityScrubScheduler != nil {
		s.parityScrubScheduler.Stop()
		s.parityScrubScheduler = nil
	}
}

// runScheduledScrub 是排程實際跑的動作:跟手動 sync/scrub 共用 paritySyncing
// single-flight(有別的同位動作在跑就跳過這次,不排隊堆疊),寫好 snapraid.conf
// 後跑一次 scrub。沒有同位碟就直接跳過(不算錯)。
func (s *Server) runScheduledScrub(ctx context.Context) error {
	snap := s.store.Snapshot()
	if snap.Pool == nil || len(snap.Pool.ParityDisks) == 0 {
		return nil
	}
	if !s.paritySyncing.CompareAndSwap(false, true) {
		s.logger.Info("scheduled scrub skipped: another parity operation is already running")
		return nil
	}
	defer s.paritySyncing.Store(false)
	s.paritySyncErr.Store(nil)
	pool := *snap.Pool
	if err := s.writeSnapraidConfig(pool); err != nil {
		msg := "writing snapraid.conf: " + err.Error()
		s.paritySyncErr.Store(&msg)
		return err
	}
	if out, err := storage.RunSnapraid(ctx, s.runner, s.snapraidCfgPath, storage.SnapraidScrub); err != nil {
		msg := err.Error()
		s.paritySyncErr.Store(&msg)
		s.logger.Error("scheduled parity scrub failed", "err", err, "out", string(out))
		return err
	}
	s.paritySyncErr.Store(nil)
	s.logger.Info("scheduled parity scrub completed", "pool", pool.Name)
	return nil
}

// handleStorageParityScheduleGet / Set 讀寫定時校驗設定。
func (s *Server) handleStorageParityScheduleGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().ParityScrub)
}

func (s *Server) handleStorageParityScheduleSet(w http.ResponseWriter, r *http.Request) {
	var cfg state.ParityScrubConfig
	if !readJSON(w, r, &cfg) {
		return
	}
	// 正規化 / 驗證輸入。
	if cfg.Enabled {
		if cfg.EveryDays < 1 {
			cfg.EveryDays = 7
		}
		if cfg.Hour < 0 || cfg.Hour > 23 || cfg.Minute < 0 || cfg.Minute > 59 {
			writeError(w, http.StatusBadRequest, errInvalidScrubSchedule)
			return
		}
	}
	if err := s.store.Update(func(st *state.State) error {
		st.ParityScrub = cfg
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.startParityScrubScheduler(cfg)
	writeJSON(w, http.StatusOK, cfg)
}

// handleStoragePoolSet 建立或取代目前的 pool 設定並持久化。刻意只驗證、
// 儲存設定,不會連帶啟動陣列 —— 「設定陣列該長什麼樣子」跟「掛載陣列讓它
// 可以讀寫」是兩個語意不同的動作,呼叫端要另外打 /array/start,理由跟
// storage.Array.Start 自己的註解一致(不要把多個危險動作隱含綁在一起)。
func (s *Server) handleStoragePoolSet(w http.ResponseWriter, r *http.Request) {
	var pool storage.PoolConfig
	if !readJSON(w, r, &pool) {
		return
	}
	if err := pool.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// 第六十輪 QA 覆核:同位同步/校驗進行中時,拒絕改池設定。否則正在跑的
	// sync goroutine 結束時會把 ParityLastSync 寫成 now,但那是「舊池」的同步,
	// 卻會讓剛換上的「新池」被誤標成「已受保護」(其實從沒同步過)。
	if s.paritySyncing.Load() {
		writeError(w, http.StatusConflict, errParitySyncInProgress)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.Pool = &pool
		// 新設定(或改設定)→ 同位保護要重新 sync 才算數,清掉上次同步時間,
		// 免得 UI 用舊的 pool 的同步時間誤標成「已受保護」。
		st.ParityLastSync = nil
		return nil
	}); err != nil {
		s.logger.Error("persisting pool config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 有同位碟就先把 snapraid.conf 寫出來,讓之後的「立即同步」有設定可用
	// (best-effort:寫不出來不擋存設定,sync 時會再寫一次)。
	if len(pool.ParityDisks) > 0 {
		if err := s.writeSnapraidConfig(pool); err != nil {
			s.logger.Warn("could not write snapraid.conf after saving pool (parity sync will regenerate it)", "err", err)
		}
	}

	// 第五十八輪全鏈路覆核(QA3):如果原本就有一個「正在啟動中(started)」
	// 的陣列,重設 pool 前要先把舊的 mergerfs 卸載掉,否則:掛載點沒變時新的
	// mergerfs 會疊掛在舊的 FUSE 掛載上面(寫入落在遮蔽層),掛載點變了時舊的
	// 掛載會被留著、變成沒人追蹤的孤兒掛載。用舊 Array 自己的 cfg 卸載(Stop
	// 用的是它記住的舊掛載點),才卸得到正確的位置。best-effort:卸不掉只記
	// 警告,不擋住重設設定。
	if old := s.getArray(); old != nil && old.Status().State == storage.StateStarted {
		if err := old.Stop(r.Context(), s.runner); err != nil {
			s.logger.Warn("could not unmount the previous array before reconfiguring (possible stale/stacked mount)", "err", err)
		}
	}

	// 換掉記憶體裡的 Array 物件：新設定跟舊陣列的執行狀態沒有關係,
	// 一律視為一個全新的、還沒啟動的陣列，即使舊陣列當時是 started 也一樣
	// ——「編輯設定」不該悄悄延續舊的執行狀態。透過 setArray 在寫鎖下換
	// 指標,跟背景監控輪詢的 getArray 讀取同步(見 Server.arrayMu)。
	array := storage.NewArray(pool)
	s.setArray(array)

	// 監控要看的磁碟使用率也跟著換成新陣列的掛載點,不然使用者改了 pool
	// 之後,監控頁顯示的還是舊路徑(或是還沒設定 pool 前的 "/")的用量,
	// 跟畫面上其他地方顯示的陣列資訊對不起來。
	s.monitorCollector.SetDiskPath(pool.MountPoint)

	// 第五十五輪覆核(產品 P1):設定好 pool 之後直接把陣列掛起來,讓它「馬上
	// 就能用」——新手嚮導建完 pool 就接著建共享,如果這裡不掛,使用者做完整個
	// 嚮導,/mnt/tank 卻沒掛載,檔案頁打不開、剛建的共享也是空的,而且畫面上
	// 完全沒有提示要去按「啟動陣列」。掛載是安全且預期的動作(不像 snapraid
	// sync 有副作用),所以在這裡連帶做。best-effort:掛不起來(mergerfs 沒裝
	// 之類)不讓「存設定」跟著失敗——設定已經存好了,回傳的 status 會帶著
	// failed 狀態與原因(前端 errorMap 會翻成看得懂的話,例如「mergerfs 尚未
	// 安裝」),使用者照著提示去補裝、再從儀表板按「啟動陣列」即可。
	if err := array.Start(r.Context(), s.runner); err != nil {
		s.logger.Warn("auto-starting the array after saving the pool failed (config saved; array left stopped)", "err", err)
	}

	writeJSON(w, http.StatusOK, array.Status())
}

// prepareDiskRequest 是 POST /api/v1/storage/disks/prepare 的請求主體。
type prepareDiskRequest struct {
	Device     string `json:"device"`
	MountPoint string `json:"mountPoint"`
}

// handleStoragePrepareDisk 把使用者「明確選定」的一顆整碟格式化成 ext4、
// 掛載到指定路徑、並寫進 fstab(開機自動掛載)。這是破壞性操作,所以是
// requireAdmin;所有安全防護(只接受整碟、拒絕使用中/系統碟、掛載點路徑
// 驗證、擋指令注入)都在 storage.PrepareDisk 裡。GoNAS 不會自動挑碟或
// 自動格式化——一定是使用者在網頁上選了特定的碟、明確確認才會到這裡。
func (s *Server) handleStoragePrepareDisk(w http.ResponseWriter, r *http.Request) {
	var req prepareDiskRequest
	if !readJSON(w, r, &req) {
		return
	}
	fstabPath := s.fstabPath
	if fstabPath == "" {
		fstabPath = storage.DefaultFstabPath
	}
	res, err := storage.PrepareDisk(r.Context(), s.runner, req.Device, req.MountPoint, fstabPath)
	if err != nil {
		s.logger.Warn("prepare disk refused or failed", "device", req.Device, "mountPoint", req.MountPoint, "err", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("prepared disk", "device", res.Device, "mountPoint", res.Mountpoint, "uuid", res.UUID)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleStorageArrayStart(w http.ResponseWriter, r *http.Request) {
	array := s.getArray()
	if array == nil {
		writeError(w, http.StatusConflict, errNoPoolConfigured)
		return
	}
	if err := array.Start(r.Context(), s.runner); err != nil {
		s.logger.Error("starting array failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, array.Status())
}

func (s *Server) handleStorageArrayStop(w http.ResponseWriter, r *http.Request) {
	array := s.getArray()
	if array == nil {
		writeError(w, http.StatusConflict, errNoPoolConfigured)
		return
	}
	if err := array.Stop(r.Context(), s.runner); err != nil {
		s.logger.Error("stopping array failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, array.Status())
}
