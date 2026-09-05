package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bng147/gonas/internal/storage"
)

func TestOpen_MissingFile_ReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error for missing file: %v", err)
	}
	snap := s.Snapshot()
	if snap.Pool != nil || len(snap.Shares) != 0 || len(snap.Users) != 0 {
		t.Errorf("expected empty initial state, got %+v", snap)
	}
}

func TestUpdate_PersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	err = s.Update(func(st *State) error {
		st.Users = append(st.Users, UserRecord{Username: "alice", Comment: "Alice Chen"})
		st.Pool = &storage.PoolConfig{Name: "tank", DataDisks: []string{"/mnt/disk1"}}
		return nil
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	// 重新從磁碟載入，確認真的寫進去了，不是只停留在記憶體裡。
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open returned error: %v", err)
	}
	snap := reloaded.Snapshot()
	if len(snap.Users) != 1 || snap.Users[0].Username != "alice" {
		t.Errorf("expected persisted user 'alice', got %+v", snap.Users)
	}
	if snap.Pool == nil || snap.Pool.Name != "tank" {
		t.Errorf("expected persisted pool 'tank', got %+v", snap.Pool)
	}
}

func TestUpdate_FailureDoesNotPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	sentinel := errors.New("validation failed")
	err = s.Update(func(st *State) error {
		st.Users = append(st.Users, UserRecord{Username: "should-not-be-saved"})
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to propagate, got: %v", err)
	}

	if len(s.Snapshot().Users) != 0 {
		t.Errorf("expected in-memory state unchanged after failed Update, got %+v", s.Snapshot().Users)
	}

	// 而且完全不該有檔案被寫出來 —— 全新安裝時 fn 失敗不該憑空產生一個 state.json。
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open returned error: %v", err)
	}
	if len(reloaded.Snapshot().Users) != 0 {
		t.Errorf("expected no file to have been written on failure, got %+v", reloaded.Snapshot())
	}
}

func TestUpdate_SequentialUpdatesAccumulate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)

	for _, name := range []string{"alice", "bob", "carol"} {
		err := s.Update(func(st *State) error {
			st.Users = append(st.Users, UserRecord{Username: name})
			return nil
		})
		if err != nil {
			t.Fatalf("Update(%q) returned error: %v", name, err)
		}
	}

	snap := s.Snapshot()
	if len(snap.Users) != 3 {
		t.Fatalf("expected 3 accumulated users, got %d: %+v", len(snap.Users), snap.Users)
	}
}

// TestSnapshot_FreshStore_SlicesMarshalAsEmptyArraysNotNull 是 Phase 4 端到端
// 驗證(用真的瀏覽器打開 Web UI)抓到的一個真實 bug 的迴歸測試：全新安裝、
// state.json 還不存在時,State{} 的切片欄位是 nil,encoding/json 會把 nil
// 切片序列化成 `null` 而不是 `[]`。前端的 renderApps() 對著
// GET /api/v1/appstore/apps 回來的資料直接呼叫 `installed.length`,
// 收到 null 就整頁掛掉 —— 而且不是靠 HTTP 錯誤/fetch 失敗觸發,所以前端
// 那層 `.catch(() => [])` 完全接不住。這裡直接斷言 JSON 序列化結果的
// 原始位元組,而不是只檢查 Go 型別層面的 len()==0,因為 bug 的本質正好
// 是「Go 看起來沒事,但序列化出來的 JSON 是 null」。
func TestSnapshot_FreshStore_SlicesMarshalAsEmptyArraysNotNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error for missing file: %v", err)
	}

	snap := s.Snapshot()
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshaling fresh snapshot: %v", err)
	}

	for _, field := range []string{`"shares":null`, `"exports":null`, `"users":null`, `"installedApps":null`, `"alertRules":null`, `"notifiers":null`, `"backupJobs":null`} {
		if strings.Contains(string(encoded), field) {
			t.Errorf("fresh state marshaled with %s, want an empty array `[]` — this is exactly the bug that crashed the Apps page: %s", field, encoded)
		}
	}
	for _, field := range []string{`"shares":[]`, `"exports":[]`, `"users":[]`, `"installedApps":[]`, `"alertRules":[]`, `"notifiers":[]`, `"backupJobs":[]`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("expected %s in marshaled fresh state, got: %s", field, encoded)
		}
	}
}

// TestOpen_LoadsPreExistingNullSlices_NormalizesThem 涵蓋另一半的情境：
// 不只是全新安裝的零值 State{},一份「手動編輯過、或是舊版本寫出來的」
// state.json 裡欄位直接是 JSON null(例如手動 `"shares": null`),重新
// Open 之後一樣要被 normalize 成空陣列,而不是把 null 原封不動載回記憶體
// 裡、繼續讓之後的 API 回應把 null 傳給前端。
func TestOpen_LoadsPreExistingNullSlices_NormalizesThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"shares":null,"exports":null,"users":null,"installedApps":null,"alertRules":null,"notifiers":null,"backupJobs":null}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("writing seed state file: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	snap := s.Snapshot()
	if snap.Shares == nil || snap.Exports == nil || snap.Users == nil || snap.InstalledApps == nil || snap.AlertRules == nil || snap.Notifiers == nil || snap.BackupJobs == nil {
		t.Fatalf("expected Open to normalize null slice fields loaded from disk, got %+v", snap)
	}

	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshaling snapshot: %v", err)
	}
	if strings.Contains(string(encoded), "null") {
		t.Errorf("expected no null fields after loading a state.json with explicit nulls, got: %s", encoded)
	}
}

// TestNormalize_WireGuardPeersNestedNilSlice 涵蓋一個巢狀情況:WireGuard
// 本身是 nilable(還沒設定介面前完全不該出現),但一旦存在,它裡面的
// Peers 切片一樣要遵守同一條「絕不序列化成 null」的規則。這是新增
// WireGuard 支援時,照著 Phase 4 那次真實 bug 的教訓,主動補上的迴歸
// 測試 —— 不用等到真的有人在瀏覽器上打開空的 peer 清單頁面才發現。
func TestNormalize_WireGuardPeersNestedNilSlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"wireGuard":{"interface":{"privateKey":"x","address":["10.0.0.1/24"],"listenPort":51820},"peers":null}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("writing seed state file: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	snap := s.Snapshot()
	if snap.WireGuard == nil {
		t.Fatalf("expected WireGuard config to be loaded, got nil")
	}
	if snap.WireGuard.Peers == nil {
		t.Fatalf("expected WireGuard.Peers to be normalized to an empty slice, got nil")
	}

	encoded, err := json.Marshal(snap.WireGuard)
	if err != nil {
		t.Fatalf("marshaling wireGuard config: %v", err)
	}
	if !strings.Contains(string(encoded), `"peers":[]`) {
		t.Errorf(`expected marshaled wireGuard config to contain "peers":[], got: %s`, encoded)
	}
}

func TestState_MarshalsWithNoAdminOrWireGuardByDefault(t *testing.T) {
	// WireGuard 是 *T 搭配 omitempty:全新安裝、使用者還沒設定 VPN 介面
	// 之前,這個欄位根本不該出現在 state.json 或任何 API 回應裡(而不是
	// 出現、但值是 null)——omitempty 對 nil pointer 的行為本來就是整個
	// 省略欄位,這裡明確測出來,確保未來重構沒有不小心把它從 pointer
	// 改成非 pointer 而讓這個保證消失。
	//
	// Admins 從 Phase 13 起是 []AdminAccount(不是單一 *AdminAccount),
	// 跟其他清單欄位(Shares/Exports/...)一樣一律存在、只是全新安裝時
	// 是空陣列——這裡改成驗證「空陣列」而不是「完全沒有這個欄位」。
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	encoded, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatalf("marshaling snapshot: %v", err)
	}
	if !strings.Contains(string(encoded), `"admins":[]`) {
		t.Errorf(`expected an empty "admins" array before any account is created, got: %s`, encoded)
	}
	if strings.Contains(string(encoded), `"wireGuard"`) {
		t.Errorf(`expected no "wireGuard" field before a VPN interface is configured, got: %s`, encoded)
	}
	if !strings.Contains(string(encoded), `"https":{`) {
		t.Errorf(`expected an "https" object (non-pointer struct) to always be present, got: %s`, encoded)
	}
}

// TestOpen_MigratesLegacySingleAdminField 涵蓋 Phase 13 的向後相容邏輯:
// 舊版 state.json 用單數的 `"admin": {...}` 欄位存唯一的管理者帳號,新版
// 結構已經不認得這個欄位名稱了。這裡確保升級後既有帳號(帳密、TOTP)
// 會被搬進新的 Admins 陣列,而不是在使用者升級 gonasd 之後憑空消失、
// 被迫走一次「還沒有管理者帳號」的初始設定流程。
func TestOpen_MigratesLegacySingleAdminField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"admin":{"username":"legacyadmin","passwordHash":"hash-abc","totpSecret":"secret-xyz","totpEnabled":true}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("writing seed state file: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	admins := s.Snapshot().Admins
	if len(admins) != 1 {
		t.Fatalf("expected the legacy admin account to be migrated into Admins, got %d entries: %+v", len(admins), admins)
	}
	got := admins[0]
	if got.Username != "legacyadmin" || got.PasswordHash != "hash-abc" || got.TOTPSecret != "secret-xyz" || !got.TOTPEnabled {
		t.Errorf("expected migrated account to preserve all fields, got %+v", got)
	}
	if got.Role != RoleAdmin {
		t.Errorf("expected migrated legacy account to get RoleAdmin, got %q", got.Role)
	}
}

// TestOpen_LegacyAdminMissingRole_DefaultsToRoleAdmin 涵蓋另一種舊版
// state.json:已經是 Phase 13 之後的 `"admins": [...]` 陣列格式,但裡面
// 的帳號是更早、還沒有 Role 欄位的版本寫入的(JSON 解析後 Role 會是空
// 字串)——一律當成 RoleAdmin 補上,不能讓既有帳號因為升級就意外被降級
// 成唯讀,見 normalize() 的註解。
func TestOpen_LegacyAdminMissingRole_DefaultsToRoleAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"admins":[{"username":"noroleyet","passwordHash":"hash-abc"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("writing seed state file: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	admins := s.Snapshot().Admins
	if len(admins) != 1 || admins[0].Role != RoleAdmin {
		t.Errorf("expected the roleless account to default to RoleAdmin, got %+v", admins)
	}
}
