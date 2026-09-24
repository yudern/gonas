package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

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
func (s *Server) handleStorageArrayStatus(w http.ResponseWriter, r *http.Request) {
	array := s.getArray()
	if array == nil {
		writeJSON(w, http.StatusOK, storage.Status{State: "unconfigured"})
		return
	}
	writeJSON(w, http.StatusOK, array.Status())
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

	if err := s.store.Update(func(st *state.State) error {
		st.Pool = &pool
		return nil
	}); err != nil {
		s.logger.Error("persisting pool config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
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
