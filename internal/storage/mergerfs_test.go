package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMountPool_CreatesMountPoint(第五十五輪覆核 P2):MountPool 要在掛載前
// 把聯合掛載點目錄建出來,否則 mergerfs 會因為目錄不存在而失敗。
func TestMountPool_CreatesMountPoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tank")
	cfg := testPoolConfig()
	cfg.MountPoint = dir
	r := &fakeRunner{} // mergerfs 假裝成功
	if err := MountPool(context.Background(), r, cfg); err != nil {
		t.Fatalf("MountPool returned error: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("expected mount point %q to have been created as a directory, err=%v", dir, err)
	}
}

// TestMountPool_MergerfsNotInstalled(第五十五輪 實機):mergerfs 沒裝時,
// exec 會回「executable file not found in $PATH」;MountPool 要把它翻成
// ErrMergerfsNotInstalled 這個固定、可翻譯的錯誤,而不是原始 exec 錯誤。
func TestMountPool_MergerfsNotInstalled(t *testing.T) {
	r := &fakeRunner{err: map[string]error{
		"mergerfs": errors.New(`exec: "mergerfs": executable file not found in $PATH`),
	}}
	err := MountPool(context.Background(), r, testPoolConfig())
	if !errors.Is(err, ErrMergerfsNotInstalled) {
		t.Fatalf("expected ErrMergerfsNotInstalled, got: %v", err)
	}
}

// 其他 mergerfs 錯誤(不是「找不到執行檔」)仍照舊包成 mount failed。
func TestMountPool_OtherMergerfsErrorNotMisclassified(t *testing.T) {
	r := &fakeRunner{err: map[string]error{
		"mergerfs": errors.New("fuse: device not found, try 'modprobe fuse' first"),
	}}
	err := MountPool(context.Background(), r, testPoolConfig())
	if errors.Is(err, ErrMergerfsNotInstalled) {
		t.Fatalf("a non-missing-binary error must NOT be reported as 'not installed': %v", err)
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}

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
