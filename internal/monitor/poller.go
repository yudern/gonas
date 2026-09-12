package monitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/bng147/gonas/internal/safe"
)

// Poller 定期呼叫 Collector.Sample，把結果寫進 History，並呼叫一個
// onSample 回呼(daemon 裡接的是告警規則引擎的 Evaluate)。拆成獨立型別
// 是因為 Collector 本身是被動、無狀態地「問一次就答一次」，History 只是
// 資料儲存，「週期性地把兩者串起來、還要通知告警引擎」是第三個關注點，
// 分開後三邊都比較容易個別測試。
type Poller struct {
	logger    *slog.Logger
	collector *Collector
	history   *History
	interval  time.Duration
	onSample  func(Snapshot)

	cancel context.CancelFunc
	done   chan struct{}
}

// NewPoller 建立但不會立刻開始跑，需要呼叫 Start。onSample 可以是 nil
// (例如測試只想確認 History 有沒有正確累積,不需要驗證告警邏輯)。
func NewPoller(logger *slog.Logger, collector *Collector, history *History, interval time.Duration, onSample func(Snapshot)) *Poller {
	return &Poller{
		logger:    logger,
		collector: collector,
		history:   history,
		interval:  interval,
		onSample:  onSample,
	}
}

// Start 立刻取樣一次(讓 Web UI 剛啟動就有資料可以顯示，不用先空等一個
// interval),然後依 interval 週期性取樣，直到 ctx 被取消或呼叫 Stop。
func (p *Poller) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.done = make(chan struct{})

	go func() {
		defer close(p.done)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			// 每一輪取樣/告警評估包一層 recover:onSample 回呼會發
			// webhook/email、讀 SMART/陣列狀態,任何一處意外 panic 不該
			// 拖垮整台 daemon,只記一筆 log、下一輪照常。見 internal/safe。
			safe.Run(p.logger, "monitor-poller", p.sampleOnce)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (p *Poller) sampleOnce() {
	snap, err := p.collector.Sample()
	if err != nil {
		p.logger.Error("monitor: sampling system metrics failed", "err", err)
		return
	}
	p.history.Add(snap)
	if p.onSample != nil {
		p.onSample(snap)
	}
}

// Stop 讓背景 goroutine 結束，並等它真的結束才回傳，方便測試確認沒有
// 殘留的 goroutine，這點跟 storage.Scheduler 的 Stop 是同一個作法。
func (p *Poller) Stop() {
	if p.cancel == nil {
		return
	}
	p.cancel()
	<-p.done
}
