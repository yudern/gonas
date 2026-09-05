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
	"sync"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/share"
	"github.com/bng147/gonas/internal/storage"
	"github.com/bng147/gonas/internal/wireguard"
)

// State 是整份持久化狀態的結構。每個欄位的零值都必須是「合理的初始狀態」
// (空陣列/nil pool),這樣全新安裝、還沒有 state.json 檔案時可以直接用
// 零值 State{} 開始運作,不需要特殊的「第一次執行」邏輯。
type State struct {
	Pool          *storage.PoolConfig     `json:"pool,omitempty"`
	Shares        []share.Share           `json:"shares"`
	Exports       []share.Export          `json:"exports"`
	Users         []UserRecord            `json:"users"`
	InstalledApps []InstalledApp          `json:"installedApps"`
	AlertRules    []monitor.AlertRule     `json:"alertRules"`
	Notifiers     []monitor.WebhookConfig `json:"notifiers"`
	Admin         *AdminAccount           `json:"admin,omitempty"`
	HTTPS         HTTPSConfig             `json:"https"`
	WireGuard     *wireguard.Config       `json:"wireGuard,omitempty"`
}

// AdminAccount 是 Web 管理介面唯一的登入帳號(跟 share.User 那種系統/
// Samba 帳號是完全不同的概念,一個是「誰能打開 GoNAS 的管理介面」,
// 一個是「誰能透過 SMB/NFS 存取檔案」)。PasswordHash 一律是
// internal/security.HashPassword 產生的編碼字串,永遠不存明文密碼;
// TOTPSecret 在使用者呼叫 /auth/totp/setup 時就會寫入,但 TOTPEnabled
// 要等使用者實際輸入一次驗證碼確認過(呼叫 /auth/totp/enable)才會
// 變成 true —— 避免使用者複製密鑰到驗證器 App 之後,萬一沒設定成功
// 就把自己鎖在登入頁面外面。
type AdminAccount struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	TOTPSecret   string `json:"totpSecret,omitempty"`
	TOTPEnabled  bool   `json:"totpEnabled"`
}

// HTTPSConfig 是 Web 管理介面的 TLS 設定。Enabled 只是「使用者想要
// HTTPS」的意圖記錄 —— 實際切換 gonasd 監聽 HTTP 或 HTTPS 只在程序啟動
// 時讀取一次(見 cmd/gonasd/main.go),改這個設定之後需要重啟 daemon
// 才會生效,API 回應會提醒這件事,詳見 internal/api 的說明。
type HTTPSConfig struct {
	Enabled  bool   `json:"enabled"`
	CertPath string `json:"certPath,omitempty"`
	KeyPath  string `json:"keyPath,omitempty"`
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
	// WireGuard 本身是 nilable(還沒設定介面前完全不該有這個欄位),但
	// 一旦存在,裡面的 Peers 切片一樣要套用同一條「絕不序列化成 null」
	// 的規則 —— 不然新增第一個 peer 之前,GET /vpn/peers 就會重演一次
	// Apps 頁面當初那個 nil slice bug。
	if st.WireGuard != nil && st.WireGuard.Peers == nil {
		st.WireGuard.Peers = []wireguard.PeerConfig{}
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
