package api

import (
	"net/http"
	"testing"
)

// 第五十八輪資安覆核(#6)的回歸測試:sameOriginRequest 對同源放行、對跨站
// 擋下、兩個標頭都沒有時放行(非瀏覽器客戶端)。
func TestSameOriginRequest(t *testing.T) {
	mk := func(origin, referer string) *http.Request {
		r, _ := http.NewRequest("POST", "http://nas.local/api/v1/storage/pool", nil)
		r.Host = "nas.local"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		return r
	}

	cases := []struct {
		name    string
		origin  string
		referer string
		want    bool
	}{
		{"same-origin Origin", "http://nas.local", "", true},
		{"cross-origin Origin", "http://evil.example", "", false},
		{"Origin present beats Referer (cross)", "http://evil.example", "http://nas.local/x", false},
		{"no Origin, same-origin Referer", "", "http://nas.local/dashboard", true},
		{"no Origin, cross Referer", "", "http://evil.example/x", false},
		{"neither header (non-browser client) allowed", "", "", true},
		{"malformed Origin blocked", "://bad", "", false},
	}
	for _, c := range cases {
		if got := sameOriginRequest(mk(c.origin, c.referer)); got != c.want {
			t.Errorf("%s: sameOriginRequest = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsSafeHTTPMethod(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isSafeHTTPMethod(m) {
			t.Errorf("%s should be a safe method", m)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if isSafeHTTPMethod(m) {
			t.Errorf("%s should NOT be a safe method (it changes state)", m)
		}
	}
}
