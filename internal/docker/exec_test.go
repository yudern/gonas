package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestExecInContainer_FullRoundTrip(t *testing.T) {
	var createdCmd []string
	var startedID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/containers/abc123/exec":
			var req execCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decoding exec create body: %v", err)
			}
			createdCmd = req.Cmd
			if req.Tty {
				t.Error("expected Tty=false for exec create")
			}
			_, _ = w.Write([]byte(`{"Id":"exec789"}`))
		case r.Method == "POST" && r.URL.Path == "/exec/exec789/start":
			startedID = "exec789"
			_, _ = w.Write(muxFrame(1, "hello.txt\nworld.txt\n"))
		case r.Method == "GET" && r.URL.Path == "/exec/exec789/json":
			_, _ = w.Write([]byte(`{"Running":false,"ExitCode":0}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	result, err := c.ExecInContainer(context.Background(), "abc123", []string{"ls"})
	if err != nil {
		t.Fatalf("ExecInContainer returned error: %v", err)
	}
	if len(createdCmd) != 1 || createdCmd[0] != "ls" {
		t.Errorf("unexpected cmd sent to exec create: %v", createdCmd)
	}
	if startedID != "exec789" {
		t.Error("expected exec/exec789/start to be called")
	}
	if result.Output != "hello.txt\nworld.txt\n" {
		t.Errorf("unexpected output: %q", result.Output)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestExecInContainer_NonZeroExitCode(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/abc123/exec":
			_, _ = w.Write([]byte(`{"Id":"exec1"}`))
		case r.URL.Path == "/exec/exec1/start":
			_, _ = w.Write(muxFrame(2, "cat: /nope: No such file or directory\n"))
		case r.URL.Path == "/exec/exec1/json":
			_, _ = w.Write([]byte(`{"Running":false,"ExitCode":1}`))
		}
	})

	result, err := c.ExecInContainer(context.Background(), "abc123", []string{"cat", "/nope"})
	if err != nil {
		t.Fatalf("ExecInContainer returned error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if result.Output == "" {
		t.Error("expected non-empty stderr output to be captured")
	}
}

func TestExecInContainer_RejectsEmptyCommand(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("expected no request to be made for an empty command, got %s %s", r.Method, r.URL.Path)
	})

	_, err := c.ExecInContainer(context.Background(), "abc123", nil)
	if err != ErrExecEmptyCommand {
		t.Errorf("got err %v, want ErrExecEmptyCommand", err)
	}
}

func TestExecInContainer_CreateFailurePropagates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: abc123"}`))
	})

	if _, err := c.ExecInContainer(context.Background(), "abc123", []string{"ls"}); err == nil {
		t.Fatal("expected error when exec create fails, got nil")
	}
}
