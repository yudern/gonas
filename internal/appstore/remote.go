package appstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// maxCatalogBytes 限制遠端目錄回應的大小。一份目錄就算有好幾百個範本,
	// 純 JSON 也遠不到 1 MiB —— 設上限是為了擋「遠端(可能被入侵或 MITM)
	// 餵一個無限大的 body 把 daemon 記憶體吃爆」這種 fail-closed 保護,
	// 跟 selfupdate.FetchManifest 的 maxManifestBytes 同一種考量。
	maxCatalogBytes = 1 << 20 // 1 MiB
	// maxCatalogApps 限制一份目錄最多幾個範本,擋住「合法大小但塞滿幾萬個
	// 微小範本」把前端/安裝流程拖垮的情況。
	maxCatalogApps = 1000
)

// FetchCatalog 從一個遠端網址抓 GoNAS App 目錄 —— 一個 AppTemplate 的 JSON
// 陣列,格式跟內建目錄(catalog.go)、跟「自訂安裝」收的 template 完全一樣。
// 這讓「擴充 App 商店」不需要改任何安裝邏輯:遠端目錄只是多一批可選的範本,
// 最後都餵給同一組 Install/Uninstall。
//
// 安全性:遠端目錄只是「建議」—— 它能宣告 image/埠/掛載/環境變數,但每一次
// 安裝仍然要使用者在 Web UI 上明確確認、填值、選掛載路徑才會真的拉映像建容器。
// 即使如此,這裡仍然 fail-closed 地層層設限:只接受 http/https(擋掉 file://
// 這類本機 scheme)、限制回應大小與範本數量、而且「每一個範本各自 Validate,
// 不合法的那一個被跳過(連同原因回報),不會讓整份目錄作廢」—— 一個手殘打錯
// 的範本不該讓其他正常的範本也裝不了。回傳 (valid 範本, 被跳過的原因清單, err)。
// 只有「連不上/狀態碼不對/整份 JSON 根本不是陣列/超出大小上限」這種整體性
// 錯誤才回 err;個別範本的問題走 skipped。
func FetchCatalog(ctx context.Context, client *http.Client, rawURL string) ([]AppTemplate, []string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, nil, fmt.Errorf("app catalog: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil, fmt.Errorf("app catalog: url must start with http:// or https://, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, nil, fmt.Errorf("app catalog: url is missing a host")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("app catalog: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("app catalog: fetching %s: %w", u.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("app catalog: server returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("app catalog: reading response: %w", err)
	}
	if len(body) > maxCatalogBytes {
		return nil, nil, fmt.Errorf("app catalog: response exceeds the %d byte limit", maxCatalogBytes)
	}

	var raw []AppTemplate
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("app catalog: response is not a JSON array of app templates: %w", err)
	}
	if len(raw) > maxCatalogApps {
		return nil, nil, fmt.Errorf("app catalog: lists %d apps, over the %d limit", len(raw), maxCatalogApps)
	}

	valid := make([]AppTemplate, 0, len(raw))
	var skipped []string
	seen := make(map[string]bool, len(raw))
	for _, tmpl := range raw {
		if err := tmpl.Validate(); err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		if seen[tmpl.ID] {
			// 同一份遠端目錄裡 ID 重複:留第一個,跳過其餘 —— ID 同時是容器/
			// 網路命名空間,不能有兩個同 ID 的範本。
			skipped = append(skipped, fmt.Sprintf("app %q: duplicate id within the catalog, keeping the first", tmpl.ID))
			continue
		}
		seen[tmpl.ID] = true
		valid = append(valid, tmpl)
	}
	return valid, skipped, nil
}
