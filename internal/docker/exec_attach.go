package docker

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InteractiveExec 是一個已經 attach 上去的互動式 exec:Conn 是跟容器 stdin/stdout
// 直接相連的原始雙向連線(TTY 模式,輸出沒有多工 framing),Reader 是套在 Conn
// 上、已經讀掉 HTTP 回應標頭的緩衝讀取器(後續要從這裡讀容器輸出,不能再直接
// 讀 Conn,否則會漏掉 bufio 已經預讀進緩衝區的位元組)。ExecID 給 resize 用。
type InteractiveExec struct {
	Conn   net.Conn
	Reader *bufio.Reader
	ExecID string
}

// Close 關掉底層連線。
func (e *InteractiveExec) Close() error {
	if e.Conn != nil {
		return e.Conn.Close()
	}
	return nil
}

// StartInteractiveExec 在容器裡開一個帶 TTY 的互動式 exec(等同 docker exec -it),
// 回傳一條可雙向讀寫的原始連線。做法:
//  1. 用一般 REST 建立 exec(Tty=true, 三個 Attach 都 true)。
//  2. 自己 dial Unix socket,手寫 `POST /exec/{id}/start` 並帶 Upgrade 標頭,
//     讓 daemon 把這條連線「hijack」成原始雙向串流 —— 這種連線沒辦法用
//     httpClient 的請求/回應模型表達(回應 body 跟請求 body 要同時持續雙向流動)。
//
// 只在真的連 Unix socket 時可用(socketPath 非空);測試用的 httptest TCP client
// 不支援這種 hijack,會回明確錯誤。
func (c *Client) StartInteractiveExec(ctx context.Context, containerID string, cmd []string) (*InteractiveExec, error) {
	if c.socketPath == "" {
		return nil, fmt.Errorf("interactive exec requires a unix socket connection")
	}
	if len(cmd) == 0 {
		return nil, ErrExecEmptyCommand
	}

	// 1. 建立 exec(帶 TTY + 三個 attach)。
	var created execCreateResponse
	createReq := execCreateRequest{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          cmd,
	}
	if err := c.doJSON(ctx, "POST", "/containers/"+url.PathEscape(containerID)+"/exec", createReq, &created); err != nil {
		return nil, fmt.Errorf("creating interactive exec for %s: %w", containerID, err)
	}

	// 2. dial socket 並手寫帶 Upgrade 的 start 請求。
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("dialing docker socket for exec attach: %w", err)
	}

	body := `{"Detach":false,"Tty":true}`
	path := c.versionedPath("/exec/" + url.PathEscape(created.ID) + "/start")
	reqLines := "POST " + path + " HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"\r\n" + body
	if _, err := conn.Write([]byte(reqLines)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("sending exec start: %w", err)
	}

	// 讀 HTTP 回應標頭。daemon 對成功的 attach 回 101 Switching Protocols
	// (hijack)或 200 OK(較舊版本);兩者之後的位元組都是原始雙向串流。
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading exec start response: %w", err)
	}
	if !strings.Contains(statusLine, " 101 ") && !strings.Contains(statusLine, " 200 ") {
		conn.Close()
		return nil, fmt.Errorf("exec start failed: %s", strings.TrimSpace(statusLine))
	}
	// 讀掉剩下的標頭(到空行為止),之後 reader 的位置就是串流資料的開頭。
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("reading exec start headers: %w", err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}

	return &InteractiveExec{Conn: conn, Reader: reader, ExecID: created.ID}, nil
}

// ResizeExec 調整一個 exec 的 TTY 視窗大小(對應 `POST /exec/{id}/resize?h=&w=`)。
// 前端終端機視窗改變大小時呼叫,讓容器裡的程式(vim/top 等)知道新的行列數。
func (c *Client) ResizeExec(ctx context.Context, execID string, height, width int) error {
	if height <= 0 || width <= 0 {
		return nil
	}
	q := url.Values{"h": {fmt.Sprintf("%d", height)}, "w": {fmt.Sprintf("%d", width)}}.Encode()
	// resize 不回 body;用 doJSON(out=nil)即可。daemon 回 200/201。
	if err := c.doJSON(ctx, "POST", "/exec/"+url.PathEscape(execID)+"/resize?"+q, nil, nil); err != nil {
		return fmt.Errorf("resizing exec %s: %w", execID, err)
	}
	return nil
}

// PathExists 檢查容器裡某個路徑存不存在(對應 HEAD /containers/{id}/archive,
// Docker 用它回報檔案的 stat;不存在回 404)。給互動式終端機挑 shell 用——
// 有些精簡映像(例如 portainer、distroless)根本沒有 /bin/sh。
func (c *Client) PathExists(ctx context.Context, containerID, path string) (bool, error) {
	q := url.Values{"path": {path}}.Encode()
	resp, err := c.do(ctx, "HEAD", "/containers/"+url.PathEscape(containerID)+"/archive?"+q, nil)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("checking %s in container %s: docker daemon returned %s", path, containerID, resp.Status)
	}
}
