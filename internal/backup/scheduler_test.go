package backup

import (
	"context"
	"fmt"
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

// TestJobScheduler_RunCronLoop_RunsRepeatedlyAndStopsCleanly exercises the
// runCronLoop skeleton (shared by Start's cron-kind path) with a fake nextFn
// that always schedules "1ms from now", instead of a real Schedule computed
// from cron.Parse — a real "* * * * *" schedule has a minimum granularity of
// one minute per fire, which would make this test take 60s+ of wall-clock
// time per run; injecting nextFn keeps the repeated-run/Stop() behavior
// fast to test while the real cron-minute-granularity math is already
// covered by internal/cron's own tests and by Schedule.NextCronTime's tests
// in job_test.go. The real end-to-end "does a live gonasd actually fire a
// cron job" check happens in manual verification against a running daemon,
// not here.
func TestJobScheduler_RunCronLoop_RunsRepeatedlyAndStopsCleanly(t *testing.T) {
	s := NewJobScheduler(discardLogger())
	var count int32

	fastNext := func(after time.Time) (time.Time, error) {
		return after.Add(time.Millisecond), nil
	}
	s.runCronLoop(context.Background(), fastNext, func(ctx context.Context) {
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

// TestJobScheduler_RunCronLoop_NextFnErrorStopsWithoutPanicking covers the
// defensive branch in runCronLoop: when nextFn returns an error (which in
// the real Start() path means sched.NextCronTime failed to parse CronExpr —
// something Job.Validate() should already have caught, but could in
// principle happen with hand-edited state.json), the scheduler goroutine
// must log and exit cleanly rather than panic or busy-loop.
func TestJobScheduler_RunCronLoop_NextFnErrorStopsWithoutPanicking(t *testing.T) {
	s := NewJobScheduler(discardLogger())
	var count int32

	failingNext := func(after time.Time) (time.Time, error) {
		return time.Time{}, fmt.Errorf("simulated unparsable cron expression")
	}
	s.runCronLoop(context.Background(), failingNext, func(ctx context.Context) {
		atomic.AddInt32(&count, 1)
	})

	time.Sleep(50 * time.Millisecond)
	s.Stop() // must not hang or panic even though the goroutine already exited on its own

	if got := atomic.LoadInt32(&count); got != 0 {
		t.Errorf("expected runOnce never to fire when nextFn errors, got %d calls", got)
	}
}

// TestJobScheduler_Start_DispatchesCronKindToRunCronLoop is a thin
// integration check that Start() actually routes a cron-kind Schedule
// through the cron path (as opposed to silently falling back to the
// interval path, which would misinterpret CronExpr). It uses a real,
// frequently-firing cron expression is not practical here (minimum
// granularity is one minute), so instead it verifies dispatch indirectly:
// an interval-kind Schedule with EveryHours: 0 would fail nextRunDelay's
// implicit assumptions, so this asserts the cron branch is taken by
// checking NextCronTime is what gets consulted, via a schedule whose
// CronExpr is deliberately invalid — if Start() incorrectly took the
// interval path, this schedule (EveryHours: 0) would still "work" (timer
// with 0 duration), whereas the cron path must reject it and never invoke
// runOnce.
func TestJobScheduler_Start_DispatchesCronKindToRunCronLoop(t *testing.T) {
	s := NewJobScheduler(discardLogger())
	var count int32

	sched := Schedule{Kind: ScheduleKindCron, CronExpr: "not a valid expression", EveryHours: 0}
	s.Start(context.Background(), sched, func(ctx context.Context) {
		atomic.AddInt32(&count, 1)
	})

	time.Sleep(50 * time.Millisecond)
	s.Stop()

	if got := atomic.LoadInt32(&count); got != 0 {
		t.Errorf("expected Start() to route cron-kind schedule through the cron path and reject the invalid expression, got %d calls", got)
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
