package storage

import (
	"context"
	"strings"
	"testing"
)

func TestBuildMergerfsArgs(t *testing.T) {
	args := BuildMergerfsArgs(testPoolConfig())

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/mnt/disk1:/mnt/disk2:/mnt/disk3") {
		t.Errorf("expected colon-joined branches in args, got: %v", args)
	}
	if args[len(args)-1] != "/mnt/tank" {
		t.Errorf("expected mount point as last arg, got: %v", args)
	}
	if !strings.Contains(joined, "func.create=mfs") {
		t.Errorf("expected most-free-space create policy in options, got: %v", args)
	}
}

func TestMountPool_RejectsInvalidConfig(t *testing.T) {
	r := &fakeRunner{}
	err := MountPool(context.Background(), r, PoolConfig{Name: "broken"})
	if err == nil {
		t.Fatal("expected error mounting invalid pool config")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected mergerfs not to be invoked for invalid config, got %v", r.calls)
	}
}

func TestMountPool_CallsMergerfs(t *testing.T) {
	r := &fakeRunner{}
	if err := MountPool(context.Background(), r, testPoolConfig()); err != nil {
		t.Fatalf("MountPool returned error: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0] != "mergerfs" {
		t.Errorf("expected exactly one call to mergerfs, got %v", r.calls)
	}
}

func TestUnmountPool_CallsFusermount(t *testing.T) {
	r := &fakeRunner{}
	if err := UnmountPool(context.Background(), r, testPoolConfig()); err != nil {
		t.Fatalf("UnmountPool returned error: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0] != "fusermount" {
		t.Errorf("expected exactly one call to fusermount, got %v", r.calls)
	}
}
