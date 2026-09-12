package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// TestRunBackupJob_FailureSendsNotification 固化第三十二輪的修法 ③:一次
// 排程備份失敗時,要透過跟告警同一組的通知管道主動送出「備份失敗」通知,
// 不再只寫 log。這裡掛一個假的 webhook,跑一個一定會失敗的備份工作
// (來源路徑不存在),斷言 webhook 真的收到一則 kind=backup_failed 的事件。
func TestRunBackupJob_FailureSendsNotification(t *testing.T) {
	s := newAuthTestServer(t)
	s.runner = storage.NewExecRunner()

	received := make(chan monitor.Event, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev monitor.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		received <- ev
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	const jobID = "job-1"
	if err := s.store.Update(func(st *state.State) error {
		st.Notifiers = append(st.Notifiers, monitor.WebhookConfig{
			ID: "w1", Name: "test-hook", URL: ts.URL, Enabled: true,
		})
		st.BackupJobs = append(st.BackupJobs, backup.Job{
			ID:             jobID,
			Name:           "nightly-docs",
			SourcePath:     "/gonas-nonexistent-source-xyz",
			DestPath:       t.TempDir(),
			RetentionCount: 1,
			Enabled:        true,
		})
		return nil
	}); err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	s.runBackupJob(context.Background(), jobID)

	select {
	case ev := <-received:
		if ev.Kind != monitor.EventKindBackupFailed {
			t.Errorf("expected kind=%q, got %q", monitor.EventKindBackupFailed, ev.Kind)
		}
		if !strings.Contains(ev.Subject, "nightly-docs") {
			t.Errorf("expected subject to name the job, got %q", ev.Subject)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no backup-failure notification was delivered to the webhook")
	}

	// 確認失敗結果也照舊有寫回 LastRun(通知是「額外」加的,不能取代原本
	// 的狀態記錄)。
	jobs := s.store.Snapshot().BackupJobs
	if len(jobs) != 1 || jobs[0].LastRun == nil || jobs[0].LastRun.Success {
		t.Errorf("expected LastRun to be recorded as failed, got %+v", jobs)
	}
}
