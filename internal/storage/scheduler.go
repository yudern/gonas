package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ParitySchedule 設定「多久做一次校驗、幾點開始」。
// 先只支援「每隔 N 天，在一天中的某個時刻執行」這種最常見的家用場景
// (例如「每 7 天，凌晨 3 點跑 scrub」),不是完整的 cron 語法。
//
// 這是刻意的简化：完整 cron 語法需要引入第三方套件，而目前這個開發環境
// 的網路白名單擋掉了 proxy.golang.org(僅允許連到套件源的存取被拒絕)。
// 之後若環境允許、或改用 vendor 方式帶入 github.com/robfig/cron，
// 可以直接把 Scheduler 內部實作換掉，對外的 ParitySchedule/Scheduler
// 介面不需要變動。
type ParitySchedule struct {
	Every      time.Duration // 例如 7*24*time.Hour
	HourOfDay  int           // 0-23，該次執行要落在哪個小時
	MinuteOfHr int           // 0-59
	Action     SnapraidAction
}

// Scheduler 依照 ParitySchedule 週期性觸發 SnapRAID 動作(通常是 scrub)。
// 用一個獨立的 goroutine + time.Timer 實作，Stop() 會確實讓 goroutine 結束，
// 避免測試或重新設定時累積 goroutine 洩漏。
type Scheduler struct {
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// NewScheduler 建立排程器但不會立刻開始跑，需呼叫 Start。
func NewScheduler(logger *slog.Logger) *Scheduler {
	return &Scheduler{logger: logger}
}

// Start 依照 sched 週期性呼叫 runOnce。runOnce 通常是包了 RunSnapraid 的
// 一個小函式，把 configPath 之類的細節都收在呼叫端，Scheduler 本身不需要
// 知道要對哪個 pool 做什麼。
func (s *Scheduler) Start(ctx context.Context, sched ParitySchedule, runOnce func(context.Context) error) {
	initialDelay := nextRunDelay(time.Now(), sched)
	s.runLoop(ctx, initialDelay, sched.Every, sched.Action, runOnce)
}

// runLoop 是實際的排程迴圈，被拆成獨立方法主要是讓測試能直接餵極短的
// initialDelay/every,不必真的等到排程算出來的凌晨時刻才能驗證行為。
func (s *Scheduler) runLoop(ctx context.Context, initialDelay, every time.Duration, action SnapraidAction, runOnce func(context.Context) error) {
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

			s.logger.Info("running scheduled parity action", "action", action)
			if err := runOnce(ctx); err != nil {
				s.logger.Error("scheduled parity action failed", "action", action, "err", err)
			}

			// 跑完一次之後，下一次是「現在起算再等 every」，而不是硬算日曆
			// 上的下一個固定時刻 —— 這樣如果某次執行拖很久(例如大型 scrub),
			// 不會緊接著又立刻觸發下一次。
			wait = every
		}
	}()
}

// Stop 讓排程 goroutine 結束，並等它真的結束才回傳，方便測試裡確定
// 沒有殘留的 goroutine 還在背景跑。
func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

// nextRunDelay 算出「從 now 起，到下一個 sched.HourOfDay:sched.MinuteOfHr 還要等多久」。
// 抽成獨立函式是為了讓測試可以直接餵固定的 now 值驗證，不用真的等到半夜三點。
func nextRunDelay(now time.Time, sched ParitySchedule) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), sched.HourOfDay, sched.MinuteOfHr, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

// Describe 回傳人類可讀的排程描述，給 API/日誌用。
func (s ParitySchedule) Describe() string {
	return fmt.Sprintf("every %s at %02d:%02d (%s)", s.Every, s.HourOfDay, s.MinuteOfHr, s.Action)
}
