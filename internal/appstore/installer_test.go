package appstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bng147/gonas/internal/docker"
)

// fakeDockerDaemon 是一個記狀態的假 Docker daemon，讓 appstore 的測試可以驗證
// 「安裝流程實際呼叫了哪些 API、順序對不對、失敗時真的有清理」，而不需要真的
// dockerd 也不需要能連上網路拉真正的映像檔。
type fakeDockerDaemon struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	networks   map[string]string // id -> name
	nextID     int
	failImage  string // 若某個 create 請求的 Image 等於這個值，故意回傳錯誤，用來測試 rollback
}

type fakeContainer struct {
	id      string
	name    string
	image   string
	labels  map[string]string
	running bool
}

func newFakeDockerDaemon() *fakeDockerDaemon {
	return &fakeDockerDaemon{
		containers: make(map[string]*fakeContainer),
		networks:   make(map[string]string),
	}
}

func (f *fakeDockerDaemon) nextIDStr(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakeDockerDaemon) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		type netOut struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		}
		out := make([]netOut, 0, len(f.networks))
		for id, name := range f.networks {
			out = append(out, netOut{ID: id, Name: name})
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("POST /networks/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		id := f.nextIDStr("net")
		f.networks[id] = body.Name
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	})

	mux.HandleFunc("DELETE /networks/{id}", func(w http.ResponseWriter, r *http.Request) {
		// 真正的 Docker API 允許用 ID 或名稱刪除網路，這裡的假伺服器也要照做,
		// 因為 appstore 是用 docker.AppNetworkName(appID) 算出的「名稱」呼叫
		// RemoveNetwork,而不是先查一次 ID。
		ref := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.networks[ref]; ok {
			delete(f.networks, ref)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		for id, name := range f.networks {
			if name == ref {
				delete(f.networks, id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "network " + ref + " not found"})
	})

	mux.HandleFunc("GET /images/json", func(w http.ResponseWriter, r *http.Request) {
		// 預設回空清單：測試裡的 image 一律視為「本機還沒有」，逼安裝流程
		// 真的走一次 PullImage,跟原本的行為保持一致。
		_, _ = w.Write([]byte(`[]`))
	})

	mux.HandleFunc("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"Pull complete"}` + "\n"))
	})

	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Image  string
			Labels map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if f.failImage != "" && body.Image == f.failImage {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "simulated create failure for " + body.Image})
			return
		}

		f.mu.Lock()
		id := f.nextIDStr("ctr")
		f.containers[id] = &fakeContainer{id: id, name: r.URL.Query().Get("name"), image: body.Image, labels: body.Labels}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Warnings": []string{}})
	})

	mux.HandleFunc("POST /containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if c, ok := f.containers[r.PathValue("id")]; ok {
			c.running = true
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /containers/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if c, ok := f.containers[r.PathValue("id")]; ok {
			c.running = false
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		delete(f.containers, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		type ctrOut struct {
			ID     string            `json:"Id"`
			Labels map[string]string `json:"Labels"`
		}
		out := make([]ctrOut, 0, len(f.containers))
		for _, c := range f.containers {
			out = append(out, ctrOut{ID: c.id, Labels: c.labels})
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	return mux
}

func newFakeClient(t *testing.T, daemon *fakeDockerDaemon) *docker.Client {
	t.Helper()
	srv := httptest.NewServer(daemon.handler())
	t.Cleanup(srv.Close)
	return docker.NewClient("", docker.WithBaseURL(srv.URL), docker.WithHTTPClient(srv.Client()))
}

func TestInstall_SingleService_NoNetworkCreated(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	result, err := Install(context.Background(), client, InstallRequest{
		Template: AppTemplate{
			ID:   "portainer",
			Name: "Portainer",
			Services: []ServiceTemplate{
				{Name: "app", Image: "portainer/portainer-ce:latest"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if result.NetworkID != "" {
		t.Errorf("expected no network for single-service app, got %q", result.NetworkID)
	}
	if len(result.ContainerIDs) != 1 {
		t.Fatalf("expected 1 container, got %d", len(result.ContainerIDs))
	}

	id := result.ContainerIDs["app"]
	daemon.mu.Lock()
	c, ok := daemon.containers[id]
	daemon.mu.Unlock()
	if !ok || !c.running {
		t.Fatalf("expected container %s to exist and be running, got %+v (ok=%v)", id, c, ok)
	}
}

func TestInstall_MultiService_CreatesSharedNetwork(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	tmpl := AppTemplate{
		ID:   "arrstack",
		Name: "Arr Stack",
		Services: []ServiceTemplate{
			{Name: "sonarr", Image: "lscr.io/linuxserver/sonarr:latest"},
			{Name: "prowlarr", Image: "lscr.io/linuxserver/prowlarr:latest"},
		},
	}

	result, err := Install(context.Background(), client, InstallRequest{Template: tmpl})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if result.NetworkID == "" {
		t.Fatal("expected a shared network to be created for a multi-service app")
	}
	if len(result.ContainerIDs) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(result.ContainerIDs))
	}

	daemon.mu.Lock()
	netName := daemon.networks[result.NetworkID]
	daemon.mu.Unlock()
	if netName != docker.AppNetworkName("arrstack") {
		t.Errorf("expected network name %q, got %q", docker.AppNetworkName("arrstack"), netName)
	}
}

func TestInstall_RollsBackOnFailure(t *testing.T) {
	daemon := newFakeDockerDaemon()
	daemon.failImage = "lscr.io/linuxserver/prowlarr:latest" // 讓第二個服務建立失敗

	client := newFakeClient(t, daemon)

	tmpl := AppTemplate{
		ID:   "arrstack",
		Name: "Arr Stack",
		Services: []ServiceTemplate{
			{Name: "sonarr", Image: "lscr.io/linuxserver/sonarr:latest"},
			{Name: "prowlarr", Image: "lscr.io/linuxserver/prowlarr:latest"},
		},
	}

	_, err := Install(context.Background(), client, InstallRequest{Template: tmpl})
	if err == nil {
		t.Fatal("expected Install to fail when second service's create fails")
	}

	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if len(daemon.containers) != 0 {
		t.Errorf("expected rollback to remove the first service's container, but %d remain: %+v", len(daemon.containers), daemon.containers)
	}
	if len(daemon.networks) != 0 {
		t.Errorf("expected rollback to remove the created network, but %d remain: %v", len(daemon.networks), daemon.networks)
	}
}

func TestInstall_MissingRequiredVolumeHostPath(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	tmpl := AppTemplate{
		ID:   "jellyfin",
		Name: "Jellyfin",
		Services: []ServiceTemplate{
			{Name: "app", Image: "jellyfin/jellyfin:latest", Volumes: []VolumeMapping{{ContainerPath: "/media"}}},
		},
	}

	_, err := Install(context.Background(), client, InstallRequest{Template: tmpl})
	if err == nil {
		t.Fatal("expected error when a volume has no host path and no override was given")
	}
}

// 第六十輪:安裝時要先確保每個 bind 掛載的宿主端目錄存在(路徑不存在自動建),
// 且要在建立容器之前做。用 EnsureHostDir hook 記錄被建了哪些路徑,不動真檔案系統。
func TestInstall_EnsuresHostDirsBeforeCreate(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	var ensured []string
	_, err := Install(context.Background(), client, InstallRequest{
		Template: AppTemplate{
			ID:   "portainer",
			Name: "Portainer",
			Services: []ServiceTemplate{
				{Name: "app", Image: "portainer/portainer-ce:latest", Volumes: []VolumeMapping{{ContainerPath: "/data"}}},
			},
		},
		Overrides: map[string]ServiceOverride{
			"app": {VolumeHostPaths: map[string]string{"/data": "/mnt/tank/appdata/portainer"}},
		},
		EnsureHostDir: func(p string) error { ensured = append(ensured, p); return nil },
	})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	found := false
	for _, p := range ensured {
		if p == "/mnt/tank/appdata/portainer" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the host mount dir to be ensured before container create; ensured=%v", ensured)
	}
}

// EnsureHostDir 失敗(例如唯讀 / 權限不足)時,整個安裝要失敗並回滾,不留下
// 半裝的容器。
func TestInstall_EnsureHostDirFailureRollsBack(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	_, err := Install(context.Background(), client, InstallRequest{
		Template: AppTemplate{
			ID:   "portainer",
			Name: "Portainer",
			Services: []ServiceTemplate{
				{Name: "app", Image: "portainer/portainer-ce:latest", Volumes: []VolumeMapping{{ContainerPath: "/data"}}},
			},
		},
		Overrides: map[string]ServiceOverride{
			"app": {VolumeHostPaths: map[string]string{"/data": "/mnt/tank/appdata/portainer"}},
		},
		EnsureHostDir: func(p string) error { return fmt.Errorf("mkdir denied") },
	})
	if err == nil {
		t.Fatal("expected install to fail when the host dir cannot be created")
	}
	daemon.mu.Lock()
	n := len(daemon.containers)
	daemon.mu.Unlock()
	if n != 0 {
		t.Errorf("expected no containers left after rollback, got %d", n)
	}
}

func TestUninstall_SingleServiceApp_NoNetworkToRemove_DoesNotError(t *testing.T) {
	// 回歸測試：早期版本會在單服務 App(從未建立過專屬網路)解除安裝時,
	// 因為「找不到網路」而誤判成錯誤。這裡確認修正後不會再發生。
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	tmpl := AppTemplate{
		ID:   "portainer",
		Name: "Portainer",
		Services: []ServiceTemplate{
			{Name: "app", Image: "portainer/portainer-ce:latest"},
		},
	}
	if _, err := Install(context.Background(), client, InstallRequest{Template: tmpl}); err != nil {
		t.Fatalf("setup Install failed: %v", err)
	}

	if err := Uninstall(context.Background(), client, "portainer"); err != nil {
		t.Fatalf("expected Uninstall of a single-service app to succeed with no error, got: %v", err)
	}

	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if len(daemon.containers) != 0 {
		t.Errorf("expected container to be removed, got: %+v", daemon.containers)
	}
}

func TestUninstall_RemovesLabeledContainersAndNetwork(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)

	tmpl := AppTemplate{
		ID:   "arrstack",
		Name: "Arr Stack",
		Services: []ServiceTemplate{
			{Name: "sonarr", Image: "lscr.io/linuxserver/sonarr:latest"},
			{Name: "prowlarr", Image: "lscr.io/linuxserver/prowlarr:latest"},
		},
	}
	if _, err := Install(context.Background(), client, InstallRequest{Template: tmpl}); err != nil {
		t.Fatalf("setup Install failed: %v", err)
	}

	// 混入一個不相干的容器，確認 Uninstall 只清掉屬於這個 app 的東西。
	daemon.mu.Lock()
	daemon.containers["other-1"] = &fakeContainer{id: "other-1", labels: map[string]string{"com.gonas.app": "unrelated"}}
	daemon.mu.Unlock()

	if err := Uninstall(context.Background(), client, "arrstack"); err != nil {
		t.Fatalf("Uninstall returned error: %v", err)
	}

	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if len(daemon.containers) != 1 || daemon.containers["other-1"] == nil {
		t.Errorf("expected only the unrelated container to remain, got: %+v", daemon.containers)
	}
	if len(daemon.networks) != 0 {
		t.Errorf("expected app network to be removed, got: %v", daemon.networks)
	}
}
