package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/state"
)

func TestReconcileInstalledApps_DropsAppsWhoseContainersAreGone(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		// 只剩 registry 的容器還在;portainer 的容器在 GoNAS 之外被刪了。
		json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "c-registry", "Labels": map[string]string{"com.gonas.app": "registry"}},
			{"Id": "c-other", "Labels": map[string]string{}},
		})
	})
	s.store.Update(func(st *state.State) error {
		st.InstalledApps = []state.InstalledApp{
			{Template: appstore.AppTemplate{ID: "portainer"}, Result: appstore.InstallResult{ContainerIDs: map[string]string{"app": "c-portainer"}}},
			{Template: appstore.AppTemplate{ID: "registry"}, Result: appstore.InstallResult{ContainerIDs: map[string]string{"app": "c-registry"}}},
			{Template: appstore.AppTemplate{ID: "custom"}, Result: appstore.InstallResult{ContainerIDs: map[string]string{"app": "c-other"}}},
		}
		return nil
	})
	rec := httptest.NewRecorder()
	s.handleAppstoreListInstalled(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var got []state.InstalledApp
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 2 || got[0].Template.ID != "registry" || got[1].Template.ID != "custom" {
		t.Fatalf("want registry+custom kept (portainer dropped), got %+v", got)
	}

	// 安裝進行中:不能清(更新途中舊容器會短暫消失)。
	s.store.Update(func(st *state.State) error {
		st.InstalledApps = append(st.InstalledApps, state.InstalledApp{Template: appstore.AppTemplate{ID: "ghost"}})
		return nil
	})
	s.appInstalling.Store(true)
	s.reconcileInstalledApps(t.Context())
	if n := len(s.store.Snapshot().InstalledApps); n != 3 {
		t.Errorf("must not prune while an install is running, have %d", n)
	}
	s.appInstalling.Store(false)
}

func TestReconcileInstalledApps_DockerDownKeepsEverything(t *testing.T) {
	s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	s.store.Update(func(st *state.State) error {
		st.InstalledApps = []state.InstalledApp{{Template: appstore.AppTemplate{ID: "portainer"}}}
		return nil
	})
	s.reconcileInstalledApps(t.Context())
	if len(s.store.Snapshot().InstalledApps) != 1 {
		t.Error("must not drop records when Docker can't be queried")
	}
}
