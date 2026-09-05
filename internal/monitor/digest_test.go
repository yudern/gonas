package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDigestScheduler_RunsRepeatedlyAndStops(t *testing.T) {
	s := NewDigestScheduler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	var runs int32
	// nextFn 回傳「立刻」("現在再過 1 毫秒"),讓排程器盡快重複觸發,
	// 不用真的等到下一個日曆分鐘——跟 backup.JobScheduler 測試同樣的
	// 技巧(見 internal/backup/scheduler_test.go 的說明,雖然這裡是
	// monitor 套件自己獨立實作的排程器)。
	nextFn := func(after time.Time) (time.Time, error) {
		return after.Add(1 * time.Millisecond), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx, nextFn, func(context.Context) {
		atomic.AddInt32(&runs, 1)
	})

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&runs) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&runs); got < 3 {
		t.Fatalf("expected at least 3 runs within the deadline, got %d", got)
	}

	s.Stop()
	afterStop := atomic.LoadInt32(&runs)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != afterStop {
		t.Errorf("expected no more runs after Stop(), went from %d to %d", afterStop, got)
	}
}

func TestDigestScheduler_StopsOnNextFnError(t *testing.T) {
	s := NewDigestScheduler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	var runs int32
	nextFn := func(after time.Time) (time.Time, error) {
		return time.Time{}, errors.New("boom")
	}

	s.Start(context.Background(), nextFn, func(context.Context) {
		atomic.AddInt32(&runs, 1)
	})

	// Stop() 在 nextFn 一開始就出錯、goroutine 已經自行結束的情況下
	// 呼叫也要安全——不能卡住等一個已經結束的 goroutine 的 done channel
	// 沒被 close。
	deadline := time.Now().Add(1 * time.Second)
	for s.done == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("expected the scheduler goroutine to have stopped after nextFn returned an error")
	}
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Errorf("expected runOnce to never be called when nextFn fails immediately, got %d calls", got)
	}
}

func TestDigestScheduler_StopBeforeStartIsNoop(t *testing.T) {
	s := NewDigestScheduler(nil)
	s.Stop() // 不應該 panic 或卡住
}

func TestBuildDigestEvent_NoFiringRulesReportsHealthy(t *testing.T) {
	in := DigestInput{
		Snapshot: Snapshot{
			CPUPercent:    12.5,
			MemPercent:    40.2,
			DiskPercent:   55.0,
			DiskPath:      "/mnt/tank",
			UptimeSeconds: 90000, // 1 天多一點
		},
	}

	ev := BuildDigestEvent(in)

	if ev.Kind != EventKindDigest {
		t.Errorf("expected EventKindDigest, got %q", ev.Kind)
	}
	if ev.Subject == "" {
		t.Error("expected a non-empty subject")
	}
	if ev.Message == "" {
		t.Error("expected a non-empty message")
	}
	if ev.At.IsZero() {
		t.Error("expected a non-zero timestamp")
	}
	if !strings.Contains(ev.Message, "12.5") || !strings.Contains(ev.Message, "40.2") || !strings.Contains(ev.Message, "55.0") {
		t.Errorf("expected the message to include the snapshot's numbers, got: %s", ev.Message)
	}
	if !strings.Contains(ev.Message, "目前沒有任何規則處於觸發中") {
		t.Errorf("expected the message to report no firing rules, got: %s", ev.Message)
	}
	if !strings.Contains(ev.Message, "還沒有設定任何備份工作") {
		t.Errorf("expected the message to report no backup jobs, got: %s", ev.Message)
	}
}

func TestBuildDigestEvent_IncludesFiringRulesAndBackupSummaries(t *testing.T) {
	in := DigestInput{
		Snapshot:        Snapshot{CPUPercent: 99.9},
		FiringRuleNames: []string{"CPU 過高", "陣列故障"},
		BackupSummaries: []string{"每日備份:上次成功於 2026-09-05 03:00"},
	}

	ev := BuildDigestEvent(in)

	if !strings.Contains(ev.Subject, "有告警觸發中") {
		t.Errorf("expected the subject to flag firing alerts, got: %s", ev.Subject)
	}
	if !strings.Contains(ev.Message, "CPU 過高") || !strings.Contains(ev.Message, "陣列故障") {
		t.Errorf("expected the message to list both firing rule names, got: %s", ev.Message)
	}
	if !strings.Contains(ev.Message, "每日備份:上次成功於 2026-09-05 03:00") {
		t.Errorf("expected the message to include the backup summary, got: %s", ev.Message)
	}
}
