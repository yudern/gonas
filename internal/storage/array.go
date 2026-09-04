package storage

import (
	"context"
	"fmt"
	"sync"
)

// ArrayState 是陣列的生命週期狀態。刻意用明確的中間態(Starting/Stopping)
// 而不是只有 Stopped/Started 兩態,這樣 Web UI 才能在啟動要花好幾秒時
// 顯示進度,而不是讓使用者以為介面卡住了。
type ArrayState string

const (
	StateStopped  ArrayState = "stopped"
	StateStarting ArrayState = "starting"
	StateStarted  ArrayState = "started"
	StateStopping ArrayState = "stopping"
	StateFailed   ArrayState = "failed"
)

// Array 把 PoolConfig 與目前的運行狀態綁在一起,並提供 Start/Stop。
// 一個 GoNAS 系統目前只設計成一個陣列(對應 Unraid 的做法),
// 之後若要支援多個獨立池,可以把這個型別放進一個 map 裡管理。
type Array struct {
	mu      sync.Mutex
	cfg     PoolConfig
	state   ArrayState
	lastErr error
}

// NewArray 建立一個尚未啟動的陣列。cfg 必須先通過 Validate() 才能 Start()。
func NewArray(cfg PoolConfig) *Array {
	return &Array{cfg: cfg, state: StateStopped}
}

// Status 回傳目前狀態的快照,供 API 回應使用。
type Status struct {
	Name    string     `json:"name"`
	State   ArrayState `json:"state"`
	Mounted string     `json:"mountPoint"`
	Error   string     `json:"error,omitempty"`
}

func (a *Array) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Status{Name: a.cfg.Name, State: a.state, Mounted: a.cfg.MountPoint}
	if a.lastErr != nil {
		s.Error = a.lastErr.Error()
	}
	return s
}

// Start 驗證設定、掛載 mergerFS 聯合掛載點,並把狀態切到 started。
// 之後 Phase 5 接上告警系統時,狀態切到 failed 應該要觸發通知 —— 這裡先
// 把 lastErr 記下來,通知的部分留給 Monitor 模組去讀取這個狀態。
//
// 刻意不在這裡呼叫 SnapRAID sync:啟動陣列(讓資料可以讀寫)跟「產生/更新
// 同位保護」是兩件語意不同的事,不應該綁在一起隱含觸發,避免使用者以為
// 陣列一啟動資料就自動被保護了。
func (a *Array) Start(ctx context.Context, r Runner) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state == StateStarted {
		return nil // 已經是啟動狀態，視為冪等成功
	}
	if a.state == StateStarting || a.state == StateStopping {
		return fmt.Errorf("array %q is currently %s, cannot start", a.cfg.Name, a.state)
	}

	if err := a.cfg.Validate(); err != nil {
		a.state = StateFailed
		a.lastErr = err
		return err
	}

	a.state = StateStarting
	a.lastErr = nil

	if err := MountPool(ctx, r, a.cfg); err != nil {
		a.state = StateFailed
		a.lastErr = err
		return err
	}

	a.state = StateStarted
	return nil
}

// Stop 卸載聯合掛載點並把狀態切回 stopped。
func (a *Array) Stop(ctx context.Context, r Runner) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state == StateStopped {
		return nil
	}
	if a.state != StateStarted && a.state != StateFailed {
		return fmt.Errorf("array %q is currently %s, cannot stop", a.cfg.Name, a.state)
	}

	a.state = StateStopping

	if err := UnmountPool(ctx, r, a.cfg); err != nil {
		a.state = StateFailed
		a.lastErr = err
		return err
	}

	a.state = StateStopped
	a.lastErr = nil
	return nil
}
