// Package netdiag 是 GoNAS 的網路診斷工具集(第六十五輪,使用者實機:裝 App
// 時「lookup registry-1.docker.io on 192.168.68.1:53: i/o timeout」——NAS 的
// DNS(路由器)不回應,連鏡像加速地址也解析不了,但介面上完全看不出來是
// 網路/DNS 的問題)。
//
// 這裡只放「純讀取、不改系統」的探測:網卡/預設閘道/DNS 伺服器的讀取、
// 指定 DNS 伺服器解析、TCP 連線測試、HTTPS 探測。改 DNS 設定在 dnsconfig.go。
// 全部只用標準函式庫。
package netdiag

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Interface 是一張網卡的摘要。
type Interface struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac,omitempty"`
	Up    bool     `json:"up"`
	Addrs []string `json:"addrs"`
}

// skipIface 過濾掉 loopback 與 Docker/虛擬網卡,只留下實體/主要網卡。
func skipIface(name string) bool {
	for _, p := range []string{"lo", "docker", "br-", "veth", "virbr", "wg", "tun", "tap", "ifb", "dummy", "sit", "gre", "ip6tnl"} {
		if name == p || strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Interfaces 列出主要網卡與其位址。
func Interfaces() []Interface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Interface
	for _, i := range ifs {
		if skipIface(i.Name) {
			continue
		}
		it := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), Up: i.Flags&net.FlagUp != 0 && i.Flags&net.FlagRunning != 0, Addrs: []string{}}
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				it.Addrs = append(it.Addrs, a.String())
			}
		}
		out = append(out, it)
	}
	return out
}

// HasIPv4 回報是否有任何主要網卡是 up 且有 IPv4 位址。
func HasIPv4(ifs []Interface) bool {
	for _, i := range ifs {
		if !i.Up {
			continue
		}
		for _, a := range i.Addrs {
			if ip, _, err := net.ParseCIDR(a); err == nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() {
				return true
			}
		}
	}
	return false
}

// RouteFile 是 Linux 的 IPv4 路由表(var 方便測試)。
var RouteFile = "/proc/net/route"

// DefaultGateway 從 /proc/net/route 讀出預設閘道與它所在的網卡。
func DefaultGateway() (gw, iface string, err error) {
	f, err := os.Open(RouteFile)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	bestMetric := int64(-1)
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(fields[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		var metric int64
		fmt.Sscan(fields[6], &metric)
		if bestMetric >= 0 && metric >= bestMetric {
			continue
		}
		ip := make(net.IP, 4)
		binary.LittleEndian.PutUint32(ip, binary.BigEndian.Uint32(raw))
		gw, iface, bestMetric = ip.String(), fields[0], metric
	}
	if gw == "" {
		return "", "", errors.New("no default route")
	}
	return gw, iface, nil
}

// ReadNameservers 讀出 resolv.conf 格式檔案裡的 nameserver。
func ReadNameservers(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}

// ResolveVia 用指定的 DNS 伺服器(server 為空 = 系統設定)解析 host,回傳
// 位址與耗時。
func ResolveVia(ctx context.Context, server, host string, timeout time.Duration) ([]string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r := net.DefaultResolver
	if server != "" {
		addr := net.JoinHostPort(server, "53")
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}}
	}
	start := time.Now()
	ips, err := r.LookupHost(ctx, host)
	return ips, time.Since(start), err
}

// TCPReach 測試能不能在 timeout 內建立 TCP 連線。
func TCPReach(ctx context.Context, addr string, timeout time.Duration) (time.Duration, error) {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	c.Close()
	return time.Since(start), nil
}

// HTTPResult 是一次 HTTPS 探測的結果。
type HTTPResult struct {
	Status int
	Dur    time.Duration
	Date   time.Time // 伺服器回應的 Date 標頭(用來檢查本機時鐘)
}

// newProbeClient 建立探測用的 HTTP client:不跟隨轉址、不重用連線。
func newProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: timeout,
			DisableKeepAlives:   true,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ProbeHTTP 對 url 發一個 GET,任何 HTTP 回應(含 401/404)都算「連得到」。
func ProbeHTTP(ctx context.Context, url string, timeout time.Duration) (HTTPResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HTTPResult{}, err
	}
	req.Header.Set("User-Agent", "GoNAS-netdiag")
	start := time.Now()
	resp, err := newProbeClient(timeout).Do(req)
	if err != nil {
		return HTTPResult{}, err
	}
	resp.Body.Close()
	res := HTTPResult{Status: resp.StatusCode, Dur: time.Since(start)}
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		res.Date = d
	}
	return res, nil
}

// ExplainErr 把常見的網路錯誤歸類成穩定的代碼,給前端翻譯成人話。
func ExplainErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "dns_not_found"
	case errors.As(err, &dnsErr):
		return "dns_failed"
	case strings.Contains(s, "x509") || strings.Contains(s, "certificate"):
		return "tls_cert"
	case strings.Contains(s, "connection refused"):
		return "refused"
	case strings.Contains(s, "network is unreachable") || strings.Contains(s, "no route to host"):
		return "unreachable"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "connection reset") || strings.Contains(s, "eof"):
		return "reset"
	}
	return "other"
}

// ShortIPs 把解析結果濃縮成好讀的摘要:IPv4 優先、最多 n 個,其餘以「+N」表示。
func ShortIPs(ips []string, n int) string {
	var v4, v6 []string
	for _, ip := range ips {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	all := append(v4, v6...)
	if len(all) <= n {
		return strings.Join(all, ", ")
	}
	return strings.Join(all[:n], ", ") + fmt.Sprintf(" (+%d)", len(all)-n)
}
