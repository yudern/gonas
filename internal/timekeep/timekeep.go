// Package timekeep 管理這台 NAS 的系統時間、時區與 NTP 網路校時。
//
// 第七十五輪(使用者:「忘了最重要的——時間」)。NAS 的日誌時間、定時校驗/
// SMART 排程、TLS 憑證有效期、檔案時間戳全都依賴系統時間正確,所以 Web UI
// 必須能設定時區、開關 NTP 自動校時、必要時手動設定時間。
//
// 底層一律透過 systemd 的 timedatectl(Debian 13 附帶);timedatectl 不可用時
// (例如開發環境沒有 systemd),讀取部分退回純 Go(time.Now + /etc/timezone),
// 設定部分回報「這台機器無法從這裡管理時間」而不是直接崩潰。只用標準函式庫 +
// 專案自己的 cmdrunner。
package timekeep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/cmdrunner"
)

// Runner 跟專案其他地方一樣用 cmdrunner.Runner,方便測試注入假的執行器。
type Runner = cmdrunner.Runner

// ZoneinfoDir 是時區資料庫目錄(列舉時區用);抽成 var 方便測試指到暫存目錄。
var ZoneinfoDir = "/usr/share/zoneinfo"

// EtcTimezone 是 timedatectl 不可用時退而求其次讀時區的檔案。
var EtcTimezone = "/etc/timezone"

// Status 是目前的時間狀態,回給 Web UI 顯示。
type Status struct {
	Timezone        string `json:"timezone"`
	LocalTime       string `json:"localTime"`       // 人類可讀的本地時間
	UTCTime         string `json:"utcTime"`         // UTC 時間
	UnixSeconds     int64  `json:"unixSeconds"`     // 給前端做即時走秒
	NTPEnabled      bool   `json:"ntpEnabled"`      // 是否開啟 NTP 自動校時
	NTPSynchronized bool   `json:"ntpSynchronized"` // 是否已經跟時間伺服器同步成功
	CanManage       bool   `json:"canManage"`       // timedatectl 是否可用(能不能改)
}

// tzPattern 限制時區字串只能是 "Asia/Shanghai" 這種安全格式,擋掉路徑穿越與
// 任何可能被當成指令參數濫用的字元(即使我們是用 argv 傳、不經過 shell,
// 仍然多一層防呆)。
var tzPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+){0,2}$`)

// GetStatus 回傳目前的時間/時區/NTP 狀態。timedatectl 可用時以它為準,
// 不可用時退回純 Go 讀取(此時 CanManage=false,前端會停用「修改」)。
func GetStatus(ctx context.Context, runner Runner) Status {
	now := time.Now()
	st := Status{
		LocalTime:   now.Format("2006-01-02 15:04:05 -0700 MST"),
		UTCTime:     now.UTC().Format("2006-01-02 15:04:05 MST"),
		UnixSeconds: now.Unix(),
	}

	out, err := runner.Run(ctx, "timedatectl", "show",
		"--property=Timezone", "--property=NTP", "--property=NTPSynchronized")
	if err != nil {
		// timedatectl 不可用:時區退回讀檔,NTP 狀態未知,標記不可管理。
		st.Timezone = readEtcTimezone()
		st.CanManage = false
		return st
	}
	st.CanManage = true
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "Timezone":
			st.Timezone = v
		case "NTP":
			st.NTPEnabled = v == "yes" || v == "true"
		case "NTPSynchronized":
			st.NTPSynchronized = v == "yes" || v == "true"
		}
	}
	if st.Timezone == "" {
		st.Timezone = readEtcTimezone()
	}
	return st
}

func readEtcTimezone() string {
	b, err := os.ReadFile(EtcTimezone)
	if err != nil {
		return "UTC"
	}
	tz := strings.TrimSpace(string(b))
	if tz == "" {
		return "UTC"
	}
	return tz
}

// ListTimezones 列出所有可選時區。優先用 timedatectl list-timezones,
// 不可用時走訪 ZoneinfoDir 自己列。結果已排序、去重。
func ListTimezones(ctx context.Context, runner Runner) []string {
	if out, err := runner.Run(ctx, "timedatectl", "list-timezones"); err == nil {
		var zs []string
		for _, line := range strings.Split(string(out), "\n") {
			z := strings.TrimSpace(line)
			if z != "" {
				zs = append(zs, z)
			}
		}
		if len(zs) > 0 {
			sort.Strings(zs)
			return zs
		}
	}
	return walkZoneinfo()
}

// walkZoneinfo 走訪時區資料庫目錄,列出像 "Asia/Shanghai" 這種時區名。
// 只收「大寫字母開頭的地區目錄」底下的檔案(Asia、Europe、America…),
// 跳過 posix/right/ 這些重複樹,以及 zone.tab 這類非時區檔。
func walkZoneinfo() []string {
	var zs []string
	seen := map[string]bool{}
	for _, region := range []string{
		"Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic",
		"Australia", "Europe", "Indian", "Pacific",
	} {
		base := filepath.Join(ZoneinfoDir, region)
		_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(ZoneinfoDir, path)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if tzPattern.MatchString(rel) && !seen[rel] {
				seen[rel] = true
				zs = append(zs, rel)
			}
			return nil
		})
	}
	// 一定要有 UTC 可選。
	if !seen["UTC"] {
		zs = append(zs, "UTC")
	}
	sort.Strings(zs)
	return zs
}

// ValidTimezone 檢查 tz 是否是一個安全、且確實存在的時區。
func ValidTimezone(tz string) bool {
	if tz == "" || !tzPattern.MatchString(tz) {
		return false
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return false
	}
	return true
}

// SetTimezone 設定系統時區。tz 會先驗證(格式 + 真的存在),再交給 timedatectl。
func SetTimezone(ctx context.Context, runner Runner, tz string) error {
	if !ValidTimezone(tz) {
		return fmt.Errorf("timekeep: invalid timezone %q", tz)
	}
	if out, err := runner.Run(ctx, "timedatectl", "set-timezone", tz); err != nil {
		return fmt.Errorf("timekeep: setting timezone: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetNTP 開啟/關閉 NTP 自動校時。
func SetNTP(ctx context.Context, runner Runner, enabled bool) error {
	arg := "false"
	if enabled {
		arg = "true"
	}
	if out, err := runner.Run(ctx, "timedatectl", "set-ntp", arg); err != nil {
		return fmt.Errorf("timekeep: setting ntp: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetTime 手動設定系統時間(只有在 NTP 關閉時才有意義,開著 NTP 時 timedatectl
// 會拒絕)。t 用本地時區格式化成 timedatectl 接受的 "YYYY-MM-DD HH:MM:SS"。
func SetTime(ctx context.Context, runner Runner, t time.Time) error {
	arg := t.Format("2006-01-02 15:04:05")
	if out, err := runner.Run(ctx, "timedatectl", "set-time", arg); err != nil {
		return fmt.Errorf("timekeep: setting time: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
