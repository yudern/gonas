// Package ups 是 GoNAS 對 UPS(不斷電系統)的整合層,建立在 NUT
// (Network UPS Tools)之上——用 `upsc` 查詢一台已在 NUT 設定好的 UPS 的
// 即時狀態(市電/電池、電量、預估續航、負載),並在「市電中斷且電量過低」
// 時觸發安全關機。
//
// 為什麼靠 NUT 而不是自己講 USB/SNMP:UPS 的通訊協定五花八門(各廠牌 USB
// HID、APC 私有、SNMP…),NUT 是 Linux 上事實標準、驅動涵蓋最廣,自己重造
// 只會做出更少相容、更難維護的東西。跟 smartctl/mergerfs 一樣,NUT 是「選用
// 的外部依賴」:沒裝就回報「未安裝」,不影響 gonasd 本身啟動。
package ups

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bng147/gonas/internal/cmdrunner"
)

// Runner 沿用 cmdrunner 那套可測試的指令執行介面(跟 storage/share 一致)。
type Runner = cmdrunner.Runner

// Status 是某台 UPS 的即時狀態快照,也是 API /ups/status 直接回傳的型別。
// 指標型欄位(可能查不到該項)用 nil 表示「這台 UPS/這個驅動沒有回報」。
type Status struct {
	Present        bool   `json:"present"` // 有沒有成功查到一台 UPS
	UPSName        string `json:"upsName,omitempty"`
	Model          string `json:"model,omitempty"`
	Status         string `json:"status,omitempty"`         // NUT 的 ups.status 原始字串,例如 "OL"、"OB LB"
	OnBattery      bool   `json:"onBattery"`                // 目前靠電池供電(市電中斷)
	LowBattery     bool   `json:"lowBattery"`               // 電池電量已低(NUT 的 LB 旗標)
	BatteryCharge  *int   `json:"batteryCharge,omitempty"`  // 電量百分比
	RuntimeSeconds *int   `json:"runtimeSeconds,omitempty"` // 預估剩餘續航(秒)
	LoadPercent    *int   `json:"loadPercent,omitempty"`    // 目前負載百分比
	Message        string `json:"message,omitempty"`        // 查不到時的人類可讀說明(給前端顯示)
}

// List 回傳 NUT 目前設定了哪些 UPS(`upsc -l`,一行一個名稱)。
func List(ctx context.Context, r Runner) ([]string, error) {
	out, err := r.Run(ctx, "upsc", "-l")
	if err != nil {
		return nil, fmt.Errorf("listing UPS devices (upsc -l): %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			names = append(names, s)
		}
	}
	return names, nil
}

// Query 查一台 UPS 的即時狀態(`upsc <name>`)。name 可以是 "ups" 或
// "ups@host" 形式(NUT 的慣例)。
func Query(ctx context.Context, r Runner, name string) (Status, error) {
	out, err := r.Run(ctx, "upsc", name)
	if err != nil {
		return Status{}, fmt.Errorf("querying UPS %q (upsc): %w", name, err)
	}
	return parseUPSC(name, out), nil
}

// parseUPSC 解析 `upsc` 的 `key: value` 逐行輸出。只挑我們要顯示/判斷的
// 欄位,其餘忽略——不同驅動回報的變數集合差很多,不強求全部都有。
func parseUPSC(name string, out []byte) Status {
	s := Status{Present: true, UPSName: name}
	for _, line := range strings.Split(string(out), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "ups.status":
			s.Status = val
			s.OnBattery, s.LowBattery = statusFlags(val)
		case "device.model", "ups.model":
			if s.Model == "" {
				s.Model = val
			}
		case "battery.charge":
			s.BatteryCharge = parseIntPtr(val)
		case "battery.runtime":
			s.RuntimeSeconds = parseIntPtr(val)
		case "ups.load":
			s.LoadPercent = parseIntPtr(val)
		}
	}
	return s
}

// statusFlags 從 NUT 的 ups.status 字串(以空白分隔的旗標,例如 "OB LB")
// 判斷是否靠電池供電(OB)、電量是否過低(LB)。OL=市電正常。
func statusFlags(raw string) (onBattery, lowBattery bool) {
	for _, f := range strings.Fields(raw) {
		switch strings.ToUpper(f) {
		case "OB":
			onBattery = true
		case "LB":
			lowBattery = true
		}
	}
	return onBattery, lowBattery
}

func parseIntPtr(s string) *int {
	// battery.runtime/charge 有時是浮點字串(例如 "3600.0"),取整數部分即可。
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		s = s[:dot]
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &v
}

// ShouldShutdown 判斷「現在是否該為了保護資料而安全關機」。條件:確實查到
// UPS、正靠電池供電(市電中斷),而且(NUT 已回報電量過低 LB)或(有設定
// 續航門檻且預估續航已低於門檻)。市電正常時永遠回 false——只有在停電且快
// 撐不住時才關機。runtimeThresholdSeconds <= 0 表示不看續航、只看 LB 旗標。
func ShouldShutdown(s Status, runtimeThresholdSeconds int) bool {
	if !s.Present || !s.OnBattery {
		return false
	}
	if s.LowBattery {
		return true
	}
	if runtimeThresholdSeconds > 0 && s.RuntimeSeconds != nil && *s.RuntimeSeconds <= runtimeThresholdSeconds {
		return true
	}
	return false
}
