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
