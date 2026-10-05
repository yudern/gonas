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
	crashImage string // 若某容器的 Image 等於這個值,start 後它「立刻退出」,用來測試裝後存活檢查
	pullCount  int    // /images/create 被呼叫幾次,驗證更新有重拉映像
	failPull   bool   // 讓 /images/create 回錯,測試更新在拉取失敗時不動舊 App
}

type fakeContainer struct {
	id      string
	name    string
	image   string
	labels  map[string]string
	running bool
	exited  bool // start 後「立刻退出」(crashImage)
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
		f.mu.Lock()
		f.pullCount++
		fail := f.failPull
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"simulated pull failure"}`))
			return
		}
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
			if f.crashImage != "" && c.image == f.crashImage {
				c.running = false
				c.exited = true // 模擬「開機即崩潰」
			} else {
				c.running = true
			}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.containers[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "no such container"})
			return
		}
		status := "running"
		exit := 0
		if c.exited {
			status = "exited"
			exit = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":    c.id,
			"State": map[string]any{"Status": status, "Running": c.running, "ExitCode": exit},
		})
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

// 第六十輪:裝好後若容器「開機即崩潰」,Install 要判定失敗並回滾,而不是
// 無條件回報成功。
func TestInstall_FailsAndRollsBackWhenContainerCrashesOnStart(t *testing.T) {
	daemon := newFakeDockerDaemon()
	daemon.crashImage = "portainer/portainer-ce:latest"
	client := newFakeClient(t, daemon)

	_, err := Install(context.Background(), client, InstallRequest{
		Template: AppTemplate{
			ID:   "portainer",
			Name: "Portainer",
			Services: []ServiceTemplate{
				{Name: "app", Image: "portainer/portainer-ce:latest"},
			},
		},
		// StartGracePeriod 0:不等待,直接 Inspect(fake 已把它標成 exited)。
	})
	if err == nil {
		t.Fatal("expected install to fail when the container exits right after start")
	}
	daemon.mu.Lock()
	n := len(daemon.containers)
	daemon.mu.Unlock()
	if n != 0 {
		t.Errorf("expected the crashed container to be rolled back, got %d left", n)
	}
}

// 第六十輪:更新一個已安裝 App —— 重拉映像 + 重建容器(容器 ID 應該換新),
// 舊容器被移除、不殘留。
func TestUpdate_RepullsAndRecreates(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)
	tmpl := AppTemplate{
		ID:   "portainer",
		Name: "Portainer",
		Services: []ServiceTemplate{
			{Name: "app", Image: "portainer/portainer-ce:latest"},
		},
	}
	first, err := Install(context.Background(), client, InstallRequest{Template: tmpl})
	if err != nil {
		t.Fatalf("initial install: %v", err)
	}
	daemon.mu.Lock()
	pullsAfterInstall := daemon.pullCount
	daemon.mu.Unlock()

	second, err := Update(context.Background(), client, InstallRequest{Template: tmpl})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	daemon.mu.Lock()
	pullsAfterUpdate := daemon.pullCount
	nContainers := len(daemon.containers)
	daemon.mu.Unlock()

	if pullsAfterUpdate <= pullsAfterInstall {
		t.Errorf("expected update to re-pull the image (pulls: install=%d, update=%d)", pullsAfterInstall, pullsAfterUpdate)
	}
	if first.ContainerIDs["app"] == second.ContainerIDs["app"] {
		t.Error("expected the container to be recreated with a new id after update")
	}
	if nContainers != 1 {
		t.Errorf("expected exactly one container after update (old removed), got %d", nContainers)
	}
}

// 更新時若「必填設定不齊」(舊版安裝沒存 overrides),要在動任何東西之前就
// 拒絕,不能把正在跑的 App 拆掉。
func TestUpdate_RefusesWhenRequiredEnvMissing_LeavesAppUntouched(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)
	tmpl := AppTemplate{
		ID:   "code-server",
		Name: "code-server",
		Services: []ServiceTemplate{
			{Name: "app", Image: "lscr.io/linuxserver/code-server:latest",
				Env: []EnvVar{{Key: "PASSWORD", Required: true}}},
		},
	}
	// 先用带 overrides 装好。
	if _, err := Install(context.Background(), client, InstallRequest{
		Template:  tmpl,
		Overrides: map[string]ServiceOverride{"app": {Env: map[string]string{"PASSWORD": "secret"}}},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	daemon.mu.Lock()
	before := len(daemon.containers)
	daemon.mu.Unlock()

	// 更新時「不带」overrides(模拟旧版安装没存)→ 必填 PASSWORD 缺 → 应拒绝。
	_, err := Update(context.Background(), client, InstallRequest{Template: tmpl})
	if err == nil {
		t.Fatal("expected update to be refused when a required env value is missing")
	}
	daemon.mu.Lock()
	after := len(daemon.containers)
	daemon.mu.Unlock()
	if after != before {
		t.Errorf("running app must be left untouched when update is refused (before=%d after=%d)", before, after)
	}
}

// 更新時拉取新映像失敗(网络/registry 挂了):舊 App 不能被拆掉。
func TestUpdate_PullFailureLeavesAppRunning(t *testing.T) {
	daemon := newFakeDockerDaemon()
	client := newFakeClient(t, daemon)
	tmpl := AppTemplate{
		ID:       "portainer",
		Name:     "Portainer",
		Services: []ServiceTemplate{{Name: "app", Image: "portainer/portainer-ce:latest"}},
	}
	if _, err := Install(context.Background(), client, InstallRequest{Template: tmpl}); err != nil {
		t.Fatalf("install: %v", err)
	}
	daemon.mu.Lock()
	before := len(daemon.containers)
	daemon.failPull = true // 之后的拉取都失败
	daemon.mu.Unlock()

	if _, err := Update(context.Background(), client, InstallRequest{Template: tmpl}); err == nil {
		t.Fatal("expected update to fail when the image pull fails")
	}
	daemon.mu.Lock()
	after := len(daemon.containers)
	daemon.mu.Unlock()
	if after != before {
		t.Errorf("app must stay running when the pre-update image pull fails (before=%d after=%d)", before, after)
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

func TestServiceOverride_EffectiveImage(t *testing.T) {
	// 沒覆寫 → 用範本預設。
	if got := (ServiceOverride{}).EffectiveImage("nginx:latest"); got != "nginx:latest" {
		t.Errorf("no override should use template image, got %q", got)
	}
	// 空白覆寫視同沒覆寫。
	if got := (ServiceOverride{Image: "   "}).EffectiveImage("nginx:latest"); got != "nginx:latest" {
		t.Errorf("blank override should fall back, got %q", got)
	}
	// 有覆寫 → 用覆寫值(去前後空白)。
	if got := (ServiceOverride{Image: " docker.m.daocloud.io/library/nginx:latest "}).EffectiveImage("nginx:latest"); got != "docker.m.daocloud.io/library/nginx:latest" {
		t.Errorf("override should win and be trimmed, got %q", got)
	}
}
