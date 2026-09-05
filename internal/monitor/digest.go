package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DigestScheduler 依照一個 cron 表達式週期性觸發一次「健康摘要」通知。
// 跟 internal/backup.JobScheduler 的 runCronLoop 是幾乎一樣的實作
// (獨立 goroutine + timer,Stop() 保證 goroutine 真的結束、nextFn 抽成
// 參數方便測試餵假的極短間隔)——刻意不共用同一個型別,理由跟
// backup.JobScheduler 開頭的說明一樣:各套件的排程邏輯各自演化,不要
// 因為「看起來很像」就跨套件依賴。
//
// 跟 JobScheduler 不同的地方是這裡只支援 cron 一種排程種類,沒有
// interval 分支——digest 是全新功能,沒有 Phase 15 之前遺留下來、需要
// 向後相容的「固定間隔」舊排程格式要支援,直接要求使用者填一個標準
// cron 表達式(例如「每天早上 8 點」的 `0 8 * * *`)比再設計一套簡化
// 介面更直接,使用者一樣可以用簡單的表達式做到「每天固定時間」這種
// 最常見的需求。
type DigestScheduler struct {
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// NewDigestScheduler 建立排程器但不會立刻開始跑,需呼叫 Start。
func NewDigestScheduler(logger *slog.Logger) *DigestScheduler {
	return &DigestScheduler{logger: logger}
}

// Start 依 nextFn 算出的下一次時刻週期性呼叫 runOnce。nextFn 抽成參數
// (而不是直接在這裡剖析 cron 表達式)是為了讓測試可以餵一個回傳極短
// 間隔的假函式驗證重複執行/Stop() 行為,不用真的等到下一個日曆分鐘,
// 跟 backup.JobScheduler.runCronLoop 是同一個理由;正式呼叫路徑
// (internal/api)一律傳一個包了 internal/cron.Parse(cronExpr) 的函式。
//
// 如果 nextFn 回傳錯誤(正常情況下不會發生,因為呼叫端在存進 state 前
// 就會驗證過 cron 語法,但防禦性地處理,例如未來有資料繞過驗證直接寫進
// state.json),記一筆 error log 就讓 goroutine 結束,不 panic,也不會
// 忙碌迴圈狂重試。
func (s *DigestScheduler) Start(ctx context.Context, nextFn func(after time.Time) (time.Time, error), runOnce func(context.Context)) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})

	go func() {
		defer close(s.done)
		for {
			next, err := nextFn(time.Now())
			if err != nil {
				if s.logger != nil {
					s.logger.Error("digest schedule could not compute next run time, stopping scheduler", "err", err)
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

// Stop 讓排程 goroutine 結束,並等它真的結束才回傳。對一個從未 Start
// 過的 DigestScheduler 呼叫是安全的no-op——跟 backup.JobScheduler.Stop
// 一樣的保護,因為 internal/api 在設定被停用/清空時可能會呼叫 Stop
// 而不確定之前是否真的 Start 過。
func (s *DigestScheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

// DigestInput 是組出一份健康摘要所需要的原始資料,由呼叫端
// (internal/api,通常是剛好手上就有的最新一次系統資源取樣、目前的
// 告警規則觸發狀態、備份工作的最近執行結果)組好交給 BuildDigestEvent——
// monitor 套件本身不知道、也不需要知道 backup.Job 或 state.State 長
// 什麼樣子,只認自己能理解的最小資料形狀,維持套件之間刻意的低耦合
// (跟這個套件其他地方,例如 AlertEngine 只認 Facts 不認
// storage.Array,是同一個原則)。
type DigestInput struct {
	Snapshot Snapshot

	// FiringRuleNames 是目前正處於觸發中的告警規則名稱清單(不是 ID——
	// digest 是給人看的摘要,人看得懂名稱,看不懂內部 ID)。
	FiringRuleNames []string

	// BackupSummaries 是每個備份工作的一句話摘要(例如「每日備份:
	// 上次成功於 2026-09-05 03:00」或「每日備份:上次失敗——rsync exit
	// status 23」),由呼叫端自己組好——備份工作的資料結構跟格式化邏輯
	// 屬於 internal/backup,不該讓 internal/monitor 反過來依賴那個套件。
	BackupSummaries []string
}

// BuildDigestEvent 把 DigestInput 組成一則 Kind=EventKindDigest 的
// Event,Subject 是給郵件標題/webhook payload 用的一行摘要,Message 是
// 給 LogNotifier/郵件內文用的完整純文字內容。刻意用固定版面的純文字
// (不是 JSON、不是 HTML)——這是給人在 log/郵件用戶端/webhook payload
// 三種完全不同的呈現環境下都要看得懂的內容,純文字是唯一在三種環境下
// 都不會跑版或需要額外剖析的格式。
func BuildDigestEvent(in DigestInput) Event {
	now := time.Now()

	healthLabel := "正常"
	if len(in.FiringRuleNames) > 0 {
		healthLabel = "有告警觸發中"
	}
	subject := fmt.Sprintf("GoNAS 健康摘要(%s)— %s", now.Format("2006-01-02"), healthLabel)

	var b strings.Builder
	fmt.Fprintf(&b, "GoNAS 健康摘要 — %s\n\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "系統資源:\n")
	fmt.Fprintf(&b, "  CPU 使用率:    %.1f%%\n", in.Snapshot.CPUPercent)
	fmt.Fprintf(&b, "  記憶體使用率:  %.1f%%\n", in.Snapshot.MemPercent)
	fmt.Fprintf(&b, "  磁碟使用率:    %.1f%%(%s)\n", in.Snapshot.DiskPercent, in.Snapshot.DiskPath)
	fmt.Fprintf(&b, "  運作時間:      %s\n\n", formatUptimeForDigest(in.Snapshot.UptimeSeconds))

	fmt.Fprintf(&b, "告警狀態:\n")
	if len(in.FiringRuleNames) == 0 {
		b.WriteString("  目前沒有任何規則處於觸發中。\n\n")
	} else {
		for _, name := range in.FiringRuleNames {
			fmt.Fprintf(&b, "  正在觸發:%s\n", name)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "備份工作:\n")
	if len(in.BackupSummaries) == 0 {
		b.WriteString("  還沒有設定任何備份工作。\n")
	} else {
		for _, summary := range in.BackupSummaries {
			fmt.Fprintf(&b, "  %s\n", summary)
		}
	}

	return Event{
		Kind:    EventKindDigest,
		At:      now,
		Subject: subject,
		Message: b.String(),
	}
}

// formatUptimeForDigest 把秒數格式化成「X 天 Y 小時」這種給人看的形式,
// 跟前端 app.js 的 formatUptime 是同樣的呈現邏輯,但這裡是後端組純文字
// 內容(郵件/log)專用,兩邊沒有共用程式碼的必要——前端那份是給瀏覽器
// DOM 用的,這裡是給純文字內容用的,格式需求不同(這裡不需要 i18n,
// 摘要內容本身固定是繁體中文,跟這個專案「原生語言是繁體中文」的
// 慣例一致)。
func formatUptimeForDigest(seconds float64) string {
	total := int64(seconds)
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%d 天 %d 小時", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d 小時 %d 分鐘", hours, minutes)
	}
	return fmt.Sprintf("%d 分鐘", minutes)
}
