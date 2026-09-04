package docker

import (
	"context"
	"fmt"
	"net/url"
)

// Network 是 `GET /networks` 回傳的精簡摘要。
type Network struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	Scope  string `json:"Scope"`
}

// ListNetworks 列出目前的 Docker 網路。
func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	var out []Network
	if err := c.doJSON(ctx, "GET", "/networks", nil, &out); err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}
	return out, nil
}

type createNetworkBody struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels,omitempty"`
}

type createNetworkResponse struct {
	ID string `json:"Id"`
}

// EnsureAppNetwork 建立一個給多容器 App 用的專屬 bridge 網路，網路名稱與
// App 商店的 app ID 綁定，方便同一個 App 底下的容器互相用容器名稱通訊,
// 也方便 Uninstall 時能準確找到要清掉的網路。若同名網路已存在就直接回傳其 ID,
// 不視為錯誤 —— 重新安裝/修復 App 時常見會重跑一次 Install。
func (c *Client) EnsureAppNetwork(ctx context.Context, appID string) (networkID string, err error) {
	existing, err := c.ListNetworks(ctx)
	if err != nil {
		return "", fmt.Errorf("checking for existing app network: %w", err)
	}
	name := AppNetworkName(appID)
	for _, n := range existing {
		if n.Name == name {
			return n.ID, nil
		}
	}

	body := createNetworkBody{
		Name:   name,
		Driver: "bridge",
		Labels: map[string]string{"com.gonas.app": appID},
	}
	var resp createNetworkResponse
	if err := c.doJSON(ctx, "POST", "/networks/create", body, &resp); err != nil {
		return "", fmt.Errorf("creating network for app %q: %w", appID, err)
	}
	return resp.ID, nil
}

// RemoveNetwork 移除一個網路（App 商店 Uninstall 用），網路名稱透過 id 或 name 皆可,
// 對應 Docker API 本身的行為。
func (c *Client) RemoveNetwork(ctx context.Context, idOrName string) error {
	if err := c.doJSON(ctx, "DELETE", "/networks/"+url.PathEscape(idOrName), nil, nil); err != nil {
		return fmt.Errorf("removing network %q: %w", idOrName, err)
	}
	return nil
}

// AppNetworkName 是 App 商店安裝多容器 App 時建立的專屬網路命名慣例,
// 匯出給 internal/appstore 在 Uninstall 時能算出同一個名稱來清理。
func AppNetworkName(appID string) string {
	return "gonas-app-" + appID
}
