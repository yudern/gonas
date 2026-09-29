package api

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/state"
)

// newTestServer 建立一個只帶 store 跟一個吃掉所有輸出的 logger 的最小
// Server,夠用來測 resolveInstallTemplate 這種只碰 s.store 的純邏輯,或是
// 需要 s.logger 不為 nil(呼叫 s.logger.Error(...))但不需要真的啟動
// HTTP、監控輪詢這些跟測試行為無關的東西的 handler。個別測試需要
// s.docker 的話自己指定,這裡故意不預設,讓「忘記設定就打真的 Docker
// daemon」這種錯誤在測試時清楚地失敗,而不是安靜地連到一個看起來正常的假物件。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	return &Server{
		store:  store,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func minimalTemplate(id string) appstore.AppTemplate {
	return appstore.AppTemplate{
		ID:   id,
		Name: "Test App " + id,
		Services: []appstore.ServiceTemplate{
			{Name: "app", Image: "gonas-test-image:local"},
		},
	}
}

func TestResolveInstallTemplate_ByCatalogID(t *testing.T) {
	s := newTestServer(t)

	if len(builtinCatalog) == 0 {
		t.Fatal("builtinCatalog is unexpectedly empty")
	}
	want := builtinCatalog[0]

	got, err := s.resolveInstallTemplate(installAppRequest{TemplateID: want.ID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("got template id %q, want %q", got.ID, want.ID)
	}
}

func TestResolveInstallTemplate_UnknownCatalogID(t *testing.T) {
	s := newTestServer(t)

	_, err := s.resolveInstallTemplate(installAppRequest{TemplateID: "does-not-exist"})
	if err != errAppNotFound {
		t.Errorf("got err %v, want errAppNotFound", err)
	}
}

// 第六十輪 QA 覆核:重複安裝同一個目錄 App 要提前擋成 409(errAppIDAlreadyInstalled),
// 而不是一路跑到 docker 才以「容器名稱已存在」爆 500。
func TestResolveInstallTemplate_CatalogAlreadyInstalled(t *testing.T) {
	s := newTestServer(t)
	want := builtinCatalog[0]
	if err := s.store.Update(func(st *state.State) error {
		st.InstalledApps = append(st.InstalledApps, state.InstalledApp{Template: want})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.resolveInstallTemplate(installAppRequest{TemplateID: want.ID})
	if err != errAppIDAlreadyInstalled {
		t.Errorf("got err %v, want errAppIDAlreadyInstalled", err)
	}
}

func TestResolveInstallTemplate_CustomTemplate(t *testing.T) {
	s := newTestServer(t)
	tmpl := minimalTemplate("my-custom-app")

	got, err := s.resolveInstallTemplate(installAppRequest{Template: &tmpl})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "my-custom-app" {
		t.Errorf("got template id %q, want %q", got.ID, "my-custom-app")
	}
}

func TestResolveInstallTemplate_NeitherProvided(t *testing.T) {
	s := newTestServer(t)

	_, err := s.resolveInstallTemplate(installAppRequest{})
	if err != errAppInstallNeedsExactlyOne {
		t.Errorf("got err %v, want errAppInstallNeedsExactlyOne", err)
	}
}

func TestResolveInstallTemplate_BothProvided(t *testing.T) {
	s := newTestServer(t)
	tmpl := minimalTemplate("whatever")

	_, err := s.resolveInstallTemplate(installAppRequest{TemplateID: builtinCatalog[0].ID, Template: &tmpl})
	if err != errAppInstallNeedsExactlyOne {
		t.Errorf("got err %v, want errAppInstallNeedsExactlyOne", err)
	}
}

func TestResolveInstallTemplate_CustomIDCollidesWithCatalog(t *testing.T) {
	s := newTestServer(t)
	tmpl := minimalTemplate(builtinCatalog[0].ID) // 撞內建目錄的 ID

	_, err := s.resolveInstallTemplate(installAppRequest{Template: &tmpl})
	if err != errAppIDConflictsWithCatalog {
		t.Errorf("got err %v, want errAppIDConflictsWithCatalog", err)
	}
}

func TestResolveInstallTemplate_CustomIDCollidesWithInstalledApp(t *testing.T) {
	s := newTestServer(t)

	if err := s.store.Update(func(st *state.State) error {
		st.InstalledApps = append(st.InstalledApps, state.InstalledApp{
			Template: minimalTemplate("already-installed"),
		})
		return nil
	}); err != nil {
		t.Fatalf("seeding installed app: %v", err)
	}

	tmpl := minimalTemplate("already-installed")
	_, err := s.resolveInstallTemplate(installAppRequest{Template: &tmpl})
	if err != errAppIDAlreadyInstalled {
		t.Errorf("got err %v, want errAppIDAlreadyInstalled", err)
	}
}

func TestResolveInstallTemplate_DifferentCustomIDsDoNotCollide(t *testing.T) {
	s := newTestServer(t)

	if err := s.store.Update(func(st *state.State) error {
		st.InstalledApps = append(st.InstalledApps, state.InstalledApp{
			Template: minimalTemplate("app-one"),
		})
		return nil
	}); err != nil {
		t.Fatalf("seeding installed app: %v", err)
	}

	tmpl := minimalTemplate("app-two")
	got, err := s.resolveInstallTemplate(installAppRequest{Template: &tmpl})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "app-two" {
		t.Errorf("got template id %q, want %q", got.ID, "app-two")
	}
}
