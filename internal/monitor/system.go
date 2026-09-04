// Package monitor 是 GoNAS 的系統資源監控與告警層：定期取樣 CPU/記憶體/
// 磁碟使用率、陣列與 SMART 健康狀態，餵給一個簡單的規則引擎，觸發時透過
// 可插拔的 Notifier 送出通知。
//
// 跟其他套件一樣刻意零第三方依賴：不用 gopsutil 之類的套件(module proxy
// 被擋,見 internal/docker 套件註解),直接解析 /proc/stat、/proc/meminfo、
// /proc/uptime 這幾個 Linux 核心一直穩定維護的介面，用 syscall.Statfs
// 算磁碟使用量 —— 這些是 top/free/df 等工具實際的資料來源，不是走捷徑。
package monitor

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Snapshot 是某個時間點的系統資源使用量快照，也是 API /monitor/system 與
// /monitor/history 兩個端點直接回傳的型別。
type Snapshot struct {
	Timestamp      time.Time `json:"timestamp"`
	CPUPercent     float64   `json:"cpuPercent"`
	MemTotalBytes  uint64    `json:"memTotalBytes"`
	MemUsedBytes   uint64    `json:"memUsedBytes"`
	MemPercent     float64   `json:"memPercent"`
	DiskPath       string    `json:"diskPath"`
	DiskTotalBytes uint64    `json:"diskTotalBytes"`
	DiskUsedBytes  uint64    `json:"diskUsedBytes"`
	DiskPercent    float64   `json:"diskPercent"`
	UptimeSeconds  float64   `json:"uptimeSeconds"`
}

// cpuTimes 是從 /proc/stat 第一行(全機加總、不分 core)解析出來的累計
// tick 數，單位是 USER_HZ(常見是 1/100 秒)，不是實際時間 —— 必須跟
// 前一次取樣的差值一起用才有意義，單一次讀數本身沒有意義。
type cpuTimes struct {
	idle  uint64
	total uint64
}

// Collector 持有計算 CPU 使用率所需要的「上一次讀數」，所以同一個
// Collector 要重複呼叫 Sample 才能拿到有意義的 CPUPercent —— 第一次呼叫
// 沒有基準點可以算差值，CPUPercent 會是 0，這點在 doc comment 跟呼叫端
// (poller)都有處理:poller 本來就是週期性呼叫，第一次的 0 值不會被當成
// 真的閒置去觸發告警(告警規則預設有「連續 N 次超標才觸發」的防抖，見
// alerts.go)。
type Collector struct {
	diskPath string

	mu       sync.Mutex
	prevCPU  cpuTimes
	havePrev bool
}

// NewCollector 建立一個 Collector，diskPath 是要回報使用率的掛載點
// (通常是陣列的 mergerFS 掛載點；陣列還沒設定時呼叫端可以先傳 "/"）。
func NewCollector(diskPath string) *Collector {
	return &Collector{diskPath: diskPath}
}

// SetDiskPath 讓呼叫端在陣列設定變更後(例如使用者第一次設定 pool)
// 更新要監控的路徑，不需要整個 Collector 重建 —— 這樣 CPU 使用率的
// 「上一次讀數」基準點不會因此被重置。
func (c *Collector) SetDiskPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diskPath = path
}

// Sample 取一次目前的系統資源快照。
func (c *Collector) Sample() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cpu, err := readCPUTimes()
	if err != nil {
		return Snapshot{}, err
	}
	var cpuPercent float64
	if c.havePrev {
		cpuPercent = cpuPercentFrom(c.prevCPU, cpu)
	}
	c.prevCPU = cpu
	c.havePrev = true

	memTotal, memAvail, err := readMemInfo()
	if err != nil {
		return Snapshot{}, err
	}
	var memUsed uint64
	if memTotal > memAvail {
		memUsed = memTotal - memAvail
	}
	var memPercent float64
	if memTotal > 0 {
		memPercent = float64(memUsed) / float64(memTotal) * 100
	}

	diskPath := c.diskPath
	if diskPath == "" {
		diskPath = "/"
	}
	diskTotal, diskUsed, err := readDiskUsage(diskPath)
	if err != nil {
		return Snapshot{}, err
	}
	var diskPercent float64
	if diskTotal > 0 {
		diskPercent = float64(diskUsed) / float64(diskTotal) * 100
	}

	uptime, err := readUptime()
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Timestamp:      time.Now(),
		CPUPercent:     cpuPercent,
		MemTotalBytes:  memTotal,
		MemUsedBytes:   memUsed,
		MemPercent:     memPercent,
		DiskPath:       diskPath,
		DiskTotalBytes: diskTotal,
		DiskUsedBytes:  diskUsed,
		DiskPercent:    diskPercent,
		UptimeSeconds:  uptime,
	}, nil
}

func readCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, fmt.Errorf("opening /proc/stat: %w", err)
	}
	defer f.Close()
	return parseCPUTimes(f)
}

// parseCPUTimes 只吃 io.Reader，方便單元測試直接餵固定內容的字串，
// 不用真的去讀這台機器的 /proc/stat。
func parseCPUTimes(r io.Reader) (cpuTimes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:] // 去掉開頭的 "cpu" 欄位名稱
		var total, idle uint64
		for i, field := range fields {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("parsing /proc/stat cpu field %d (%q): %w", i, field, err)
			}
			total += v
			// /proc/stat 的欄位順序是 user nice system idle iowait irq
			// softirq steal guest guest_nice；index 3(idle)跟 4(iowait)
			// 都算「沒在做事」，這跟 top/vmstat 算 CPU 使用率的方式一致。
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return cpuTimes{idle: idle, total: total}, nil
	}
	if err := sc.Err(); err != nil {
		return cpuTimes{}, fmt.Errorf("reading /proc/stat: %w", err)
	}
	return cpuTimes{}, fmt.Errorf(`no aggregate "cpu " line found in /proc/stat`)
}

func cpuPercentFrom(prev, cur cpuTimes) float64 {
	if cur.total < prev.total || cur.idle < prev.idle {
		// 計數器理論上單調遞增，唯一會倒退的情況是核心重開機或計數器
		// 溢位重繞，這種情況直接回 0 比硬算一個沒有意義的負數/爆量安全。
		return 0
	}
	deltaTotal := cur.total - prev.total
	deltaIdle := cur.idle - prev.idle
	if deltaTotal == 0 {
		return 0
	}
	pct := (1 - float64(deltaIdle)/float64(deltaTotal)) * 100
	switch {
	case pct < 0:
		return 0
	case pct > 100:
		return 100
	default:
		return pct
	}
}

func readMemInfo() (total, available uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("opening /proc/meminfo: %w", err)
	}
	defer f.Close()
	return parseMemInfo(f)
}

// parseMemInfo 一樣只吃 io.Reader 方便測試。/proc/meminfo 每行格式是
// `Key:            12345 kB`，這裡直接假設單位一律是 kB(核心目前所有
// 版本都是如此),轉成 bytes 方便跟 Snapshot 其他欄位一致。
func parseMemInfo(r io.Reader) (total, available uint64, err error) {
	sc := bufio.NewScanner(r)
	values := make(map[string]uint64)
	for sc.Scan() {
		line := sc.Text()
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB"))
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, convErr := strconv.ParseUint(fields[0], 10, 64)
		if convErr != nil {
			continue
		}
		values[strings.TrimSpace(key)] = v * 1024
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("reading /proc/meminfo: %w", err)
	}

	total, ok := values["MemTotal"]
	if !ok {
		return 0, 0, fmt.Errorf("missing MemTotal in /proc/meminfo")
	}
	if avail, ok := values["MemAvailable"]; ok {
		return total, avail, nil
	}
	// 舊核心(3.14 之前)沒有 MemAvailable，退化用 MemFree+Buffers+Cached
	// 估算「實際可用」的記憶體，這是 free(1) 在沒有 MemAvailable 時的作法。
	return total, values["MemFree"] + values["Buffers"] + values["Cached"], nil
}

func readDiskUsage(path string) (total, used uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := uint64(stat.Bsize) //nolint:unconvert // Bsize 的型別在不同平台(int32/int64)不一樣，統一轉成 uint64
	total = stat.Blocks * bsize
	free := stat.Bfree * bsize
	if total >= free {
		used = total - free
	}
	return total, used, nil
}

func readUptime() (float64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("reading /proc/uptime: %w", err)
	}
	return parseUptime(string(data))
}

func parseUptime(content string) (float64, error) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, fmt.Errorf("unexpected /proc/uptime format: %q", content)
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parsing /proc/uptime: %w", err)
	}
	return v, nil
}
