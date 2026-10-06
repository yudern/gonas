package netdiag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// DNSConfigurator 負責「持久地」改 NAS 的 DNS 伺服器。Debian 上管 DNS 的
// 元件有好幾種,改錯地方的結果是「當下有效、DHCP 續租或重開機後又被蓋回去」,
// 所以先偵測目前是哪一種,再改對應的設定:
//
//	resolved       — systemd-resolved(resolv.conf 指向 127.0.0.53):寫
//	                 /etc/systemd/resolved.conf.d/gonas-dns.conf,重啟 resolved
//	networkmanager — NetworkManager 在跑:nmcli 改目前連線的 ipv4.dns 並
//	                 ignore-auto-dns,再 device reapply
//	resolvconf     — resolv.conf 由 resolvconf 套件產生:寫 head 檔、resolvconf -u
//	plain          — 一般 ifupdown + DHCP 用戶端(GoNAS 安裝映像的預設):
//	                 在 dhclient.conf / dhcpcd.conf 加「GoNAS 管理區塊」讓 DHCP
//	                 不再覆寫 DNS,並立即改寫 /etc/resolv.conf(第一次改之前先備份)
//
// 所有路徑都是欄位,測試指到暫存目錄;Run 用來執行系統指令(測試換成假的)。
type DNSConfigurator struct {
	ResolvConf     string
	ResolvedDropin string
	ResolvconfHead string
	DhclientConf   string
	DhcpcdConf     string
	BackupPath     string
	Run            func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// DefaultDNSConfigurator 回傳正式環境的路徑設定。
func DefaultDNSConfigurator(run func(ctx context.Context, name string, args ...string) ([]byte, error), backupDir string) *DNSConfigurator {
	return &DNSConfigurator{
		ResolvConf:     "/etc/resolv.conf",
		ResolvedDropin: "/etc/systemd/resolved.conf.d/gonas-dns.conf",
		ResolvconfHead: "/etc/resolvconf/resolv.conf.d/head",
		DhclientConf:   "/etc/dhcp/dhclient.conf",
		DhcpcdConf:     "/etc/dhcpcd.conf",
		BackupPath:     filepath.Join(backupDir, "resolv.conf.before-gonas"),
		Run:            run,
	}
}

const (
	blockBegin = "# BEGIN GoNAS DNS (managed by the GoNAS web UI — edit there)"
	blockEnd   = "# END GoNAS DNS"
)

// DNSState 是目前 DNS 設定的摘要。
type DNSState struct {
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"` // 目前實際在用的上游 DNS
	Managed bool     `json:"managed"` // 是否是 GoNAS 設定的(而不是 DHCP 自動取得)
}

// Mode 偵測目前由誰管理 DNS。
func (c *DNSConfigurator) Mode(ctx context.Context) string {
	if target, err := os.Readlink(c.ResolvConf); err == nil {
		switch {
		case strings.Contains(target, "systemd/resolve"):
			return "resolved"
		case strings.Contains(target, "resolvconf"):
			return "resolvconf"
		}
	}
	ns := ReadNameservers(c.ResolvConf)
	if len(ns) == 1 && ns[0] == "127.0.0.53" {
		return "resolved"
	}
	if c.Run != nil {
		if out, err := c.Run(ctx, "systemctl", "is-active", "NetworkManager"); err == nil && strings.TrimSpace(string(out)) == "active" {
			return "networkmanager"
		}
	}
	return "plain"
}

// State 回傳目前的 DNS 設定摘要。
func (c *DNSConfigurator) State(ctx context.Context) DNSState {
	mode := c.Mode(ctx)
	st := DNSState{Mode: mode, Servers: ReadNameservers(c.ResolvConf)}
	switch mode {
	case "resolved":
		if ns := ReadNameservers("/run/systemd/resolve/resolv.conf"); len(ns) > 0 {
			st.Servers = ns
		}
		_, err := os.Stat(c.ResolvedDropin)
		st.Managed = err == nil
	case "resolvconf":
		st.Managed = fileHasBlock(c.ResolvconfHead)
	case "networkmanager":
		st.Managed = false
		if c.Run != nil {
			if out, err := c.Run(ctx, "nmcli", "-t", "-f", "IP4.DNS", "device", "show"); err == nil {
				var ns []string
				for _, l := range strings.Split(string(out), "\n") {
					if i := strings.Index(l, ":"); i >= 0 && strings.HasPrefix(l, "IP4.DNS") {
						ns = append(ns, strings.TrimSpace(l[i+1:]))
					}
				}
				if len(ns) > 0 {
					st.Servers = ns
				}
			}
		}
	default:
		st.Managed = fileHasBlock(c.DhclientConf) || fileHasBlock(c.DhcpcdConf) || fileContains(c.ResolvConf, blockBegin)
	}
	if st.Servers == nil {
		st.Servers = []string{}
	}
	return st
}

// ValidateServers 確認是 1~3 個合法 IP。
func ValidateServers(servers []string) ([]string, error) {
	var out []string
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if net.ParseIP(s) == nil {
			return nil, fmt.Errorf("%q is not a valid IP address", s)
		}
		out = append(out, s)
	}
	if len(out) == 0 || len(out) > 3 {
		return nil, errors.New("specify between 1 and 3 DNS server IP addresses")
	}
	return out, nil
}

// Set 持久地把 DNS 改成 servers。
func (c *DNSConfigurator) Set(ctx context.Context, servers []string) error {
	servers, err := ValidateServers(servers)
	if err != nil {
		return err
	}
	switch c.Mode(ctx) {
	case "resolved":
		body := "# Written by GoNAS (web UI → Network → DNS)\n[Resolve]\nDNS=" + strings.Join(servers, " ") + "\nDomains=~.\n"
		if err := writeFileMkdir(c.ResolvedDropin, body); err != nil {
			return err
		}
		return c.run(ctx, "systemctl", "restart", "systemd-resolved")
	case "networkmanager":
		return c.nmSet(ctx, strings.Join(servers, " "), "yes")
	case "resolvconf":
		if err := replaceBlock(c.ResolvconfHead, nameserverLines(servers)); err != nil {
			return err
		}
		return c.run(ctx, "resolvconf", "-u")
	}
	// plain
	if fileExists(c.DhclientConf) {
		if err := replaceBlock(c.DhclientConf, "supersede domain-name-servers "+strings.Join(servers, ", ")+";\n"); err != nil {
			return err
		}
	}
	if fileExists(c.DhcpcdConf) {
		if err := replaceBlock(c.DhcpcdConf, "static domain_name_servers="+strings.Join(servers, " ")+"\n"); err != nil {
			return err
		}
	}
	return c.writeResolvConf(servers)
}

// Reset 移除 GoNAS 的設定,回到自動(DHCP)取得 DNS。
func (c *DNSConfigurator) Reset(ctx context.Context) error {
	switch c.Mode(ctx) {
	case "resolved":
		if err := os.Remove(c.ResolvedDropin); err != nil && !os.IsNotExist(err) {
			return err
		}
		return c.run(ctx, "systemctl", "restart", "systemd-resolved")
	case "networkmanager":
		return c.nmSet(ctx, "", "no")
	case "resolvconf":
		if err := replaceBlock(c.ResolvconfHead, ""); err != nil {
			return err
		}
		return c.run(ctx, "resolvconf", "-u")
	}
	for _, f := range []string{c.DhclientConf, c.DhcpcdConf} {
		if fileExists(f) {
			if err := replaceBlock(f, ""); err != nil {
				return err
			}
		}
	}
	// 還原第一次修改前的 resolv.conf(DHCP 給的那份);沒有備份就保留現狀,
	// 等下次 DHCP 續租時由 DHCP 用戶端重寫。
	if b, err := os.ReadFile(c.BackupPath); err == nil {
		if err := os.WriteFile(c.ResolvConf, b, 0o644); err != nil {
			return err
		}
		_ = os.Remove(c.BackupPath)
	}
	return nil
}

func (c *DNSConfigurator) run(ctx context.Context, name string, args ...string) error {
	if c.Run == nil {
		return nil
	}
	if out, err := c.Run(ctx, name, args...); err != nil {
		return fmt.Errorf("%s %s: %w %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// nmSet 對每個「有實體裝置」的作用中連線改 DNS,並 reapply。
func (c *DNSConfigurator) nmSet(ctx context.Context, dns, ignoreAuto string) error {
	if c.Run == nil {
		return errors.New("NetworkManager mode needs a command runner")
	}
	out, err := c.Run(ctx, "nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active")
	if err != nil {
		return fmt.Errorf("listing NetworkManager connections: %w", err)
	}
	changed := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		i := strings.LastIndex(l, ":")
		if i <= 0 {
			continue
		}
		name, dev := l[:i], l[i+1:]
		if dev == "" || skipIface(dev) {
			continue
		}
		if err := c.run(ctx, "nmcli", "connection", "modify", name, "ipv4.dns", dns, "ipv4.ignore-auto-dns", ignoreAuto); err != nil {
			return err
		}
		if err := c.run(ctx, "nmcli", "device", "reapply", dev); err != nil {
			return err
		}
		changed++
	}
	if changed == 0 {
		return errors.New("no active NetworkManager connection found")
	}
	return nil
}

// writeResolvConf 立即改寫 /etc/resolv.conf:保留 search/domain/options,
// 換掉 nameserver。第一次改之前先備份原檔,給 Reset 還原。
func (c *DNSConfigurator) writeResolvConf(servers []string) error {
	if fi, err := os.Lstat(c.ResolvConf); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		// 是連結(由其他元件管理)就不硬寫,前面的模式判斷應已處理。
		return fmt.Errorf("%s is a symlink managed by another component", c.ResolvConf)
	}
	orig, _ := os.ReadFile(c.ResolvConf)
	if !fileExists(c.BackupPath) && len(orig) > 0 && !strings.Contains(string(orig), blockBegin) {
		if err := writeFileMkdir(c.BackupPath, string(orig)); err != nil {
			return fmt.Errorf("backing up %s: %w", c.ResolvConf, err)
		}
	}
	var keep []string
	inBlock := false
	for _, l := range strings.Split(string(orig), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == blockBegin:
			inBlock = true
			continue
		case t == blockEnd:
			inBlock = false
			continue
		case inBlock:
			continue
		}
		f := strings.Fields(t)
		if len(f) > 0 && (f[0] == "search" || f[0] == "domain" || f[0] == "options") {
			keep = append(keep, t)
		}
	}
	body := blockBegin + "\n" + nameserverLines(servers) + blockEnd + "\n"
	if len(keep) > 0 {
		body += strings.Join(keep, "\n") + "\n"
	}
	tmp := c.ResolvConf + ".gonas-tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.ResolvConf)
}

func nameserverLines(servers []string) string {
	var b strings.Builder
	for _, s := range servers {
		b.WriteString("nameserver " + s + "\n")
	}
	return b.String()
}

// replaceBlock 把檔案裡的 GoNAS 管理區塊換成 content(content 空 = 移除區塊)。
// 檔案不存在時,content 非空才建立。
func replaceBlock(path, content string) error {
	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out []string
	inBlock := false
	for _, l := range strings.Split(string(orig), "\n") {
		t := strings.TrimSpace(l)
		if t == blockBegin {
			inBlock = true
			continue
		}
		if t == blockEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			out = append(out, l)
		}
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if content != "" {
		if text != "" {
			text += "\n\n"
		}
		text += blockBegin + "\n" + content + blockEnd
	}
	if text != "" {
		text += "\n"
	}
	if len(orig) == 0 && content == "" {
		return nil
	}
	return writeFileMkdir(path, text)
}

func writeFileMkdir(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func fileContains(p, sub string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains(string(b), sub)
}

func fileHasBlock(p string) bool { return fileContains(p, blockBegin) }
