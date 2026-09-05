package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/backup"
)

// TestHandleBackupJobsCreate_AcceptsCronKindSchedule 驗證 Phase 15 新增的
// cron 排程種類能透過既有的備份工作建立 API 完整儲存/回傳 —— 這條路徑
// 刻意沒有另外寫一個 handleBackupJobsCreate 分支邏輯,backup.Schedule
// 本身的 Validate()/JSON tag 擴充完就「自動」支援了,這個測試就是在驗證
// 這個「自動」的假設真的成立(handler 沒有任何地方假設 Schedule 一定是
// interval 種類、沒有任何欄位白名單漏掉 kind/cronExpr)。job 刻意不啟用
// (Enabled: false),避免測試留下一個一直在背景跑、直到行程結束才會被
// 回收的排程 goroutine。
func TestHandleBackupJobsCreate_AcceptsCronKindSchedule(t *testing.T) {
	s := newTestServer(t)

	body := createBackupJobRequest{
		Name:           "cron nightly",
		SourcePath:     "/mnt/tank/media",
		DestPath:       "/mnt/backup",
		RetentionCount: 7,
		Enabled:        false,
		Schedule: backup.Schedule{
			Kind:     backup.ScheduleKindCron,
			CronExpr: "30 2 * * *",
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/jobs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleBackupJobsCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var created backup.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if created.Schedule.Kind != backup.ScheduleKindCron {
		t.Errorf("expected persisted schedule kind %q, got %q", backup.ScheduleKindCron, created.Schedule.Kind)
	}
	if created.Schedule.CronExpr != "30 2 * * *" {
		t.Errorf("expected persisted cron expr '30 2 * * *', got %q", created.Schedule.CronExpr)
	}

	stored := s.store.Snapshot().BackupJobs
	if len(stored) != 1 {
		t.Fatalf("expected 1 backup job persisted to state, got %d", len(stored))
	}
	if stored[0].Schedule.CronExpr != "30 2 * * *" {
		t.Errorf("expected state.json to retain the cron expression, got %q", stored[0].Schedule.CronExpr)
	}
}

// TestHandleBackupJobsCreate_RejectsInvalidCronExpr 驗證語法錯誤的 cron
// 表達式在寫進 state.json 之前就會被 400 擋下,而不是留到排程真的觸發
// 那一刻才發現剖析失敗。
func TestHandleBackupJobsCreate_RejectsInvalidCronExpr(t *testing.T) {
	s := newTestServer(t)

	body := createBackupJobRequest{
		Name:           "broken cron",
		SourcePath:     "/mnt/tank/media",
		DestPath:       "/mnt/backup",
		RetentionCount: 7,
		Enabled:        false,
		Schedule: backup.Schedule{
			Kind:     backup.ScheduleKindCron,
			CronExpr: "this is not cron syntax",
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/jobs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleBackupJobsCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid cron expression, got %d: %s", rec.Code, rec.Body.String())
	}

	if stored := s.store.Snapshot().BackupJobs; len(stored) != 0 {
		t.Errorf("expected invalid job to NOT be persisted, but state has %d backup jobs", len(stored))
	}
}

// TestHandleBackupJobsCreate_IntervalKindStillWorks 是一個回歸測試:確認
// Phase 15 的 Schedule 擴充沒有動到原本(Kind 留空)的 interval 排程建立
// 路徑的行為。
func TestHandleBackupJobsCreate_IntervalKindStillWorks(t *testing.T) {
	s := newTestServer(t)

	body := createBackupJobRequest{
		Name:           "classic interval",
		SourcePath:     "/mnt/tank/media",
		DestPath:       "/mnt/backup",
		RetentionCount: 7,
		Enabled:        false,
		Schedule: backup.Schedule{
			EveryHours:   24,
			HourOfDay:    3,
			MinuteOfHour: 0,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/jobs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleBackupJobsCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var created backup.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if created.Schedule.EffectiveKind() != backup.ScheduleKindInterval {
		t.Errorf("expected effective kind %q, got %q", backup.ScheduleKindInterval, created.Schedule.EffectiveKind())
	}
	if created.Schedule.EveryHours != 24 {
		t.Errorf("expected EveryHours 24 preserved, got %d", created.Schedule.EveryHours)
	}
}

// TestHandleBackupJobsCreate_EnabledCronJob_SchedulerStartsAndStops 驗證
// Enabled: true 的 cron 工作真的會走到 startBackupScheduler ->
// backup.JobScheduler.Start 的 cron 分支而不會 panic 或立即報錯,並確認
// stopBackupScheduler 能乾淨地把它停掉,不會在測試結束後留下一個還在跑
// 的 goroutine。
func TestHandleBackupJobsCreate_EnabledCronJob_SchedulerStartsAndStops(t *testing.T) {
	s := newTestServer(t)

	body := createBackupJobRequest{
		Name:           "enabled cron",
		SourcePath:     "/mnt/tank/media",
		DestPath:       "/mnt/backup",
		RetentionCount: 7,
		Enabled:        true,
		Schedule: backup.Schedule{
			Kind:     backup.ScheduleKindCron,
			CronExpr: "0 0 1 1 *", // once a year, effectively never fires during the test
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/jobs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.backupSchedulers = make(map[string]*backup.JobScheduler)
	s.handleBackupJobsCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var created backup.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	s.backupMu.Lock()
	_, running := s.backupSchedulers[created.ID]
	s.backupMu.Unlock()
	if !running {
		t.Fatalf("expected an enabled cron job to register a running scheduler")
	}

	s.stopBackupScheduler(created.ID) // must return promptly, not hang
}
