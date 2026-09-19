// Package state 是 GoNAS 唯一的設定持久化層：把 Web UI 上使用者設定的
// pool、共享、匯出、使用者、已安裝的 App 存成一份 JSON 檔案。
//
// 刻意不用資料庫：這個開發環境的網路白名單擋掉了 Go module proxy(見
// internal/docker 套件註解),沒有辦法拉任何 SQL driver;而且對 GoNAS
// 現在的資料量與存取型態(單一管理者、設定變更不頻繁)來說,一份原子寫入
// 的 JSON 檔案已經足夠,也更符合「單一靜態執行檔、開箱即用」的設計目標
// —— 這跟 Unraid 本身用 flat config 檔案而不是資料庫存設定是同一個思路。
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/cron"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/share"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/wireguard"
)

// State 是整份持久化狀態的結構。每個欄位的零值都必須是「合理的初始狀態」
// (空陣列/nil pool),這樣全新安裝、還沒有 state.json 檔案時可以直接用
// 零值 State{} 開始運作,不需要特殊的「第一次執行」邏輯。
type State struct {
	Pool           *storage.PoolConfig     `json:"pool,omitempty"`
	Shares         []share.Share           `json:"shares"`
	Exports        []share.Export          `json:"exports"`
	Users          []UserRecord            `json:"users"`
	InstalledApps  []InstalledApp          `json:"installedApps"`
	AlertRules     []monitor.AlertRule     `json:"alertRules"`
	Notifiers      []monitor.WebhookConfig `json:"notifiers"`
	EmailNotifiers []monitor.EmailConfig   `json:"emailNotifiers"`
	Admins         []AdminAccount          `json:"admins"`
	HTTPS          HTTPSConfig             `json:"https"`
	WireGuard      *wireguard.Config       `json:"wireGuard,omitempty"`
	BackupJobs     []backup.Job            `json:"backupJobs"`
	Update         UpdateConfig            `json:"update"`
	AuditLog       []AuditEntry            `json:"auditLog"`
	Digest         DigestConfig            `json:"digest"`
	UPS            UPSConfig               `json:"ups"`
}

// DigestConfig 是 Phase 18c 新增的週期性健康摘要設定。CronExpr 留空
// (預設值)代表使用者完全沒有設定過——跟 UpdateConfig.ManifestURL 是
// 同一種「預設不做任何背景動作」的隱私/最小驚訝設計:Enabled 就算被
// 設成 true,只要 CronExpr 是空字串,internal/api 就不會啟動任何背景
// 排程 goroutine。
//
// NotifierIDs/EmailNotifierIDs 是要把摘要送去的既有 webhook/email
// 通知管道 ID 子集合(跟 state.State.Notifiers/EmailNotifiers 是同一批
// 設定,digest 刻意不另外設計一套平行的通知管道設定表單)——留空
// 代表「這種類型的管道都不送」,不是「全部都送」,使用者要明確勾選,
// 避免新增一個 webhook/email 管道時意外也開始收到 digest。LastSentAt
// 是最近一次成功送出的時間,nil 代表從來沒送過,給 Web UI 顯示用。
type DigestConfig struct {
	Enabled          bool       `json:"enabled"`
	CronExpr         string     `json:"cronExpr,omitempty"`
	NotifierIDs      []string   `json:"notifierIds,omitempty"`
	EmailNotifierIDs []string   `json:"emailNotifierIds,omitempty"`
	LastSentAt       *time.Time `json:"lastSentAt,omitempty"`
}

// UPSConfig 是 UPS(不斷電系統)整合的持久化設定。UPSName 是 NUT 裡的
// UPS 名稱(upsc 用),Enabled 決定要不要顯示/輪詢狀態,
// ShutdownOnLowBattery 決定「市電中斷且電量過低」時要不要自動安全關機,
// RuntimeThresholdSeconds 是「預估續航低於這個秒數就關機」的門檻(0=只看
// NUT 的 LB 低電量旗標,不看續航)。見 internal/ups。
type UPSConfig struct {
	Enabled                 bool   `json:"enabled"`
	UPSName                 string `json:"upsName,omitempty"`
	ShutdownOnLowBattery    bool   `json:"shutdownOnLowBattery"`
	RuntimeThresholdSeconds int    `json:"runtimeThresholdSeconds"`
}

// Validate 只在 Enabled 為 true 時要求 CronExpr 是合法的 cron 表達式——
// 停用時 CronExpr 留著舊值或留空都無所謂(不會有任何背景排程去剖析
// 它),跟 backup.Schedule.Validate 對 cron 種類排程的檢查邏輯是同一套
// (直接呼叫 internal/cron.Parse,順便也就此擋掉之後排程執行期間才
// 發現語法錯誤的可能)。
func (c DigestConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.CronExpr) == "" {
		return fmt.Errorf("cronExpr is required when the digest is enabled")
	}
	if _, err := cron.Parse(c.CronExpr); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	return nil
}

// AuditEntry 是 Phase 18b 新增的一筆管理者動作紀錄——「誰、什麼時候、
// 對哪一支端點做了什麼、結果的 HTTP 狀態碼」。刻意只記錄會改動系統
// 狀態的動作(HTTP 方法不是 GET 的請求,見
// internal/api.requireAdmin 包住的紀錄邏輯),單純瀏覽/查詢不記錄——
// 這是一份「誰動過什麼」的稽核紀錄,不是完整的存取紀錄,兩者用途不同,
// 全部都記反而會把真正重要的「誰改了設定」淹沒在大量查詢紀錄裡。
//
// Detail 是給人看的一句話摘要(例如「刪除共享 media」),由呼叫端
// (通常是各個 handler 或 requireAdmin 本身)視情況提供;沒有特別
// 摘要的動作(絕大多數)就只留下方法+路徑本身,前端一樣看得懂發生了
// 什麼事,不需要每一支 handler 都額外花力氣組一句話。
type AuditEntry struct {
	At         time.Time `json:"at"`
	Username   string    `json:"username"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"statusCode"`
	Detail     string    `json:"detail,omitempty"`
}

// UpdateConfig 是 Phase 17 新增的自我更新設定。ManifestURL 留空(預設值)
// 代表使用者完全沒有設定過,自我更新的背景檢查器(見
// internal/selfupdate.Checker)就完全不會對外發任何網路請求——這是
// 刻意的隱私/安全預設值:GoNAS 不會在使用者不知情的情況下自動去某個
// 內建網址「打電話回家」問有沒有新版本,一定要使用者自己在 Web UI 填了
// 一個更新資訊來源(通常是使用者自己架設或信任的一個靜態 JSON 檔案
// URL,見 internal/selfupdate 套件文件的 Manifest 格式說明)才會開始
// 檢查。這跟 HTTPS 設定不同,不需要重啟才會生效——背景檢查器每次要
// 檢查前都重新讀一次目前設定的網址,設定改變後最晚下一次檢查週期就會
// 生效。
type UpdateConfig struct {
	ManifestURL string `json:"manifestUrl,omitempty"`
}

// AdminAccount 是 Web 管理介面的一個登入帳號(跟 share.User 那種系統/
// Samba 帳號是完全不同的概念,一個是「誰能打開 GoNAS 的管理介面」,
// 一個是「誰能透過 SMB/NFS 存取檔案」)。PasswordHash 一律是
// internal/security.HashPassword 產生的編碼字串,永遠不存明文密碼;
// TOTPSecret 在使用者呼叫 /auth/totp/setup 時就會寫入,但 TOTPEnabled
// 要等使用者實際輸入一次驗證碼確認過(呼叫 /auth/totp/enable)才會
// 變成 true —— 避免使用者複製密鑰到驗證器 App 之後,萬一沒設定成功
// 就把自己鎖在登入頁面外面。
//
// Phase 13 之前,Web 管理介面只允許存在「唯一」一個管理帳號
// (State.Admin 曾經是 *AdminAccount 單一指標)。Phase 13 把它換成
// Admins 陣列,讓同一台 NAS 可以有多組各自獨立的登入(帳密、TOTP 都
// 互不相干),並且加上 Role 區分權限:RoleAdmin 可以做任何事,
// RoleViewer 只能讀(GET),不能新增/修改/刪除任何設定或資料 ——
// 詳見 internal/api 的 requireAdmin 中介層。
type AdminAccount struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	TOTPSecret   string `json:"totpSecret,omitempty"`
	TOTPEnabled  bool   `json:"totpEnabled"`
	Role         string `json:"role"`
	// MustChangePassword 為 true 時,這個帳號被要求在下一次登入後立刻
	// 修改密碼才能做任何 admin 操作。目前唯一會把它設成 true 的地方是
	// appliance 映像用 `gonasd -seed-default-admin` 預先建立的那組預設
	// admin(帳密都是 gonas)——好記的預設密碼方便第一次登入,但一登入
	// 就強制改掉,避免預設密碼一直有效。使用者透過 /auth/password 改完
	// 密碼後,handleAuthChangePassword 會把這個旗標清成 false;
	// requireAdmin 中介層在這個旗標為 true 時,會擋掉除了「改自己密碼」
	// 以外的所有 admin 操作(回 403 password_change_required)。
	// omitempty:一般帳號沒有這個旗標,序列化時不必寫出來,保持
	// state.json 乾淨、也跟舊版檔案相容。
	MustChangePassword bool `json:"mustChangePassword,omitempty"`

	// RecoveryCodes 是 2FA 救援碼的 SHA-256(hex),一組一次性代碼(第三十輪
	// 覆核補上):啟用 TOTP 時產生,弄丟驗證器時可用其中一組代替 TOTP 碼登入,
	// 用掉一組就從這裡移除一組。永遠只存雜湊,不存明文(明文只在產生當下
	// 回給使用者看一次)。omitempty:沒啟用 2FA 的帳號不必寫出來。
	RecoveryCodes []string `json:"recoveryCodes,omitempty"`

	// LastTOTPCounter 是這個帳號最近一次成功登入所用的 TOTP 時間窗
	// (counter)。第三十輪覆核抓到 TOTP 碼在 30 秒窗內可重放 —— 登入驗證
	// 成功後把 counter 記在這裡,拒絕同一個或更舊的 counter 再次被用,消除
	// 窗內重放。omitempty:沒用過 TOTP 登入的帳號是 0。
	LastTOTPCounter uint64 `json:"lastTotpCounter,omitempty"`
}

// 目前僅有的兩種帳號權限。RoleAdmin 是完全權限(建立/修改/刪除任何
// 東西,包含管理其他帳號);RoleViewer 是唯讀權限,只能查看現有設定跟
// 資料,不能做任何寫入動作(連自己的密碼/TOTP 都可以改——那是「管理
// 自己的帳號」,不算「管理 NAS 設定」,見 internal/api 對 auth 端點的
// 例外處理)。刻意只做兩級,不做更細的、逐頁面/逐功能的權限矩陣——
// 對一個家用/小型辦公室 NAS,「誰有完整權限」跟「誰只能看」這兩級已經
// 涵蓋絕大多數實際情境,更細的權限模型只會增加使用者設定帳號時要理解
// 的複雜度,換不到對應的實際價值。
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// IsValidRole 檢查一個角色字串是否是上面兩個常數之一 —— 建立/驗證帳號
// 時共用,避免打錯字或之後不小心塞進第三種沒有對應中介層邏輯的角色值。
func IsValidRole(role string) bool {
	return role == RoleAdmin || role == RoleViewer
}

// HTTPSConfig 是 Web 管理介面的 TLS 設定。Enabled 只是「使用者想要
// HTTPS」的意圖記錄 —— 實際切換 gonasd 監聽 HTTP 或 HTTPS 只在程序啟動
// 時讀取一次(見 cmd/gonasd/main.go),改這個設定之後需要重啟 daemon
// 才會生效,API 回應會提醒這件事,詳見 internal/api 的說明。
//
// Hosts 記錄使用者當初開啟 HTTPS 時填的 SAN 主機名稱/IP(見
// internal/security.GenerateSelfSignedCert)。Phase 15 之前這個欄位不
// 存在,憑證簽發完就沒人記得原本填了什麼——這對「憑證快到期時背景自動
// 重簽」這個新功能是個問題:重簽時如果不知道原本的 hosts,只能退回
// localhost/127.0.0.1,會讓使用者原本填的區網 IP/DDNS 網域悄悄從新憑證
// 的 SAN 裡消失。加上這個欄位後,續期時原封不動沿用同一組 hosts。
// omitempty + 全新欄位:讀到 Phase 15 之前、完全沒有這個欄位的舊
// state.json 時,Hosts 就是 nil,不需要任何遷移程式碼——只是續期時會
// 退回 localhost/127.0.0.1(等於「這份憑證最早不是 Phase 15 之後開的
// HTTPS」的自然結果),使用者只要重新在 Web UI 存一次 HTTPS 設定,
// Hosts 就會補上。
type HTTPSConfig struct {
	Enabled  bool     `json:"enabled"`
	CertPath string   `json:"certPath,omitempty"`
	KeyPath  string   `json:"keyPath,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
}

// UserRecord 是 Web UI 顯示用的使用者中繼資料。真正的帳號存在系統的
// /etc/passwd,這裡只是記「GoNAS 建立過這個帳號」，方便列表跟之後刪除時
// 對應回系統帳號,不重複儲存密碼等敏感資訊。
type UserRecord struct {
	Username string `json:"username"`
	Comment  string `json:"comment,omitempty"`
}

// InstalledApp 記錄一次成功的 App 商店安裝，讓 Web UI 能列出「已安裝的 App」
// 而不用每次都重新掃描 Docker 標籤(掃描仍然是 Uninstall 的保險機制，
// 兩者互補：這裡是快速路徑，標籤是遇到 state.json 遺失時的復原路徑)。
type InstalledApp struct {
	Template appstore.AppTemplate   `json:"template"`
	Result   appstore.InstallResult `json:"result"`
}

// Store 是執行期持有 State 並負責原子讀寫的物件。所有方法都是
// goroutine-safe 的,因為 Web UI 的多個 HTTP 請求可能同時進來。
type Store struct {
	mu   sync.RWMutex
	path string
	data State
}

// normalize 確保所有切片欄位都不是 nil。Go 的 encoding/json 會把 nil
// 切片序列化成 `null`,但這幾個欄位在 Web UI 那邊一律當成陣列處理
// (例如直接讀 .length)——一個全新安裝、state.json 還不存在的零值
// State{},或是舊版 state.json 裡剛好缺欄位/欄位是 null,都不該讓前端
// 收到 null 就整頁掛掉。呼叫端只讀,所以這裡淺淺地把 nil 換成空切片就夠,
// 不需要真的深拷貝。
func (st *State) normalize() {
	if st.Shares == nil {
		st.Shares = []share.Share{}
	}
	if st.Exports == nil {
		st.Exports = []share.Export{}
	}
	if st.Users == nil {
		st.Users = []UserRecord{}
	}
	if st.InstalledApps == nil {
		st.InstalledApps = []InstalledApp{}
	}
	if st.AlertRules == nil {
		st.AlertRules = []monitor.AlertRule{}
	}
	if st.Notifiers == nil {
		st.Notifiers = []monitor.WebhookConfig{}
	}
	if st.EmailNotifiers == nil {
		st.EmailNotifiers = []monitor.EmailConfig{}
	}
	if st.Admins == nil {
		st.Admins = []AdminAccount{}
	}
	// 舊版(Phase 13 之前)建立的帳號沒有 Role 欄位,JSON 解析後會是
	// 空字串——一律當成 RoleAdmin 補上,保留它們原本「唯一管理者、
	// 什麼都能做」的權限,不會因為升級就意外被降級成唯讀。
	for i := range st.Admins {
		if st.Admins[i].Role == "" {
			st.Admins[i].Role = RoleAdmin
		}
	}
	// WireGuard 本身是 nilable(還沒設定介面前完全不該有這個欄位),但
	// 一旦存在,裡面的 Peers 切片一樣要套用同一條「絕不序列化成 null」
	// 的規則 —— 不然新增第一個 peer 之前,GET /vpn/peers 就會重演一次
	// Apps 頁面當初那個 nil slice bug。
	if st.WireGuard != nil && st.WireGuard.Peers == nil {
		st.WireGuard.Peers = []wireguard.PeerConfig{}
	}
	if st.BackupJobs == nil {
		st.BackupJobs = []backup.Job{}
	}
	if st.AuditLog == nil {
		st.AuditLog = []AuditEntry{}
	}
	if st.Digest.NotifierIDs == nil {
		st.Digest.NotifierIDs = []string{}
	}
	if st.Digest.EmailNotifierIDs == nil {
		st.Digest.EmailNotifierIDs = []string{}
	}
}

// Open 從 path 載入既有的狀態檔;檔案不存在時視為全新安裝，回傳一個空的
// Store 而不是錯誤 —— 這是第一次執行 GoNAS 的正常情況,不是異常。
func Open(path string) (*Store, error) {
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s.data.normalize()
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}

	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("parsing state file %s: %w", path, err)
	}

	// Phase 13 把單一 Admin *AdminAccount 換成 Admins []AdminAccount。
	// 舊版 state.json 裡是 `"admin": {...}` 這個單數欄位,現在的 State
	// 結構已經不認得這個 key 了,json.Unmarshal 會直接忽略它,如果不
	// 另外處理,舊使用者升級後帳號會憑空消失、被迫走一次「還沒有管理者
	// 帳號」的初始設定流程——這裡額外解析一次舊欄位,搬進新的 Admins
	// 陣列裡,讓既有的帳密/TOTP 設定在升級後繼續可用。
	var legacy struct {
		Admin *AdminAccount `json:"admin"`
	}
	if err := json.Unmarshal(data, &legacy); err == nil && legacy.Admin != nil && len(s.data.Admins) == 0 {
		migrated := *legacy.Admin
		if migrated.Role == "" {
			migrated.Role = RoleAdmin
		}
		s.data.Admins = append(s.data.Admins, migrated)
	}

	s.data.normalize()
	return s, nil
}

// Snapshot 回傳目前狀態的一份副本，呼叫端可以安心讀取，不會跟其他
// goroutine 的寫入互相影響(淺拷貝對這裡的用途已經足夠：呼叫端只讀,
// 不會去改 Snapshot 裡的切片內容)。這裡也順手 normalize 一次,對已經
// 從 Open 正確初始化過的 Store 是沒有作用的 no-op,但對之後如果有新程式碼
// 路徑直接建構 Store{} 而略過 Open 的情況,是最後一道防線。
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := s.data
	snap.normalize()
	return snap
}

// Update 在鎖保護下呼叫 fn 修改狀態，成功後原子寫回磁碟;fn 回傳錯誤時
// 完全不寫檔,狀態維持修改前的樣子 —— 呼叫端不需要自己處理「一半寫入」
// 的情況。
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 先在副本上操作，fn 失敗就直接丟棄，s.data 完全不受影響。
	next := s.data
	if err := fn(&next); err != nil {
		return err
	}
	// fn 可能把某個切片欄位設回 nil（例如清空一個清單時手滑寫成
	// `st.Shares = nil` 而不是 `st.Shares = []share.Share{}`）,寫檔跟
	// 之後的 Snapshot 前都再 normalize 一次，確保這個欄位永遠不會以
	// null 的型態出現在 state.json 或是 API 回應裡。
	next.normalize()

	if err := s.writeLocked(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// SeedAdminIfEmpty 只在「目前完全沒有任何管理帳號」時,新增一組帳號
// (角色一律 RoleAdmin、MustChangePassword=true),並回傳 true 表示真的
// 加了;如果已經有帳號了,什麼都不做、回傳 false —— 這讓它可以安全地
// 重複執行(冪等),不會覆蓋掉使用者已經設好的帳號。
//
// passwordHash 必須是 internal/security.HashPassword 產生的編碼字串
// (這個套件刻意不 import security,避免相依循環,也讓這個方法純粹是
// 「狀態操作」、好測試——雜湊由呼叫端算好傳進來)。
//
// 這是給 appliance 映像的 `gonasd -seed-default-admin` 用的:預先建立
// 一組好記的預設 admin(帳密都是 gonas),但標記為「必須第一次登入就
// 改密碼」,避免預設密碼一直有效。
func (s *Store) SeedAdminIfEmpty(username, passwordHash string) (bool, error) {
	added := false
	err := s.Update(func(st *State) error {
		if len(st.Admins) > 0 {
			return nil
		}
		st.Admins = append(st.Admins, AdminAccount{
			Username:           username,
			PasswordHash:       passwordHash,
			Role:               RoleAdmin,
			MustChangePassword: true,
		})
		added = true
		return nil
	})
	return added, err
}

func (s *Store) writeLocked(data State) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("ensuring state dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".gonas-state-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp state file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil { // state.json 可能含密碼相關中繼資料，不給其他使用者讀
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("renaming temp state file to %s: %w", s.path, err)
	}
	return nil
}
