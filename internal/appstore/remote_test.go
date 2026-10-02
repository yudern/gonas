package appstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newCatalogServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCatalog_ValidArray(t *testing.T) {
	cat := []AppTemplate{
		{ID: "a", Name: "A", Services: []ServiceTemplate{{Name: "app", Image: "img:a"}}},
		{ID: "b", Name: "B", Services: []ServiceTemplate{{Name: "app", Image: "img:b"}}},
	}
	raw, _ := json.Marshal(cat)
	srv := newCatalogServer(t, http.StatusOK, string(raw))

	got, skipped, err := FetchCatalog(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("expected no skipped templates, got %v", skipped)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("unexpected templates: %+v", got)
	}
}

func TestFetchCatalog_SkipsInvalidKeepsValid(t *testing.T) {
	// 第二個範本沒有 services(Validate 會擋),應被跳過但不讓整份作廢。
	body := `[
	  {"id":"ok","name":"OK","services":[{"name":"app","image":"img:ok"}]},
	  {"id":"bad","name":"Bad"},
	  {"id":"ok","name":"Dup","services":[{"name":"app","image":"img:dup"}]}
	]`
	srv := newCatalogServer(t, http.StatusOK, body)

	got, skipped, err := FetchCatalog(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ok" {
		t.Errorf("expected only the valid 'ok' template, got %+v", got)
	}
	if len(skipped) != 2 {
		t.Errorf("expected 2 skipped (invalid + duplicate id), got %v", skipped)
	}
}

func TestFetchCatalog_Non200(t *testing.T) {
	srv := newCatalogServer(t, http.StatusNotFound, "nope")
	_, _, err := FetchCatalog(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestFetchCatalog_NotAJSONArray(t *testing.T) {
	srv := newCatalogServer(t, http.StatusOK, `{"not":"an array"}`)
	_, _, err := FetchCatalog(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error when the body is not a JSON array")
	}
}

func TestFetchCatalog_RejectsNonHTTPScheme(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://host/x", "/local/path"} {
		if _, _, err := FetchCatalog(context.Background(), http.DefaultClient, u); err == nil {
			t.Errorf("expected %q to be rejected as a non-http(s) scheme", u)
		}
	}
}

func TestFetchCatalog_RejectsOversizeBody(t *testing.T) {
	// 造一個超過 maxCatalogBytes 的合法 JSON 陣列。
	var b strings.Builder
	b.WriteString("[")
	n := (maxCatalogBytes / 60) + 100
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"app%d","name":"N","services":[{"name":"app","image":"i:1"}]}`, i)
	}
	b.WriteString("]")
	srv := newCatalogServer(t, http.StatusOK, b.String())

	_, _, err := FetchCatalog(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a body over the size limit")
	}
}
