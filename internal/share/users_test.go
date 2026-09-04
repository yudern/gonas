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
