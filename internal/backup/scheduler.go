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
// state 的一個小函式)。依 sched.EffectiveKind() 分成兩條路徑:
// interval 種類完全沿用原本「固定間隔」的 runLoop,行為/既有測試不變;
// cron 種類改用 runCronLoop,每一次執行前都重新用
// internal/cron.Schedule.Next 算「下一次真正該跑的日曆時刻」,而不是
// 像固定間隔那樣硬加一個 time.Duration —— 這是 cron 語意本身要求的
// (例如「每月 1 號」這種排程,兩次執行之間的秒數每個月都不一樣,不能
// 用固定間隔表示)。
func (s *JobScheduler) Start(ctx context.Context, sched Schedule, runOnce func(context.Context)) {
	if sched.EffectiveKind() == ScheduleKindCron {
		s.runCronLoop(ctx, sched.NextCronTime, runOnce)
		return
	}
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

// runCronLoop 跟 runLoop 是同樣的「獨立 goroutine + timer,ctx 取消時
// 保證乾淨結束」骨架,差別只在等待時間怎麼算:每次(包括第一次)都呼叫
// nextFn(time.Now()) 重新算,而不是沿用一個算好的固定 Duration。
// nextFn 抽成參數(而不是直接在這裡呼叫 sched.NextCronTime)是為了讓
// 測試可以餵一個假的、回傳極短間隔的函式驗證重複執行/Stop() 行為,不用
// 真的等到下一個日曆分鐘 —— 跟 nextRunDelay 抽成獨立函式方便測試是同一個
// 理由。正式呼叫路徑(Start)一律傳 sched.NextCronTime。
//
// 如果 nextFn 回傳錯誤(正常情況下不會發生,因為 Job.Validate 已經擋在
// 前面,但防禦性地處理,例如未來有資料是繞過 Validate 直接寫進
// state.json 的舊資料),記一筆 error log 就讓 goroutine 結束,不用
// panic,也不會忙碌迴圈狂重試。
func (s *JobScheduler) runCronLoop(ctx context.Context, nextFn func(after time.Time) (time.Time, error), runOnce func(context.Context)) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})

	go func() {
		defer close(s.done)
		for {
			next, err := nextFn(time.Now())
			if err != nil {
				if s.logger != nil {
					s.logger.Error("cron schedule could not compute next run time, stopping scheduler", "err", err)
				}
				return
			}

			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			runOnce(ctx)
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
