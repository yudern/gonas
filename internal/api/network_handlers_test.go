package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/netdiag"
)

func TestNetworkTool_RejectsArgumentInjection(t *testing.T) {
	s := newTestServer(t)
	for _, target := range []string{"-c 1000 1.1.1.1", "--help", "a;rm -rf /", "$(id)", ""} {
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(netToolRequest{Tool: "ping", Target: target})
		s.handleNetworkTool(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("target %q: want 400, got %d", target, rec.Code)
		}
	}
}

func TestNetworkTool_PortCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.handleNetworkTool(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"tool":"port","target":"`+host+`","port":`+port+`}`)))
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("open port should be reported ok: %s", rec.Body.String())
	}
}

func TestNetworkDNS_SetViaHandler(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t)
	s.dnsCfg = &netdiag.DNSConfigurator{
		ResolvConf:   filepath.Join(dir, "resolv.conf"),
		DhclientConf: filepath.Join(dir, "dhclient.conf"),
		DhcpcdConf:   filepath.Join(dir, "dhcpcd.conf"),
		BackupPath:   filepath.Join(dir, "bak"),
	}
	os.WriteFile(s.dnsCfg.ResolvConf, []byte("nameserver 192.168.68.1\n"), 0o644)
	os.WriteFile(s.dnsCfg.DhclientConf, []byte(""), 0o644)

	rec := httptest.NewRecorder()
	s.handleNetworkDNSSet(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"servers":["223.5.5.5","119.29.29.29"]}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"managed":true`) {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.handleNetworkDNSSet(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"servers":["not-an-ip"]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid IP should be 400, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleNetworkDNSReset(rec, httptest.NewRequest(http.MethodDelete, "/", nil))
	b, _ := os.ReadFile(s.dnsCfg.ResolvConf)
	if rec.Code != http.StatusOK || string(b) != "nameserver 192.168.68.1\n" {
		t.Errorf("reset: %d %q", rec.Code, b)
	}
}

// 體檢在沙盒裡真的跑一次(網路受限也沒關係):只確認結構完整、不會卡住。
func TestNetworkDiagnose_Runs(t *testing.T) {
	oldD, oldH, oldT := diagDNSTimeout, diagHTTPTimeout, diagTCPTimeout
	diagDNSTimeout, diagHTTPTimeout, diagTCPTimeout = time.Second, 2*time.Second, time.Second
	defer func() { diagDNSTimeout, diagHTTPTimeout, diagTCPTimeout = oldD, oldH, oldT }()
	s := newTestServer(t)
	s.runner = &recordingRunner{}
	dir := t.TempDir()
	s.dnsCfg = &netdiag.DNSConfigurator{ResolvConf: filepath.Join(dir, "resolv.conf")}
	s.dockerDaemonJSONPath = filepath.Join(dir, "daemon.json")
	start := time.Now()
	d := s.diagnose(context.Background())
	if time.Since(start) > 15*time.Second {
		t.Errorf("diagnose took too long: %v", time.Since(start))
	}
	ids := map[string]bool{}
	for _, c := range d.Checks {
		ids[c.ID] = true
	}
	for _, want := range []string{"link", "internet", "dns", "https:dockerhub", "https:debian"} {
		if !ids[want] {
			t.Errorf("missing check %q in %+v", want, d.Checks)
		}
	}
	if d.Hints == nil {
		t.Error("hints must be a non-nil list")
	}
}
