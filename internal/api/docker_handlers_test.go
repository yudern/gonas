package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/doctor"
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

// 第六十輪回歸:只要 docker daemon ping 得到,系統診斷頁的 docker 這項就要
// 顯示「已安裝」,跟儀表板/應用頁的 Docker 狀態一致——即使測試環境裡
// exec.LookPath("docker") 找不到 CLI 也一樣(這正是使用者實機「儀表板可用、
// 診斷頁未安裝」矛盾的修法)。
func TestHandleDoctorStatus_DockerReconciledWithDaemonPing(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	s.handleDoctorStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var pkgs []doctor.PackageStatus
	if err := json.NewDecoder(rec.Body).Decode(&pkgs); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	found := false
	for _, p := range pkgs {
		if p.Apt == "docker.io" {
			found = true
			if !p.Installed {
				t.Errorf("docker.io should be reported installed when the daemon pings OK; got %+v", p)
			}
			if len(p.Missing) != 0 {
				t.Errorf("docker.io should have no missing commands when daemon pings OK; got %v", p.Missing)
			}
		}
	}
	if !found {
		t.Fatal("expected a docker.io entry in the doctor status")
	}
}

// daemon ping 失敗時不做補正:docker 這項照 exec.LookPath 的結果走(測試環境
// 通常沒裝 docker CLI → 未安裝),不會被錯誤地標成已安裝。
func TestHandleDoctorStatus_DockerNotReconciledWhenPingFails(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	s.handleDoctorStatus(rec, req)

	var pkgs []doctor.PackageStatus
	if err := json.NewDecoder(rec.Body).Decode(&pkgs); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	for _, p := range pkgs {
		if p.Apt == "docker.io" && p.Installed {
			// 只有在這台 CI 機器剛好真的裝了 docker CLI 時才可能為 true;
			// 沙盒環境沒有,所以這裡預期是 false。若哪天 CI 裝了 docker,
			// 這個斷言要放寬——但目前用來確認「ping 失敗不會亂補正」。
			if _, err := exec.LookPath("docker"); err != nil {
				t.Errorf("docker.io must not be marked installed when neither the CLI exists nor the daemon pings; got %+v", p)
			}
		}
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
