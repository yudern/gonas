package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/state"
)

// newDigestTestServer 補上 digest handler 需要、但 newTestServer 沒有
// 預設初始化的欄位——monitorCollector/monitorHistory/alertEngine 平常
// 是 New() 在啟動時建立的,單元測試不會經過那條路徑,得自己補齊,
// 跟 newTestServerWithUpdateChecker(system_update_handlers_test.go)是
// 同樣的考量。monitorHistory 故意留空(不呼叫 Add),讓
// runDigestSend 走「History 沒有資料,現場取樣一次」那條路徑,這樣
// 測試不用依賴時間點正好取過樣。
func newDigestTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	s := &Server{
		store:            store,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitorCollector: monitor.NewCollector("/"),
		monitorHistory:   monitor.NewHistory(10),
		alertEngine:      monitor.NewAlertEngine(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	return s
}

func TestHandleMonitorDigestGet_DefaultsToDisabled(t *testing.T) {
	s := newDigestTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/digest", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDigestGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp digestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Enabled {
		t.Error("expected disabled by default")
	}
	if resp.NotifierIDs == nil || resp.EmailNotifierIDs == nil {
		t.Errorf("expected empty (not null) id slices, got %+v", resp)
	}
	if resp.LastSentAt != "" {
		t.Errorf("expected no lastSentAt yet, got %q", resp.LastSentAt)
	}
}

func TestHandleMonitorDigestSet_RejectsInvalidCronWhenEnabled(t *testing.T) {
	s := newDigestTestServer(t)

	body := jsonBody(t, digestSettingsRequest{Enabled: true, CronExpr: "not a cron expr"})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/monitor/digest", body)
	rec := httptest.NewRecorder()
	s.handleMonitorDigestSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid cron expression, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMonitorDigestSet_PersistsAndStartsScheduler(t *testing.T) {
	s := newDigestTestServer(t)
	t.Cleanup(s.stopDigestScheduler)

	body := jsonBody(t, digestSettingsRequest{Enabled: true, CronExpr: "0 8 * * *", NotifierIDs: []string{"wh1"}})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/monitor/digest", body)
	rec := httptest.NewRecorder()
	s.handleMonitorDigestSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	snap := s.store.Snapshot().Digest
	if !snap.Enabled || snap.CronExpr != "0 8 * * *" {
		t.Errorf("expected persisted digest config to reflect the request, got %+v", snap)
	}
	if len(snap.NotifierIDs) != 1 || snap.NotifierIDs[0] != "wh1" {
		t.Errorf("expected notifierIds to persist, got %+v", snap.NotifierIDs)
	}

	s.digestMu.Lock()
	started := s.digestScheduler != nil
	s.digestMu.Unlock()
	if !started {
		t.Error("expected enabling the digest with a valid cron expression to start the background scheduler")
	}

	// 停用之後排程要真的停掉,不能留著一個孤兒 goroutine 繼續跑。
	body2 := jsonBody(t, digestSettingsRequest{Enabled: false, CronExpr: "0 8 * * *"})
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/monitor/digest", body2)
	rec2 := httptest.NewRecorder()
	s.handleMonitorDigestSet(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 disabling, got %d: %s", rec2.Code, rec2.Body.String())
	}
	s.digestMu.Lock()
	stopped := s.digestScheduler == nil
	s.digestMu.Unlock()
	if !stopped {
		t.Error("expected disabling the digest to stop the background scheduler")
	}
}

// TestHandleMonitorDigestSend_RealWebhookReceivesDigestPayload 是端到端
// 驗證:架一個真正的 httptest.Server 當 webhook 接收端,設定成
// digest 的目標通知管道之一,呼叫真正的 handleMonitorDigestSend,確認
// webhook 真的收到一個 Kind=="digest" 的 JSON payload,而且
// state.State.Digest.LastSentAt 被更新。
func TestHandleMonitorDigestSend_RealWebhookReceivesDigestPayload(t *testing.T) {
	received := make(chan monitor.Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev monitor.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decoding webhook payload: %v", err)
		}
		received <- ev
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := newDigestTestServer(t)
	if err := s.store.Update(func(st *state.State) error {
		st.Notifiers = []monitor.WebhookConfig{{ID: "wh1", Name: "test", URL: srv.URL, Enabled: true}}
		st.BackupJobs = []backup.Job{{ID: "b1", Name: "每日備份"}}
		st.Digest = state.DigestConfig{NotifierIDs: []string{"wh1"}}
		return nil
	}); err != nil {
		t.Fatalf("seeding notifier: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/monitor/digest/send", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDigestSend(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case ev := <-received:
		if ev.Kind != monitor.EventKindDigest {
			t.Errorf("expected kind %q, got %q", monitor.EventKindDigest, ev.Kind)
		}
		if ev.Subject == "" || ev.Message == "" {
			t.Errorf("expected a non-empty subject/message, got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the webhook to receive the digest")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.store.Snapshot().Digest.LastSentAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.store.Snapshot().Digest.LastSentAt == nil {
		t.Error("expected lastSentAt to be recorded after a successful send")
	}
}

// TestHandleMonitorDigestSend_NoNotifiersConfiguredStillSucceeds 驗證
// 完全沒有選取任何 webhook/email 管道時,送出動作依然「成功」(至少會
// 走 LogNotifier 那條保底路徑),不會因為使用者還沒接上任何外部管道就
// 整個失敗——這是使用者在正式設定通知管道之前,拿來確認「摘要內容長
// 什麼樣子」的合理用法。
func TestHandleMonitorDigestSend_NoNotifiersConfiguredStillSucceeds(t *testing.T) {
	s := newDigestTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/monitor/digest/send", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDigestSend(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.store.Snapshot().Digest.LastSentAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.store.Snapshot().Digest.LastSentAt == nil {
		t.Error("expected lastSentAt to be recorded even with no webhook/email notifiers configured")
	}
}

func TestSummarizeBackupJob(t *testing.T) {
	neverRun := backup.Job{Name: "測試工作"}
	if got := summarizeBackupJob(neverRun); got != "測試工作:還沒有執行過。" {
		t.Errorf("unexpected summary for a job that never ran: %q", got)
	}

	finishedAt := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	success := backup.Job{Name: "每日備份", LastRun: &backup.RunResult{Success: true, FinishedAt: finishedAt}}
	if got := summarizeBackupJob(success); got != "每日備份:上次成功於 2026-09-05 03:00。" {
		t.Errorf("unexpected summary for a successful job: %q", got)
	}

	failed := backup.Job{Name: "每日備份", LastRun: &backup.RunResult{Success: false, FinishedAt: finishedAt, Error: "rsync exit status 23"}}
	if got := summarizeBackupJob(failed); got != "每日備份:上次執行失敗(2026-09-05 03:00)——rsync exit status 23。" {
		t.Errorf("unexpected summary for a failed job: %q", got)
	}
}
