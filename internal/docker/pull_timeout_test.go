package docker

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 第六十四輪回歸:dockerd 拉映像時要等 manifest 解析完才送出 HTTP 標頭;
// 超過一般呼叫的 ResponseHeaderTimeout 時,拉映像仍必須成功(不能被砍),
// 一般呼叫逾時的錯誤也不能再說成「dockerd 沒在跑」。
func TestPullImage_SlowHeadersDoNotTimeOut(t *testing.T) {
	old := responseHeaderTimeout
	responseHeaderTimeout = 200 * time.Millisecond
	defer func() { responseHeaderTimeout = old }()

	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond) // 模擬 dockerd 在解析 manifest
		if strings.HasSuffix(r.URL.Path, "/images/create") {
			w.Write([]byte(`{"status":"Pulling from library/x"}` + "\n" + `{"status":"Download complete"}` + "\n"))
			return
		}
		w.Write([]byte("OK"))
	})}
	go srv.Serve(ln)
	defer srv.Close()

	c := NewClient(sock)
	if err := c.PullImage(context.Background(), "x:latest", nil); err != nil {
		t.Fatalf("pull with slow headers must succeed, got %v", err)
	}
	err = c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected the normal call to hit the header timeout")
	}
	if strings.Contains(err.Error(), "is dockerd running") {
		t.Errorf("a timeout must not be reported as 'dockerd not running': %v", err)
	}
}

func TestDo_DialFailureSaysDockerdNotRunning(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	err := c.PullImage(context.Background(), "x:latest", nil)
	if err == nil || !strings.Contains(err.Error(), "is dockerd running") {
		t.Fatalf("missing socket should be reported as dockerd not running, got %v", err)
	}
}
