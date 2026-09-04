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

	for _, field := range []string{`"shares":null`, `"exports":null`, `"users":null`, `"installedApps":null`} {
		if strings.Contains(string(encoded), field) {
			t.Errorf("fresh state marshaled with %s, want an empty array `[]` — this is exactly the bug that crashed the Apps page: %s", field, encoded)
		}
	}
	for _, field := range []string{`"shares":[]`, `"exports":[]`, `"users":[]`, `"installedApps":[]`} {
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
	raw := `{"shares":null,"exports":null,"users":null,"installedApps":null}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("writing seed state file: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	snap := s.Snapshot()
	if snap.Shares == nil || snap.Exports == nil || snap.Users == nil || snap.InstalledApps == nil {
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
