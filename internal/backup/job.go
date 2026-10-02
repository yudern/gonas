// Package backup 提供以 rsync 為底、rsnapshot 式的硬連結輪替快照備份。
//
// 為什麼不是「再實作一次 SnapRAID」:SnapRAID(見 internal/storage)保護的是
// 「同一份資料、同一顆陣列裡的位元衰減/單顆碟故障」,備份要處理的是完全
// 不同的風險 ——「使用者自己刪錯檔案」「勒索軟體把整個陣列的資料都改寫/
// 加密掉」「陣列本身這個地點發生火災/淹水」,這些情境下同一個陣列內的
// 同位校驗完全無能為力,一定要有一份實體上分開、而且能回到「過去某個
// 時間點」的複本。rsync + 硬連結輪替(跟 macOS Time Machine、rsnapshot
// 是同一套經典技巧)可以做到「每次備份看起來像一份完整複本,但沒有變更
// 的檔案在磁碟上其實只佔一份空間」,不需要底層檔案系統支援 btrfs/ZFS
// 快照(GoNAS 刻意不綁定特定檔案系統,見 internal/storage 的說明)。
//
// 跟專案其他套件一樣零第三方依賴:硬連結輪替本身是純標準函式庫的檔案
// 系統操作,唯一呼叫外部指令的地方(實際複製資料)一樣透過既有的
// cmdrunner.Runner 抽象呼叫系統的 rsync ——這個開發沙盒沒有安裝 rsync
// (套件源被同一層網路白名單擋掉,見一貫的「已知取捨」),所以指令組裝
// 靠假的 Runner 驗證,硬連結輪替/清理邏輯本身是純檔案系統操作,不依賴
// rsync 存不存在,一樣能在這台機器上對著真正的臨時目錄完整測試。
package backup

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/cron"
)

// ScheduleKind 決定 Schedule 用哪一種方式決定「多久跑一次」。刻意用字串
// 常數而不是自訂型別+iota,是因為 Schedule 直接序列化進 state.json,字串
// 常數對「舊 state.json 檔案裡完全沒有 kind 欄位」這件事最友善 ——
// JSON 反序列化時空字串會被 EffectiveKind() 當成 ScheduleKindInterval,
// 不需要額外寫任何遷移程式碼,舊資料原封不動繼續運作。
const (
	// ScheduleKindInterval 是原本(Phase 1 就有)的「每隔 N 小時、在某個
	// 時刻開始」簡化排程,也是 Kind 欄位為空字串時的預設行為。
	ScheduleKindInterval = "interval"
	// ScheduleKindCron 是 Phase 15 新增的標準 5 欄位 cron 語法排程,見
	// internal/cron 套件。
	ScheduleKindCron = "cron"
)

// Schedule 決定一個備份工作多久執行一次、幾點開始。歷史上這裡只有
// 「每隔 N 小時、在某個時刻執行」的簡化排程 —— 跟
// internal/storage.ParitySchedule 是同一種簡化排程,理由也一樣:完整
// cron 語法當初需要第三方套件,而這個開發沙盒的網路白名單擋掉了 Go
// module proxy。internal/cron 套件(見該套件文件)後來补上了一個零
// 依賴、純標準函式庫的 cron 剖析器/排程計算,所以這裡改成「新增」而不是
// 「取代」:Kind 欄位額外選擇 "cron" 時改用 CronExpr,Kind 維持空字串或
// "interval" 時完全照舊,既有的 EveryHours/HourOfDay/MinuteOfHour 三個
// 欄位、既有的 state.json 檔案不需要任何遷移程式碼就能繼續動作 ——
// 這比「把舊排程有損地轉換成 cron 表達式」安全,因為 EveryHours 不是
// 24 的因數時(例如「每 5 小時」)根本沒有對應的標準 cron 寫法可以精確
// 表達同一件事。
type Schedule struct {
	Kind         string `json:"kind,omitempty"`
	EveryHours   int    `json:"everyHours"`
	HourOfDay    int    `json:"hourOfDay"`
	MinuteOfHour int    `json:"minuteOfHour"`
	CronExpr     string `json:"cronExpr,omitempty"`
}

// EffectiveKind 回傳這份排程實際生效的種類,把「Kind 欄位是空字串」
// (所有 Phase 15 之前建立的 Job、或是 Web UI 表單忘記帶欄位)當成
// ScheduleKindInterval,而不是當成一種要另外處理的錯誤狀態。
func (s Schedule) EffectiveKind() string {
	if s.Kind == "" {
		return ScheduleKindInterval
	}
	return s.Kind
}

// Validate 檢查排程欄位是否落在合理範圍,依 EffectiveKind() 分流:
// interval 種類檢查原本三個欄位,cron 種類改成用 internal/cron.Parse
// 驗證 CronExpr 語法是否合法(順便也就此擋掉之後 Scheduler 執行期間
// 才發現語法錯誤的可能)。
func (s Schedule) Validate() error {
	switch s.EffectiveKind() {
	case ScheduleKindCron:
		if strings.TrimSpace(s.CronExpr) == "" {
			return fmt.Errorf("cronExpr is required when schedule kind is %q", ScheduleKindCron)
		}
		if _, err := cron.Parse(s.CronExpr); err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}
		return nil
	case ScheduleKindInterval:
		if s.EveryHours <= 0 {
			return fmt.Errorf("everyHours must be positive, got %d", s.EveryHours)
		}
		if s.HourOfDay < 0 || s.HourOfDay > 23 {
			return fmt.Errorf("hourOfDay must be between 0 and 23, got %d", s.HourOfDay)
		}
		if s.MinuteOfHour < 0 || s.MinuteOfHour > 59 {
			return fmt.Errorf("minuteOfHour must be between 0 and 59, got %d", s.MinuteOfHour)
		}
		return nil
	default:
		return fmt.Errorf("unknown schedule kind %q, expected %q or %q", s.Kind, ScheduleKindInterval, ScheduleKindCron)
	}
}

// Interval 把 EveryHours 轉成 time.Duration,給 Scheduler 內部使用。
// 只有 interval 種類的排程會呼叫這個方法。
func (s Schedule) Interval() time.Duration {
	return time.Duration(s.EveryHours) * time.Hour
}

// NextCronTime 剖析 CronExpr 並算出嚴格晚於 after 的下一次執行時間。
// 只有 cron 種類的排程會呼叫這個方法;Validate() 已經確保 CronExpr
// 語法合法,這裡的剖析錯誤理論上不會發生,但仍然把錯誤原樣往上傳,不
// 用 panic —— 呼叫端(Scheduler)可以決定要記 log 還是中止排程。
func (s Schedule) NextCronTime(after time.Time) (time.Time, error) {
	sched, err := cron.Parse(s.CronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after)
}

// Describe 回傳人類可讀的排程描述,給 API/Web UI 顯示用。
func (s Schedule) Describe() string {
	if s.EffectiveKind() == ScheduleKindCron {
		return fmt.Sprintf("Cron 排程:%s", s.CronExpr)
	}
	return fmt.Sprintf("每 %d 小時,從 %02d:%02d 開始", s.EveryHours, s.HourOfDay, s.MinuteOfHour)
}

// RunResult 記錄一次備份執行的結果,存在 Job.LastRun 裡,讓 Web UI 不用
// 另外維護一份執行紀錄資料庫就能顯示「上次備份是什麼時候、成功了沒」。
// 刻意只留最後一次的結果而不是完整歷史紀錄 —— 完整的執行歷史更適合用
// 磁碟上的快照目錄清單本身呈現(每個快照目錄的時間戳記就是一次成功
// 執行的紀錄),LastRun 只需要回答「現在狀態好不好」這個立即的問題。
type RunResult struct {
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	Success     bool      `json:"success"`
	Error       string    `json:"error,omitempty"`
	SnapshotDir string    `json:"snapshotDir,omitempty"`
}

// RemoteDest 是「異地備份」的遠端目的地(rsync over SSH)。第六十輪產品覆核:
// 備份套件文件自己說備份是為了防「火災/水災/失竊」,但只能備到同一台機器的
// 另一顆碟,並不真正離線異地。設了 Remote 的 Job 改成「鏡像到遠端主機」——
// 用 rsync 把來源同步到遠端一個目錄(含 --delete,讓遠端是來源的即時鏡像)。
//
// 刻意只支援金鑰認證(不收密碼):daemon 以 root 跑,把明文密碼存進 state.json
// 風險太高;金鑰路徑指向 NAS 上一把這個 Job 專用、無密碼短語的私鑰即可。
type RemoteDest struct {
	Host   string `json:"host"`             // 遠端主機名/IP
	User   string `json:"user"`             // SSH 使用者
	Port   int    `json:"port,omitempty"`   // SSH 埠,預設 22
	Path   string `json:"path"`             // 遠端的目的地絕對路徑
	SSHKey string `json:"sshKey,omitempty"` // NAS 上的私鑰檔路徑(留空則用預設金鑰/agent)
}

// Job 是一份備份工作的設定:把 SourcePath 底下的內容備份到 DestPath 底下
// 專屬於這個 Job 的子目錄,保留最近 RetentionCount 份快照。設了 Remote 時改為
// 「異地鏡像」模式(見 RemoteDest),此時不做本機快照輪替,DestPath 不使用。
type Job struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	SourcePath     string      `json:"sourcePath"`
	DestPath       string      `json:"destPath"`
	RetentionCount int         `json:"retentionCount"`
	Enabled        bool        `json:"enabled"`
	Schedule       Schedule    `json:"schedule"`
	Remote         *RemoteDest `json:"remote,omitempty"`
	LastRun        *RunResult  `json:"lastRun,omitempty"`
}

// IsRemote 回報這是不是一份異地鏡像(SSH)備份。
func (j Job) IsRemote() bool { return j.Remote != nil }

// noUnsafeChars 擋掉會破壞 rsync 遠端規格 / ssh -e 字串的字元(空白、換行、
// 控制字元、引號等)。遠端欄位最後會進 exec 參數(非 shell),但仍要擋空白,
// 因為 rsync 的 `-e "ssh …"` 是以空白切分的,而 user@host:path 也不能有空白。
func noUnsafeChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == ' ' || r == '\t' || r == '"' || r == '\'' || r == '`' {
			return false
		}
	}
	return true
}

// Validate 檢查一份 Job 設定是否完整、安全。特別檢查來源/目的地路徑
// 不能互相包含 —— 這不是吹毛求疵:如果 DestPath 剛好落在 SourcePath
// 底下(或反過來),rsync 備份時會把自己剛寫出來的快照當成來源的一部分
// 一起複製進下一層快照,每次執行的資料量會像雪球一樣越滾越大,最壞
// 情況下把整顆硬碟塞爆。
func (j Job) Validate() error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("backup job name is required")
	}
	if !filepath.IsAbs(j.SourcePath) {
		return fmt.Errorf("source path must be an absolute path, got %q", j.SourcePath)
	}
	if err := j.Schedule.Validate(); err != nil {
		return fmt.Errorf("invalid schedule: %w", err)
	}

	// 異地鏡像(SSH):驗證遠端欄位,不走本機目的地/保留份數那套。
	if j.IsRemote() {
		rd := j.Remote
		if strings.TrimSpace(rd.Host) == "" || !noUnsafeChars(rd.Host) {
			return fmt.Errorf("remote host is required and must not contain spaces or special characters")
		}
		if strings.TrimSpace(rd.User) == "" || !noUnsafeChars(rd.User) {
			return fmt.Errorf("remote user is required and must not contain spaces or special characters")
		}
		if !strings.HasPrefix(rd.Path, "/") || !noUnsafeChars(rd.Path) {
			return fmt.Errorf("remote path must be an absolute path with no spaces/special characters, got %q", rd.Path)
		}
		if rd.Port < 0 || rd.Port > 65535 {
			return fmt.Errorf("remote port out of range: %d", rd.Port)
		}
		if rd.SSHKey != "" && (!filepath.IsAbs(rd.SSHKey) || !noUnsafeChars(rd.SSHKey)) {
			return fmt.Errorf("ssh key path must be an absolute path with no spaces/special characters")
		}
		return nil
	}

	// 本機快照備份:原本的檢查。
	if !filepath.IsAbs(j.DestPath) {
		return fmt.Errorf("destination path must be an absolute path, got %q", j.DestPath)
	}
	if pathContainsOrEqual(j.SourcePath, j.DestPath) || pathContainsOrEqual(j.DestPath, j.SourcePath) {
		return fmt.Errorf("source path %q and destination path %q must not contain one another", j.SourcePath, j.DestPath)
	}
	if j.RetentionCount <= 0 {
		return fmt.Errorf("retentionCount must be positive, got %d", j.RetentionCount)
	}
	return nil
}

// pathContainsOrEqual 回傳 child 是不是等於 parent,或是在 parent 底下。
// 兩邊都先 filepath.Clean 過,避免 "/mnt/tank/" 跟 "/mnt/tank" 這種純粹
// 尾端斜線差異被誤判成「沒有包含關係」。
func pathContainsOrEqual(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}
