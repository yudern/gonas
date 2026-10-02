package storage

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DeviceForSmart 把一個「池裡的項目」解析成 smartctl 真正能開啟的整顆磁碟
// 區塊裝置節點。池設定裡的 DataDisks/ParityDisks 存的是「掛載點」(例如
// /mnt/disk1,見 PoolConfig 說明),而 smartctl 只能開裝置節點(/dev/sdX)
// —— 直接把掛載點丟給 smartctl 會以「打不開裝置」失敗。這個函式補上中間那層
// 解析:
//   - path 已經是 /dev/... 就原樣回傳(防禦性:有些呼叫點本來就拿到裝置節點,
//     測試也常直接餵 /dev/sdX)。
//   - 否則用 findmnt 找出掛載點背後的來源裝置(可能是分割區 /dev/sda1),再用
//     lsblk 的 PKNAME 往上找到整顆磁碟(/dev/sda)—— SMART 要對整顆碟下,不是
//     對分割區。拿不到 PKNAME(來源本身就是整碟、或 LVM/md 這類沒有單一父裝置
//     的情況)就退回用來源本身。
//
// 之所以做這層而不是「叫使用者在池設定裡填裝置節點」:GoNAS 的池刻意以掛載點
// 為單位(mergerfs 合併的是已掛載的檔案系統),裝置節點(/dev/sdX)還會因為
// 重開機、換插槽而改變,掛載點才是穩定的識別。
func DeviceForSmart(ctx context.Context, r Runner, path string) (string, error) {
	if strings.HasPrefix(path, "/dev/") {
		return path, nil
	}
	out, err := r.Run(ctx, "findmnt", "-n", "-o", "SOURCE", "--target", path)
	if err != nil {
		return "", fmt.Errorf("resolving block device for mount %s: %w", path, err)
	}
	source := strings.TrimSpace(string(out))
	// findmnt 對 btrfs 之類可能回 "/dev/sda1[/subvol]";只取裝置節點本體。
	if i := strings.IndexByte(source, '['); i >= 0 {
		source = strings.TrimSpace(source[:i])
	}
	if source == "" {
		return "", fmt.Errorf("no block device is mounted at %s", path)
	}
	// 往上找整顆磁碟。PKNAME 為空代表 source 本身就是整碟。
	if pk, err := r.Run(ctx, "lsblk", "-n", "-o", "PKNAME", source); err == nil {
		name := strings.TrimSpace(string(pk))
		if i := strings.IndexByte(name, '\n'); i >= 0 {
			name = strings.TrimSpace(name[:i]) // 多行時取第一行
		}
		if name != "" {
			return "/dev/" + name, nil
		}
	}
	return source, nil
}

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
