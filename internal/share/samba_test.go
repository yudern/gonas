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

// TestShareValidate_RejectsBadValidUser 固化第五十六輪覆核(QA6):valid-user
// 名字含逗號/空白/控制字元都要被擋(否則 "valid users = a, b" 會被拆錯)。
func TestShareValidate_RejectsBadValidUser(t *testing.T) {
	for _, bad := range []string{"alice bob", "alice,bob", "alice\tbob", "alice\nbob"} {
		sh := Share{Name: "media", Path: "/mnt/tank/media", ValidUsers: []string{bad}}
		if err := sh.Validate(); err == nil {
			t.Errorf("expected valid-user %q to be rejected", bad)
		}
	}
	// 正常名字要能過。
	if err := (Share{Name: "media", Path: "/mnt/tank/media", ValidUsers: []string{"alice", "bob"}}).Validate(); err != nil {
		t.Fatalf("expected a normal share to pass, got: %v", err)
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

// 第五十八輪全鏈路覆核(產品 P2)的回歸測試:EnsureSambaInclude 必須真的把
// GoNAS 的共享檔用 `include =` 接進 smb.conf,否則 smbd 永遠讀不到、共享看不到。
func TestEnsureSambaInclude(t *testing.T) {
	const inc = "/etc/samba/gonas-shares.conf"

	t.Run("appends include when missing, preserves existing content", func(t *testing.T) {
		dir := t.TempDir()
		p := dir + "/smb.conf"
		orig := "[global]\n\tworkgroup = WORKGROUP\n\n[printers]\n\tpath = /var/spool/samba\n"
		if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := EnsureSambaInclude(p, inc); err != nil {
			t.Fatalf("EnsureSambaInclude: %v", err)
		}
		got, _ := readFile(p)
		if !strings.Contains(got, "workgroup = WORKGROUP") || !strings.Contains(got, "[printers]") {
			t.Error("original smb.conf content was not preserved")
		}
		if !strings.Contains(got, "include = "+inc) {
			t.Errorf("include line not added; got:\n%s", got)
		}
	})

	t.Run("idempotent when include already present", func(t *testing.T) {
		dir := t.TempDir()
		p := dir + "/smb.conf"
		orig := "[global]\n\tinclude = " + inc + "\n"
		if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := EnsureSambaInclude(p, inc); err != nil {
			t.Fatalf("EnsureSambaInclude: %v", err)
		}
		got, _ := readFile(p)
		if strings.Count(got, "include = "+inc) != 1 {
			t.Errorf("include line should appear exactly once, got:\n%s", got)
		}
	})

	t.Run("a commented-out include does not count", func(t *testing.T) {
		dir := t.TempDir()
		p := dir + "/smb.conf"
		orig := "[global]\n\t# include = " + inc + "\n"
		if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := EnsureSambaInclude(p, inc); err != nil {
			t.Fatalf("EnsureSambaInclude: %v", err)
		}
		got, _ := readFile(p)
		// 應該補上一行「未註解」的 include(現在含註解那行 + 新加的實際那行）。
		lines := strings.Split(got, "\n")
		active := 0
		for _, l := range lines {
			tl := strings.TrimSpace(l)
			if strings.HasPrefix(tl, "#") || strings.HasPrefix(tl, ";") {
				continue
			}
			if strings.HasPrefix(strings.ToLower(tl), "include") && strings.Contains(tl, inc) {
				active++
			}
		}
		if active != 1 {
			t.Errorf("expected exactly one active include line, got %d; content:\n%s", active, got)
		}
	})

	t.Run("creates a minimal smb.conf when absent", func(t *testing.T) {
		dir := t.TempDir()
		p := dir + "/smb.conf" // 不建立,模擬檔案不存在
		if err := EnsureSambaInclude(p, inc); err != nil {
			t.Fatalf("EnsureSambaInclude: %v", err)
		}
		got, err := readFile(p)
		if err != nil {
			t.Fatalf("smb.conf was not created: %v", err)
		}
		if !strings.Contains(got, "[global]") || !strings.Contains(got, "include = "+inc) {
			t.Errorf("minimal smb.conf missing [global] or include; got:\n%s", got)
		}
	})
}
