package share

import (
	"context"
	"os"
	"strings"
	"testing"
)

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

func TestGenerateSambaConfig(t *testing.T) {
	shares := []Share{
		{Name: "media", Path: "/mnt/tank/media", Comment: "Movies and TV", ReadOnly: false, GuestOK: false, ValidUsers: []string{"alice", "bob"}},
		{Name: "public", Path: "/mnt/tank/public", GuestOK: true},
	}

	out, err := GenerateSambaConfig(shares, "/etc/samba/gonas-shares.conf")
	if err != nil {
		t.Fatalf("GenerateSambaConfig returned error: %v", err)
	}

	for _, want := range []string{
		"[media]",
		"path = /mnt/tank/media",
		"comment = Movies and TV",
		"read only = no",
		"valid users = alice, bob",
		"[public]",
		"guest ok = yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected generated config to contain %q, got:\n%s", want, out)
		}
	}
}

func TestGenerateSambaConfig_RejectsInvalidShare(t *testing.T) {
	if _, err := GenerateSambaConfig([]Share{{Name: "", Path: "/mnt/x"}}, ""); err == nil {
		t.Fatal("expected error for share with empty name")
	}
}

func TestGenerateSambaConfig_RejectsDuplicateNames(t *testing.T) {
	shares := []Share{
		{Name: "media", Path: "/mnt/tank/media"},
		{Name: "media", Path: "/mnt/tank/other"},
	}
	if _, err := GenerateSambaConfig(shares, ""); err == nil {
		t.Fatal("expected error for duplicate share names")
	}
}

func TestGenerateSambaConfig_RejectsUnsafeName(t *testing.T) {
	if _, err := GenerateSambaConfig([]Share{{Name: `evil"]\n[global`, Path: "/mnt/x"}}, ""); err == nil {
		t.Fatal("expected error for share name containing smb.conf-breaking characters")
	}
}

func TestValidateSambaConfig_PropagatesFailure(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"testparm": errBoom}}
	if err := ValidateSambaConfig(context.Background(), r, "/etc/samba/gonas-shares.conf"); err == nil {
		t.Fatal("expected error when testparm fails")
	}
}

func TestReloadSamba_CallsSmbcontrol(t *testing.T) {
	r := &fakeRunner{}
	if err := ReloadSamba(context.Background(), r); err != nil {
		t.Fatalf("ReloadSamba returned error: %v", err)
	}
	last := r.lastCall()
	if last.name != "smbcontrol" {
		t.Errorf("expected smbcontrol to be called, got %q", last.name)
	}
}

func TestSyncSambaPassword_SendsPasswordTwiceOnStdin(t *testing.T) {
	r := &fakeRunner{}
	if err := SyncSambaPassword(context.Background(), r, "alice", "s3cret"); err != nil {
		t.Fatalf("SyncSambaPassword returned error: %v", err)
	}
	last := r.lastCall()
	if last.name != "smbpasswd" {
		t.Errorf("expected smbpasswd to be called, got %q", last.name)
	}
	if string(last.stdin) != "s3cret\ns3cret\n" {
		t.Errorf("expected password sent twice on stdin, got %q", string(last.stdin))
	}
	// 密碼不該以參數形式出現在指令列上（會被其他使用者用 ps 看到）。
	for _, a := range last.args {
		if strings.Contains(a, "s3cret") {
			t.Errorf("password leaked into command-line args: %v", last.args)
		}
	}
}

func TestWriteConfigAtomically(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/gonas-shares.conf"

	if err := WriteConfigAtomically(path, "hello"); err != nil {
		t.Fatalf("WriteConfigAtomically returned error: %v", err)
	}
	data, err := readFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if data != "hello" {
		t.Errorf("expected file content %q, got %q", "hello", data)
	}

	// 覆寫一次，確認第二次寫入也乾淨（沒有殘留暫存檔）。
	if err := WriteConfigAtomically(path, "world"); err != nil {
		t.Fatalf("second WriteConfigAtomically returned error: %v", err)
	}
	data, _ = readFile(path)
	if data != "world" {
		t.Errorf("expected overwritten content %q, got %q", "world", data)
	}
}
