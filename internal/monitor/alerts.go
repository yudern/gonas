package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// EventKind 分辨 Notify 收到的 Event 到底是什麼性質的內容。零值
// EventKindAlert 維持 Phase 5/Phase 14 就有的「一條告警規則觸發狀態
// 轉換」語意跟既有 JSON payload 格式完全不變(舊版已經在用的 webhook
// 接收端不會因為這次改動而收到多出來的、看不懂的必要欄位變動)。
// EventKindDigest 是 Phase 18c 新增的「週期性健康摘要」——同一組
// Notifier(LogNotifier/WebhookNotifier/EmailNotifier)能直接重複利用
// 來送這種完全不同性質的內容,不需要另外設計一整套平行的通知管道
// 介面,見 digest.go 的說明。
type EventKind string

const (
	EventKindAlert  EventKind = ""
	EventKindDigest EventKind = "digest"
	// EventKindBackupFailed 是「一次排程備份失敗」的一次性通知。跟 digest
	// 一樣走 Subject/Message 這組通用欄位(Rule/Firing/Value 沒有意義),
	// 由組出 Event 的那一端(internal/api 的備份執行邏輯)決定標題與內文。
	// 第三十二輪新增:原本備份失敗只寫 log + 更新 LastRun,使用者不打開
	// 網頁根本不知道備份已經連續失敗——現在改成也走通知管道主動告知。
	EventKindBackupFailed EventKind = "backup_failed"
)

// Event 是交給 Notifier 的通知內容。Kind 為零值(EventKindAlert)時,
// 意思是「一條告警規則從『沒觸發』變成『觸發中』、或從『觸發中』變回
// 『沒觸發』」,Firing=false 代表「解除」通知,而不是「這條規則本來就
// 沒事」——AlertEngine 只在狀態真的轉換時才會產生這種 Event。
//
// Kind 為 EventKindDigest 時,Rule/Firing/Value 這三個告警專用的欄位
// 沒有意義(維持零值),改看 Subject/Message——digest 的內容天生是
// 「多個不相關指標的摘要」,沒辦法套用告警事件那種「一條規則、一個
// 數值、一個門檻」的固定欄位表示,直接讓組出 Event 的那一端
// (BuildDigestEvent)決定要講什麼比較合理,Notifier 只需要知道
// 「這是一段給人看的標題+內文」。
type Event struct {
	Kind   EventKind `json:"kind,omitempty"`
	Rule   AlertRule `json:"rule,omitempty"`
	Firing bool      `json:"firing,omitempty"`
	Value  float64   `json:"value,omitempty"`
	At     time.Time `json:"at"`

	Subject string `json:"subject,omitempty"`
	Message string `json:"message,omitempty"`
}

// Notifier 是告警通知的送出介面，讓 AlertEngine 不用知道通知實際上是
// 寫進 log、打一個 webhook、還是之後可能加的其他管道。
type Notifier interface {
	Notify(ctx context.Context, ev Event) error
}

// LogNotifier 把告警寫進結構化 log，永遠可用、不需要任何額外設定,
// 是 AlertEngine 的預設/保底通知管道 —— 就算使用者沒設定任何 webhook,
// 告警至少會出現在 daemon 的 log 裡，而不是悄悄地什麼都沒發生。
type LogNotifier struct {
	logger *slog.Logger
}

func NewLogNotifier(logger *slog.Logger) *LogNotifier {
	return &LogNotifier{logger: logger}
}

func (n *LogNotifier) Notify(_ context.Context, ev Event) error {
	if ev.Kind == EventKindDigest {
		n.logger.Info("health digest", "subject", ev.Subject)
		return nil
	}
	if ev.Kind == EventKindBackupFailed {
		n.logger.Error("backup failed", "subject", ev.Subject, "message", ev.Message)
		return nil
	}
	if ev.Firing {
		n.logger.Warn("alert firing", "rule", ev.Rule.Name, "metric", ev.Rule.Metric, "value", ev.Value, "threshold", ev.Rule.Threshold)
	} else {
		n.logger.Info("alert resolved", "rule", ev.Rule.Name, "metric", ev.Rule.Metric, "value", ev.Value)
	}
	return nil
}

// WebhookConfig 是使用者設定的一個 webhook 通知端點，持久化在
// internal/state 裡(見 state.State.Notifiers)。
type WebhookConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// Validate 檢查 webhook 設定本身，不會真的發出任何網路請求 —— 這個
// 開發沙盒的網路白名單擋掉了任意外部 URL(見專案其他套件的「已知取捨」),
// 所以這裡的驗證刻意只做「URL 格式合不合法、是不是 http(s)」這種靜態
// 檢查,實際送達與否交給送出當下的錯誤處理跟使用者自己確認。
func (c WebhookConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("notifier name is required")
	}
	if c.URL == "" {
		return fmt.Errorf("notifier url is required")
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("invalid notifier url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("notifier url must be http or https, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("notifier url is missing a host")
	}
	return nil
}

// WebhookNotifier 用標準函式庫的 net/http 把 Event 編碼成 JSON、POST 給
// 設定的 URL —— 跟 internal/docker 一樣不透過任何第三方 HTTP 用戶端套件。
type WebhookNotifier struct {
	config     WebhookConfig
	httpClient *http.Client
}

// NewWebhookNotifier 建立一個 WebhookNotifier。httpClient 為 nil 時使用
// 一個有 5 秒逾時的預設用戶端,避免一個沒回應的端點卡住整個告警評估流程；
// 測試可以傳入指向 httptest.Server 的用戶端。
func NewWebhookNotifier(cfg WebhookConfig, httpClient *http.Client) *WebhookNotifier {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &WebhookNotifier{config: cfg, httpClient: httpClient}
}

func (n *WebhookNotifier) Notify(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encoding webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.config.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook %q: %w", n.config.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %q returned unexpected status %d", n.config.Name, resp.StatusCode)
	}
	return nil
}

// MultiNotifier 把一個 Event 同時送給多個 Notifier，即使其中幾個失敗
// 也會繼續嘗試其他的 —— 一個 webhook 端點掛掉，不該連 log 通知都一起
// 沒送到。回傳遇到的第一個錯誤方便呼叫端記 log，但不影響其他 Notifier
// 是否被呼叫到。
type MultiNotifier struct {
	Notifiers []Notifier
}

func (m MultiNotifier) Notify(ctx context.Context, ev Event) error {
	var firstErr error
	for _, n := range m.Notifiers {
		if err := n.Notify(ctx, ev); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// AlertEngine 評估一組 AlertRule 對著一份 Facts 的結果，只在規則的觸發
// 狀態真的「轉換」時才呼叫 Notifier —— 例如 CPU 使用率持續 95% 十分鐘,
// 使用者只會收到一次「觸發」通知，而不是每一輪輪詢都再收到一次。
type AlertEngine struct {
	logger *slog.Logger

	mu       sync.Mutex
	firing   map[string]bool
	notifier Notifier
}

// NewAlertEngine 建立一個 AlertEngine，一開始只掛預設的 LogNotifier；
// 呼叫端(通常是 internal/api,在使用者新增/修改 webhook 設定時)可以
// 用 SetNotifier 換掉，換的時候不影響既有的觸發狀態記錄。
func NewAlertEngine(logger *slog.Logger) *AlertEngine {
	return &AlertEngine{
		logger:   logger,
		firing:   make(map[string]bool),
		notifier: NewLogNotifier(logger),
	}
}

// SetNotifier 替換目前使用的 Notifier(通常是一個包了 LogNotifier +
// 使用者設定的所有已啟用 webhook 的 MultiNotifier)。
func (e *AlertEngine) SetNotifier(n Notifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n == nil {
		n = NewLogNotifier(e.logger)
	}
	e.notifier = n
}

// Evaluate 對每一條已啟用的規則評估目前的 Facts，狀態轉換時送出通知。
// 規則本身不合法(理論上不該發生，因為 API 層在存進 state 前就會呼叫
// AlertRule.Validate)或評估過程出錯，只記 log、不會讓其他規則的評估
// 因此中斷。
func (e *AlertEngine) Evaluate(ctx context.Context, rules []AlertRule, facts Facts) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		cond, err := evaluateRule(r, facts)
		if err != nil {
			e.logger.Error("monitor: evaluating alert rule failed", "rule", r.Name, "err", err)
			continue
		}

		e.mu.Lock()
		wasFiring := e.firing[r.ID]
		stateChanged := cond != wasFiring
		if stateChanged {
			e.firing[r.ID] = cond
		}
		notifier := e.notifier
		e.mu.Unlock()

		if !stateChanged {
			continue
		}

		value := valueForRule(r, facts)
		ev := Event{Rule: r, Firing: cond, Value: value, At: time.Now()}
		if err := notifier.Notify(ctx, ev); err != nil {
			e.logger.Error("monitor: sending alert notification failed", "rule", r.Name, "err", err)
		}
	}
}

// Firing 回傳目前處於觸發中的規則 ID 集合的副本，給 API /monitor/alerts
// 端點在每條規則旁邊標出目前是否正在告警用。
func (e *AlertEngine) Firing() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]bool, len(e.firing))
	for k, v := range e.firing {
		out[k] = v
	}
	return out
}

// Forget 移除一條規則的觸發狀態記錄，在規則被刪除時呼叫,避免 firing
// map 裡累積永遠不會再被評估、也不會再被清掉的殘留項目。
func (e *AlertEngine) Forget(ruleID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.firing, ruleID)
}

func valueForRule(r AlertRule, f Facts) float64 {
	switch r.Metric {
	case MetricCPUPercent:
		return f.Snapshot.CPUPercent
	case MetricMemPercent:
		return f.Snapshot.MemPercent
	case MetricDiskPercent:
		return f.Snapshot.DiskPercent
	case MetricArrayFailed:
		if f.ArrayFailed {
			return 1
		}
		return 0
	case MetricSmartFailed:
		if f.SmartFailed {
			return 1
		}
		return 0
	default:
		return 0
	}
}
