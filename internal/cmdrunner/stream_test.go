package cmdrunner

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestRunStream_LinesAndCR(t *testing.T) {
	r := NewExecRunner().(StreamRunner)
	var mu sync.Mutex
	var lines []string
	out, err := r.RunStream(context.Background(), func(l string) {
		mu.Lock()
		lines = append(lines, l)
		mu.Unlock()
	}, "sh", "-c", `printf 'a\nprog 10%%\rprog 50%%\rprog 100%%\n'; echo err >&2`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "prog 10%", "prog 50%", "prog 100%", "err"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", lines, want)
	}
	if !strings.Contains(string(out), "err") {
		t.Errorf("combined output missing stderr: %q", out)
	}
	if _, err := r.RunStream(context.Background(), nil, "sh", "-c", "echo boom; exit 3"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failure should carry output tail, got %v", err)
	}
}
