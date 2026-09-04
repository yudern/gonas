package storage

import (
	"context"
	"testing"
)

func TestArray_StartStop_HappyPath(t *testing.T) {
	r := &fakeRunner{}
	a := NewArray(testPoolConfig())

	if got := a.Status().State; got != StateStopped {
		t.Fatalf("expected initial state stopped, got %s", got)
	}

	if err := a.Start(context.Background(), r); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if got := a.Status().State; got != StateStarted {
		t.Fatalf("expected state started after Start, got %s", got)
	}

	// 重複呼叫 Start 應該是冪等的，不應該回錯誤或重新掛載。
	if err := a.Start(context.Background(), r); err != nil {
		t.Fatalf("second Start should be a no-op, got error: %v", err)
	}

	if err := a.Stop(context.Background(), r); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if got := a.Status().State; got != StateStopped {
		t.Fatalf("expected state stopped after Stop, got %s", got)
	}
}

func TestArray_Start_InvalidConfigGoesToFailed(t *testing.T) {
	r := &fakeRunner{}
	a := NewArray(PoolConfig{Name: "broken"}) // 缺 data/parity disk

	if err := a.Start(context.Background(), r); err == nil {
		t.Fatal("expected error starting array with invalid config")
	}
	status := a.Status()
	if status.State != StateFailed {
		t.Errorf("expected state failed, got %s", status.State)
	}
	if status.Error == "" {
		t.Error("expected Status().Error to be populated")
	}

	// mergerfs 完全不該被呼叫到，因為在掛載之前設定就沒通過驗證。
	if len(r.calls) != 0 {
		t.Errorf("expected no external commands run for invalid config, got %v", r.calls)
	}
}

func TestArray_Start_MergerfsFailureGoesToFailed(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"mergerfs": errBoom}}
	a := NewArray(testPoolConfig())

	if err := a.Start(context.Background(), r); err == nil {
		t.Fatal("expected error when mergerfs mount fails")
	}
	if got := a.Status().State; got != StateFailed {
		t.Fatalf("expected state failed, got %s", got)
	}
}

func TestArray_Stop_WhenAlreadyStopped_IsNoop(t *testing.T) {
	r := &fakeRunner{}
	a := NewArray(testPoolConfig())

	if err := a.Stop(context.Background(), r); err != nil {
		t.Fatalf("Stop on an already-stopped array should be a no-op, got error: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no external commands run, got %v", r.calls)
	}
}
