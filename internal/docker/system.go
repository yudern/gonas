package docker

import (
	"context"
	"fmt"
	"io"
)

// Ping 確認 Docker daemon 有在監聽、socket 可以連得上。GoNAS 啟動時可以先
// Ping 一次，Ping 不到就在 Web UI 上顯示清楚的「Docker 未安裝或未啟動」,
// 而不是讓每個 App 商店操作都各自丟出一個難懂的連線錯誤。
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, "GET", "/_ping", nil)
	if err != nil {
		return fmt.Errorf("pinging docker daemon: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("docker daemon ping returned unexpected status %s", resp.Status)
	}
	return nil
}

// VersionInfo 是 `GET /version` 的精簡摘要。
type VersionInfo struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	Os         string `json:"Os"`
	Arch       string `json:"Arch"`
}

// Version 查詢 Docker daemon 的版本資訊，用來在 Web UI 的系統資訊頁顯示,
// 也可以在未來排錯時判斷「這台機器上的 Docker 版本是不是太舊」。
func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	var out VersionInfo
	if err := c.doJSON(ctx, "GET", "/version", nil, &out); err != nil {
		return VersionInfo{}, fmt.Errorf("getting docker version: %w", err)
	}
	return out, nil
}
