package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/state"
)

// TestMergedCatalog_BuiltinThenRemote_RemoteCannotShadowBuiltin 驗證合併目錄:
// 內建範本全部在前且標成 "builtin";遠端範本接在後面標成 "remote";而遠端一個
// 跟內建同 ID 的範本會被丟掉(內建優先,防 shadow)。
func TestMergedCatalog_BuiltinThenRemote_RemoteCannotShadowBuiltin(t *testing.T) {
	s := newTestServer(t)
	s.catalogHTTPClient = http.DefaultClient
	builtinID := builtinCatalog[0].ID

	s.catalogMu.Lock()
	s.remoteCatalog = []appstore.AppTemplate{
		{ID: builtinID, Name: "Evil Shadow", Services: []appstore.ServiceTemplate{{Name: "app", Image: "evil:latest"}}},
		{ID: "remote-only", Name: "Remote Only", Services: []appstore.ServiceTemplate{{Name: "app", Image: "ro:latest"}}},
	}
	s.catalogMu.Unlock()

	merged := s.mergedCatalog()
	if len(merged) != len(builtinCatalog)+1 {
		t.Fatalf("expected builtin count + 1 remote-only, got %d (builtin=%d)", len(merged), len(builtinCatalog))
	}
	// 所有內建在前且 source=builtin。
	for i := range builtinCatalog {
		if merged[i].Source != "builtin" {
			t.Errorf("entry %d expected source builtin, got %q", i, merged[i].Source)
		}
	}
	last := merged[len(merged)-1]
	if last.ID != "remote-only" || last.Source != "remote" {
		t.Errorf("expected last entry to be the remote-only app, got %+v", last)
	}
	// 被 shadow 的那個 builtinID 仍然指向內建 image,不是 evil:latest。
	for _, e := range merged {
		if e.ID == builtinID && e.Services[0].Image == "evil:latest" {
			t.Error("a remote template shadowed a builtin one — builtin must win")
		}
	}
}

// TestTemplateByID_BuiltinWinsOverRemote 驗證安裝查找也遵守「內建優先」。
func TestTemplateByID_BuiltinWinsOverRemote(t *testing.T) {
	s := newTestServer(t)
	builtinID := builtinCatalog[0].ID
	s.catalogMu.Lock()
	s.remoteCatalog = []appstore.AppTemplate{
		{ID: builtinID, Name: "Shadow", Services: []appstore.ServiceTemplate{{Name: "app", Image: "evil:latest"}}},
		{ID: "remote-x", Name: "Remote X", Services: []appstore.ServiceTemplate{{Name: "app", Image: "rx:latest"}}},
	}
	s.catalogMu.Unlock()

	got, ok := s.templateByID(builtinID)
	if !ok || got.Services[0].Image == "evil:latest" {
		t.Errorf("templateByID must return the builtin template for %q, got %+v", builtinID, got)
	}
	if rx, ok := s.templateByID("remote-x"); !ok || rx.Services[0].Image != "rx:latest" {
		t.Errorf("templateByID should find the remote-only template, got %+v ok=%v", rx, ok)
	}
	if _, ok := s.templateByID("does-not-exist"); ok {
		t.Error("templateByID should report not-found for an unknown id")
	}
}

// TestCatalogSourceSet_FetchesAndReportsStatus 驗證 PUT source 會持久化網址、
// 同步抓一次,並在回應裡帶上抓取結果(本例遠端回兩個合法範本)。
func TestCatalogSourceSet_FetchesAndReportsStatus(t *testing.T) {
	s := newTestServer(t)
	s.catalogHTTPClient = http.DefaultClient

	body := `[{"id":"ext1","name":"Ext1","services":[{"name":"app","image":"e:1"}]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	reqBody, _ := json.Marshal(catalogSourceSetRequest{URL: srv.URL})
	req := httptest.NewRequest("PUT", "/api/v1/appstore/catalog/source", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	s.handleAppstoreCatalogSourceSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp catalogSourceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.URL != srv.URL || resp.RemoteCount != 1 || resp.Error != "" {
		t.Errorf("unexpected source status: %+v", resp)
	}
	// 網址已持久化。
	if s.store.Snapshot().AppCatalogURL != srv.URL {
		t.Errorf("catalog url was not persisted, got %q", s.store.Snapshot().AppCatalogURL)
	}
	// 合併目錄現在含那個遠端 App。
	if _, ok := s.templateByID("ext1"); !ok {
		t.Error("expected the fetched remote template to be installable by id")
	}
}

// TestCatalogSourceSet_EmptyURLClearsCache 驗證把網址清空會清掉遠端快取。
func TestCatalogSourceSet_EmptyURLClearsCache(t *testing.T) {
	s := newTestServer(t)
	s.catalogHTTPClient = http.DefaultClient
	s.catalogMu.Lock()
	s.remoteCatalog = []appstore.AppTemplate{{ID: "x", Name: "X", Services: []appstore.ServiceTemplate{{Name: "app", Image: "x:1"}}}}
	s.catalogMu.Unlock()
	if err := s.store.Update(func(st *state.State) error { st.AppCatalogURL = "http://old"; return nil }); err != nil {
		t.Fatal(err)
	}

	reqBody, _ := json.Marshal(catalogSourceSetRequest{URL: ""})
	req := httptest.NewRequest("PUT", "/api/v1/appstore/catalog/source", bytes.NewReader(reqBody))
	req = req.WithContext(context.Background())
	rec := httptest.NewRecorder()
	s.handleAppstoreCatalogSourceSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if n := len(s.mergedCatalog()); n != len(builtinCatalog) {
		t.Errorf("expected remote cache cleared (only builtin left), got %d", n)
	}
}
