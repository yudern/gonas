package docker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestContainerLogs_DecodesMultiplexedStream(t *testing.T) {
	var gotPath, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write(muxFrame(1, "server started\n"))
		_, _ = w.Write(muxFrame(2, "a warning on stderr\n"))
	})

	logs, err := c.ContainerLogs(context.Background(), "abc123", ContainerLogsOptions{Tail: "100", Timestamps: true})
	if err != nil {
		t.Fatalf("ContainerLogs returned error: %v", err)
	}

	if gotPath != "/containers/abc123/logs" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if !containsAll(gotQuery, "stdout=1", "stderr=1", "tail=100", "timestamps=1") {
		t.Errorf("unexpected query: %s", gotQuery)
	}

	want := "server started\na warning on stderr\n"
	if logs != want {
		t.Errorf("got %q, want %q", logs, want)
	}
}

func TestContainerLogs_DefaultsTailToAll(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
	})

	if _, err := c.ContainerLogs(context.Background(), "abc123", ContainerLogsOptions{}); err != nil {
		t.Fatalf("ContainerLogs returned error: %v", err)
	}
	if !containsAll(gotQuery, "tail=all") {
		t.Errorf("expected tail=all when Tail is empty, got query %q", gotQuery)
	}
}

func TestContainerLogs_DaemonErrorIsPropagated(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: abc123"}`))
	})

	if _, err := c.ContainerLogs(context.Background(), "abc123", ContainerLogsOptions{}); err == nil {
		t.Fatal("expected error for missing container, got nil")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
