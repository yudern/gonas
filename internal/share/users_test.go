package share

import (
	"context"
	"strings"
	"testing"
)

func TestCreateUser_BuildsExpectedArgs(t *testing.T) {
	r := &fakeRunner{}
	if err := CreateUser(context.Background(), r, User{Username: "alice", Comment: "Alice Chen"}); err != nil {
		t.Fatalf("CreateUser returned error: %v", err)
	}

	last := r.lastCall()
	if last.name != "useradd" {
		t.Fatalf("expected useradd to be called, got %q", last.name)
	}
	joined := strings.Join(last.args, " ")
	if !strings.Contains(joined, "-M") || !strings.Contains(joined, "-s /usr/sbin/nologin") {
		t.Errorf("expected no-home, no-shell flags, got args: %v", last.args)
	}
	if last.args[len(last.args)-1] != "alice" {
		t.Errorf("expected username as last arg, got: %v", last.args)
	}
}

func TestCreateUser_RejectsInvalidUsername(t *testing.T) {
	r := &fakeRunner{}
	if err := CreateUser(context.Background(), r, User{Username: "Alice Chen!"}); err == nil {
		t.Fatal("expected error for invalid username")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected useradd not to be called for an invalid username, got %v", r.calls)
	}
}

func TestDeleteUser_CallsUserdel(t *testing.T) {
	r := &fakeRunner{}
	if err := DeleteUser(context.Background(), r, "alice"); err != nil {
		t.Fatalf("DeleteUser returned error: %v", err)
	}
	last := r.lastCall()
	if last.name != "userdel" || len(last.args) != 1 || last.args[0] != "alice" {
		t.Errorf("unexpected call: %+v", last)
	}
}

func TestSetSystemPassword_SendsUsernameColonPasswordOnStdin(t *testing.T) {
	r := &fakeRunner{}
	if err := SetSystemPassword(context.Background(), r, "alice", "s3cret"); err != nil {
		t.Fatalf("SetSystemPassword returned error: %v", err)
	}
	last := r.lastCall()
	if last.name != "chpasswd" {
		t.Errorf("expected chpasswd to be called, got %q", last.name)
	}
	if string(last.stdin) != "alice:s3cret\n" {
		t.Errorf("unexpected stdin: %q", string(last.stdin))
	}
}

// 第三十輪覆核(資深安全工程師)抓到的漏洞的回歸測試:密碼含換行時,
// chpasswd 會把它當成第二行 "帳號:密碼",可改掉別的帳號(例如 root)的
// 密碼。修好之後,含控制字元的密碼必須被擋下、且 chpasswd 完全不被呼叫。
func TestSetSystemPassword_RejectsNewlineInjection(t *testing.T) {
	r := &fakeRunner{}
	err := SetSystemPassword(context.Background(), r, "alice", "x\nroot:pwned123")
	if err == nil {
		t.Fatal("expected error for password containing a newline (chpasswd injection)")
	}
	if len(r.calls) != 0 {
		t.Errorf("chpasswd must NOT be called when the password is unsafe, got calls: %v", r.calls)
	}
}

func TestValidatePassword(t *testing.T) {
	for _, p := range []string{"s3cret", "a b c", "günther-π", "long correct horse battery"} {
		if err := ValidatePassword(p); err != nil {
			t.Errorf("ValidatePassword(%q) should pass, got %v", p, err)
		}
	}
	for _, p := range []string{"x\nroot:pwned", "tab\there", "cr\rhere", "nul\x00byte"} {
		if err := ValidatePassword(p); err == nil {
			t.Errorf("ValidatePassword(%q) should reject control chars", p)
		}
	}
}

func TestSetSystemPassword_RejectsColonInUsername(t *testing.T) {
	r := &fakeRunner{}
	if err := SetSystemPassword(context.Background(), r, "a:b", "s3cret"); err == nil {
		t.Fatal("expected error for username containing a colon")
	}
	if len(r.calls) != 0 {
		t.Errorf("chpasswd must NOT be called for an unsafe username, got: %v", r.calls)
	}
}

func TestDeleteUser_RejectsInvalidUsername(t *testing.T) {
	r := &fakeRunner{}
	if err := DeleteUser(context.Background(), r, "-rf"); err == nil {
		t.Fatal("expected error for invalid username")
	}
	if len(r.calls) != 0 {
		t.Errorf("userdel must NOT be called for an invalid username, got: %v", r.calls)
	}
}

func TestSyncSambaPassword_RejectsControlChars(t *testing.T) {
	r := &fakeRunner{}
	if err := SyncSambaPassword(context.Background(), r, "alice", "x\npwn"); err == nil {
		t.Fatal("expected error for password with control chars")
	}
	if len(r.calls) != 0 {
		t.Errorf("smbpasswd must NOT be called when the password is unsafe, got: %v", r.calls)
	}
}

func TestCreateGroup_RejectsInvalidName(t *testing.T) {
	r := &fakeRunner{}
	if err := CreateGroup(context.Background(), r, "bad name!"); err == nil {
		t.Fatal("expected error for invalid group name")
	}
	if len(r.calls) != 0 {
		t.Errorf("groupadd must NOT be called for an invalid group name, got: %v", r.calls)
	}
}

func TestCreateGroup_CallsGroupadd(t *testing.T) {
	r := &fakeRunner{}
	if err := CreateGroup(context.Background(), r, "media-users"); err != nil {
		t.Fatalf("CreateGroup returned error: %v", err)
	}
	last := r.lastCall()
	if last.name != "groupadd" || len(last.args) != 1 || last.args[0] != "media-users" {
		t.Errorf("unexpected call: %+v", last)
	}
}
