package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/storage"
)

// countingSmartRunner 記錄 smartctl 被 fork 了幾次(執行緒安全,因為背景
// SMART 重整 goroutine 會從另一條 goroutine 呼叫它),並可指定某顆碟回報
// FAILED。只認得 smartctl,其他指令一律當成測試寫錯。
type countingSmartRunner struct {
	mu      sync.Mutex
	calls   int
	failDev string // 要回報 FAILED 的裝置;"" 代表全部 PASSED
}

func (r *countingSmartRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "smartctl" {
		return nil, errors.New("unexpected command: " + name)
	}
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	dev := args[len(args)-1]
	result := "PASSED"
	if r.failDev != "" && dev == r.failDev {
		result = "FAILED"
	}
	return []byte("=== START OF READ SMART DATA SECTION ===\n" +
		"SMART overall-health self-assessment test result: " + result + "\n"), nil
}

func (r *countingSmartRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunWithStdin call: " + name)
}

func (r *countingSmartRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// waitSmartSettled 等背景 SMART 重整 goroutine 跑完(smartChecked 被填上、
// smartChecking 歸零),最多等 2 秒。
func waitSmartSettled(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.smartMu.Lock()
		done := !s.smartChecking && !s.smartChecked.IsZero()
		s.smartMu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for background SMART refresh to settle")
}

// TestCachedSmartFailed_DoesNotProbePerCall 固化第三十四輪效能修法的核心:
// 監控輪詢每 10 秒都會問一次「SMART 有沒有故障」,但實際 fork smartctl 的
// 次數必須被 smartCheckInterval 的快取擋住——不管問幾次,一個間隔內只會
// 對每顆碟各跑一次 smartctl。若有人把快取拿掉、退回「每次都 probe」,這個
// 測試會因為呼叫次數暴增而失敗。這正是讓孱弱 ARM CPU 不被 fork 洪流拖垮、
// 讓硬碟能休眠的關鍵不變式。
func TestCachedSmartFailed_DoesNotProbePerCall(t *testing.T) {
	s := newTestServer(t)
	runner := &countingSmartRunner{}
	s.runner = runner

	pool := &storage.PoolConfig{
		DataDisks:   []string{"/dev/sda", "/dev/sdb"},
		ParityDisks: []string{"/dev/sdc"},
	}

	// 模擬輪詢連問 50 次(相當於現實中 ~8 分鐘、每 10 秒一次)。
	for i := 0; i < 50; i++ {
		s.cachedSmartFailed(pool)
	}
	waitSmartSettled(t, s)

	// 一個間隔內只該有一輪重整:3 顆碟 → 剛好 3 次 smartctl,而不是
	// 50 次呼叫 × 3 顆碟 = 150 次。
	if got := runner.count(); got != 3 {
		t.Fatalf("expected exactly 3 smartctl invocations (one refresh over 3 disks), got %d — SMART is being probed too often", got)
	}

	// 全部 PASSED,快取值應為 false。
	if s.cachedSmartFailed(pool) {
		t.Errorf("expected cached SmartFailed=false when every disk passes")
	}

	// 再問幾次,間隔還沒到,smartctl 次數不該再增加。
	for i := 0; i < 10; i++ {
		s.cachedSmartFailed(pool)
	}
	if got := runner.count(); got != 3 {
		t.Errorf("expected still 3 smartctl invocations within the cache interval, got %d", got)
	}
}

// TestCachedSmartFailed_ReportsFailure 確認快取化沒有把「真的抓到故障」這件
// 事弄丟:某顆碟回報 FAILED 時,背景重整跑完後快取值要變成 true。
func TestCachedSmartFailed_ReportsFailure(t *testing.T) {
	s := newTestServer(t)
	runner := &countingSmartRunner{failDev: "/dev/sdb"}
	s.runner = runner

	pool := &storage.PoolConfig{DataDisks: []string{"/dev/sda", "/dev/sdb"}}

	s.cachedSmartFailed(pool) // 觸發背景重整,這次先回傳尚未更新的 false
	waitSmartSettled(t, s)

	if !s.cachedSmartFailed(pool) {
		t.Errorf("expected cached SmartFailed=true after a disk reports FAILED")
	}
}

// TestCachedSmartFailed_NilPool 沒有設定儲存池時不該去碰 smartctl。
func TestCachedSmartFailed_NilPool(t *testing.T) {
	s := newTestServer(t)
	runner := &countingSmartRunner{}
	s.runner = runner

	if s.cachedSmartFailed(nil) {
		t.Errorf("expected SmartFailed=false with no pool configured")
	}
	waitSmartSettled(t, s)
	if got := runner.count(); got != 0 {
		t.Errorf("expected no smartctl invocations with nil pool, got %d", got)
	}
}
