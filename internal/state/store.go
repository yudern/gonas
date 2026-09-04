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
	"github.com/bng147/gonas/internal/share"
	"github.com/bng147/gonas/internal/storage"
)

// State 是整份持久化狀態的結構。每個欄位的零值都必須是「合理的初始狀態」
// (空陣列/nil pool),這樣全新安裝、還沒有 state.json 檔案時可以直接用
// 零值 State{} 開始運作,不需要特殊的「第一次執行」邏輯。
type State struct {
	Pool          *storage.PoolConfig `json:"pool,omitempty"`
	Shares        []share.Share       `json:"shares"`
	Exports       []share.Export      `json:"exports"`
	Users         []UserRecord        `json:"users"`
	InstalledApps []InstalledApp      `json:"installedApps"`
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

// Open 從 path 載入既有的狀態檔;檔案不存在時視為全新安裝，回傳一個空的
// Store 而不是錯誤 —— 這是第一次執行 GoNAS 的正常情況,不是異常。
func Open(path string) (*Store, error) {
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}

	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("parsing state file %s: %w", path, err)
	}
	return s, nil
}

// Snapshot 回傳目前狀態的一份副本，呼叫端可以安心讀取，不會跟其他
// goroutine 的寫入互相影響(淺拷貝對這裡的用途已經足夠：呼叫端只讀,
// 不會去改 Snapshot 裡的切片內容)。
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
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
