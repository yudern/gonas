package configsync

import (
	"path/filepath"
	"testing"

	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

func sampleState() state.State {
	return state.State{
		Pool: &storage.PoolConfig{Name: "tank", MountPoint: "/mnt/tank"},
		Admins: []state.AdminAccount{
			{Username: "alice", PasswordHash: "HASH", Role: state.RoleAdmin, TOTPSecret: "SECRET", TOTPEnabled: true, RecoveryCodes: []string{"a", "b"}, LastTOTPCounter: 42},
		},
		EmailNotifiers: []monitor.EmailConfig{{ID: "e1", SMTPHost: "smtp.x", Password: "p@ss"}},
		AlertRules:     []monitor.AlertRule{{ID: "r1"}},
		UPS:            state.UPSConfig{Enabled: true, UPSName: "ups1"},
		AppCatalogURL:  "https://example/catalog.json",
		AuditLog:       []state.AuditEntry{{Username: "alice", Path: "/x"}},
	}
}

func TestExportRedactsSecretsKeepsHash(t *testing.T) {
	ex := Export(sampleState(), "v1.2.3", "nas")
	if ex.Magic != Magic || ex.Version != CurrentVersion {
		t.Fatalf("bad header: %+v", ex)
	}
	a := ex.Config.Admins[0]
	if a.PasswordHash != "HASH" {
		t.Error("password hash should be kept (it is a hash, not plaintext)")
	}
	if a.TOTPSecret != "" || a.RecoveryCodes != nil || a.TOTPEnabled || a.LastTOTPCounter != 0 {
		t.Errorf("2FA secrets must be redacted: %+v", a)
	}
	if ex.Config.EmailNotifiers[0].Password != "" {
		t.Error("email password must be redacted")
	}
	if ex.Config.AuditLog != nil {
		t.Error("audit log must not be exported")
	}
}

func TestValidate(t *testing.T) {
	if err := (ExportFile{Magic: "nope", Version: 1}).Validate(); err == nil {
		t.Error("bad magic should fail")
	}
	if err := (ExportFile{Magic: Magic, Version: 99}).Validate(); err == nil {
		t.Error("future version should fail")
	}
	if err := (ExportFile{Magic: Magic, Version: 1}).Validate(); err != nil {
		t.Errorf("valid file should pass: %v", err)
	}
}

func TestApplyRestoresSelectedSections(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := Export(sampleState(), "v1", "nas")

	// 只還原帳號 + 系統設定,不還原通知。
	rep, err := Apply(store, ex, Sections{Accounts: true, SystemConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	snap := store.Snapshot()
	if len(snap.Admins) != 1 || snap.Admins[0].Username != "alice" {
		t.Errorf("accounts not restored: %+v", snap.Admins)
	}
	if !snap.UPS.Enabled || snap.UPS.UPSName != "ups1" {
		t.Errorf("system config not restored: %+v", snap.UPS)
	}
	if snap.AppCatalogURL != "https://example/catalog.json" {
		t.Errorf("app catalog url not restored")
	}
	if len(snap.AlertRules) != 0 {
		t.Errorf("notifications should NOT be restored when not selected")
	}
	// 手動清單應包含 pool 與 shares。
	foundPool := false
	for _, m := range rep.Manual {
		if m == "pool" {
			foundPool = true
		}
	}
	if !foundPool {
		t.Errorf("manual list should mention pool: %v", rep.Manual)
	}
}

func TestApplyRejectsNoAdmin(t *testing.T) {
	dir := t.TempDir()
	store, _ := state.Open(filepath.Join(dir, "state.json"))
	ex := Export(state.State{Admins: []state.AdminAccount{{Username: "v", Role: state.RoleViewer, PasswordHash: "H"}}}, "v1", "nas")
	if _, err := Apply(store, ex, Sections{Accounts: true}); err == nil {
		t.Error("restoring accounts with no admin should fail")
	}
}
