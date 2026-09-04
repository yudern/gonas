package storage

import (
	"context"
	"encoding/json"
	"fmt"
)

// Disk 代表一顆實體區塊裝置(非分割區)。SizeBytes / Rotational 這些
// 欄位是為了之後 Web UI 要顯示「這顆是 HDD 還是 SSD、多大容量」而準備的。
type Disk struct {
	Name       string `json:"name"` // 例如 "sda"
	Path       string `json:"path"` // 例如 "/dev/sda"
	SizeBytes  int64  `json:"sizeBytes"`
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	FSType     string `json:"fsType,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Rotational bool   `json:"rotational"` // true=傳統硬碟, false=SSD/NVMe
}

// lsblkOutput 對應 `lsblk -J` 的最外層 JSON 結構。
type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

// lsblkDevice 只取我們用得到的欄位;lsblk 版本間欄位差異用
// `omitempty`/指標式的容錯處理,避免舊版 lsblk 缺欄位就整個解析失敗。
type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Type       string        `json:"type"`
	Size       json.Number   `json:"size"`
	Model      string        `json:"model"`
	Serial     string        `json:"serial"`
	FSType     string        `json:"fstype"`
	Mountpoint string        `json:"mountpoint"`
	Rota       bool          `json:"rota"`
	Children   []lsblkDevice `json:"children,omitempty"`
}

// DiscoverDisks 探測系統上所有「整顆」區塊裝置(type=="disk"),
// 不含分割區。之後 Storage Manager 會用 Path 當作 pool 設定裡
// data/parity disk 的識別依據。
func DiscoverDisks(ctx context.Context, r Runner) ([]Disk, error) {
	out, err := r.Run(ctx, "lsblk",
		"-J", // JSON 輸出
		"-b", // 容量以 byte 為單位,不要 lsblk 自己換算單位
		"-o", "NAME,PATH,TYPE,SIZE,MODEL,SERIAL,FSTYPE,MOUNTPOINT,ROTA",
	)
	if err != nil {
		return nil, fmt.Errorf("lsblk failed: %w", err)
	}

	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	disks := make([]Disk, 0, len(parsed.BlockDevices))
	for _, dev := range parsed.BlockDevices {
		if dev.Type != "disk" {
			continue
		}
		size, _ := dev.Size.Int64()
		disks = append(disks, Disk{
			Name:       dev.Name,
			Path:       devicePath(dev),
			SizeBytes:  size,
			Model:      dev.Model,
			Serial:     dev.Serial,
			FSType:     dev.FSType,
			Mountpoint: dev.Mountpoint,
			Rotational: dev.Rota,
		})
	}
	return disks, nil
}

func devicePath(dev lsblkDevice) string {
	if dev.Path != "" {
		return dev.Path
	}
	return "/dev/" + dev.Name
}
