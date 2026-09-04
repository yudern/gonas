package storage

import (
	"context"
	"strings"
	"testing"
)

func testPoolConfig() PoolConfig {
	return PoolConfig{
		Name:         "tank",
		DataDisks:    []string{"/mnt/disk1", "/mnt/disk2", "/mnt/disk3"},
		ParityDisks:  []string{"/mnt/parity1"},
		MountPoint:   "/mnt/tank",
		ContentFiles: []string{"/mnt/disk1", "/boot/config/snapraid"},
	}
}

func TestGenerateSnapraidConfig(t *testing.T) {
	out, err := GenerateSnapraidConfig(testPoolConfig())
	if err != nil {
		t.Fatalf("GenerateSnapraidConfig returned error: %v", err)
	}

	wantLines := []string{
		"parity /mnt/parity1/snapraid.parity",
		"content /mnt/disk1/snapraid.content",
		"content /boot/config/snapraid/snapraid.content",
		"data d1 /mnt/disk1",
		"data d2 /mnt/disk2",
		"data d3 /mnt/disk3",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("expected generated config to contain %q, got:\n%s", want, out)
		}
	}
}

func TestGenerateSnapraidConfig_RejectsInvalidPool(t *testing.T) {
	bad := PoolConfig{Name: "broken"} // 沒有 data/parity disk
	if _, err := GenerateSnapraidConfig(bad); err == nil {
		t.Fatal("expected error generating config for invalid pool, got nil")
	}
}

func TestRunSnapraid_BuildsExpectedCommand(t *testing.T) {
	r := &fakeRunner{output: map[string][]byte{"snapraid": []byte("no differences\n")}}

	out, err := RunSnapraid(context.Background(), r, "/etc/gonas/snapraid-tank.conf", SnapraidDiff)
	if err != nil {
		t.Fatalf("RunSnapraid returned error: %v", err)
	}
	if string(out) != "no differences\n" {
		t.Errorf("unexpected output: %q", out)
	}
	if len(r.calls) != 1 || r.calls[0] != "snapraid" {
		t.Errorf("expected exactly one call to snapraid, got %v", r.calls)
	}
}

func TestRunSnapraid_PropagatesFailure(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"snapraid": errBoom}}
	if _, err := RunSnapraid(context.Background(), r, "/etc/gonas/snapraid-tank.conf", SnapraidSync); err == nil {
		t.Fatal("expected error to propagate from failed snapraid sync")
	}
}
