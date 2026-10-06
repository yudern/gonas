package netdiag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultGateway_ParsesProcRoute(t *testing.T) {
	f := filepath.Join(t.TempDir(), "route")
	body := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"enp1s0\t00000000\t0144A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"enp1s0\t0044A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	os.WriteFile(f, []byte(body), 0o644)
	old := RouteFile
	RouteFile = f
	defer func() { RouteFile = old }()
	gw, iface, err := DefaultGateway()
	if err != nil || gw != "192.168.68.1" || iface != "enp1s0" {
		t.Fatalf("got %q %q %v", gw, iface, err)
	}
}

func newCfg(t *testing.T) (*DNSConfigurator, string, *[]string) {
	dir := t.TempDir()
	var calls []string
	c := &DNSConfigurator{
		ResolvConf:     filepath.Join(dir, "resolv.conf"),
		ResolvedDropin: filepath.Join(dir, "resolved.conf.d/gonas-dns.conf"),
		ResolvconfHead: filepath.Join(dir, "resolvconf/head"),
		DhclientConf:   filepath.Join(dir, "dhclient.conf"),
		DhcpcdConf:     filepath.Join(dir, "dhcpcd.conf"),
		BackupPath:     filepath.Join(dir, "backup/resolv.conf.before-gonas"),
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return []byte("inactive\n"), nil
		},
	}
	return c, dir, &calls
}

func TestDNS_PlainSetAndReset(t *testing.T) {
	c, _, _ := newCfg(t)
	orig := "domain lan\nsearch lan\nnameserver 192.168.68.1\n"
	os.WriteFile(c.ResolvConf, []byte(orig), 0o644)
	os.WriteFile(c.DhclientConf, []byte("request subnet-mask, routers;\n"), 0o644)
	ctx := context.Background()

	if m := c.Mode(ctx); m != "plain" {
		t.Fatalf("mode = %s", m)
	}
	if err := c.Set(ctx, []string{"223.5.5.5", "119.29.29.29"}); err != nil {
		t.Fatal(err)
	}
	rc, _ := os.ReadFile(c.ResolvConf)
	if !strings.Contains(string(rc), "nameserver 223.5.5.5\nnameserver 119.29.29.29") || strings.Contains(string(rc), "192.168.68.1") || !strings.Contains(string(rc), "search lan") {
		t.Errorf("resolv.conf = %q", rc)
	}
	dh, _ := os.ReadFile(c.DhclientConf)
	if !strings.Contains(string(dh), "supersede domain-name-servers 223.5.5.5, 119.29.29.29;") || !strings.Contains(string(dh), "request subnet-mask") {
		t.Errorf("dhclient.conf = %q", dh)
	}
	st := c.State(ctx)
	if !st.Managed || strings.Join(st.Servers, ",") != "223.5.5.5,119.29.29.29" {
		t.Errorf("state = %+v", st)
	}
	// 再設一次:區塊被替換而不是重複。
	if err := c.Set(ctx, []string{"8.8.8.8"}); err != nil {
		t.Fatal(err)
	}
	dh, _ = os.ReadFile(c.DhclientConf)
	if strings.Count(string(dh), blockBegin) != 1 || strings.Contains(string(dh), "223.5.5.5") {
		t.Errorf("block not replaced: %q", dh)
	}
	if err := c.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	rc, _ = os.ReadFile(c.ResolvConf)
	if string(rc) != orig {
		t.Errorf("reset should restore original resolv.conf, got %q", rc)
	}
	dh, _ = os.ReadFile(c.DhclientConf)
	if strings.Contains(string(dh), blockBegin) || !strings.Contains(string(dh), "request subnet-mask") {
		t.Errorf("reset dhclient.conf = %q", dh)
	}
}

func TestDNS_ResolvedMode(t *testing.T) {
	c, dir, calls := newCfg(t)
	stub := filepath.Join(dir, "systemd/resolve/stub-resolv.conf")
	os.MkdirAll(filepath.Dir(stub), 0o755)
	os.WriteFile(stub, []byte("nameserver 127.0.0.53\n"), 0o644)
	os.Symlink(stub, c.ResolvConf)
	ctx := context.Background()
	if m := c.Mode(ctx); m != "resolved" {
		t.Fatalf("mode = %s", m)
	}
	if err := c.Set(ctx, []string{"223.5.5.5"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.ResolvedDropin)
	if !strings.Contains(string(b), "DNS=223.5.5.5") || !strings.Contains(string(b), "Domains=~.") {
		t.Errorf("dropin = %q", b)
	}
	if !strings.Contains(strings.Join(*calls, "|"), "systemctl restart systemd-resolved") {
		t.Errorf("calls = %v", *calls)
	}
}

func TestValidateServers(t *testing.T) {
	if _, err := ValidateServers([]string{"1.2.3.4", "bad"}); err == nil {
		t.Error("expected invalid IP error")
	}
	if _, err := ValidateServers(nil); err == nil {
		t.Error("expected error for empty list")
	}
	if got, err := ValidateServers([]string{" 223.5.5.5 ", "", "2400:3200::1"}); err != nil || len(got) != 2 {
		t.Errorf("got %v %v", got, err)
	}
}

func TestShortIPs(t *testing.T) {
	got := ShortIPs([]string{"2600::1", "1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"}, 3)
	if got != "1.1.1.1, 2.2.2.2, 3.3.3.3 (+2)" {
		t.Errorf("got %q", got)
	}
}
