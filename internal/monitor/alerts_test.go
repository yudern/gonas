package monitor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeNotifier 記錄每一次 Notify 呼叫，讓測試斷言 AlertEngine 什麼時候
// (不)送出通知，不需要真的架一個 HTTP 端點。
type fakeNotifier struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (f *fakeNotifier) Notify(_ context.Context, ev Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return f.err
}

func (f *fakeNotifier) recorded() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Event, len(f.events))
	copy(out, f.events)
	return out
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAlertEngine_Evaluate_FiresOnceOnTransitionToTriggered(t *testing.T) {
	engine := NewAlertEngine(newTestLogger())
	fake := &fakeNotifier{}
	engine.SetNotifier(fake)

	rule := AlertRule{ID: "r1", Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: true}

	// 第一輪:95% 超標,應該觸發一次通知。
	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 95}})
	// 第二、三輪:持續超標,不應該重複通知(防抖)。
	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 96}})
	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 97}})

	events := fake.recorded()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 notification for sustained firing, got %d: %+v", len(events), events)
	}
	if !events[0].Firing {
		t.Errorf("expected first event to be Firing=true, got %+v", events[0])
	}
	if events[0].Value != 95 {
		t.Errorf("expected recorded Value=95, got %v", events[0].Value)
	}
}

func TestAlertEngine_Evaluate_NotifiesOnResolve(t *testing.T) {
	engine := NewAlertEngine(newTestLogger())
	fake := &fakeNotifier{}
	engine.SetNotifier(fake)

	rule := AlertRule{ID: "r1", Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: true}

	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 95}}) // fires
	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 10}}) // resolves

	events := fake.recorded()
	if len(events) != 2 {
		t.Fatalf("expected 2 notifications (fire + resolve), got %d: %+v", len(events), events)
	}
	if !events[0].Firing {
		t.Errorf("expected event[0].Firing=true, got %+v", events[0])
	}
	if events[1].Firing {
		t.Errorf("expected event[1].Firing=false (resolved), got %+v", events[1])
	}
}

func TestAlertEngine_Evaluate_DisabledRuleNeverFires(t *testing.T) {
	engine := NewAlertEngine(newTestLogger())
	fake := &fakeNotifier{}
	engine.SetNotifier(fake)

	rule := AlertRule{ID: "r1", Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: false}
	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 99}})

	if len(fake.recorded()) != 0 {
		t.Errorf("expected disabled rule to never notify, got %+v", fake.recorded())
	}
}

func TestAlertEngine_Firing_ReflectsCurrentState(t *testing.T) {
	engine := NewAlertEngine(newTestLogger())
	rule := AlertRule{ID: "r1", Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: true}

	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 95}})
	if !engine.Firing()["r1"] {
		t.Errorf("expected rule r1 to be recorded as firing")
	}

	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 10}})
	if engine.Firing()["r1"] {
		t.Errorf("expected rule r1 to no longer be firing after resolving")
	}
}

func TestAlertEngine_Forget_RemovesFiringState(t *testing.T) {
	engine := NewAlertEngine(newTestLogger())
	rule := AlertRule{ID: "r1", Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: true}

	engine.Evaluate(context.Background(), []AlertRule{rule}, Facts{Snapshot: Snapshot{CPUPercent: 95}})
	if _, ok := engine.Firing()["r1"]; !ok {
		t.Fatalf("expected r1 to be tracked before Forget")
	}
	engine.Forget("r1")
	if _, ok := engine.Firing()["r1"]; ok {
		t.Errorf("expected r1 to be removed from firing state after Forget")
	}
}

func TestAlertEngine_InvalidRuleMetric_DoesNotBlockOtherRules(t *testing.T) {
	engine := NewAlertEngine(newTestLogger())
	fake := &fakeNotifier{}
	engine.SetNotifier(fake)

	broken := AlertRule{ID: "bad", Name: "bad rule", Metric: "bogus", Enabled: true}
	good := AlertRule{ID: "good", Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: true}

	engine.Evaluate(context.Background(), []AlertRule{broken, good}, Facts{Snapshot: Snapshot{CPUPercent: 95}})

	events := fake.recorded()
	if len(events) != 1 || events[0].Rule.ID != "good" {
		t.Errorf("expected the broken rule to be skipped and the good rule to still fire, got %+v", events)
	}
}

func TestWebhookConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     WebhookConfig
		wantErr bool
	}{
		{"valid https", WebhookConfig{Name: "slack", URL: "https://example.com/hook"}, false},
		{"valid http", WebhookConfig{Name: "local", URL: "http://localhost:9000/hook"}, false},
		{"missing name", WebhookConfig{URL: "https://example.com/hook"}, true},
		{"missing url", WebhookConfig{Name: "slack"}, true},
		{"bad scheme", WebhookConfig{Name: "slack", URL: "ftp://example.com/hook"}, true},
		{"missing host", WebhookConfig{Name: "slack", URL: "https:///hook"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestWebhookNotifier_PostsEventAsJSON 對著一個真的在跑的 httptest.Server
// 送出通知(不是假的 http.RoundTripper),斷言請求方法、Content-Type、
// 以及請求主體確實是可以解回 Event 的合法 JSON —— 這是這個套件裡
// 唯一會真的過網路(即使只是 loopback)的路徑,值得用真實的 HTTP
// server 驗證,而不是只斷言呼叫參數。
func TestWebhookNotifier_PostsEventAsJSON(t *testing.T) {
	var gotMethod, gotContentType string
	var gotBody Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("server failed to decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewWebhookNotifier(WebhookConfig{Name: "test", URL: srv.URL, Enabled: true}, srv.Client())
	ev := Event{
		Rule:   AlertRule{ID: "r1", Name: "high cpu", Metric: MetricCPUPercent},
		Firing: true,
		Value:  95.5,
	}
	if err := notifier.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody.Rule.ID != "r1" || gotBody.Value != 95.5 || !gotBody.Firing {
		t.Errorf("decoded request body = %+v, want to match sent event", gotBody)
	}
}

func TestWebhookNotifier_NonSuccessStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	notifier := NewWebhookNotifier(WebhookConfig{Name: "test", URL: srv.URL}, srv.Client())
	err := notifier.Notify(context.Background(), Event{Rule: AlertRule{ID: "r1"}})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestMultiNotifier_ContinuesAfterOneFails(t *testing.T) {
	failing := &fakeNotifier{err: context.DeadlineExceeded}
	succeeding := &fakeNotifier{}
	multi := MultiNotifier{Notifiers: []Notifier{failing, succeeding}}

	err := multi.Notify(context.Background(), Event{Rule: AlertRule{ID: "r1"}})
	if err == nil {
		t.Fatal("expected the first notifier's error to propagate")
	}
	if len(succeeding.recorded()) != 1 {
		t.Errorf("expected the second notifier to still be called despite the first failing, got %d calls", len(succeeding.recorded()))
	}
}
