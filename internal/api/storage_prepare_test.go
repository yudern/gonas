package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// prepareRunner 模擬準備硬碟會用到的外部指令,可設定某顆碟是否已掛載。
type prepareRunner struct {
	mounted map[string]string // path -> mountpoint(空=可用)
	ranMkfs bool
}

func (p *prepareRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "lsblk":
		dev := args[len(args)-1]
		return []byte(`{"blockdevices":[{"path":"` + dev + `","type":"disk","mountpoint":"` + p.mounted[dev] + `"}]}`), nil
	case "mkfs.ext4":
		p.ranMkfs = true
		return nil, nil
	case "mkdir", "mount":
		return nil, nil
	case "blkid":
		return []byte("TEST-UUID\n"), nil
	}
	return nil, errors.New("unexpected command: " + name)
}

func (p *prepareRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunWithStdin: " + name)
}

func TestHandleStoragePrepareDisk_OK(t *testing.T) {
	s := newTestServer(t)
	s.runner = &prepareRunner{mounted: map[string]string{"/dev/sdb": ""}}
	s.fstabPath = filepath.Join(t.TempDir(), "fstab")

	body := `{"device":"/dev/sdb","mountPoint":"/mnt/disk1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/disks/prepare", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStoragePrepareDisk(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res storage_prepareResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if res.Mountpoint != "/mnt/disk1" || res.UUID != "TEST-UUID" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestHandleStoragePrepareDisk_RefusesMountedDisk(t *testing.T) {
	s := newTestServer(t)
	pr := &prepareRunner{mounted: map[string]string{"/dev/sda": "/"}} // 系統碟
	s.runner = pr
	s.fstabPath = filepath.Join(t.TempDir(), "fstab")

	body := `{"device":"/dev/sda","mountPoint":"/mnt/disk1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/disks/prepare", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStoragePrepareDisk(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a mounted/system disk, got %d: %s", rec.Code, rec.Body.String())
	}
	if pr.ranMkfs {
		t.Fatal("SAFETY BUG: mkfs ran on a mounted disk via the API")
	}
}

// storage_prepareResult 對應 storage.PrepareResult 的 JSON,避免測試檔
// 直接依賴該型別名稱(只需要驗證回傳欄位)。
type storage_prepareResult struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fsType"`
	UUID       string `json:"uuid"`
}
