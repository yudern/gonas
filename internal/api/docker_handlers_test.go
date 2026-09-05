package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bng147/gonas/internal/docker"
)

// muxFrame 組出一個 Docker Engine API 多工串流格式的 frame，格式細節見
// internal/docker/stream.go 的 demuxStream 註解。這裡重新實作一次(而不是
// 匯入 internal/docker 的測試專用 helper)是因為那個 helper 是那個套件的
// 未匯出測試輔助函式，跨套件測試不共用未匯出符號是 Go 測試慣例。
func muxFrame(streamType byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	return append(header, []byte(payload)...)
}

// newTestServerWithDocker 建立一個 Server,把 s.docker 指向一個 httptest
// 假的 Docker daemon,讓 handler 層的路由/請求解析/回應格式可以獨立於
// internal/docker 套件本身的邏輯被測試。
func newTestServerWithDocker(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	s := newTestServer(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s.docker = docker.NewClient("", docker.WithBaseURL(srv.URL), docker.WithHTTPClient(srv.Client()))
	return s
}

func TestHandleContainerLogs(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/abc123/logs" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		_, _ = w.Write(muxFrame(1, "started ok\n"))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/docker/containers/abc123/logs", nil)
	req.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()

	s.handleContainerLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp containerLogsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Logs != "started ok\n" {
		t.Errorf("unexpected logs: %q", resp.Logs)
	}
}

func TestHandleContainerLogs_UpstreamErrorMapsTo500(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: abc123"}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/docker/containers/abc123/logs", nil)
	req.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()

	s.handleContainerLogs(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleContainerExec(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/abc123/exec":
			_, _ = w.Write([]byte(`{"Id":"exec1"}`))
		case "/exec/exec1/start":
			_, _ = w.Write(muxFrame(1, "root\n"))
		case "/exec/exec1/json":
			_, _ = w.Write([]byte(`{"Running":false,"ExitCode":0}`))
		default:
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
		}
	})

	body, _ := json.Marshal(containerExecRequest{Cmd: []string{"whoami"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docker/containers/abc123/exec", bytes.NewReader(body))
	req.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()

	s.handleContainerExec(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp containerExecResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Output != "root\n" || resp.ExitCode != 0 {
		t.Errorf("unexpected result: %+v", resp)
	}
}

func TestHandleContainerExec_EmptyCommandIsBadRequest(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("expected no upstream request for an empty command, got %s", r.URL.Path)
	})

	body, _ := json.Marshal(containerExecRequest{Cmd: nil})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docker/containers/abc123/exec", bytes.NewReader(body))
	req.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()

	s.handleContainerExec(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleContainerExec_MalformedBodyIsBadRequest(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("expected no upstream request for a malformed body, got %s", r.URL.Path)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/docker/containers/abc123/exec", strings.NewReader("not json"))
	req.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()

	s.handleContainerExec(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
