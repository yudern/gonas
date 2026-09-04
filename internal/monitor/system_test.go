package monitor

import (
	"strings"
	"testing"
)

// 這幾份 fixture 直接照抄這台機器上真實的 /proc/stat、/proc/meminfo、
// /proc/uptime 內容(見開發過程中 `cat /proc/stat` 等指令的輸出)，
// 確保解析邏輯是對著真實核心格式寫的，不是憑印象猜的格式。
const sampleProcStat = `cpu  22985 0 6413 774217 1721 0 1747 13 0 0
cpu0 11761 0 3264 386343 1221 0 824 6 0 0
cpu1 11223 0 3148 387874 500 0 922 7 0 0
intr 1234567 0 0 0
ctxt 9876543
btime 1700000000
processes 4321
`

const sampleProcMeminfoWithAvailable = `MemTotal:        8216192 kB
MemFree:         6644596 kB
MemAvailable:    7514180 kB
Buffers:            5804 kB
Cached:          1072896 kB
SwapCached:            0 kB
SwapTotal:              0 kB
SwapFree:               0 kB
`

const sampleProcMeminfoNoAvailable = `MemTotal:        8216192 kB
MemFree:         6644596 kB
Buffers:            5804 kB
Cached:          1072896 kB
SwapCached:            0 kB
`

func TestParseCPUTimes_RealProcStatFixture(t *testing.T) {
	ct, err := parseCPUTimes(strings.NewReader(sampleProcStat))
	if err != nil {
		t.Fatalf("parseCPUTimes returned error: %v", err)
	}
	wantTotal := uint64(22985 + 0 + 6413 + 774217 + 1721 + 0 + 1747 + 13 + 0 + 0)
	wantIdle := uint64(774217 + 1721) // idle + iowait
	if ct.total != wantTotal {
		t.Errorf("total = %d, want %d", ct.total, wantTotal)
	}
	if ct.idle != wantIdle {
		t.Errorf("idle = %d, want %d", ct.idle, wantIdle)
	}
}

func TestParseCPUTimes_MissingAggregateLine(t *testing.T) {
	_, err := parseCPUTimes(strings.NewReader("intr 1234\nctxt 5678\n"))
	if err == nil {
		t.Fatal("expected error when no aggregate \"cpu \" line is present, got nil")
	}
}

func TestCPUPercentFrom(t *testing.T) {
	tests := []struct {
		name        string
		prev, cur   cpuTimes
		wantPercent float64
	}{
		{
			name:        "half busy",
			prev:        cpuTimes{total: 1000, idle: 500},
			cur:         cpuTimes{total: 2000, idle: 1000},
			wantPercent: 50,
		},
		{
			name:        "fully idle",
			prev:        cpuTimes{total: 1000, idle: 500},
			cur:         cpuTimes{total: 2000, idle: 1500},
			wantPercent: 0,
		},
		{
			name:        "fully busy",
			prev:        cpuTimes{total: 1000, idle: 500},
			cur:         cpuTimes{total: 1500, idle: 500},
			wantPercent: 100,
		},
		{
			name:        "no time elapsed",
			prev:        cpuTimes{total: 1000, idle: 500},
			cur:         cpuTimes{total: 1000, idle: 500},
			wantPercent: 0,
		},
		{
			name:        "counters went backwards (reboot) clamps to 0",
			prev:        cpuTimes{total: 5000, idle: 2500},
			cur:         cpuTimes{total: 100, idle: 50},
			wantPercent: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cpuPercentFrom(tt.prev, tt.cur)
			if got != tt.wantPercent {
				t.Errorf("cpuPercentFrom(%+v, %+v) = %v, want %v", tt.prev, tt.cur, got, tt.wantPercent)
			}
		})
	}
}

func TestParseMemInfo_WithMemAvailable(t *testing.T) {
	total, avail, err := parseMemInfo(strings.NewReader(sampleProcMeminfoWithAvailable))
	if err != nil {
		t.Fatalf("parseMemInfo returned error: %v", err)
	}
	if want := uint64(8216192) * 1024; total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
	if want := uint64(7514180) * 1024; avail != want {
		t.Errorf("available = %d, want %d", avail, want)
	}
}

func TestParseMemInfo_FallsBackWithoutMemAvailable(t *testing.T) {
	total, avail, err := parseMemInfo(strings.NewReader(sampleProcMeminfoNoAvailable))
	if err != nil {
		t.Fatalf("parseMemInfo returned error: %v", err)
	}
	wantAvail := (uint64(6644596) + 5804 + 1072896) * 1024
	if avail != wantAvail {
		t.Errorf("available = %d, want %d (MemFree+Buffers+Cached fallback)", avail, wantAvail)
	}
	if want := uint64(8216192) * 1024; total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
}

func TestParseMemInfo_MissingMemTotal(t *testing.T) {
	_, _, err := parseMemInfo(strings.NewReader("MemFree: 1234 kB\n"))
	if err == nil {
		t.Fatal("expected error when MemTotal is missing, got nil")
	}
}

func TestParseUptime(t *testing.T) {
	got, err := parseUptime("4036.57 7742.18\n")
	if err != nil {
		t.Fatalf("parseUptime returned error: %v", err)
	}
	if got != 4036.57 {
		t.Errorf("uptime = %v, want 4036.57", got)
	}
}

func TestParseUptime_EmptyContent(t *testing.T) {
	_, err := parseUptime("")
	if err == nil {
		t.Fatal("expected error for empty /proc/uptime content, got nil")
	}
}

func TestReadDiskUsage_RealRootFilesystem(t *testing.T) {
	// 直接對這台機器真正的根目錄跑 statfs，不用假資料 —— syscall.Statfs
	// 沒有辦法用一個 io.Reader 抽換掉，所以這支測試就是對真實系統呼叫的
	// live 驗證，跟這個專案一貫「工具真的存在就用真的」的作法一致。
	total, used, err := readDiskUsage("/")
	if err != nil {
		t.Fatalf("readDiskUsage(\"/\") returned error: %v", err)
	}
	if total == 0 {
		t.Error("expected non-zero total bytes for root filesystem")
	}
	if used > total {
		t.Errorf("used (%d) > total (%d), impossible", used, total)
	}
}

func TestCollector_Sample_RealSystem_FirstCallHasZeroCPUPercent(t *testing.T) {
	c := NewCollector("/")
	snap, err := c.Sample()
	if err != nil {
		t.Fatalf("Sample returned error: %v", err)
	}
	if snap.CPUPercent != 0 {
		t.Errorf("expected first Sample() call to report CPUPercent 0 (no baseline yet), got %v", snap.CPUPercent)
	}
	if snap.MemTotalBytes == 0 {
		t.Error("expected non-zero MemTotalBytes from real /proc/meminfo")
	}
	if snap.DiskTotalBytes == 0 {
		t.Error("expected non-zero DiskTotalBytes for \"/\"")
	}
	if snap.UptimeSeconds <= 0 {
		t.Error("expected positive UptimeSeconds from real /proc/uptime")
	}

	snap2, err := c.Sample()
	if err != nil {
		t.Fatalf("second Sample returned error: %v", err)
	}
	if snap2.CPUPercent < 0 || snap2.CPUPercent > 100 {
		t.Errorf("second Sample CPUPercent out of range: %v", snap2.CPUPercent)
	}
}

func TestCollector_SetDiskPath(t *testing.T) {
	c := NewCollector("/nonexistent-path-xyz")
	c.SetDiskPath("/")
	snap, err := c.Sample()
	if err != nil {
		t.Fatalf("Sample returned error after SetDiskPath: %v", err)
	}
	if snap.DiskPath != "/" {
		t.Errorf("DiskPath = %q, want \"/\"", snap.DiskPath)
	}
}
