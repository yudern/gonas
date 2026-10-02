package storage

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestNextRunDelay_LaterToday(t *testing.T) {
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	sched := ParitySchedule{HourOfDay: 15, MinuteOfHr: 30}

	got := nextRunDelay(now, sched)
	want := 5*time.Hour + 30*time.Minute
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestNextRunDelay_AlreadyPassedToday_RollsToTomorrow(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	sched := ParitySchedule{HourOfDay: 3, MinuteOfHr: 0}

	got := nextRunDelay(now, sched)
	want := 7 * time.Hour // 20:00 -> 隔天 03:00
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestNextRunDelay_ExactlyNow_RollsToTomorrow(t *testing.T) {
	now := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	sched := ParitySchedule{HourOfDay: 3, MinuteOfHr: 0}

	got := nextRunDelay(now, sched)
	if got != 24*time.Hour {
		t.Errorf("expected exactly-now to roll to next day (24h), got %s", got)
	}
}

func TestScheduler_RunLoop_TriggersAndStopsCleanly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewScheduler(logger)

	var runs int32
	runOnce := func(ctx context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}

	// 用極短的 initialDelay/every,不等真正排程算出來的時刻。
	s.runLoop(context.Background(), time.Millisecond, 5*time.Millisecond, string(SnapraidScrub), runOnce)

	// 等到至少跑了 2 次(證明「跑完一次之後還會再跑」的迴圈邏輯正確)。
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&runs) < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected at least 2 runs within timeout, got %d", atomic.LoadInt32(&runs))
		case <-time.After(time.Millisecond):
		}
	}

	s.Stop()

	countAtStop := atomic.LoadInt32(&runs)
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&runs) != countAtStop {
		t.Errorf("expected no further runs after Stop, count went from %d to %d", countAtStop, atomic.LoadInt32(&runs))
	}
}
