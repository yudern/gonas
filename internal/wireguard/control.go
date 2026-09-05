package wireguard

import (
	"context"
	"fmt"

	"github.com/bng147/gonas/internal/cmdrunner"
)

// Up 透過 `wg-quick up <confPath>` 啟用一個 WireGuard 介面。confPath
// 通常是 GenerateConfig 產生的內容寫到磁碟後的路徑;wg-quick 會拿檔名
// (去掉副檔名)當介面名稱,所以呼叫端要自己確保檔名合理,例如
// `/etc/wireguard/wg0.conf` 對應介面 `wg0`。
func Up(ctx context.Context, r cmdrunner.Runner, confPath string) error {
	if _, err := r.Run(ctx, "wg-quick", "up", confPath); err != nil {
		return fmt.Errorf("wg-quick up %s: %w", confPath, err)
	}
	return nil
}

// Down 透過 `wg-quick down <confPath>` 停用一個 WireGuard 介面。
func Down(ctx context.Context, r cmdrunner.Runner, confPath string) error {
	if _, err := r.Run(ctx, "wg-quick", "down", confPath); err != nil {
		return fmt.Errorf("wg-quick down %s: %w", confPath, err)
	}
	return nil
}

// Status 回傳 `wg show <iface>` 的原始輸出。刻意不在這裡把輸出解析成
// 結構化型別 —— `wg show` 是給人讀的純文字格式,不像 JSON 那樣有穩定的
// 欄位保證,GoNAS 目前只需要「介面存不存在、有沒有 handshake 過」這種
// 粗粒度資訊,交給呼叫端(internal/api)自己決定要不要進一步解析,
// 不需要在這裡做一個容易因為 wireguard-tools 版本更新格式微調就壞掉的
// parser。
func Status(ctx context.Context, r cmdrunner.Runner, iface string) (string, error) {
	out, err := r.Run(ctx, "wg", "show", iface)
	if err != nil {
		return "", fmt.Errorf("wg show %s: %w", iface, err)
	}
	return string(out), nil
}
