package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/safe"
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

// redactWebhookURL 遮蔽 webhook URL 裡的密鑰部分——只保留 scheme://host,
// 有路徑就以「/…」帶過(Slack/Discord 的密鑰通常就在路徑裡)。第五十八輪
// 資安覆核(#2):notifier 清單端點是 requireAuth(不是 requireAdmin),
// 低權限的 viewer 帳號讀得到,原本回傳完整 URL 等於把可對頻道發訊息的密鑰
// 洩漏給 viewer。notifier 只有建立/刪除、沒有編輯,前端不需要拿回完整 URL,
// 所以清單一律遮蔽。
func redactWebhookURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(hidden)"
	}
	s := u.Scheme + "://" + u.Host
	if u.Path != "" && u.Path != "/" {
		s += "/…"
	}
	return s
}

func (s *Server) handleMonitorNotifiersList(w http.ResponseWriter, r *http.Request) {
	notifiers := s.store.Snapshot().Notifiers
	out := make([]monitor.WebhookConfig, len(notifiers))
	for i, n := range notifiers {
		n.URL = redactWebhookURL(n.URL)
		out[i] = n
	}
	writeJSON(w, http.StatusOK, out)
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

func (s *Server) handleMonitorEmailNotifiersList(w http.ResponseWriter, r *http.Request) {
	// 第五十八輪資安覆核(#1):這個端點是 requireAuth,viewer 帳號也讀得到。
	// EmailConfig.Password 是 SMTP 密碼,絕不能回給前端(跟 auth 那邊「永不
	// 回傳 PasswordHash/TOTPSecret」同一條規則)。清掉 Password 再回;因為它
	// 帶 omitempty,清成空字串後就不會出現在 JSON 裡。email notifier 只有
	// 建立/刪除、沒有編輯,前端不需要拿回密碼。
	notifiers := s.store.Snapshot().EmailNotifiers
	out := make([]monitor.EmailConfig, len(notifiers))
	for i, n := range notifiers {
		n.Password = ""
		out[i] = n
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMonitorEmailNotifiersCreate(w http.ResponseWriter, r *http.Request) {
	var cfg monitor.EmailConfig
	if !readJSON(w, r, &cfg) {
		return
	}
	cfg.ID = newID()
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.EmailNotifiers = append(st.EmailNotifiers, cfg)
		return nil
	}); err != nil {
		s.logger.Error("persisting email notifier config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.rebuildNotifier()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleMonitorEmailNotifiersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.EmailNotifiers[:0]
		for _, n := range st.EmailNotifiers {
			if n.ID == id {
				found = true
				continue
			}
			kept = append(kept, n)
		}
		st.EmailNotifiers = kept
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errEmailNotifierNotFound)
		return
	}

	s.rebuildNotifier()
	w.WriteHeader(http.StatusNoContent)
}

// rebuildNotifier 依照目前持久化的 webhook/email 設定重新組出
// AlertEngine 要用的 Notifier,永遠包含一個 LogNotifier 保底,再加上
// 每個已啟用的 webhook 跟 email 管道 —— 新增/刪除任一種通知端點之後
// 都要呼叫這個方法,daemon 剛啟動時也是靠它從 state.json 裡讀回既有
// 設定。
// buildNotifier 依目前 state 裡「已啟用」的 webhook / email 設定組出一個
// MultiNotifier(永遠含保底的 LogNotifier)。rebuildNotifier 用它更新告警
// 引擎的通知管道;需要送一次性通知(例如備份失敗)的地方也用它,確保
// 走的是跟告警完全一樣的一組管道,不會出現「告警有發、備份失敗卻沒發」
// 這種不一致。
func (s *Server) buildNotifier() monitor.MultiNotifier {
	notifiers := []monitor.Notifier{monitor.NewLogNotifier(s.logger)}
	snap := s.store.Snapshot()
	for _, cfg := range snap.Notifiers {
		if !cfg.Enabled {
			continue
		}
		notifiers = append(notifiers, monitor.NewWebhookNotifier(cfg, nil))
	}
	for _, cfg := range snap.EmailNotifiers {
		if !cfg.Enabled {
			continue
		}
		notifiers = append(notifiers, monitor.NewEmailNotifier(cfg, nil))
	}
	return monitor.MultiNotifier{Notifiers: notifiers}
}

func (s *Server) rebuildNotifier() {
	s.alertEngine.SetNotifier(s.buildNotifier())
}

// onMonitorSample 是餵給 monitor.Poller 的回呼：把這一輪的系統資源快照,
// 加上目前的陣列/SMART 健康狀態,一起交給 AlertEngine 評估。
func (s *Server) onMonitorSample(snap monitor.Snapshot) {
	// 一輪只快照一次 state(第三十四輪效能稽核:原本這裡拿規則、
	// anyDiskSmartFailed 裡又拿一次 pool,每 10 秒重複兩次)。
	st := s.store.Snapshot()

	facts := monitor.Facts{Snapshot: snap}

	if array := s.getArray(); array != nil {
		facts.ArrayFailed = array.Status().State == storage.StateFailed
	}
	// 只讀 SMART 健康的快取值(非阻塞);快取過期時會在背景重跑一次
	// 真正的 smartctl,不會卡住這條每 10 秒一次的輪詢。見 smartCheckInterval。
	facts.SmartFailed = s.cachedSmartFailed(st.Pool)

	s.alertEngine.Evaluate(context.Background(), st.AlertRules, facts)
}

// cachedSmartFailed 回傳「是否有任何碟的 SMART 回報 FAILED」的快取值。
// 快取超過 smartCheckInterval 時,啟動一個背景 goroutine 重跑真正的
// smartctl 檢查(同時間只會有一個在跑),當下這次呼叫仍立刻回傳上一次
// 的快取值——SMART 故障不是需要「這一秒就反應」的事件,用稍微舊一點的
// 值換掉「每 10 秒同步 fork 一堆 smartctl、把硬碟吵醒」完全划算。
func (s *Server) cachedSmartFailed(pool *storage.PoolConfig) bool {
	s.smartMu.Lock()
	failed := s.smartFailed
	needRefresh := !s.smartChecking && time.Since(s.smartChecked) >= smartCheckInterval
	if needRefresh {
		s.smartChecking = true
	}
	s.smartMu.Unlock()

	if needRefresh {
		go safe.Run(s.logger, "smart-health-refresh", func() {
			result := s.probeSmartFailed(context.Background(), pool)
			s.smartMu.Lock()
			s.smartFailed = result
			s.smartChecked = time.Now()
			s.smartChecking = false
			s.smartMu.Unlock()
		})
	}
	return failed
}

// probeSmartFailed 是實際去 fork smartctl 的那一段——對 pool 裡每一顆
// 資料碟+同位碟跑一次 `smartctl -a`,任何一顆回報非 PASSED 就算失敗。
// 只由 cachedSmartFailed 的背景 goroutine 呼叫(至多每 smartCheckInterval
// 一次),不再掛在每 10 秒的輪詢上。
func (s *Server) probeSmartFailed(ctx context.Context, pool *storage.PoolConfig) bool {
	if pool == nil {
		return false
	}

	// pool 裡存的是掛載點(/mnt/diskN),smartctl 只能開裝置節點(/dev/sdX)——
	// 直接拿掛載點查會永遠「打不開」而靜默略過,等於告警從不觸發。先解析成裝置
	// 節點(見 storage.DeviceForSmart;會去重同一顆碟的多個分割區)再查。
	devices := s.resolveSmartDevices(ctx, pool)

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
