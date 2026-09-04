package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient 起一個假的 Docker daemon HTTP server，讓測試完全不需要真的
// dockerd 或 Unix socket 就能驗證 client 組出來的請求對不對、對回應解析對不對。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestListContainers(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/containers/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("all") != "" {
			t.Errorf("expected all-false call to omit ?all, got query %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"Id":"abc123","Names":["/plex"],"Image":"plexinc/pms-docker","State":"running","Status":"Up 2 hours"}]`))
	})

	containers, err := c.ListContainers(context.Background(), false)
	if err != nil {
		t.Fatalf("ListContainers returned error: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "abc123" || containers[0].State != "running" {
		t.Errorf("unexpected result: %+v", containers)
	}
}

func TestCreateContainer_BuildsExpectedBody(t *testing.T) {
	var captured dockerCreateBody
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/create" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("name") != "jellyfin" {
			t.Errorf("expected name=jellyfin query param, got %q", r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"Id":"newid123","Warnings":[]}`))
	})

	id, warnings, err := c.CreateContainer(context.Background(), CreateContainerRequest{
		Name:  "jellyfin",
		Image: "jellyfin/jellyfin:latest",
		Env:   []string{"PUID=1000"},
		Ports: []PortSpec{{ContainerPort: 8096, HostPort: 8096}},
		Mounts: []Mount{
			{HostPath: "/mnt/tank/media", ContainerPath: "/media", ReadOnly: true},
		},
		NetworkMode: "gonas-app-jellyfin",
		Labels:      map[string]string{"com.gonas.app": "jellyfin"},
	})
	if err != nil {
		t.Fatalf("CreateContainer returned error: %v", err)
	}
	if id != "newid123" || len(warnings) != 0 {
		t.Errorf("unexpected result: id=%q warnings=%v", id, warnings)
	}

	if captured.Image != "jellyfin/jellyfin:latest" {
		t.Errorf("expected image to be forwarded, got %q", captured.Image)
	}
	if captured.HostConfig.NetworkMode != "gonas-app-jellyfin" {
		t.Errorf("expected network mode forwarded, got %q", captured.HostConfig.NetworkMode)
	}
	if captured.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Errorf("expected default restart policy 'unless-stopped', got %q", captured.HostConfig.RestartPolicy.Name)
	}
	if len(captured.HostConfig.Binds) != 1 || captured.HostConfig.Binds[0] != "/mnt/tank/media:/media:ro" {
		t.Errorf("unexpected binds: %v", captured.HostConfig.Binds)
	}
	if _, ok := captured.ExposedPorts["8096/tcp"]; !ok {
		t.Errorf("expected 8096/tcp in ExposedPorts, got %v", captured.ExposedPorts)
	}
	if pb, ok := captured.HostConfig.PortBindings["8096/tcp"]; !ok || pb[0].HostPort != "8096" {
		t.Errorf("unexpected port bindings: %v", captured.HostConfig.PortBindings)
	}
}

func TestStartStopRemoveContainer(t *testing.T) {
	var gotMethod, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.StartContainer(context.Background(), "abc"); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/containers/abc/start" {
		t.Errorf("unexpected request: %s %s", gotMethod, gotPath)
	}

	if err := c.StopContainer(context.Background(), "abc", 15); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}
	if gotPath != "/containers/abc/stop?t=15" {
		t.Errorf("unexpected stop path: %s", gotPath)
	}

	if err := c.RemoveContainer(context.Background(), "abc", true); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/containers/abc?force=1" {
		t.Errorf("unexpected remove request: %s %s", gotMethod, gotPath)
	}
}

func TestDoJSON_NonSuccessDecodesAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"container name already in use"}`))
	})

	err := c.StartContainer(context.Background(), "dup")
	if err == nil || !strings.Contains(err.Error(), "container name already in use") {
		t.Fatalf("expected error containing daemon message, got: %v", err)
	}
}

func TestPullImage_ReportsProgressAndSucceeds(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromImage") != "alpine" || r.URL.Query().Get("tag") != "3.20" {
			t.Errorf("unexpected pull query: %s", r.URL.RawQuery)
		}
		lines := []string{
			`{"status":"Pulling from library/alpine"}`,
			`{"status":"Downloading","progress":"[====>    ] 1MB/2MB","id":"a1b2c3"}`,
			`{"status":"Pull complete","id":"a1b2c3"}`,
		}
		for _, l := range lines {
			_, _ = w.Write([]byte(l + "\n"))
		}
	})

	var statuses []string
	err := c.PullImage(context.Background(), "alpine:3.20", func(s string) { statuses = append(statuses, s) })
	if err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
	if len(statuses) != 3 || statuses[2] != "Pull complete" {
		t.Errorf("unexpected progress statuses: %v", statuses)
	}
}

func TestPullImage_ErrorEmbeddedInStream(t *testing.T) {
	// 這是 Docker /images/create 最容易踩的坑：HTTP 200，但錯誤藏在串流裡。
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"Pulling from library/doesnotexist"}` + "\n"))
		_, _ = w.Write([]byte(`{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n"))
	})

	err := c.PullImage(context.Background(), "doesnotexist:latest", nil)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("expected pull to surface embedded stream error, got: %v", err)
	}
}

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ ref, wantRepo, wantTag string }{
		{"nginx", "nginx", "latest"},
		{"nginx:1.27", "nginx", "1.27"},
		{"jellyfin/jellyfin:latest", "jellyfin/jellyfin", "latest"},
		{"myregistry.local:5000/app", "myregistry.local:5000/app", "latest"},
		{"myregistry.local:5000/app:v1", "myregistry.local:5000/app", "v1"},
	}
	for _, tc := range cases {
		repo, tag := splitImageRef(tc.ref)
		if repo != tc.wantRepo || tag != tc.wantTag {
			t.Errorf("splitImageRef(%q) = (%q, %q), want (%q, %q)", tc.ref, repo, tag, tc.wantRepo, tc.wantTag)
		}
	}
}

func TestImageExists(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"Id":"sha256:abc","RepoTags":["gonas-e2e-test:local","nginx:1.27"]}]`))
	})

	cases := []struct {
		ref  string
		want bool
	}{
		{"gonas-e2e-test:local", true},
		{"nginx:1.27", true},
		{"nginx", false}, // 沒指定 tag，等同 "nginx:latest"，跟已存在的 "nginx:1.27" 不同參照
		{"redis:latest", false},
	}
	for _, tc := range cases {
		got, err := c.ImageExists(context.Background(), tc.ref)
		if err != nil {
			t.Fatalf("ImageExists(%q) returned error: %v", tc.ref, err)
		}
		if got != tc.want {
			t.Errorf("ImageExists(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

func TestPing_Success(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "OK")
	})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
}

func TestEnsureAppNetwork_ReusesExisting(t *testing.T) {
	var createCalled bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/networks":
			_, _ = w.Write([]byte(`[{"Id":"net1","Name":"gonas-app-jellyfin","Driver":"bridge","Scope":"local"}]`))
		case r.Method == "POST" && r.URL.Path == "/networks/create":
			createCalled = true
			_, _ = w.Write([]byte(`{"Id":"shouldnothappen"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	id, err := c.EnsureAppNetwork(context.Background(), "jellyfin")
	if err != nil {
		t.Fatalf("EnsureAppNetwork returned error: %v", err)
	}
	if id != "net1" {
		t.Errorf("expected to reuse existing network id 'net1', got %q", id)
	}
	if createCalled {
		t.Error("expected EnsureAppNetwork not to call /networks/create when network already exists")
	}
}

func TestEnsureAppNetwork_CreatesWhenMissing(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/networks":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && r.URL.Path == "/networks/create":
			_, _ = w.Write([]byte(`{"Id":"newnet1"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	id, err := c.EnsureAppNetwork(context.Background(), "jellyfin")
	if err != nil {
		t.Fatalf("EnsureAppNetwork returned error: %v", err)
	}
	if id != "newnet1" {
		t.Errorf("expected newly created network id, got %q", id)
	}
}
