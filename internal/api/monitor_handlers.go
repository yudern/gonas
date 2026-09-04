package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// handleMonitorSystem 回傳最新一筆系統資源快照。Poller 一啟動就會立刻
// 取樣一次(見 monitor.Poller.Start),所以正常情況下 History 裡幾乎
// 馬上就有資料；萬一剛好在那個極短的時間窗口內被打到(History 還是空的),
// 直接同步再取樣一次而不是回一個「還沒有資料」的錯誤 —— 這支端點的
// 呼叫端(儀表板/監控頁)只想要一個當下能顯示的數字。
func (s *Server) handleMonitorSystem(w http.ResponseWriter, r *http.Request) {
	if snap, ok := s.monitorHistory.Latest(); ok {
		writeJSON(w, http.StatusOK, snap)
		return
	}
	snap, err := s.monitorCollector.Sample()
	if err != nil {
		s.logger.Error("monitor: on-demand system sample failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleMonitorHistory 回傳最近一段時間的取樣紀錄，給 Web UI 畫趨勢圖。
func (s *Server) handleMonitorHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.monitorHistory.Snapshot())
}

// alertRuleView 是 GET /monitor/alerts 回應的單一項目：規則本身加上
// AlertEngine 記錄的「目前是不是正在觸發中」，Web UI 才能直接畫燈號,
// 不用自己重新計算一次。
type alertRuleView struct {
	monitor.AlertRule
	Firing bool `json:"firing"`
}

func (s *Server) handleMonitorAlertsList(w http.ResponseWriter, r *http.Request) {
	rules := s.store.Snapshot().AlertRules
	firing := s.alertEngine.Firing()

	views := make([]alertRuleView, 0, len(rules))
	for _, rule := range rules {
		views = append(views, alertRuleView{AlertRule: rule, Firing: firing[rule.ID]})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleMonitorAlertsCreate(w http.ResponseWriter, r *http.Request) {
	var rule monitor.AlertRule
	if !readJSON(w, r, &rule) {
		return
	}
	rule.ID = newID()
	if err := rule.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.AlertRules = append(st.AlertRules, rule)
		return nil
	}); err != nil {
		s.logger.Error("persisting alert rule failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, alertRuleView{AlertRule: rule, Firing: false})
}

func (s *Server) handleMonitorAlertsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.AlertRules[:0]
		for _, rule := range st.AlertRules {
			if rule.ID == id {
				found = true
				continue
			}
			kept = append(kept, rule)
		}
		st.AlertRules = kept
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errAlertRuleNotFound)
		return
	}

	// 規則刪掉了,連帶清掉它在 AlertEngine 裡「目前是否觸發中」的記錄,
	// 避免之後同一個隨機 ID 萬一被重用(機率極低,但沒有理由留著垃圾)。
	s.alertEngine.Forget(id)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMonitorNotifiersList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().Notifiers)
}

func (s *Server) handleMonitorNotifiersCreate(w http.ResponseWriter, r *http.Request) {
	var cfg monitor.WebhookConfig
	if !readJSON(w, r, &cfg) {
		return
	}
	cfg.ID = newID()
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.Notifiers = append(st.Notifiers, cfg)
		return nil
	}); err != nil {
		s.logger.Error("persisting notifier config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.rebuildNotifier()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleMonitorNotifiersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.Notifiers[:0]
		for _, n := range st.Notifiers {
			if n.ID == id {
				found = true
				continue
			}
			kept = append(kept, n)
		}
		st.Notifiers = kept
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errNotifierNotFound)
		return
	}

	s.rebuildNotifier()
	w.WriteHeader(http.StatusNoContent)
}

// rebuildNotifier 依照目前持久化的 webhook 設定重新組出 AlertEngine 要用
// 的 Notifier,永遠包含一個 LogNotifier 保底,再加上每個已啟用的
// webhook —— 新增/刪除通知端點之後都要呼叫這個方法,daemon 剛啟動時
// 也是靠它從 state.json 裡讀回既有設定。
func (s *Server) rebuildNotifier() {
	notifiers := []monitor.Notifier{monitor.NewLogNotifier(s.logger)}
	for _, cfg := range s.store.Snapshot().Notifiers {
		if !cfg.Enabled {
			continue
		}
		notifiers = append(notifiers, monitor.NewWebhookNotifier(cfg, nil))
	}
	s.alertEngine.SetNotifier(monitor.MultiNotifier{Notifiers: notifiers})
}

// onMonitorSample 是餵給 monitor.Poller 的回呼：把這一輪的系統資源快照,
// 加上目前的陣列/SMART 健康狀態,一起交給 AlertEngine 評估。
func (s *Server) onMonitorSample(snap monitor.Snapshot) {
	facts := monitor.Facts{Snapshot: snap}

	if s.array != nil {
		facts.ArrayFailed = s.array.Status().State == storage.StateFailed
	}
	facts.SmartFailed = s.anyDiskSmartFailed(context.Background())

	rules := s.store.Snapshot().AlertRules
	s.alertEngine.Evaluate(context.Background(), rules, facts)
}

// anyDiskSmartFailed 對目前 pool 設定裡的每一顆碟跑一次 SMART 健康檢查。
// 刻意把單次呼叫的逾時抓短(5 秒):告警評估是背景週期性工作,不該因為
// 一顆碟的 smartctl 掛住就拖累整個輪詢節奏；某顆碟檢查失敗(裝置不支援
// SMART、smartctl 沒裝等)只記 debug log 略過，不會被當成「SMART 故障」
// 誤觸發告警 —— 只有真的讀到 Passed=false 才算數。
func (s *Server) anyDiskSmartFailed(ctx context.Context) bool {
	pool := s.store.Snapshot().Pool
	if pool == nil {
		return false
	}

	devices := make([]string, 0, len(pool.DataDisks)+len(pool.ParityDisks))
	devices = append(devices, pool.DataDisks...)
	devices = append(devices, pool.ParityDisks...)

	for _, dev := range devices {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		health, err := storage.CheckSmartHealth(checkCtx, s.runner, dev)
		cancel()
		if err != nil {
			s.logger.Debug("monitor: smart check failed, skipping", "device", dev, "err", err)
			continue
		}
		if !health.Passed {
			return true
		}
	}
	return false
}
