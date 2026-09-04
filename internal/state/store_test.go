package state

import (
	"errors"
	"path/filepath"
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
