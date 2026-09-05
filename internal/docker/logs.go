package docker

import (
	"context"
	"fmt"
	"net/url"
)

// ContainerLogsOptions 控制要抓多少、什麼種類的 log。
type ContainerLogsOptions struct {
	// Tail 是要抓最後幾行,留空(或 "all")等同不限制。跟 `docker logs --tail`
	// 語意一致,直接轉送給 Docker Engine API。
	Tail string
	// Timestamps 是否在每一行前面加上時間戳記,方便對照事件發生的時間點。
	Timestamps bool
}

// ContainerLogs 抓一個容器的 stdout/stderr,回傳已經解開多工串流格式的純文字
// (格式細節見 stream.go 的 demuxStream 註解)。這是 Web UI「應用程式」頁面
// 診斷一個容器為什麼一直重啟/沒有正常回應時最直接的入口，等同 `docker logs`。
func (c *Client) ContainerLogs(ctx context.Context, id string, opts ContainerLogsOptions) (string, error) {
	tail := opts.Tail
	if tail == "" {
		tail = "all"
	}
	q := url.Values{
		"stdout": {"1"},
		"stderr": {"1"},
		"tail":   {tail},
	}
	if opts.Timestamps {
		q.Set("timestamps", "1")
	}
	path := "/containers/" + url.PathEscape(id) + "/logs?" + q.Encode()

	resp, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return "", fmt.Errorf("fetching logs for container %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", decodeAPIError(resp)
	}

	out, err := demuxStream(resp.Body)
	if err != nil {
		return out, fmt.Errorf("reading logs for container %s: %w", id, err)
	}
	return out, nil
}
