package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type powerRunner struct {
	calls   []string
	failCmd bool
}

func (p *powerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	p.calls = append(p.calls, name+" "+args[0])
	if p.failCmd {
		return nil, errors.New("systemctl boom")
	}
	return nil, nil
}
func (p *powerRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected")
}

func TestHandleSystemPower(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    func(*Server, http.ResponseWriter, *http.Request)
		wantCmd string
	}{
		{"shutdown", (*Server).handleSystemPowerShutdown, "systemctl poweroff"},
		{"reboot", (*Server).handleSystemPowerReboot, "systemctl reboot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			pr := &powerRunner{}
			s.runner = pr
			rec := httptest.NewRecorder()
			tc.call(s, rec, httptest.NewRequest(http.MethodPost, "/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if len(pr.calls) != 1 || pr.calls[0] != tc.wantCmd {
				t.Fatalf("expected %q, got %v", tc.wantCmd, pr.calls)
			}
		})
	}
}

func TestHandleSystemPower_FailureReports500(t *testing.T) {
	s := newTestServer(t)
	s.runner = &powerRunner{failCmd: true}
	rec := httptest.NewRecorder()
	s.handleSystemPowerReboot(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on command failure, got %d", rec.Code)
	}
}
