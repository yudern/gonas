package docker

import (
	"context"
	"fmt"
)

// Volume 是 Docker 具名磁碟區的精簡摘要。
//
// GoNAS 的 App 商店預設鼓勵使用者把資料掛到儲存池底下的實際路徑(bind mount,
// 對應 CreateContainerRequest.Mounts),而不是 Docker 具名磁碟區 —— 這樣資料
// 才會確實落在 SnapRAID 保護得到的陣列裡，而不是藏在 Docker 自己的
// /var/lib/docker/volumes 底下。ListVolumes 主要是給 Web UI 顯示「系統上
// 還有哪些既有磁碟區」，方便使用者遷移舊設定，而不是鼓勵新裝的 App 用它。
type Volume struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
}

type listVolumesResponse struct {
	Volumes  []Volume `json:"Volumes"`
	Warnings []string `json:"Warnings"`
}

// ListVolumes 列出目前的 Docker 具名磁碟區。
func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	var out listVolumesResponse
	if err := c.doJSON(ctx, "GET", "/volumes", nil, &out); err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	return out.Volumes, nil
}
