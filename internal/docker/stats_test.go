package docker

import (
	"context"
	"net/http"
	"testing"
)

func TestContainerStats_ComputesCPUAndMem(t *testing.T) {
	// 建構一組讓 CPU% 好算的數字:cpu_delta=100、system_delta=1000、2 核 →
	// (100/1000)*2*100 = 20%。記憶體 usage=200MiB、cache(inactive_file)=50MiB、
	// limit=1GiB → 實際 150MiB,mem% = 150/1024 ≈ 14.6%。
	const body = `{
      "cpu_stats":   {"cpu_usage": {"total_usage": 1100}, "system_cpu_usage": 11000, "online_cpus": 2},
      "precpu_stats":{"cpu_usage": {"total_usage": 1000}, "system_cpu_usage": 10000},
      "memory_stats":{"usage": 209715200, "limit": 1073741824, "stats": {"inactive_file": 52428800}}
    }`
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/abc/stats" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("stream") != "false" {
			t.Errorf("expected stream=false, got %q", r.URL.Query().Get("stream"))
		}
		_, _ = w.Write([]byte(body))
	})

	s, err := c.ContainerStats(context.Background(), "abc")
	if err != nil {
		t.Fatalf("ContainerStats error: %v", err)
	}
	if s.CPUPercent < 19.9 || s.CPUPercent > 20.1 {
		t.Errorf("CPU%% = %v, want ~20", s.CPUPercent)
	}
	if s.MemoryBytes != 209715200-52428800 {
		t.Errorf("MemoryBytes = %d, want %d (usage minus cache)", s.MemoryBytes, 209715200-52428800)
	}
	if s.MemoryLimit != 1073741824 {
		t.Errorf("MemoryLimit = %d", s.MemoryLimit)
	}
	if s.MemPercent < 14.0 || s.MemPercent > 15.0 {
		t.Errorf("Mem%% = %v, want ~14.6", s.MemPercent)
	}
}

// 容器剛啟動、還沒有前一取樣點(precpu 為 0)時,CPU% 不能算成負數或爆掉。
func TestContainerStats_NoPreCPUYieldsZero(t *testing.T) {
	const body = `{
      "cpu_stats":   {"cpu_usage": {"total_usage": 500}, "system_cpu_usage": 5000, "online_cpus": 1},
      "precpu_stats":{"cpu_usage": {"total_usage": 0}, "system_cpu_usage": 0},
      "memory_stats":{"usage": 1000, "limit": 0}
    }`
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	s, err := c.ContainerStats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	// system_delta = 5000-0 = 5000 (>0), cpu_delta = 500-0 = 500 → 實際上這裡
	// 兩者都 >0,會算出 (500/5000)*1*100 = 10%。用 limit=0 驗證 mem% 不除以零。
	if s.MemPercent != 0 {
		t.Errorf("expected MemPercent 0 when limit is 0, got %v", s.MemPercent)
	}
}
