package storage

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
)

// SmartHealth 是單顆硬碟的簡化 SMART 狀態,足夠讓 Web UI 畫出「健康 / 需注意 / 故障」
// 這種燈號,細部完整 attribute 表格留給之後有真正需求時再擴充。
type SmartHealth struct {
	Passed      bool `json:"passed"`                // overall-health self-assessment
	TempCelsius *int `json:"tempCelsius,omitempty"` // 沒讀到溫度時是 nil,而不是 0(0 度是合法值,不能拿來當「無資料」)
}

var (
	healthLineRe = regexp.MustCompile(`(?i)SMART overall-health self-assessment test result:\s*(PASSED|FAILED|OK)`)
	// 溫度屬性在不同廠牌硬碟上 ID 不一定相同(194 Temperature_Celsius 最常見,
	// 190 Airflow_Temperature_Cel 次之),用屬性名稱比對比用 ID 數字更穩定。
	// 欄位依序為 FLAG VALUE WORST THRESH TYPE UPDATED WHEN_FAILED RAW_VALUE,
	// 要跳過前 7 欄才是我們要的 RAW_VALUE(實際攝氏溫度)。
	tempLineRe = regexp.MustCompile(`(?i)Temperature_Celsius\s+\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+(\d+)`)
)

// CheckSmartHealth 對單一裝置跑 `smartctl -a`,解析出整體健康狀態與目前溫度。
// 刻意不要求呼叫端先跑 `-H` 再跑 `-A` 兩次:一次 `-a` 就同時包含健康摘要
// 與屬性表,減少對硬碟的存取次數(尤其是還沒睡眠的傳統硬碟,SMART 存取本身
// 也會讓它保持喚醒)。
func CheckSmartHealth(ctx context.Context, r Runner, device string) (SmartHealth, error) {
	out, err := r.Run(ctx, "smartctl", "-a", device)
	if err != nil {
		// 第五十八輪全鏈路覆核(QA2):smartctl 用「位元遮罩」結束碼回報狀態
		// ——bit 3(值 8)正是「整體健康評估 = FAILING」時會設的位元,bit 6/7
		// 代表有記錄到的錯誤等。也就是說,一顆「SMART 已判定故障」的碟,
		// smartctl 會以非零結束碼退出,但完整報告(含 overall-health 那一行)
		// 仍然印在 stdout 上(cmd.Output() 會把 stdout 一起帶回來)。原本只要
		// 非零就丟棄輸出、回錯,上層 probeSmartFailed 遇到錯誤就 `continue`
		// 略過那顆碟,結果 facts.SmartFailed 永遠是 false——SMART 告警對「正在
		// 故障的那顆碟」永遠不會觸發,使用者不會收到通知。修法:只要輸出裡
		// 有可解析的健康摘要,就把非零結束碼當成 smartctl 的狀態碼、照常解析;
		// 只有「啟動失敗(找不到執行檔、開不了裝置)」這種輸出裡沒有健康行的
		// 情況,才當成真的查詢失敗回錯。
		if healthLineRe.Match(out) {
			return parseSmartOutput(out), nil
		}
		return SmartHealth{}, fmt.Errorf("smartctl -a %s: %w", device, err)
	}
	return parseSmartOutput(out), nil
}

func parseSmartOutput(out []byte) SmartHealth {
	h := SmartHealth{Passed: true} // 找不到健康行時,寧可預設「未知當作通過」而不是嚇唬使用者;呼叫端可用 Passed 搭配額外欄位之後再細化

	if m := healthLineRe.FindSubmatch(out); m != nil {
		result := string(m[1])
		h.Passed = !equalFoldASCII(result, "FAILED")
	}

	if m := tempLineRe.FindSubmatch(out); m != nil {
		if v, err := strconv.Atoi(string(m[1])); err == nil {
			h.TempCelsius = &v
		}
	}

	return h
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
