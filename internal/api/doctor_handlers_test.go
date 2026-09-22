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
	// installErr,若非空,只讓「apt-get install」那一步回這個錯誤(update 照常
	// 成功),用來模擬「找不到套件」之類的離線情境。
	installErr error
}

func (d *doctorRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	d.calls = append(d.calls, name+" "+strings.Join(args, " "))
	if d.failCmd {
		return nil, errors.New("apt boom")
	}
	if d.installErr != nil && strings.Contains(strings.Join(args, " "), "apt-get install") {
		return nil, d.installErr
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

// TestHandleDoctorInstall_SingleFlight(第五十二輪 S-3):已經有一個安裝在跑
// (doctorInstalling 為 true)時,再進來的安裝請求必須直接回 409,而不是放
// 第二個 apt-get 去撞 dpkg 的獨佔鎖。
func TestHandleDoctorInstall_SingleFlight(t *testing.T) {
	s := newTestServer(t)
	dr := &doctorRunner{}
	s.runner = dr
	// 模擬「已經有一個安裝在進行中」。
	if !s.doctorInstalling.CompareAndSwap(false, true) {
		t.Fatal("expected the install flag to start unset")
	}
	defer s.doctorInstalling.Store(false)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"apt":"samba"}`)
	s.handleDoctorInstall(rec, httptest.NewRequest(http.MethodPost, "/", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while an install is in progress, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dr.calls) != 0 {
		t.Fatalf("apt must NOT run for a rejected concurrent install, got calls: %v", dr.calls)
	}
}

// TestHandleDoctorInstall_OfflinePackageNotFound(第五十三輪 實機):離線
// NAS 上 apt 找不到套件(「Unable to locate package」)時,要回 502 + 可行動
// 的訊息,而不是把 apt 的原始英文錯誤直接丟出去。
func TestHandleDoctorInstall_OfflinePackageNotFound(t *testing.T) {
	s := newTestServer(t)
	dr := &doctorRunner{installErr: errors.New("env [...] apt-get install: exit status 100 (stderr: E: Unable to locate package samba)")}
	s.runner = dr
	rec := httptest.NewRecorder()
	s.handleDoctorInstall(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"apt":"samba"}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when apt can't locate the package, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "current sources") && !strings.Contains(rec.Body.String(), "no internet") {
		t.Fatalf("expected an actionable offline/network message, got: %s", rec.Body.String())
	}
}

// TestHandleDoctorInstall_NoInstallationCandidate 覆蓋另一種 apt 常見講法。
func TestHandleDoctorInstall_NoInstallationCandidate(t *testing.T) {
	s := newTestServer(t)
	dr := &doctorRunner{installErr: errors.New("exit status 100 (stderr: E: Package 'nfs-kernel-server' has no installation candidate)")}
	s.runner = dr
	rec := httptest.NewRecorder()
	s.handleDoctorInstall(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"apt":"nfs-kernel-server"}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for 'no installation candidate', got %d: %s", rec.Code, rec.Body.String())
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
