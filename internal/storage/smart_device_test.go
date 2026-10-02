package storage

import (
	"context"
	"errors"
	"testing"
)

// devResolveRunner 模擬 findmnt / lsblk 的輸出,驗證掛載點 → 整碟裝置的解析。
type devResolveRunner struct {
	findmntOut string
	findmntErr error
	pkname     string // lsblk PKNAME 的輸出(空字串=來源本身就是整碟)
	pknameErr  error
}

func (r devResolveRunner) Run(_ context.Context, name string, _ ...string) ([]byte, error) {
	switch name {
	case "findmnt":
		return []byte(r.findmntOut), r.findmntErr
	case "lsblk":
		return []byte(r.pkname), r.pknameErr
	default:
		return nil, errors.New("unexpected command: " + name)
	}
}
func (r devResolveRunner) RunWithStdin(_ context.Context, _ []byte, name string, _ ...string) ([]byte, error) {
	return r.Run(context.Background(), name)
}

func TestDeviceForSmart_PassesThroughDeviceNode(t *testing.T) {
	// 已經是 /dev/... 的不經 findmnt/lsblk,原樣回傳。
	got, err := DeviceForSmart(context.Background(), devResolveRunner{}, "/dev/sdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sdb" {
		t.Errorf("expected passthrough /dev/sdb, got %q", got)
	}
}

func TestDeviceForSmart_ResolvesMountToWholeDisk(t *testing.T) {
	// 掛載點 → 分割區 /dev/sda1 → 整碟 /dev/sda。
	r := devResolveRunner{findmntOut: "/dev/sda1\n", pkname: "sda\n"}
	got, err := DeviceForSmart(context.Background(), r, "/mnt/disk1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sda" {
		t.Errorf("expected /dev/sda, got %q", got)
	}
}

func TestDeviceForSmart_SourceIsWholeDiskNoPkname(t *testing.T) {
	// 整碟直接掛載(沒有分割區),PKNAME 空 → 用 source 本身。
	r := devResolveRunner{findmntOut: "/dev/sdc\n", pkname: ""}
	got, err := DeviceForSmart(context.Background(), r, "/mnt/diskc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sdc" {
		t.Errorf("expected /dev/sdc, got %q", got)
	}
}

func TestDeviceForSmart_StripsBtrfsSubvolSuffix(t *testing.T) {
	// findmnt 對 btrfs 可能回 "/dev/sda1[/@subvol]";只取裝置本體。
	r := devResolveRunner{findmntOut: "/dev/sda1[/@data]\n", pkname: "sda\n"}
	got, err := DeviceForSmart(context.Background(), r, "/mnt/btr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sda" {
		t.Errorf("expected /dev/sda after stripping subvol, got %q", got)
	}
}

func TestDeviceForSmart_FindmntFailsIsError(t *testing.T) {
	r := devResolveRunner{findmntErr: errors.New("findmnt: not a mount point")}
	if _, err := DeviceForSmart(context.Background(), r, "/mnt/nope"); err == nil {
		t.Error("expected an error when findmnt can't resolve the mount")
	}
}
