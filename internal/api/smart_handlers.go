package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// startSmartTestScheduler 依 cfg 啟動(或停掉)定時 SMART 自我測試背景排程。
// 停舊啟新,給 PUT 設定後與 New() 啟動時共用。cfg.Enabled 為 false 時只停不啟。
func (s *Server) startSmartTestScheduler(cfg state.SmartTestConfig) {
	s.smartTestMu.Lock()
	defer s.smartTestMu.Unlock()
	if s.smartTestScheduler != nil {
		s.smartTestScheduler.Stop()
		s.smartTestScheduler = nil
	}
	if !cfg.Enabled {
		return
	}
	every := time.Duration(cfg.EveryDays) * 24 * time.Hour
	if every <= 0 {
		every = 7 * 24 * time.Hour
	}
	s.smartTestScheduler = storage.NewScheduler(s.logger)
	s.smartTestScheduler.StartPeriodic(context.Background(), every, cfg.Hour, cfg.Minute, "smart-selftest", s.runScheduledSmartTest)
	s.logger.Info("scheduled SMART self-test enabled", "everyDays", cfg.EveryDays, "at", cfg.Hour, "kind", cfg.Kind)
}

func (s *Server) stopSmartTestScheduler() {
	s.smartTestMu.Lock()
	defer s.smartTestMu.Unlock()
	if s.smartTestScheduler != nil {
		s.smartTestScheduler.Stop()
		s.smartTestScheduler = nil
	}
}

// smartTestDevices 回傳要跑自我測試的裝置清單 —— 跟被動 SMART 監控
// (probeSmartFailed)完全一樣:pool 的資料碟 + 同位碟,確保兩者看的是同一批碟。
func smartTestDevices(pool *storage.PoolConfig) []string {
	if pool == nil {
		return nil
	}
	devices := make([]string, 0, len(pool.DataDisks)+len(pool.ParityDisks))
	devices = append(devices, pool.DataDisks...)
	devices = append(devices, pool.ParityDisks...)
	return devices
}

// runScheduledSmartTest 是排程實際跑的動作:對 pool 裡每一顆碟觸發一次 SMART
// 自我測試(由硬碟自己在背景跑,不佔 goroutine)。沒設 pool 就跳過(不算錯)。
// best-effort:個別碟觸發失敗只記 log,不讓其他碟跟著不測。
func (s *Server) runScheduledSmartTest(ctx context.Context) error {
	snap := s.store.Snapshot()
	devices := smartTestDevices(snap.Pool)
	if len(devices) == 0 {
		return nil
	}
	kind := storage.SmartSelfTestKind(snap.SmartTest.Kind)
	if !kind.Valid() {
		kind = storage.SmartSelfTestShort
	}
	for _, dev := range devices {
		runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := storage.StartSmartSelfTest(runCtx, s.runner, dev, kind); err != nil {
			s.logger.Warn("starting scheduled SMART self-test failed", "device", dev, "kind", kind, "err", err)
		}
		cancel()
	}
	now := time.Now()
	if err := s.store.Update(func(st *state.State) error {
		st.SmartTest.LastRunAt = &now
		return nil
	}); err != nil {
		s.logger.Warn("persisting SMART self-test last-run time failed", "err", err)
	}
	s.logger.Info("scheduled SMART self-test triggered", "devices", len(devices), "kind", kind)
	return nil
}

func (s *Server) handleStorageSmartScheduleGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().SmartTest)
}

func (s *Server) handleStorageSmartScheduleSet(w http.ResponseWriter, r *http.Request) {
	var cfg state.SmartTestConfig
	if !readJSON(w, r, &cfg) {
		return
	}
	if cfg.Enabled {
		if cfg.EveryDays < 1 {
			cfg.EveryDays = 7
		}
		if cfg.Hour < 0 || cfg.Hour > 23 || cfg.Minute < 0 || cfg.Minute > 59 {
			writeError(w, http.StatusBadRequest, errInvalidSmartSchedule)
			return
		}
		if !storage.SmartSelfTestKind(cfg.Kind).Valid() {
			cfg.Kind = string(storage.SmartSelfTestShort)
		}
	}
	// 保留先前記錄的 LastRunAt(設定表單不帶這個欄位,別被零值蓋掉)。
	prev := s.store.Snapshot().SmartTest.LastRunAt
	cfg.LastRunAt = prev
	if err := s.store.Update(func(st *state.State) error {
		st.SmartTest = cfg
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.startSmartTestScheduler(cfg)
	writeJSON(w, http.StatusOK, cfg)
}

// smartTestRunRequest 是手動觸發自我測試的請求 body:kind 選 short/long,留空
// 用設定裡的 kind(再退回 short)。
type smartTestRunRequest struct {
	Kind string `json:"kind"`
}

// handleStorageSmartTestRun 立刻對 pool 裡每顆碟觸發一次自我測試(不等測試跑完,
// 測試由硬碟自己背景執行)。requireAdmin。沒設 pool 回 400。
func (s *Server) handleStorageSmartTestRun(w http.ResponseWriter, r *http.Request) {
	var req smartTestRunRequest
	if !readJSON(w, r, &req) {
		return
	}
	snap := s.store.Snapshot()
	devices := smartTestDevices(snap.Pool)
	if len(devices) == 0 {
		writeError(w, http.StatusBadRequest, errNoPoolConfigured)
		return
	}
	kind := storage.SmartSelfTestKind(req.Kind)
	if !kind.Valid() {
		if k := storage.SmartSelfTestKind(snap.SmartTest.Kind); k.Valid() {
			kind = k
		} else {
			kind = storage.SmartSelfTestShort
		}
	}

	var failed []string
	for _, dev := range devices {
		runCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		if err := storage.StartSmartSelfTest(runCtx, s.runner, dev, kind); err != nil {
			s.logger.Warn("manual SMART self-test failed to start", "device", dev, "err", err)
			failed = append(failed, dev)
		}
		cancel()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"kind":    string(kind),
		"devices": devices,
		"failed":  failed,
		"started": len(devices) - len(failed),
	})
}

// handleStorageSmartSelfTestLog 讀某顆碟的自我測試紀錄。device 以 query 參數
// 指定,且必須是目前 pool 裡的碟(擋住拿任意路徑去 fork smartctl)。requireAuth。
func (s *Server) handleStorageSmartSelfTestLog(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if device == "" {
		writeError(w, http.StatusBadRequest, errSmartDeviceRequired)
		return
	}
	// 只允許查目前 pool 裡的碟 —— 不讓這個端點變成「對任意路徑 fork smartctl」。
	allowed := false
	for _, d := range smartTestDevices(s.store.Snapshot().Pool) {
		if d == device {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusBadRequest, errSmartDeviceUnknown)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	entries, err := storage.ReadSmartSelfTestLog(ctx, s.runner, device)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []storage.SmartSelfTestEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
