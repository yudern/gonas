package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// prepFakeRunner 模擬準備硬碟會用到的外部指令。lsblk 的掛載點輸出可設定
// (用來模擬「碟可用」與「碟正被掛載」兩種情況),並記錄跑過哪些指令。
type prepFakeRunner struct {
	mu          sync.Mutex
	calls       []string
	lsblkMounts map[string]string // path -> mountpoint(空=未掛載)
	lsblkType   string            // lsblk 回報的 type;空字串預設 "disk"
	uuid        string
	failMkfs    bool
	busyMounts  map[string]bool // 已經有東西掛在上面的掛載點(模擬 findmnt)
}

func (f *prepFakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	switch name {
	case "lsblk":
		dev := args[len(args)-1]
		mp := f.lsblkMounts[dev]
		typ := f.lsblkType
		if typ == "" {
			typ = "disk"
		}
		return []byte(`{"blockdevices":[{"path":"` + dev + `","type":"` + typ + `","mountpoint":"` + mp + `"}]}`), nil
	case "findmnt":
		target := args[len(args)-1]
		if f.busyMounts[target] {
			return []byte(target + "\n"), nil
		}
		return nil, errors.New("findmnt: not mounted") // 模擬非零退出
	case "mkfs.ext4":
		if f.failMkfs {
			return nil, errors.New("mkfs boom")
		}
		return nil, nil
	case "mkdir", "mount":
		return nil, nil
	case "blkid":
		return []byte(f.uuid + "\n"), nil
	}
	return nil, errors.New("unexpected command: " + name)
}

func (f *prepFakeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunWithStdin: " + name)
}

func (f *prepFakeRunner) ran(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func TestPrepareDisk_HappyPath(t *testing.T) {
	fstab := filepath.Join(t.TempDir(), "fstab")
	r := &prepFakeRunner{lsblkMounts: map[string]string{"/dev/sdb": ""}, uuid: "ABC-123"}

	res, err := PrepareDisk(context.Background(), r, "/dev/sdb", "/mnt/disk1", fstab)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if res.UUID != "ABC-123" || res.Mountpoint != "/mnt/disk1" || res.FSType != "ext4" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !r.ran("mkfs.ext4") || !r.ran("mount") {
		t.Errorf("expected mkfs.ext4 and mount to run; calls=%v", r.calls)
	}
	data, _ := os.ReadFile(fstab)
	if !strings.Contains(string(data), "UUID=ABC-123 /mnt/disk1 ext4") {
		t.Errorf("fstab missing entry, got: %q", string(data))
	}
}

func TestPrepareDisk_RefusesBusyMountpoint(t *testing.T) {
	// 真機測試抓到的坑:第二顆碟想掛到已經有碟的 /mnt/disk1,必須被擋、
	// 而且不能跑到 mkfs(否則就把一顆好碟格式化了卻掛不上去)。
	r := &prepFakeRunner{
		lsblkMounts: map[string]string{"/dev/sdc": ""},
		busyMounts:  map[string]bool{"/mnt/disk1": true},
		uuid:        "x",
	}
	_, err := PrepareDisk(context.Background(), r, "/dev/sdc", "/mnt/disk1", filepath.Join(t.TempDir(), "fstab"))
	if err == nil {
		t.Fatal("expected refusal for an already-used mount point, got nil")
	}
	if r.ran("mkfs.ext4") {
		t.Fatal("SAFETY BUG: mkfs ran even though the mount point was already in use")
	}
}

func TestPrepareDisk_AllowsFreeMountpoint(t *testing.T) {
	// disk1 已被佔用,但這顆要掛到空閒的 disk2 —— 應該正常通過。
	fstab := filepath.Join(t.TempDir(), "fstab")
	r := &prepFakeRunner{
		lsblkMounts: map[string]string{"/dev/sdc": ""},
		busyMounts:  map[string]bool{"/mnt/disk1": true},
		uuid:        "DEF-456",
	}
	res, err := PrepareDisk(context.Background(), r, "/dev/sdc", "/mnt/disk2", fstab)
	if err != nil {
		t.Fatalf("expected success mounting to a free point, got: %v", err)
	}
	if res.Mountpoint != "/mnt/disk2" {
		t.Fatalf("unexpected mountpoint: %+v", res)
	}
}

func TestPrepareDisk_RefusesMountedDisk(t *testing.T) {
	// 系統碟情境:sda 上有分割區掛在 / —— 一定要被擋下、且不能跑到 mkfs。
	r := &prepFakeRunner{lsblkMounts: map[string]string{"/dev/sda": "/"}, uuid: "x"}
	_, err := PrepareDisk(context.Background(), r, "/dev/sda", "/mnt/disk1", filepath.Join(t.TempDir(), "fstab"))
	if err == nil {
		t.Fatal("expected refusal for a mounted/system disk, got nil")
	}
	if r.ran("mkfs.ext4") {
		t.Fatal("SAFETY BUG: mkfs ran on a mounted disk")
	}
}

func TestPrepareDisk_RejectsBadDevice(t *testing.T) {
	// 被 deviceRe 擋下的非法路徑(注入/路徑穿越/空白/無 /dev 前綴)。
	// 每個案例用全新的 runner,避免上一個案例的呼叫紀錄污染 mkfs 檢查。
	bad := []string{
		"/dev/sdb; rm -rf /", // 注入
		"sdb",                // 不是絕對 /dev 路徑
		"/dev/../etc/passwd", // 路徑穿越
		"/dev/sd b",          // 空白
	}
	for _, d := range bad {
		r := &prepFakeRunner{lsblkMounts: map[string]string{}}
		if _, err := PrepareDisk(context.Background(), r, d, "/mnt/disk1", filepath.Join(t.TempDir(), "fstab")); err == nil {
			t.Errorf("expected device %q to be rejected", d)
		}
		if r.ran("mkfs.ext4") {
			t.Fatalf("SAFETY BUG: mkfs ran for bad device %q", d)
		}
	}
}

func TestPrepareDisk_RejectsPartition(t *testing.T) {
	// /dev/sdb1 路徑格式合法,但 lsblk 回報 type=part(分割區,不是整碟)
	// —— 必須被擋下,且不能跑到 mkfs。
	r := &prepFakeRunner{lsblkMounts: map[string]string{"/dev/sdb1": ""}, lsblkType: "part"}
	if _, err := PrepareDisk(context.Background(), r, "/dev/sdb1", "/mnt/disk1", filepath.Join(t.TempDir(), "fstab")); err == nil {
		t.Fatal("expected a partition to be rejected (whole disk required)")
	}
	if r.ran("mkfs.ext4") {
		t.Fatal("SAFETY BUG: mkfs ran on a partition")
	}
}

func TestPrepareDisk_RejectsBadMountpoint(t *testing.T) {
	r := &prepFakeRunner{lsblkMounts: map[string]string{"/dev/sdb": ""}, uuid: "x"}
	bad := []string{
		"/etc/gonas",  // 不在 /mnt 底下
		"/mnt/",       // 空的子路徑
		"/mnt/disk 1", // 空白
		"/mnt/../etc", // ..
		"/mnt/a\nb",   // 換行
	}
	for _, mp := range bad {
		if _, err := PrepareDisk(context.Background(), r, "/dev/sdb", mp, filepath.Join(t.TempDir(), "fstab")); err == nil {
			t.Errorf("expected mount point %q to be rejected", mp)
		}
	}
}

func TestPrepareDisk_FstabIdempotent(t *testing.T) {
	fstab := filepath.Join(t.TempDir(), "fstab")
	r := &prepFakeRunner{lsblkMounts: map[string]string{"/dev/sdb": ""}, uuid: "U1"}
	for i := 0; i < 3; i++ {
		if _, err := PrepareDisk(context.Background(), r, "/dev/sdb", "/mnt/disk1", fstab); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	data, _ := os.ReadFile(fstab)
	if n := strings.Count(string(data), "/mnt/disk1"); n != 1 {
		t.Errorf("expected exactly 1 fstab entry for /mnt/disk1, got %d:\n%s", n, string(data))
	}
}
