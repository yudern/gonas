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
)

// Schedule 決定一個備份工作多久執行一次、幾點開始 —— 跟
// internal/storage.ParitySchedule 是同一種「不是完整 cron 語法,只支援
// 每隔 N 小時、在某個時刻執行」的簡化排程,理由也一樣:完整 cron 語法
// 需要第三方套件。備份工作用「小時」而不是 storage 那邊的 time.Duration
// 當單位,是因為 Duration 序列化成 JSON 是奈秒數的整數,對 Web UI 表單
// 使用者來說完全不是一個直覺的輸入欄位,直接用「每幾小時」這種整數
// 反而更貼近實際使用情境(例如「每 24 小時」「每 168 小時(一週)」)。
type Schedule struct {
	EveryHours   int `json:"everyHours"`
	HourOfDay    int `json:"hourOfDay"`
	MinuteOfHour int `json:"minuteOfHour"`
}

// Validate 檢查排程欄位是否落在合理範圍。
func (s Schedule) Validate() error {
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
}

// Interval 把 EveryHours 轉成 time.Duration,給 Scheduler 內部使用。
func (s Schedule) Interval() time.Duration {
	return time.Duration(s.EveryHours) * time.Hour
}

// Describe 回傳人類可讀的排程描述,給 API/Web UI 顯示用。
func (s Schedule) Describe() string {
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

// Job 是一份備份工作的設定:把 SourcePath 底下的內容備份到 DestPath 底下
// 專屬於這個 Job 的子目錄,保留最近 RetentionCount 份快照。
type Job struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	SourcePath     string     `json:"sourcePath"`
	DestPath       string     `json:"destPath"`
	RetentionCount int        `json:"retentionCount"`
	Enabled        bool       `json:"enabled"`
	Schedule       Schedule   `json:"schedule"`
	LastRun        *RunResult `json:"lastRun,omitempty"`
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
	if !filepath.IsAbs(j.DestPath) {
		return fmt.Errorf("destination path must be an absolute path, got %q", j.DestPath)
	}
	if pathContainsOrEqual(j.SourcePath, j.DestPath) || pathContainsOrEqual(j.DestPath, j.SourcePath) {
		return fmt.Errorf("source path %q and destination path %q must not contain one another", j.SourcePath, j.DestPath)
	}
	if j.RetentionCount <= 0 {
		return fmt.Errorf("retentionCount must be positive, got %d", j.RetentionCount)
	}
	if err := j.Schedule.Validate(); err != nil {
		return fmt.Errorf("invalid schedule: %w", err)
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
