package security

import (
	"testing"
	"time"
)

func TestSessionManager_CreateAndValidate(t *testing.T) {
	m := NewSessionManager(time.Hour)

	s, err := m.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if s.Token == "" {
		t.Fatal("expected a non-empty session token")
	}
	if s.Username != "admin" {
		t.Errorf("Username = %q, want %q", s.Username, "admin")
	}

	got, ok := m.Validate(s.Token)
	if !ok {
		t.Fatal("expected freshly created session to validate")
	}
	if got.Username != "admin" {
		t.Errorf("validated session Username = %q, want %q", got.Username, "admin")
	}
}

func TestSessionManager_Create_TokensAreUnique(t *testing.T) {
	m := NewSessionManager(time.Hour)
	a, err := m.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	b, err := m.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if a.Token == b.Token {
		t.Error("expected two sessions to get different tokens")
	}
}

func TestSessionManager_Validate_UnknownToken(t *testing.T) {
	m := NewSessionManager(time.Hour)
	_, ok := m.Validate("this-token-was-never-issued")
	if ok {
		t.Error("expected an unknown token to fail validation")
	}
}

func TestSessionManager_Validate_EmptyToken(t *testing.T) {
	m := NewSessionManager(time.Hour)
	_, ok := m.Validate("")
	if ok {
		t.Error("expected an empty token to fail validation")
	}
}

func TestSessionManager_Validate_ExpiredTokenIsRejectedAndPurged(t *testing.T) {
	m := NewSessionManager(10 * time.Millisecond)
	s, err := m.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	_, ok := m.Validate(s.Token)
	if ok {
		t.Error("expected an expired session to fail validation")
	}

	// 過期後再驗證一次,確認真的從內部 map 被清掉了(懶惰清理),
	// 而不是每次都要重新判斷時間 —— 用 map 長度間接觀察內部狀態。
	m.mu.Lock()
	_, stillPresent := m.sessions[s.Token]
	m.mu.Unlock()
	if stillPresent {
		t.Error("expected expired session to be purged from internal storage after Validate")
	}
}

func TestSessionManager_Revoke(t *testing.T) {
	m := NewSessionManager(time.Hour)
	s, err := m.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	m.Revoke(s.Token)

	_, ok := m.Validate(s.Token)
	if ok {
		t.Error("expected a revoked session to fail validation")
	}
}

func TestSessionManager_Revoke_UnknownTokenIsNoop(t *testing.T) {
	m := NewSessionManager(time.Hour)
	m.Revoke("never-existed") // 不應該 panic
}

func TestSessionManager_RevokeAllForUser(t *testing.T) {
	m := NewSessionManager(time.Hour)
	a1, _ := m.Create("alice")
	a2, _ := m.Create("alice")
	b, _ := m.Create("bob")

	m.RevokeAllForUser("alice")

	if _, ok := m.Validate(a1.Token); ok {
		t.Error("expected alice's first session to be revoked")
	}
	if _, ok := m.Validate(a2.Token); ok {
		t.Error("expected alice's second session to be revoked")
	}
	if _, ok := m.Validate(b.Token); !ok {
		t.Error("expected bob's session to remain valid (different user)")
	}
}
