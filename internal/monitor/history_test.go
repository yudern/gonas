package monitor

import (
	"testing"
	"time"
)

func TestHistory_Add_RespectsCapacity(t *testing.T) {
	h := NewHistory(3)
	base := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h.Add(Snapshot{Timestamp: base.Add(time.Duration(i) * time.Second), CPUPercent: float64(i)})
	}

	got := h.Snapshot()
	if len(got) != 3 {
		t.Fatalf("expected 3 retained samples, got %d: %+v", len(got), got)
	}
	// 應該保留最新的 3 筆(index 2,3,4),最舊的 2 筆被丟掉。
	wantCPU := []float64{2, 3, 4}
	for i, snap := range got {
		if snap.CPUPercent != wantCPU[i] {
			t.Errorf("sample %d: CPUPercent = %v, want %v", i, snap.CPUPercent, wantCPU[i])
		}
	}
}

func TestHistory_NewHistory_NonPositiveCapacityDefaultsToOne(t *testing.T) {
	h := NewHistory(0)
	h.Add(Snapshot{CPUPercent: 1})
	h.Add(Snapshot{CPUPercent: 2})

	got := h.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected capacity to default to 1, got %d samples", len(got))
	}
	if got[0].CPUPercent != 2 {
		t.Errorf("expected only the latest sample retained, got %+v", got[0])
	}
}

func TestHistory_Latest_EmptyReturnsFalse(t *testing.T) {
	h := NewHistory(5)
	_, ok := h.Latest()
	if ok {
		t.Error("expected Latest() to report ok=false on an empty History")
	}
}

func TestHistory_Latest_ReturnsMostRecent(t *testing.T) {
	h := NewHistory(5)
	h.Add(Snapshot{CPUPercent: 10})
	h.Add(Snapshot{CPUPercent: 20})

	latest, ok := h.Latest()
	if !ok {
		t.Fatal("expected ok=true after adding samples")
	}
	if latest.CPUPercent != 20 {
		t.Errorf("Latest() CPUPercent = %v, want 20", latest.CPUPercent)
	}
}

func TestHistory_Snapshot_ReturnsIndependentCopy(t *testing.T) {
	h := NewHistory(5)
	h.Add(Snapshot{CPUPercent: 1})

	snap := h.Snapshot()
	snap[0].CPUPercent = 999 // 修改回傳的副本不該影響 History 內部狀態

	again := h.Snapshot()
	if again[0].CPUPercent != 1 {
		t.Errorf("expected History internal state unaffected by mutating a returned Snapshot slice, got %v", again[0].CPUPercent)
	}
}
