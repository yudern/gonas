// Package docker 是 GoNAS 對 Docker Engine 的整合層。
//
// 刻意不依賴官方的 github.com/docker/docker SDK:一來這個開發環境的網路
// 白名單擋掉了 Go module proxy(見 README「已知取捨」),二來 Docker Engine
// API 本身就是一份穩定的 REST 規格(https://docs.docker.com/engine/api/),
// 直接用標準函式庫的 net/http 打 Unix socket 就能做到我們需要的子集,
// 不需要整個 SDK 的重量,也讓 GoNAS 維持「單一靜態執行檔、零第三方依賴」
// 的設計原則。
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const defaultSocketPath = "/var/run/docker.sock"

// Client 是對單一 Docker daemon 的最小 REST client。
type Client struct {
	httpClient *http.Client
	// baseURL 預設是 "http://docker" 這個佔位主機名稱,實際連線由 DialContext
	// 接管走 Unix socket。單元測試會用 WithBaseURL 換成 httptest.Server 的
	// 真實網址,搭配 WithHTTPClient 換成一般的 TCP client,這樣完全不用碰
	// 真的 Docker daemon 就能測試 client 邏輯。
	baseURL string
	// apiVersion 若非空字串，會加進每個請求路徑前面(例如 "v1.43" -> "/v1.43/containers/json")。
	// 留空的話直接打未帶版本的路徑，daemon 會用它自己支援的最新版本回應 —— 這對
	// 「不確定使用者機器上是哪個 Docker 版本」的情境最保險，所以設成預設值。
	apiVersion string
	// socketPath 是 Unix socket 路徑,給需要「原始雙向串流」(互動式終端機的
	// exec attach,見 exec_attach.go)自己直接 dial socket 用 —— 那種 hijack 連線
	// 沒辦法走 httpClient 的一般請求/回應模型。一般 REST 呼叫仍走 httpClient。
	socketPath string
}

// Option 是建立 Client 時的選用設定。
type Option func(*Client)

// WithAPIVersion 明確指定要打的 Docker Engine API 版本(例如 "v1.43")。
func WithAPIVersion(v string) Option {
	return func(c *Client) { c.apiVersion = v }
}

// WithHTTPClient 換掉底層的 http.Client,測試時用來接一般 TCP 而不是 Unix socket。
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithBaseURL 換掉預設的 "http://docker" 佔位網址,測試時指向 httptest.Server。
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = u }
}

// NewClient 建立一個透過 Unix socket 跟本機 Docker daemon 溝通的 Client。
// socketPath 留空時使用預設的 /var/run/docker.sock。
func NewClient(socketPath string, opts ...Option) *Client {
	if socketPath == "" {
		socketPath = defaultSocketPath
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// 連線本身要快速失敗(socket 不在/dockerd 沒跑),但「連上之後
			// 傳多久」不在這裡限制——見下面為何不設 http.Client.Timeout。
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, "unix", socketPath)
		},
		// 「送出請求後、拿到回應標頭前」的上限:dockerd 活著但卡住時能快速
		// 報錯,又不會影響拿到標頭之後的長時間 body 串流(拉映像/看 log)。
		ResponseHeaderTimeout: 30 * time.Second,
	}

	c := &Client{
		// 第六十輪(QA+產品覆核 P0):這裡原本設了 http.Client.Timeout=30s。
		// 但 http.Client.Timeout 是「涵蓋整個請求,包含讀 response body」的總
		// 上限——它會把 PullImage(串流下載整個映像,動輒數百 MB、要好幾分鐘)
		// 跟 ContainerLogs(可能持續串流)在 30 秒後硬砍掉,導致「應用商店裝
		// 稍大的映像(WordPress/mysql/code-server)必定失敗」這個最核心的 bug。
		// 改成不設總上限,改由「每次呼叫傳進來的 context deadline」控制個別操作
		// 該等多久(ping 用請求 context、pull 用很寬鬆的背景 context),再靠上面
		// transport 的 DialTimeout / ResponseHeaderTimeout 擋住「socket 不通/
		// daemon 卡死」這兩種真正該快速失敗的情況。
		httpClient: &http.Client{Transport: transport},
		baseURL:    "http://docker",
		socketPath: socketPath,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// apiError 對應 Docker daemon 回傳非 2xx 時常見的 {"message": "..."} 錯誤格式。
type apiError struct {
	Message string `json:"message"`
}

// do 送出一個請求並回傳原始 *http.Response,呼叫端負責關閉 Body。
// 主機名稱用 "docker" 只是佔位符 —— 實際連線完全由上面的 DialContext 接管,
// 不會真的做 DNS 查詢。
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.baseURL + c.versionedPath(path)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("building request %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling docker daemon %s %s: %w (is dockerd running / socket reachable?)", method, path, err)
	}
	return resp, nil
}

func (c *Client) versionedPath(path string) string {
	if c.apiVersion == "" {
		return path
	}
	return "/" + c.apiVersion + path
}

// doJSON 送出請求,若回傳非 2xx 就把 body 解析成 apiError 回傳有意義的錯誤訊息,
// 成功的話把 body decode 進 out(out 為 nil 代表呼叫端不關心回應內容,例如 204 No Content)。
func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, out any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	resp, err := c.do(ctx, method, path, bodyReader)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		// 仍然要把 body 讀乾淨,避免連線因為沒讀完而無法被 keep-alive 重用。
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s %s: %w", method, path, err)
	}
	return nil
}

func decodeAPIError(resp *http.Response) error {
	data, _ := io.ReadAll(resp.Body)
	var apiErr apiError
	if err := json.Unmarshal(data, &apiErr); err == nil && apiErr.Message != "" {
		return fmt.Errorf("docker daemon returned %s: %s", resp.Status, apiErr.Message)
	}
	return fmt.Errorf("docker daemon returned %s: %s", resp.Status, string(data))
}
