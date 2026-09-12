package monitor

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoller_Start_SamplesImmediatelyAndPeriodically(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewCollector("/")
	history := NewHistory(50)

	var onSampleCalls int32
	p := NewPoller(logger, collector, history, 5*time.Millisecond, func(Snapshot) {
		atomic.AddInt32(&onSampleCalls, 1)
	})

	p.Start(context.Background())
	defer p.Stop()

	// 等到至少取樣 3 次(第一次是 Start 立刻觸發的，之後是週期性的)。
	deadline := time.After(2 * time.Second)
	for {
		if len(history.Snapshot()) >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for at least 3 samples, got %d", len(history.Snapshot()))
		case <-time.After(time.Millisecond):
		}
	}

	if atomic.LoadInt32(&onSampleCalls) < 3 {
		t.Errorf("expected onSample to have been called at least 3 times, got %d", onSampleCalls)
	}
}

// TestPoller_SurvivesPanickingCallback 固化第三十二輪的修法:onSample
// 回呼 panic 時,poller 的背景 goroutine 不能崩潰(在 Go 裡未 recover 的
// goroutine panic 會讓整個測試進程掛掉,所以「測試能跑完」本身就是證明),
// 而且要繼續往下一輪取樣。
func TestPoller_SurvivesPanickingCallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewCollector("/")
	history := NewHistory(50)

	var calls int32
	p := NewPoller(logger, collector, history, 5*time.Millisecond, func(Snapshot) {
		atomic.AddInt32(&calls, 1)
		panic("callback boom") // 每一輪都 panic
	})
	p.Start(context.Background())
	defer p.Stop()

	// 如果 panic 沒被 recover,第一輪就會讓進程崩潰,根本等不到第 3 次。
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("poller did not survive panicking callbacks; only %d calls", atomic.LoadInt32(&calls))
		case <-time.After(time.Millisecond):
		}
	}
}

func TestPoller_Stop_EndsBackgroundGoroutineCleanly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewCollector("/")
	history := NewHistory(50)

	p := NewPoller(logger, collector, history, time.Millisecond, nil)
	p.Start(context.Background())

	// 讓它先跑幾輪，再停止 —— Stop() 會等背景 goroutine 真的結束才回傳
	// (見 poller.go 的 <-p.done),所以這裡如果 goroutine 卡住,測試本身
	// 就會 hang 掉，是很直接的迴歸測試。
	time.Sleep(20 * time.Millisecond)
	p.Stop()

	countAfterStop := len(history.Snapshot())
	time.Sleep(20 * time.Millisecond)
	if got := len(history.Snapshot()); got != countAfterStop {
		t.Errorf("expected no more samples after Stop(), had %d then %d", countAfterStop, got)
	}
}

func TestPoller_SamplingError_DoesNotPanicAndSkipsOnSample(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// 一個不存在的路徑會讓 Collector.Sample 的 statfs 呼叫失敗，藉此驗證
	// Poller 對取樣錯誤只記 log、不會整個 panic 掉，也不會誤把錯誤的
	// snapshot 交給 onSample。
	collector := NewCollector("/this/path/does/not/exist/gonas-test")
	history := NewHistory(10)

	var onSampleCalls int32
	p := NewPoller(logger, collector, history, 5*time.Millisecond, func(Snapshot) {
		atomic.AddInt32(&onSampleCalls, 1)
	})

	p.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	p.Stop()

	if len(history.Snapshot()) != 0 {
		t.Errorf("expected no samples recorded when Sample() always errors, got %d", len(history.Snapshot()))
	}
	if atomic.LoadInt32(&onSampleCalls) != 0 {
		t.Errorf("expected onSample never called when Sample() always errors, got %d calls", onSampleCalls)
	}
}
