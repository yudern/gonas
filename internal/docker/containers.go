package docker

import (
	"context"
	"fmt"
	"net/url"
)

// Container 是 `GET /containers/json` 回傳的精簡摘要,只取 GoNAS 目前用得到的欄位。
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	Command string            `json:"Command"`
	State   string            `json:"State"`  // running / exited / created / paused ...
	Status  string            `json:"Status"` // 人類可讀，例如 "Up 3 hours"
	Labels  map[string]string `json:"Labels"`
	Ports   []PortBinding     `json:"Ports"`
}

// PortBinding 對應容器目前實際的埠對應狀態(不是設定,是查詢結果)。
type PortBinding struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type"` // "tcp" / "udp"
}

// ListContainers 列出容器。all=false 只回傳執行中的,all=true 連同已停止的也列出
// (Web UI 的「應用程式」頁面需要看到停掉的容器才能重新啟動它們)。
func (c *Client) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	path := "/containers/json"
	if all {
		path += "?all=1"
	}
	var out []Container
	if err := c.doJSON(ctx, "GET", path, nil, &out); err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	return out, nil
}

// PortSpec 是使用者/App 範本要求的埠對應設定(輸入,對應上面查詢用的 PortBinding)。
type PortSpec struct {
	ContainerPort int
	HostPort      int
	Protocol      string // "tcp" 或 "udp"，留空預設 "tcp"
}

// Mount 是一個 bind mount:把主機路徑掛進容器裡。
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// CreateContainerRequest 是建立容器所需的精簡設定，刻意只涵蓋 GoNAS App
// 商店會用到的欄位(image、指令、環境變數、埠、掛載、重啟策略、標籤、網路),
// 不是 Docker Engine API 完整的 ContainerConfig。
type CreateContainerRequest struct {
	Name          string
	Image         string
	Cmd           []string
	Env           []string // "KEY=VALUE" 格式，與 docker run -e 相同慣例
	Ports         []PortSpec
	Mounts        []Mount
	RestartPolicy string // "no" / "on-failure" / "unless-stopped" / "always"
	NetworkMode   string // 留空則用預設 bridge；App 商店的多容器應用會傳自建網路名稱
	Labels        map[string]string
}

// dockerCreateBody 是實際送給 Docker Engine API 的 JSON 結構(駝峰字首大寫是
// Docker API 的慣例，不是我們自己的風格選擇)。
type dockerCreateBody struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   dockerHostConfig    `json:"HostConfig"`
}

type dockerHostConfig struct {
	Binds         []string                 `json:"Binds,omitempty"`
	PortBindings  map[string][]portBinding `json:"PortBindings,omitempty"`
	RestartPolicy dockerRestartPolicy      `json:"RestartPolicy,omitempty"`
	NetworkMode   string                   `json:"NetworkMode,omitempty"`
}

type portBinding struct {
	HostPort string `json:"HostPort"`
}

type dockerRestartPolicy struct {
	Name string `json:"Name,omitempty"`
}

type createContainerResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// CreateContainer 建立一個容器（不會啟動它，呼叫端要另外呼叫 StartContainer,
// 這跟 `docker create` / `docker run` 的差異一致，讓「建立」與「啟動」語意分開,
// 跟 Array.Start 裡「掛載陣列」與「觸發同位校驗」分開是同一個設計原則)。
func (c *Client) CreateContainer(ctx context.Context, req CreateContainerRequest) (id string, warnings []string, err error) {
	body := dockerCreateBody{
		Image:  req.Image,
		Cmd:    req.Cmd,
		Env:    req.Env,
		Labels: req.Labels,
		HostConfig: dockerHostConfig{
			NetworkMode: req.NetworkMode,
			RestartPolicy: dockerRestartPolicy{
				Name: normalizeRestartPolicy(req.RestartPolicy),
			},
		},
	}

	if len(req.Mounts) > 0 {
		body.HostConfig.Binds = make([]string, 0, len(req.Mounts))
		for _, m := range req.Mounts {
			spec := m.HostPath + ":" + m.ContainerPath
			if m.ReadOnly {
				spec += ":ro"
			}
			body.HostConfig.Binds = append(body.HostConfig.Binds, spec)
		}
	}

	if len(req.Ports) > 0 {
		body.ExposedPorts = make(map[string]struct{}, len(req.Ports))
		body.HostConfig.PortBindings = make(map[string][]portBinding, len(req.Ports))
		for _, p := range req.Ports {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			key := fmt.Sprintf("%d/%s", p.ContainerPort, proto)
			body.ExposedPorts[key] = struct{}{}
			body.HostConfig.PortBindings[key] = []portBinding{{HostPort: fmt.Sprintf("%d", p.HostPort)}}
		}
	}

	path := "/containers/create"
	if req.Name != "" {
		path += "?" + url.Values{"name": {req.Name}}.Encode()
	}

	var resp createContainerResponse
	if err := c.doJSON(ctx, "POST", path, body, &resp); err != nil {
		return "", nil, fmt.Errorf("creating container %q from image %q: %w", req.Name, req.Image, err)
	}
	return resp.ID, resp.Warnings, nil
}

func normalizeRestartPolicy(p string) string {
	switch p {
	case "", "no", "on-failure", "unless-stopped", "always":
		if p == "" {
			return "unless-stopped" // NAS 常駐服務的合理預設，而不是 Docker 原生預設的 "no"
		}
		return p
	default:
		return "unless-stopped"
	}
}

// StartContainer 啟動一個已建立的容器。
func (c *Client) StartContainer(ctx context.Context, id string) error {
	if err := c.doJSON(ctx, "POST", "/containers/"+id+"/start", nil, nil); err != nil {
		return fmt.Errorf("starting container %s: %w", id, err)
	}
	return nil
}

// StopContainer 停止一個執行中的容器，timeoutSec 是給容器優雅關閉的時間，
// 超過就強制 kill(對應 `docker stop -t`)。
func (c *Client) StopContainer(ctx context.Context, id string, timeoutSec int) error {
	path := fmt.Sprintf("/containers/%s/stop?t=%d", id, timeoutSec)
	if err := c.doJSON(ctx, "POST", path, nil, nil); err != nil {
		return fmt.Errorf("stopping container %s: %w", id, err)
	}
	return nil
}

// RestartContainer 重啟一個容器(對應 `docker restart -t`)。timeoutSec 是
// 停止階段給容器優雅關閉的秒數,超過就強制 kill,然後再啟動。對「改了設定、
// 或容器卡住了想重來一次」很常用,所以 Web「應用程式」頁面每個服務都有這個
// 按鈕。
func (c *Client) RestartContainer(ctx context.Context, id string, timeoutSec int) error {
	path := fmt.Sprintf("/containers/%s/restart?t=%d", id, timeoutSec)
	if err := c.doJSON(ctx, "POST", path, nil, nil); err != nil {
		return fmt.Errorf("restarting container %s: %w", id, err)
	}
	return nil
}

// RemoveContainer 刪除一個容器。force=true 時即使還在跑也會被強制移除,
// App 商店的「解除安裝」會需要，但 API 呼叫端要清楚知道自己要求的是強制移除。
func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	path := "/containers/" + id
	if force {
		path += "?force=1"
	}
	if err := c.doJSON(ctx, "DELETE", path, nil, nil); err != nil {
		return fmt.Errorf("removing container %s: %w", id, err)
	}
	return nil
}

// ContainerInspect 是 `GET /containers/{id}/json` 的精簡結果，用來查詢單一容器的
// 詳細執行狀態(例如 Install 完成後確認真的啟動成功、退出碼是多少)。
type ContainerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
		Error    string `json:"Error"`
	} `json:"State"`
}

// InspectContainer 查詢單一容器的詳細狀態。
func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerInspect, error) {
	var out ContainerInspect
	if err := c.doJSON(ctx, "GET", "/containers/"+id+"/json", nil, &out); err != nil {
		return ContainerInspect{}, fmt.Errorf("inspecting container %s: %w", id, err)
	}
	return out, nil
}
