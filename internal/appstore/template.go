// Package appstore 是 GoNAS 的「App 商店」：把一份宣告式的 App 範本
// (image、埠、掛載、環境變數……)翻譯成實際的 docker.CreateContainerRequest
// 並串接安裝/解除安裝的生命週期。
//
// 這裡刻意不解析真正的 docker-compose.yml —— 完整 compose 規格涵蓋
// depends_on、healthcheck、build context 等大量語意，對「使用者在 Web UI
// 上填一份表單、按下安裝」這個場景是不必要的重量。取而代之的是一份
// GoNAS 自訂、範圍小很多的 JSON 範本格式，其設計目標是「一對一對應到
// Unraid Community Applications / CasaOS App Store 那種模板」，多容器
// App(例如 *arr 全家桶那種需要 sonarr+radarr+prowlarr 互通)則用
// Services 陣列表示，共用同一個由 GoNAS 建立的專屬 bridge 網路。
package appstore

import "fmt"

// AppTemplate 描述一個可安裝的 App。
type AppTemplate struct {
	ID          string `json:"id"` // 全域唯一識別碼，同時是網路/容器命名空間
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"` // 例如 "media", "downloads", "smart-home"
	Icon        string `json:"icon,omitempty"`

	// Services 至少要有一個。單一服務的 App(多數情況)就只有一個元素;
	// 多容器 App 則列出每個要一起安裝/移除的服務。
	Services []ServiceTemplate `json:"services"`
}

// ServiceTemplate 對應一個容器。命名跟欄位刻意貼近 docker.CreateContainerRequest,
// 減少翻譯層要做的事，但保留 Env/Ports/Volumes 的「範本 vs. 使用者填的值」語意分離。
type ServiceTemplate struct {
	Name    string   `json:"name"` // 在 App 內的服務名稱，例如 "app" 或 "db"
	Image   string   `json:"image"`
	Command []string `json:"command,omitempty"`

	Env     []EnvVar        `json:"env,omitempty"`
	Ports   []PortMapping   `json:"ports,omitempty"`
	Volumes []VolumeMapping `json:"volumes,omitempty"`

	RestartPolicy string `json:"restartPolicy,omitempty"` // 留空時套用 docker 套件的預設 "unless-stopped"
}

// EnvVar 描述一個環境變數「洞」，Default 有值時使用者可以不填,
// Required=true 且沒有 Default 時,Web UI 必須擋下安裝直到使用者填值。
type EnvVar struct {
	Key         string `json:"key"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PortMapping 是範本建議的埠對應；HostPort 給預設值，使用者在安裝時通常可以改
// (避免多個 App 搶同一個埠),Web UI 負責做那層互動，這裡只帶預設建議值。
type PortMapping struct {
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Protocol      string `json:"protocol,omitempty"` // "tcp"（預設）或 "udp"
}

// VolumeMapping 描述一個掛載。HostPath 留空代表「安裝精靈必須讓使用者選一個
// 陣列上的路徑」，這是刻意不給預設值 —— 資料要落在哪顆碟、哪個 pool，
// 不該由 App 範本作者幫使用者決定。
type VolumeMapping struct {
	ContainerPath string `json:"containerPath"`
	HostPath      string `json:"hostPath,omitempty"`
	ReadOnly      bool   `json:"readOnly,omitempty"`
}

// Validate 檢查範本本身的結構完整性（不是檢查使用者填的值，那是 ResolveEnv 的工作）。
func (t AppTemplate) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("app template: id is required")
	}
	if t.Name == "" {
		return fmt.Errorf("app template %q: name is required", t.ID)
	}
	if len(t.Services) == 0 {
		return fmt.Errorf("app template %q: at least one service is required", t.ID)
	}

	seenNames := make(map[string]bool, len(t.Services))
	for _, svc := range t.Services {
		if svc.Name == "" {
			return fmt.Errorf("app template %q: service name cannot be empty", t.ID)
		}
		if seenNames[svc.Name] {
			return fmt.Errorf("app template %q: duplicate service name %q", t.ID, svc.Name)
		}
		seenNames[svc.Name] = true

		if svc.Image == "" {
			return fmt.Errorf("app template %q: service %q has no image", t.ID, svc.Name)
		}
		for _, e := range svc.Env {
			if e.Key == "" {
				return fmt.Errorf("app template %q: service %q has an env var with an empty key", t.ID, svc.Name)
			}
			if e.Required && e.Default != "" {
				return fmt.Errorf("app template %q: service %q env %q cannot be both required and have a default", t.ID, svc.Name, e.Key)
			}
		}
	}
	return nil
}

// ResolveEnv 把範本宣告的環境變數與使用者填的值合併，回傳最終要傳給容器的
// "KEY=VALUE" 清單。userValues 只需要填「使用者真的想覆蓋或必填」的 key,
// 其餘用 Default。缺少必填值時回傳明確錯誤，而不是靜默用空字串帶過。
func ResolveEnv(svc ServiceTemplate, userValues map[string]string) ([]string, error) {
	resolved := make([]string, 0, len(svc.Env))
	for _, e := range svc.Env {
		val, provided := userValues[e.Key]
		if !provided {
			val = e.Default
		}
		if val == "" && e.Required {
			return nil, fmt.Errorf("missing required value for %q", e.Key)
		}
		resolved = append(resolved, e.Key+"="+val)
	}
	return resolved, nil
}
