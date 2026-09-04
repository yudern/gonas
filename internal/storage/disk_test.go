package storage

import (
	"context"
	"errors"
	"testing"
)

// errBoom 是共用的假錯誤,存放在這裡讓同套件下其他 _test.go 檔案也能重複使用。
var errBoom = errors.New("boom")

// fakeRunner 讓測試可以模擬「系統上有哪些硬碟」而不需要真的執行 lsblk。
type fakeRunner struct {
	output map[string][]byte // key: 指令名稱, value: 要回傳的假輸出
	err    map[string]error
	calls  []string // 記錄呼叫過的指令,方便斷言
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.err[name]; ok {
		return nil, err
	}
	return f.output[name], nil
}

func (f *fakeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

const sampleLsblkJSON = `{
  "blockdevices": [
    {"name":"sda","path":"/dev/sda","type":"disk","size":"4000787030016","model":"WDC WD40EFAX","serial":"WD-ABC123","fstype":null,"mountpoint":null,"rota":true,
      "children":[{"name":"sda1","path":"/dev/sda1","type":"part","size":"4000785203200","model":null,"serial":null,"fstype":"xfs","mountpoint":"/mnt/disk1","rota":true}]},
    {"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":"1000204886016","model":"Samsung SSD 980","serial":"S123456","fstype":null,"mountpoint":null,"rota":false},
    {"name":"loop0","path":"/dev/loop0","type":"loop","size":"63488","model":null,"serial":null,"fstype":"squashfs","mountpoint":"/snap/core/1","rota":false}
  ]
}`

func TestDiscoverDisks_FiltersToWholeDisksOnly(t *testing.T) {
	r := &fakeRunner{output: map[string][]byte{"lsblk": []byte(sampleLsblkJSON)}}

	disks, err := DiscoverDisks(context.Background(), r)
	if err != nil {
		t.Fatalf("DiscoverDisks returned error: %v", err)
	}

	// loop0 是 type=loop,不應該出現在結果裡(它是 snap 掛載用的,不是真的硬碟)。
	if len(disks) != 2 {
		t.Fatalf("expected 2 disks (sda, nvme0n1), got %d: %+v", len(disks), disks)
	}

	if disks[0].Path != "/dev/sda" || !disks[0].Rotational {
		t.Errorf("expected sda to be rotational HDD, got %+v", disks[0])
	}
	if disks[0].SizeBytes != 4000787030016 {
		t.Errorf("expected sda size 4000787030016, got %d", disks[0].SizeBytes)
	}

	if disks[1].Path != "/dev/nvme0n1" || disks[1].Rotational {
		t.Errorf("expected nvme0n1 to be non-rotational SSD, got %+v", disks[1])
	}
	if disks[1].Model != "Samsung SSD 980" {
		t.Errorf("expected model 'Samsung SSD 980', got %q", disks[1].Model)
	}
}

func TestDiscoverDisks_LsblkFailure(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"lsblk": errBoom}}

	if _, err := DiscoverDisks(context.Background(), r); err == nil {
		t.Fatal("expected error when lsblk fails, got nil")
	}
}
