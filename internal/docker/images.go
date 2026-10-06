package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Image 是 `GET /images/json` 回傳的精簡摘要。
type Image struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Size     int64    `json:"Size"`
	Created  int64    `json:"Created"` // unix timestamp
}

// ListImages 列出本機已有的映像檔，App 商店安裝前可以先檢查「這個 image 是不是已經拉過了」。
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var out []Image
	if err := c.doJSON(ctx, "GET", "/images/json", nil, &out); err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}
	return out, nil
}

// ImageExists 檢查本機是否已經有這個 image(依 repo:tag 比對 RepoTags)。
// App 商店安裝時會先檢查這個，已經存在就不需要再打去 registry —— 這對
// 網路受限的環境(例如企業內網、離線環境，或單純不想每次安裝都重新拉一次
// 巨大映像檔)特別重要,也讓「本機自建的映像檔」可以直接被安裝使用。
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	images, err := c.ListImages(ctx)
	if err != nil {
		return false, fmt.Errorf("checking whether image %q exists locally: %w", ref, err)
	}
	normalizedRef := normalizeRef(ref)
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if normalizeRef(tag) == normalizedRef {
				return true, nil
			}
		}
	}
	return false, nil
}

// RemoveImage 刪除一個映像(依 ID 或 repo:tag)。force=true 時即使還有 tag 或
// 被已停止的容器參照也強制移除。被「執行中」容器使用的映像,Docker 會回 409,
// 錯誤會原樣透傳給呼叫端(讓前端顯示「映像使用中,無法刪除」)。
func (c *Client) RemoveImage(ctx context.Context, ref string, force bool) error {
	path := "/images/" + url.PathEscape(ref)
	if force {
		path += "?force=1"
	}
	if err := c.doJSON(ctx, "DELETE", path, nil, nil); err != nil {
		return fmt.Errorf("removing image %q: %w", ref, err)
	}
	return nil
}

// ImagePruneResult 是清理未使用映像後回報釋放了多少空間。
type ImagePruneResult struct {
	SpaceReclaimed int64 `json:"spaceReclaimed"`
}

// PruneImages 清掉所有「懸空(dangling,沒有任何 tag)」的映像,對應
// `docker image prune`。這是安全的清理——只刪沒 tag、也沒被容器用的中間層,
// 不會動到正在用的映像。回傳釋放的位元組數。
func (c *Client) PruneImages(ctx context.Context) (ImagePruneResult, error) {
	var raw struct {
		SpaceReclaimed int64 `json:"SpaceReclaimed"`
	}
	if err := c.doJSON(ctx, "POST", "/images/prune", nil, &raw); err != nil {
		return ImagePruneResult{}, fmt.Errorf("pruning images: %w", err)
	}
	return ImagePruneResult{SpaceReclaimed: raw.SpaceReclaimed}, nil
}

// normalizeRef 讓 "nginx" 與 "nginx:latest" 被視為同一個參照，比對 RepoTags
// 時才不會因為使用者沒寫 tag 就誤判成「本機沒有」。
func normalizeRef(ref string) string {
	repo, tag := splitImageRef(ref)
	return repo + ":" + tag
}

// pullProgressLine 是 Docker `/images/create` 串流回應中的一行 NDJSON。
// 這支 API 有個容易踩的坑：就算最終失敗，HTTP 狀態碼通常還是 200，錯誤是
// 包在串流某一行的 "error" 欄位裡，不檢查這個欄位就會誤判成功。
type pullProgressLine struct {
	Status   string `json:"status"`
	Error    string `json:"error"`
	Progress string `json:"progress"`
	ID       string `json:"id"`
}

// PullImage 從映像倉庫拉取 image（對應 `docker pull`）。onProgress 是選用的回呼,
// 每收到一行進度就呼叫一次，可以拿去更新 Web UI 上的拉取進度條；傳 nil 表示不關心進度。
func (c *Client) PullImage(ctx context.Context, ref string, onProgress func(status string)) error {
	repo, tag := splitImageRef(ref)
	path := "/images/create?" + url.Values{"fromImage": {repo}, "tag": {tag}}.Encode()

	resp, err := c.doLong(ctx, "POST", path, nil)
	if err != nil {
		return fmt.Errorf("pulling image %q: %w", ref, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Docker 的進度行可能包含長字串（層級 ID、進度條字元畫面），放寬預設的
	// bufio.Scanner 緩衝上限，避免大圖層的一行進度把 scan 弄壞。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var p pullProgressLine
		if err := json.Unmarshal(line, &p); err != nil {
			continue // 忽略解析不了的行，不要因為一行雜訊就整個拉取判定失敗
		}
		if p.Error != "" {
			return fmt.Errorf("pulling image %q: %s", ref, p.Error)
		}
		if onProgress != nil && p.Status != "" {
			onProgress(p.Status)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading pull progress stream for %q: %w", ref, err)
	}
	return nil
}

// splitImageRef 把 "nginx:latest" 拆成 ("nginx", "latest")，沒有指定 tag 時
// 預設 "latest",跟 `docker pull` 的行為一致。不處理帶 digest(@sha256:...)的
// 參照 —— App 商店範本目前只用 repo:tag 這種常見寫法。
func splitImageRef(ref string) (repo, tag string) {
	for i := len(ref) - 1; i >= 0; i-- {
		switch ref[i] {
		case ':':
			return ref[:i], ref[i+1:]
		case '/':
			// 遇到 '/' 還沒看到 ':'，代表沒有 tag（例如 "myregistry.local:5000/app"
			// 這種 registry 帶埠號但 image 本身沒 tag 的情況要小心不要誤判)。
			return ref, "latest"
		}
	}
	return ref, "latest"
}
