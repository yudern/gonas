package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// parityRunner 記錄被呼叫過的指令,全部回成功。
type parityRunner struct {
	mu    sync.Mutex
	calls []string
}

func (p *parityRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}
func (p *parityRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, nil
}
func (p *parityRunner) sawSnapraidSync() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.calls {
		if strings.Contains(c, "snapraid") && strings.Contains(c, "sync") {
			return true
		}
	}
	return false
}

func parityPool() *storage.PoolConfig {
	return &storage.PoolConfig{
		Name:         "tank",
		MountPoint:   "/mnt/tank",
		DataDisks:    []string{"/mnt/disk1", "/mnt/disk2"},
		ParityDisks:  []string{"/mnt/parity1"},
		ContentFiles: []string{"/mnt/disk1", "/mnt/disk2"},
	}
}

// 第五十八輪產品 P1 的回歸測試:同步真的會呼叫 `snapraid sync`、寫出設定檔,
// 並在成功後記錄 ParityLastSync(否則 UI 永遠顯示「尚未受保護」)。
func TestHandleStorageArraySync_RunsSyncAndRecordsTime(t *testing.T) {
	s := newTestServer(t)
	pr := &parityRunner{}
	s.runner = pr
	s.snapraidCfgPath = filepath.Join(t.TempDir(), "snapraid.conf")

	if err := s.store.Update(func(st *state.State) error {
		st.Pool = parityPool()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/v1/storage/array/sync", nil)
	rec := httptest.NewRecorder()
	s.handleStorageArraySync(rec, req)
	if rec.Code != 202 {
		t.Fatalf("expected 202 Accepted, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 等背景 goroutine 跑完(single-flight 旗標歸零)。
	deadline := time.Now().Add(5 * time.Second)
	for s.paritySyncing.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.paritySyncing.Load() {
		t.Fatal("parity sync did not finish within the timeout")
	}
	if !pr.sawSnapraidSync() {
		t.Errorf("expected `snapraid ... sync` to be invoked; calls=%v", pr.calls)
	}
	if s.store.Snapshot().ParityLastSync == nil {
		t.Error("expected ParityLastSync to be recorded after a successful sync")
	}
	if _, err := os.Stat(s.snapraidCfgPath); err != nil {
		t.Errorf("expected snapraid.conf to be written at %s: %v", s.snapraidCfgPath, err)
	}
}

// 沒有同位碟的 pool 不該能觸發同步——回 400,而不是啟動一個沒有意義的 sync。
func TestHandleStorageArraySync_NoParityRejected(t *testing.T) {
	s := newTestServer(t)
	s.runner = &parityRunner{}
	s.snapraidCfgPath = filepath.Join(t.TempDir(), "snapraid.conf")

	if err := s.store.Update(func(st *state.State) error {
		st.Pool = &storage.PoolConfig{
			Name: "tank", MountPoint: "/mnt/tank",
			DataDisks: []string{"/mnt/disk1"}, ContentFiles: []string{"/mnt/disk1"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/v1/storage/array/sync", nil)
	rec := httptest.NewRecorder()
	s.handleStorageArraySync(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for a pool with no parity disk, got %d (%s)", rec.Code, rec.Body.String())
	}
}
