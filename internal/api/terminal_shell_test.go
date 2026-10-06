package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPickContainerShell(t *testing.T) {
	cases := []struct {
		name    string
		exists  map[string]bool
		want    string
		noShell bool
	}{
		{"bash preferred", map[string]bool{"/bin/bash": true, "/bin/sh": true}, "/bin/bash", false},
		{"sh only", map[string]bool{"/bin/sh": true}, "/bin/sh", false},
		{"busybox", map[string]bool{"/busybox/sh": true}, "/busybox/sh", false},
		{"distroless", map[string]bool{}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDocker(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/archive") {
					if tc.exists[r.URL.Query().Get("path")] {
						w.WriteHeader(http.StatusOK)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
					return
				}
				// inspect:容器存在
				w.Write([]byte(`{"Id":"abc","State":{"Running":true}}`))
			})
			got, no := s.pickContainerShell(t.Context(), "abc")
			if got != tc.want || no != tc.noShell {
				t.Errorf("got (%q,%v), want (%q,%v)", got, no, tc.want, tc.noShell)
			}
		})
	}
}
