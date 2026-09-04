package share

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateExportsConfig(t *testing.T) {
	exports := []Export{
		{
			Path: "/mnt/tank/media",
			Clients: []NFSClientRule{
				{CIDR: "192.168.1.0/24", Options: []string{"rw", "sync", "no_subtree_check"}},
				{CIDR: "10.0.0.5", Options: []string{"ro", "sync"}},
			},
		},
	}

	out, err := GenerateExportsConfig(exports)
	if err != nil {
		t.Fatalf("GenerateExportsConfig returned error: %v", err)
	}

	for _, want := range []string{
		"/mnt/tank/media 192.168.1.0/24(rw,sync,no_subtree_check)",
		"/mnt/tank/media 10.0.0.5(ro,sync)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected exports config to contain %q, got:\n%s", want, out)
		}
	}
}

func TestGenerateExportsConfig_RejectsExportWithNoClients(t *testing.T) {
	exports := []Export{{Path: "/mnt/tank/media"}}
	if _, err := GenerateExportsConfig(exports); err == nil {
		t.Fatal("expected error for export with no client rules (unrestricted exports are disallowed by design)")
	}
}

func TestGenerateExportsConfig_RejectsClientWithNoOptions(t *testing.T) {
	exports := []Export{{
		Path:    "/mnt/tank/media",
		Clients: []NFSClientRule{{CIDR: "10.0.0.0/8"}},
	}}
	if _, err := GenerateExportsConfig(exports); err == nil {
		t.Fatal("expected error for client rule with no options")
	}
}

func TestReloadNFS_CallsExportfs(t *testing.T) {
	r := &fakeRunner{}
	if err := ReloadNFS(context.Background(), r); err != nil {
		t.Fatalf("ReloadNFS returned error: %v", err)
	}
	last := r.lastCall()
	if last.name != "exportfs" {
		t.Errorf("expected exportfs to be called, got %q", last.name)
	}
}
