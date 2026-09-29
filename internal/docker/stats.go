package docker

import (
	"context"
	"fmt"
	"net/url"
)

// ContainerStats 是一次性(stream=false)容器資源用量的精簡結果:CPU 百分比、
// 記憶體用量/上限。給 Web「應用程式」頁面每個容器顯示即時負載用——這是
// Portainer/Unraid Docker 分頁的基本期待(第六十輪產品覆核)。
type ContainerStats struct {
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes uint64  `json:"memoryBytes"`
	MemoryLimit uint64  `json:"memoryLimit"`
	MemPercent  float64 `json:"memPercent"`
}

// dockerStatsRaw 對應 Docker Engine API `GET /containers/{id}/stats?stream=false`
// 回應裡我們用得到的欄位。CPU% 的算法沿用 docker CLI 的官方公式:
// (cpu_delta / system_delta) * online_cpus * 100。
type dockerStatsRaw struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

// ContainerStats 抓一次容器的資源用量(stream=false,daemon 回一份就結束)。
// stream=false 時 daemon 仍會回「兩個取樣點」讓 precpu 有值可算 delta,所以
// 單次呼叫就能算出有意義的 CPU%。
func (c *Client) ContainerStats(ctx context.Context, id string) (ContainerStats, error) {
	var raw dockerStatsRaw
	if err := c.doJSON(ctx, "GET", "/containers/"+url.PathEscape(id)+"/stats?stream=false", nil, &raw); err != nil {
		return ContainerStats{}, fmt.Errorf("getting stats for container %s: %w", id, err)
	}

	out := ContainerStats{}

	// CPU%:官方公式。任何一個 delta 為非正數(容器剛啟動、還沒有前一取樣點)
	// 就回 0,不要算出負數或除以零。
	cpuDelta := float64(raw.CPUStats.CPUUsage.TotalUsage) - float64(raw.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(raw.CPUStats.SystemCPUUsage) - float64(raw.PreCPUStats.SystemCPUUsage)
	cpus := float64(raw.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = 1
	}
	if cpuDelta > 0 && sysDelta > 0 {
		out.CPUPercent = (cpuDelta / sysDelta) * cpus * 100.0
	}

	// 記憶體:Docker 的 usage 含 page cache,扣掉 cache 才是「實際佔用」,跟
	// docker stats 顯示的一致(cgroup v2 用 inactive_file,v1 用 cache)。
	mem := raw.MemoryStats.Usage
	if cache, ok := raw.MemoryStats.Stats["inactive_file"]; ok && cache <= mem {
		mem -= cache
	} else if cache, ok := raw.MemoryStats.Stats["cache"]; ok && cache <= mem {
		mem -= cache
	}
	out.MemoryBytes = mem
	out.MemoryLimit = raw.MemoryStats.Limit
	if raw.MemoryStats.Limit > 0 {
		out.MemPercent = float64(mem) / float64(raw.MemoryStats.Limit) * 100.0
	}
	return out, nil
}
