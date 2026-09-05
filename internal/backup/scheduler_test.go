package backup

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestJobScheduler_RunsRepeatedlyAndStopsCleanly(t *testing.T) {
	s := NewJobScheduler(discardLogger())
	var count int32

	// runLoop 是內部方法,直接餵極短的 initialDelay/every,不用真的等到
	// 排程算出來的凌晨時刻才能驗證行為(跟 internal/storage.Scheduler 的
	// 既有測試手法一致)。
	s.runLoop(context.Background(), time.Millisecond, 5*time.Millisecond, func(ctx context.Context) {
		atomic.AddInt32(&count, 1)
	})

	time.Sleep(30 * time.Millisecond)
	s.Stop()

	got := atomic.LoadInt32(&count)
	if got < 2 {
		t.Errorf("expected runOnce to fire at least twice within the wait window, got %d", got)
	}

	after := atomic.LoadInt32(&count)
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&count) != after {
		t.Error("expected no further runs after Stop()")
	}
}

func TestJobScheduler_StopBeforeStart_DoesNotPanic(t *testing.T) {
	s := NewJobScheduler(discardLogger())
	s.Stop() // 從沒呼叫過 Start,Stop 必須是安全的 no-op
}

func TestNextRunDelay_BeforeTimeToday_WaitsUntilToday(t *testing.T) {
	now := time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC)
	sched := Schedule{EveryHours: 24, HourOfDay: 3, MinuteOfHour: 30}

	delay := nextRunDelay(now, sched)

	want := 2*time.Hour + 30*time.Minute
	if delay != want {
		t.Errorf("nextRunDelay() = %v, want %v", delay, want)
	}
}

func TestNextRunDelay_AfterTimeToday_WaitsUntilTomorrow(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	sched := Schedule{EveryHours: 24, HourOfDay: 3, MinuteOfHour: 30}

	delay := nextRunDelay(now, sched)

	want := 17*time.Hour + 30*time.Minute
	if delay != want {
		t.Errorf("nextRunDelay() = %v, want %v", delay, want)
	}
}
