package api

import (
	"errors"
	"net/http"
	"testing"
)

func TestDockerErrStatus(t *testing.T) {
	cases := []struct {
		msg  string
		dflt int
		want int
	}{
		{"Error: No such container: abc", http.StatusInternalServerError, http.StatusNotFound},
		{"no such image", http.StatusConflict, http.StatusNotFound},
		{"image is being used by running container", http.StatusConflict, http.StatusConflict},
		{"conflict: unable to remove", http.StatusInternalServerError, http.StatusConflict},
		{"something else entirely", http.StatusInternalServerError, http.StatusInternalServerError},
		{"transient daemon error", http.StatusConflict, http.StatusConflict},
	}
	for _, c := range cases {
		if got := dockerErrStatus(errors.New(c.msg), c.dflt); got != c.want {
			t.Errorf("dockerErrStatus(%q, %d) = %d, want %d", c.msg, c.dflt, got, c.want)
		}
	}
}
