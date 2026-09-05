package backup

import (
	"context"
	"log/slog"
	"time"
)

// JobScheduler 依照 Schedule 週期性觸發一個備份工作。跟
// internal/storage.Scheduler 是幾乎一樣的實作(獨立 goroutine +
// time.Timer,Stop() 保證 goroutine 真的結束),刻意不共用同一個型別 ——
// internal/security 套件開頭已經說明過同樣的取捨:讓 internal/backup
// 只依賴標準函式庫跟自己的 cmdrunner,不因為「剛好排程邏輯長得很像」就
// 跨套件依賴 internal/storage,兩個套件之後各自演化(例如 storage 的
// 排程之後要換成完整 cron 套件)不會互相牽動。
type JobScheduler struct {
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// NewJobScheduler 建立排程器但不會立刻開始跑,需呼叫 Start。
func NewJobScheduler(logger *slog.Logger) *JobScheduler {
	return &JobScheduler{logger: logger}
}

// Start 依照 sched 週期性呼叫 runOnce(通常是包了 RunBackup 跟寫回
// state 的一個小函式)。
func (s *JobScheduler) Start(ctx context.Context, sched Schedule, runOnce func(context.Context)) {
	initialDelay := nextRunDelay(time.Now(), sched)
	s.runLoop(ctx, initialDelay, sched.Interval(), runOnce)
}

func (s *JobScheduler) runLoop(ctx context.Context, initialDelay, every time.Duration, runOnce func(context.Context)) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})

	go func() {
		defer close(s.done)
		wait := initialDelay
		for {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			runOnce(ctx)

			// 跑完一次之後,下一次是「現在起算再等 every」,而不是硬算
			// 日曆上的下一個固定時刻 —— 這樣如果某次備份拖很久(資料量
			// 大、目的地是網路儲存),不會緊接著又立刻觸發下一次。
			wait = every
		}
	}()
}

// Stop 讓排程 goroutine 結束,並等它真的結束才回傳。
func (s *JobScheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

// nextRunDelay 算出「從 now 起,到下一個 sched.HourOfDay:sched.MinuteOfHour
// 還要等多久」。抽成獨立函式是為了讓測試可以直接餵固定的 now 值驗證。
func nextRunDelay(now time.Time, sched Schedule) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), sched.HourOfDay, sched.MinuteOfHour, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}
