package storage

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SmartSelfTestKind 是 SMART 自我測試的種類。short 幾分鐘跑完(快速掃描關鍵
// 項目),long 會完整掃過整顆碟的每一個磁區,可能跑數小時——但也是唯一能
// 提前發現「尚未被讀到、但已經壞掉的磁區」的方式,對「平常很少被讀到的冷資料」
// 特別有價值。刻意用字串常數(而不是 iota),理由跟 backup.ScheduleKind 一樣:
// 直接序列化進 state.json,對舊檔案/空值最友善。
type SmartSelfTestKind string

const (
	SmartSelfTestShort SmartSelfTestKind = "short"
	SmartSelfTestLong  SmartSelfTestKind = "long"
)

// Valid 回報 kind 是不是支援的種類。
func (k SmartSelfTestKind) Valid() bool {
	return k == SmartSelfTestShort || k == SmartSelfTestLong
}

// StartSmartSelfTest 對單一裝置觸發一次 SMART 自我測試(`smartctl -t short|long`)。
// smartctl -t 會把測試交給硬碟「自己」在背景執行後立刻返回——測試不佔 CPU、
// 不佔 GoNAS 的 goroutine,結果之後透過 ReadSmartSelfTestLog 從硬碟的自我測試
// 紀錄讀回。所以這個函式「成功」只代表「成功要求硬碟開始測試」,不代表測試
// 通過;要看結果得之後讀 log。
func StartSmartSelfTest(ctx context.Context, r Runner, device string, kind SmartSelfTestKind) error {
	if !kind.Valid() {
		return fmt.Errorf("unknown smart self-test kind %q", kind)
	}
	if out, err := r.Run(ctx, "smartctl", "-t", string(kind), device); err != nil {
		// smartctl 用位元遮罩結束碼;-t 啟動測試時只要輸出裡有「已開始」的字樣
		// 就算成功。把輸出一起帶進錯誤方便診斷(例如裝置不支援自我測試)。
		return fmt.Errorf("smartctl -t %s %s: %w: %s", kind, device, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SmartSelfTestEntry 是硬碟自我測試紀錄裡的一列。
type SmartSelfTestEntry struct {
	Num           int    `json:"num"`                     // 紀錄編號(# 1 最新)
	Description   string `json:"description"`             // 例如 "Short offline" / "Extended offline"
	Status        string `json:"status"`                  // 例如 "Completed without error"
	RemainingPct  int    `json:"remainingPct"`            // 剩餘百分比(0=已完成,>0=進行中)
	LifetimeHours int    `json:"lifetimeHours,omitempty"` // 當時的通電時數
	Passed        bool   `json:"passed"`                  // Status 是否代表「無錯誤完成」
}

// selfTestLineRe 解析 `smartctl -l selftest` 輸出裡以 "#" 開頭的紀錄列:
//
//	# 1  Extended offline    Completed without error       00%      1234         -
//
// 欄位之間用兩個以上空白分隔;Description 與 Status 本身含單一空白,所以用
// 「兩個以上空白」當欄位邊界,再用 \d+% 定位剩餘百分比、後面接通電時數。
var selfTestLineRe = regexp.MustCompile(`(?m)^#\s*(\d+)\s+(\S.*?\S)\s{2,}(\S.*?)\s+(\d+)%\s+(\d+)`)

// ReadSmartSelfTestLog 讀某顆碟的自我測試紀錄(`smartctl -l selftest`),解析成
// 結構化列表(最新的在前,跟 smartctl 的輸出順序一致)。讀不到任何紀錄列回傳
// 空切片(不是錯誤)——一顆從沒跑過自我測試的新碟就是這種情況。
func ReadSmartSelfTestLog(ctx context.Context, r Runner, device string) ([]SmartSelfTestEntry, error) {
	out, err := r.Run(ctx, "smartctl", "-l", "selftest", device)
	if err != nil && !selfTestLineRe.Match(out) {
		// 跟 CheckSmartHealth 同理:smartctl 常以非零結束碼退出,但只要 stdout
		// 裡有可解析的紀錄列,就照常解析;完全沒有紀錄列才當成真的失敗。
		return nil, fmt.Errorf("smartctl -l selftest %s: %w", device, err)
	}
	return parseSelfTestLog(out), nil
}

func parseSelfTestLog(out []byte) []SmartSelfTestEntry {
	var entries []SmartSelfTestEntry
	for _, line := range strings.Split(string(out), "\n") {
		m := selfTestLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		remaining, _ := strconv.Atoi(m[4])
		lifetime, _ := strconv.Atoi(m[5])
		status := strings.TrimSpace(m[3])
		entries = append(entries, SmartSelfTestEntry{
			Num:           num,
			Description:   strings.TrimSpace(m[2]),
			Status:        status,
			RemainingPct:  remaining,
			LifetimeHours: lifetime,
			// 「無錯誤完成」才算通過;「in progress」「interrupted」「Completed:
			// read failure」等都不是通過。用關鍵字判斷,對不同 smartctl 版本的
			// 細微措辭差異比精確字串比對更穩健。
			Passed: strings.Contains(strings.ToLower(status), "without error"),
		})
	}
	return entries
}
