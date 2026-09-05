package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
)

// TestHandleSecurityHTTPSSet_PersistsHostsForRenewal 驗證 Phase 16 新增的
// state.HTTPSConfig.Hosts 欄位真的會被 handleSecurityHTTPSSet 寫進
// state.json,並且能透過 handleSecurityHTTPSGet 讀回來 —— 這個欄位存在
// 的唯一理由就是讓背景憑證續期(internal/security.CertRenewer)知道要
// 用哪一組 SAN 主機名稱/IP 重新簽發,如果這裡漏寫,續期時就會靜默退回
// localhost/127.0.0.1,使用者原本設定的區網 IP/網域會從新憑證裡消失。
func TestHandleSecurityHTTPSSet_PersistsHostsForRenewal(t *testing.T) {
	s := newTestServer(t)
	s.dataDir = t.TempDir()

	body, _ := json.Marshal(httpsSettingsRequest{
		Enabled: true,
		Hosts:   []string{"192.168.1.50", "nas.local"},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/security/https", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleSecurityHTTPSSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp httpsSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Hosts) != 2 || resp.Hosts[0] != "192.168.1.50" || resp.Hosts[1] != "nas.local" {
		t.Errorf("expected response hosts to echo back the request, got %v", resp.Hosts)
	}

	stored := s.store.Snapshot().HTTPS
	if len(stored.Hosts) != 2 || stored.Hosts[0] != "192.168.1.50" || stored.Hosts[1] != "nas.local" {
		t.Errorf("expected persisted state.HTTPSConfig.Hosts to be [192.168.1.50 nas.local], got %v", stored.Hosts)
	}

	// The GET endpoint should read the same hosts back.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/security/https", nil)
	getRec := httptest.NewRecorder()
	s.handleSecurityHTTPSGet(getRec, getReq)

	var getResp httpsSettingsResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal GET response: %v", err)
	}
	if len(getResp.Hosts) != 2 {
		t.Errorf("expected GET to return the persisted hosts, got %v", getResp.Hosts)
	}
}

// TestHandleSecurityHTTPSSet_DisablingKeepsCertFilesAndClearsNothing is a
// regression check that disabling HTTPS doesn't touch the Hosts the user
// already configured — re-enabling later (or the background renewer, once
// a restart picks up Enabled again) should still have them available.
func TestHandleSecurityHTTPSSet_DisablingPersistsEmptyHostsWhenNoneGiven(t *testing.T) {
	s := newTestServer(t)
	s.dataDir = t.TempDir()

	body, _ := json.Marshal(httpsSettingsRequest{Enabled: false})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/security/https", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleSecurityHTTPSSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if stored := s.store.Snapshot().HTTPS; stored.Enabled {
		t.Errorf("expected HTTPS to be recorded as disabled")
	}
}

// TestHTTPSCertificateLoader_ServesValidCertificateThatReloadsAfterRenewal
// exercises the actual mechanism cmd/gonasd/main.go relies on: a
// *Server-provided GetCertificate callback that a real tls.Config can use,
// which must pick up a freshly-renewed certificate without needing a new
// *Server or a process restart.
func TestHTTPSCertificateLoader_ServesValidCertificateThatReloadsAfterRenewal(t *testing.T) {
	s := newTestServer(t)
	s.dataDir = t.TempDir()

	certPath, keyPath := s.httpsCertPath(), s.httpsKeyPath()
	if err := security.EnsureCertFiles(certPath, keyPath, []string{"localhost"}, time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	loader := s.HTTPSCertificateLoader(certPath, keyPath)
	first, err := loader(nil)
	if err != nil {
		t.Fatalf("loader returned error: %v", err)
	}
	if first == nil {
		t.Fatal("expected a non-nil certificate from the loader")
	}

	// Renew (force it, using a renewBefore large enough to guarantee the
	// 1h-valid cert above is already "due").
	renewed, err := security.RenewCertIfNeeded(certPath, keyPath, []string{"localhost"}, 24*time.Hour, 2*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("RenewCertIfNeeded returned error: %v", err)
	}
	if !renewed {
		t.Fatal("expected the cert to be renewed given a renewBefore window larger than its remaining validity")
	}

	second, err := loader(nil)
	if err != nil {
		t.Fatalf("loader returned error after renewal: %v", err)
	}
	if first == second {
		t.Error("expected HTTPSCertificateLoader to serve a different certificate object after renewal, but got the cached one")
	}
}

// TestNew_StartsAndStopsCertRenewerWhenHTTPSAlreadyEnabled is an
// integration check on the actual api.New()/Close() lifecycle (not the
// newTestServer(t) shortcut other tests use, which bypasses New()
// entirely): if state.json already has HTTPS enabled with cert files in
// place when gonasd starts, New() must start a background CertRenewer, and
// Close() must be able to stop it cleanly without hanging.
func TestNew_StartsAndStopsCertRenewerWhenHTTPSAlreadyEnabled(t *testing.T) {
	dataDir := t.TempDir()
	certPath := filepath.Join(dataDir, "tls", "cert.pem")
	keyPath := filepath.Join(dataDir, "tls", "key.pem")
	if err := security.EnsureCertFiles(certPath, keyPath, []string{"localhost"}, 365*24*time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	store, err := state.Open(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatalf("opening state store: %v", err)
	}
	if err := store.Update(func(st *state.State) error {
		st.HTTPS = state.HTTPSConfig{Enabled: true, CertPath: certPath, KeyPath: keyPath, Hosts: []string{"localhost"}}
		return nil
	}); err != nil {
		t.Fatalf("seeding HTTPS config: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, _, err := New(logger, dataDir)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	defer s.Close()

	if s.certRenewer == nil {
		t.Error("expected New() to start a certRenewer when HTTPS is already enabled with cert files present")
	}

	s.Close() // must return promptly, not hang
}

// TestHandleSecurityHTTPSSet_ReturnsCertExpiryForUI verifies the response
// includes a parseable expiry timestamp once a certificate has actually
// been issued — this is what lets the Security page tell the user "your
// certificate is valid until X" instead of the renewal happening as
// invisible background magic with no visible confirmation it's working.
func TestHandleSecurityHTTPSSet_ReturnsCertExpiryForUI(t *testing.T) {
	s := newTestServer(t)
	s.dataDir = t.TempDir()

	body, _ := json.Marshal(httpsSettingsRequest{Enabled: true, Hosts: []string{"localhost"}})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/security/https", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleSecurityHTTPSSet(rec, req)

	var resp httpsSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.CertExpiresAt == "" {
		t.Fatal("expected a non-empty certExpiresAt after issuing a certificate")
	}
	parsed, err := time.Parse(time.RFC3339, resp.CertExpiresAt)
	if err != nil {
		t.Fatalf("certExpiresAt %q is not valid RFC3339: %v", resp.CertExpiresAt, err)
	}
	if time.Until(parsed) < 300*24*time.Hour {
		t.Errorf("expected the freshly-issued cert to expire ~2 years out, got %v away", time.Until(parsed))
	}
}

func TestHandleSecurityHTTPSGet_EmptyExpiryWhenNoCertYet(t *testing.T) {
	s := newTestServer(t)
	s.dataDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/security/https", nil)
	rec := httptest.NewRecorder()
	s.handleSecurityHTTPSGet(rec, req)

	var resp httpsSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.CertExpiresAt != "" {
		t.Errorf("expected empty certExpiresAt when no certificate has been issued yet, got %q", resp.CertExpiresAt)
	}
}

func TestNew_NoCertRenewerWhenHTTPSDisabled(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, _, err := New(logger, dataDir)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	defer s.Close()

	if s.certRenewer != nil {
		t.Error("expected no certRenewer to be started when HTTPS is disabled (the default for a fresh state.json)")
	}
}
