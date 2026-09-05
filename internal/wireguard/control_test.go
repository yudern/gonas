package wireguard

import (
	"context"
	"errors"
	"testing"
)

// fakeRunner 記錄每一次 Run 呼叫的指令與參數,讓測試斷言 Up/Down/Status
// 組出來的指令列正確,不需要真的執行 wg-quick/wg(這台開發機沒有裝
// wireguard-tools,見套件開頭註解)。這是跟 internal/storage、
// internal/share 的既有測試同一個手法。
type fakeRunner struct {
	calls   []call
	output  []byte
	failErr error
}

type call struct {
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name: name, args: args})
	if f.failErr != nil {
		return nil, f.failErr
	}
	return f.output, nil
}

func (f *fakeRunner) RunWithStdin(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

func TestUp_RunsWgQuickUpWithConfPath(t *testing.T) {
	r := &fakeRunner{}
	if err := Up(context.Background(), r, "/etc/wireguard/wg0.conf"); err != nil {
		t.Fatalf("Up returned error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected exactly 1 command, got %d: %+v", len(r.calls), r.calls)
	}
	got := r.calls[0]
	if got.name != "wg-quick" {
		t.Errorf("command = %q, want wg-quick", got.name)
	}
	wantArgs := []string{"up", "/etc/wireguard/wg0.conf"}
	if !equalStrings(got.args, wantArgs) {
		t.Errorf("args = %v, want %v", got.args, wantArgs)
	}
}

func TestUp_PropagatesRunnerError(t *testing.T) {
	r := &fakeRunner{failErr: errors.New("wg-quick: command not found")}
	err := Up(context.Background(), r, "/etc/wireguard/wg0.conf")
	if err == nil {
		t.Fatal("expected Up to propagate the runner's error")
	}
}

func TestDown_RunsWgQuickDownWithConfPath(t *testing.T) {
	r := &fakeRunner{}
	if err := Down(context.Background(), r, "/etc/wireguard/wg0.conf"); err != nil {
		t.Fatalf("Down returned error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected exactly 1 command, got %d: %+v", len(r.calls), r.calls)
	}
	got := r.calls[0]
	if got.name != "wg-quick" {
		t.Errorf("command = %q, want wg-quick", got.name)
	}
	wantArgs := []string{"down", "/etc/wireguard/wg0.conf"}
	if !equalStrings(got.args, wantArgs) {
		t.Errorf("args = %v, want %v", got.args, wantArgs)
	}
}

func TestStatus_RunsWgShowAndReturnsOutput(t *testing.T) {
	r := &fakeRunner{output: []byte("interface: wg0\n  public key: abc\n  listening port: 51820\n")}
	out, err := Status(context.Background(), r, "wg0")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if out != string(r.output) {
		t.Errorf("Status output = %q, want %q", out, string(r.output))
	}
	if len(r.calls) != 1 || r.calls[0].name != "wg" {
		t.Fatalf("expected a single `wg` command, got %+v", r.calls)
	}
	wantArgs := []string{"show", "wg0"}
	if !equalStrings(r.calls[0].args, wantArgs) {
		t.Errorf("args = %v, want %v", r.calls[0].args, wantArgs)
	}
}

func TestStatus_PropagatesRunnerError(t *testing.T) {
	r := &fakeRunner{failErr: errors.New("wg: command not found")}
	_, err := Status(context.Background(), r, "wg0")
	if err == nil {
		t.Fatal("expected Status to propagate the runner's error")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
