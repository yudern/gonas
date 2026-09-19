package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type doctorRunner struct {
	calls   []string
	failCmd bool
}

func (d *doctorRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	d.calls = append(d.calls, name+" "+strings.Join(args, " "))
	if d.failCmd {
		return nil, errors.New("apt boom")
	}
	return nil, nil
}
func (d *doctorRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected")
}

func TestHandleDoctorInstall_RejectsNonAllowlistedPackage(t *testing.T) {
	s := newTestServer(t)
	dr := &doctorRunner{}
	s.runner = dr
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"apt":"evil-package; rm -rf /"}`)
	s.handleDoctorInstall(rec, httptest.NewRequest(http.MethodPost, "/", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-allowlisted package, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dr.calls) != 0 {
		t.Fatalf("apt must NOT run for a non-allowlisted package, got calls: %v", dr.calls)
	}
}

func TestHandleDoctorInstall_InstallsAllowlistedPackage(t *testing.T) {
	s := newTestServer(t)
	dr := &doctorRunner{}
	s.runner = dr
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"apt":"samba"}`)
	s.handleDoctorInstall(rec, httptest.NewRequest(http.MethodPost, "/", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// 應該真的有一條 apt-get install ... samba 的呼叫
	found := false
	for _, c := range dr.calls {
		if strings.Contains(c, "apt-get install") && strings.HasSuffix(c, "samba") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an apt-get install ... samba call, got: %v", dr.calls)
	}
}

func TestHandleDoctorStatus_OK(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.handleDoctorStatus(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"apt"`) {
		t.Fatalf("expected package status JSON, got: %s", rec.Body.String())
	}
}
