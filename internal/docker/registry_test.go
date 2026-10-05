package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		in       string
		registry string
		repo     string
		tag      string
		isDigest bool
	}{
		{"nginx", "registry-1.docker.io", "library/nginx", "latest", false},
		{"nginx:1.25", "registry-1.docker.io", "library/nginx", "1.25", false},
		{"user/app:v2", "registry-1.docker.io", "user/app", "v2", false},
		{"ghcr.io/owner/app:stable", "ghcr.io", "owner/app", "stable", false},
		{"myreg:5000/team/app:1", "myreg:5000", "team/app", "1", false},
		{"localhost/app", "localhost", "app", "latest", false},
		{"nginx@sha256:abc", "registry-1.docker.io", "library/nginx", "sha256:abc", true},
	}
	for _, c := range cases {
		got := ParseImageRef(c.in)
		if got.Registry != c.registry || got.Repo != c.repo || got.Tag != c.tag || got.IsDigest != c.isDigest {
			t.Errorf("ParseImageRef(%q) = %+v, want registry=%q repo=%q tag=%q digest=%v",
				c.in, got, c.registry, c.repo, c.tag, c.isDigest)
		}
	}
}

func TestParseBearerChallenge(t *testing.T) {
	realm, params := parseBearerChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if realm != "https://auth.docker.io/token" {
		t.Errorf("realm = %q", realm)
	}
	if params["service"] != "registry.docker.io" {
		t.Errorf("service = %q", params["service"])
	}
	if params["scope"] != "repository:library/nginx:pull" {
		t.Errorf("scope = %q", params["scope"])
	}
}

func TestExtractJSONString(t *testing.T) {
	if got := extractJSONString([]byte(`{"token":"abc123","expires_in":300}`), "token"); got != "abc123" {
		t.Errorf("got %q", got)
	}
	if got := extractJSONString([]byte(`{"access_token":"xyz"}`), "access_token"); got != "xyz" {
		t.Errorf("got %q", got)
	}
	if got := extractJSONString([]byte(`{"nope":1}`), "token"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// TestFetchRemoteDigest_TokenDanceAndDigest 模擬「401 → 換 token → 帶 token
// HEAD 拿到摘要」的完整流程(用 httptest 假 registry + 假 auth)。
func TestFetchRemoteDigest_TokenDanceAndDigest(t *testing.T) {
	var auth *httptest.Server
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+auth.URL+`",service="reg",scope="repository:library/nginx:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		w.WriteHeader(http.StatusOK)
	}))
	defer reg.Close()
	auth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("service") != "reg" || !strings.Contains(r.URL.Query().Get("scope"), "library/nginx") {
			t.Errorf("token request missing service/scope: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"token":"tok-123"}`))
	}))
	defer auth.Close()

	ref := ImageRef{Registry: strings.TrimPrefix(reg.URL, "https://"), Repo: "library/nginx", Tag: "latest"}
	// reg.URL 是 http://127.0.0.1:port;FetchRemoteDigest 固定用 https://,所以
	// 這裡改用一個指到測試伺服器的 transport 來測流程邏輯。
	ref.Registry = strings.TrimPrefix(reg.URL, "http://")
	client := reg.Client()
	// 讓 https://<ref.Registry>/... 實際連到 httptest(它是 http),用自訂
	// transport 改寫 scheme。
	client.Transport = rewriteToHTTP{base: reg.Client().Transport}

	digest, err := FetchRemoteDigest(context.Background(), client, ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if digest != "sha256:deadbeef" {
		t.Errorf("digest = %q, want sha256:deadbeef", digest)
	}
}

// rewriteToHTTP 把 https 請求改寫成 http,讓測試能用 httptest(http)驗證
// FetchRemoteDigest 固定組出的 https URL 的流程邏輯。
type rewriteToHTTP struct{ base http.RoundTripper }

func (rt rewriteToHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		req.URL.Scheme = "http"
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
