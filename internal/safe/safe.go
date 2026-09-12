// Package safe 提供背景 goroutine 用的 panic 防護小工具。
//
// 背景:在 Go 裡,一個 goroutine 如果 panic 而且沒有被 recover,整個
// process 會直接崩潰——不只是那個 goroutine。GoNAS 這種「開機擺著跑
// 好幾個月、沒人盯著」的 appliance,有好幾個長駐背景 goroutine(監控
// 輪詢、備份/同位/digest 排程、憑證續期、更新檢查),它們每一輪要做的事
// 都會碰到外部世界(執行外部指令、發 webhook/email、讀 /proc、算 SMART
// 健康狀態……),任何一處意料之外的 panic(nil 解參考、第三方回傳的
// 奇怪資料、模板錯誤)都會把整台 daemon 拖垮;如果那個 panic 是必然
// 發生的,systemd 重啟後還會變成崩潰迴圈。
//
// 第三十二輪(全鏈路覆核後的修法):HTTP 請求早就有 withRecover 這層
// 保護(見 internal/api/router.go),但背景 goroutine 一個都沒有。這個
// 套件把「執行一段工作,萬一 panic 就 recover + 記 log + 讓呼叫端的
// 迴圈繼續下一輪」這件事收斂成一個地方,讓每個背景排程器用同一套一致的
// 做法,不用各自複製一段 defer/recover。
package safe

import (
	"log/slog"
	"runtime/debug"
)

// Run 執行 fn,如果 fn panic 就攔下來、透過 logger 記一筆含堆疊的 error
// log,不讓 panic 繼續往上傳播(也就是不會讓呼叫它的 goroutine、進而
// 整個 process 崩潰)。label 用來在 log 裡標明是哪個背景工作出事,方便
// 事後從 journald 直接看出來。logger 為 nil 時安全(靜默 recover)——
// 這只會出現在測試,正式程式碼都會帶一個真的 logger。
//
// 用法(在背景排程器的迴圈體裡):
//
//	for {
//	    ... 等 ticker/timer ...
//	    safe.Run(logger, "monitor-poller", func() { doOneRound() })
//	}
//
// 一輪 panic 只會少做這一輪、留一筆 log,下一輪照常。
func Run(logger *slog.Logger, label string, fn func()) {
	defer func() {
		if rec := recover(); rec != nil && logger != nil {
			logger.Error("background task panicked and was recovered",
				"task", label,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn()
}
