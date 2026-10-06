package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/netdiag"
)

// 第六十五輪:網路診斷頁(使用者:「是不是還差一個檢測 NAS 有沒有網路的
// 工具及周邊其他工具」)。實機問題是 NAS 的 DNS(路由器 192.168.68.1)不回應,
// 導致 Docker Hub、鏡像加速地址全部解析不了,但介面上只看到「拉鏡像失敗」。
// 這頁提供:一鍵體檢(每一層都測、紅綠燈 + 原因 + 建議)、DNS 設定(持久)、
// DNS / 鏡像加速地址測速、ping / 埠 / 路由追蹤 / DNS 查詢 / HTTP 小工具。

// publicDNSCandidates 是 DNS 測速的候選公共 DNS(國內優先)。
var publicDNSCandidates = []string{"223.5.5.5", "223.6.6.6", "119.29.29.29", "180.76.76.76", "114.114.114.114", "1.1.1.1", "8.8.8.8"}

// mirrorCandidates 是 Docker Hub 鏡像加速地址的候選清單(2026 年常見的公共
// 加速源;可用性經常變動,所以一律以實測結果為準,不保證任何一個長期可用)。
var mirrorCandidates = []string{
	"https://docker.m.daocloud.io",
	"https://docker.1ms.run",
	"https://docker.xuanyuan.me",
	"https://docker.1panel.live",
	"https://hub.rat.dev",
}

// dnsTestHost 是測 DNS 用的網域(Docker Hub 的 registry,正是實機解析失敗的那個)。
const dnsTestHost = "registry-1.docker.io"

// dnsConfigurator 回傳 DNS 設定器;測試可以用 s.dnsCfg 換成指向暫存目錄的版本。
func (s *Server) dnsConfigurator() *netdiag.DNSConfigurator {
	if s.dnsCfg != nil {
		return s.dnsCfg
	}
	return netdiag.DefaultDNSConfigurator(s.runner.Run, s.dataDir)
}

type networkOverview struct {
	Hostname     string              `json:"hostname"`
	Interfaces   []netdiag.Interface `json:"interfaces"`
	Gateway      string              `json:"gateway,omitempty"`
	GatewayIface string              `json:"gatewayIface,omitempty"`
	DNS          netdiag.DNSState    `json:"dns"`
}

// handleNetworkOverview:GET /api/v1/network。
func (s *Server) handleNetworkOverview(w http.ResponseWriter, r *http.Request) {
	ov := networkOverview{Interfaces: netdiag.Interfaces(), DNS: s.dnsConfigurator().State(r.Context())}
	if ov.Interfaces == nil {
		ov.Interfaces = []netdiag.Interface{}
	}
	ov.Hostname, _ = os.Hostname()
	ov.Gateway, ov.GatewayIface, _ = netdiag.DefaultGateway()
	writeJSON(w, http.StatusOK, ov)
}

// netCheck 是體檢的一個項目。Group 給前端分組顯示,ErrCode 給前端翻譯。
type netCheck struct {
	ID      string `json:"id"`
	Group   string `json:"group"` // link / gateway / internet / dns / https / clock
	Target  string `json:"target,omitempty"`
	OK      bool   `json:"ok"`
	Warn    bool   `json:"warn,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
	ErrCode string `json:"errCode,omitempty"`
}

type netDiagnosis struct {
	Checks []netCheck `json:"checks"`
	Hints  []string   `json:"hints"`
	// SuggestDNS 是實測可用的公共 DNS(給「一鍵改 DNS」用)。
	SuggestDNS []string `json:"suggestDns,omitempty"`
}

func fail(c netCheck, err error) netCheck {
	c.OK = false
	c.Error = err.Error()
	c.ErrCode = netdiag.ExplainErr(err)
	return c
}

// probeTimeout 等可在測試中縮短。
var (
	diagDNSTimeout  = 4 * time.Second
	diagHTTPTimeout = 8 * time.Second
	diagTCPTimeout  = 4 * time.Second
)

// handleNetworkDiagnose:POST /api/v1/network/diagnose。所有項目並行跑,
// 總時間約等於最慢的一項(< 10 秒)。
func (s *Server) handleNetworkDiagnose(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.diagnose(ctx))
}

func (s *Server) diagnose(ctx context.Context) netDiagnosis {
	var mu sync.Mutex
	var checks []netCheck
	add := func(c netCheck) {
		mu.Lock()
		checks = append(checks, c)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	goCheck := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}

	ifs := netdiag.Interfaces()
	link := netCheck{ID: "link", Group: "link", OK: netdiag.HasIPv4(ifs)}
	var names []string
	for _, i := range ifs {
		if i.Up {
			names = append(names, i.Name+" "+strings.Join(i.Addrs, ", "))
		}
	}
	link.Detail = strings.Join(names, "; ")
	add(link)

	gw, _, gwErr := netdiag.DefaultGateway()
	if gwErr != nil {
		add(fail(netCheck{ID: "gateway", Group: "gateway"}, errors.New("no default route (no gateway configured)")))
	} else {
		goCheck(func() { add(s.checkGateway(ctx, gw)) })
	}

	// 不需要 DNS 的對外連線:直接連公共 DNS 的 53 埠。
	goCheck(func() {
		c := netCheck{ID: "internet", Group: "internet", Target: "223.5.5.5:53 / 119.29.29.29:53 / 1.1.1.1:53"}
		var lastErr error
		for _, a := range []string{"223.5.5.5:53", "119.29.29.29:53", "1.1.1.1:53"} {
			d, err := netdiag.TCPReach(ctx, a, diagTCPTimeout)
			if err == nil {
				c.OK, c.Ms, c.Detail = true, d.Milliseconds(), a
				add(c)
				return
			}
			lastErr = err
		}
		add(fail(c, lastErr))
	})

	// 系統 DNS(Docker 實際用的就是這個)。
	goCheck(func() {
		c := netCheck{ID: "dns", Group: "dns", Target: dnsTestHost}
		ips, d, err := netdiag.ResolveVia(ctx, "", dnsTestHost, diagDNSTimeout)
		if err != nil {
			add(fail(c, err))
			return
		}
		c.OK, c.Ms, c.Detail = true, d.Milliseconds(), netdiag.ShortIPs(ips, 3)
		add(c)
	})
	// 每一台設定中的 DNS 伺服器各自測。
	for _, ns := range s.dnsConfigurator().State(ctx).Servers {
		if ns == "127.0.0.53" {
			continue
		}
		ns := ns
		goCheck(func() {
			c := netCheck{ID: "dns:" + ns, Group: "dns", Target: ns}
			ips, d, err := netdiag.ResolveVia(ctx, ns, dnsTestHost, diagDNSTimeout)
			if err != nil {
				add(fail(c, err))
				return
			}
			c.OK, c.Ms, c.Detail = true, d.Milliseconds(), netdiag.ShortIPs(ips, 3)
			add(c)
		})
	}
	// 公共 DNS(給建議用)。
	var pubMu sync.Mutex
	var goodPublic []struct {
		ip string
		ms int64
	}
	for _, ns := range []string{"223.5.5.5", "119.29.29.29"} {
		ns := ns
		goCheck(func() {
			c := netCheck{ID: "dnspub:" + ns, Group: "dns", Target: ns}
			ips, d, err := netdiag.ResolveVia(ctx, ns, dnsTestHost, diagDNSTimeout)
			if err != nil {
				add(fail(c, err))
				return
			}
			c.OK, c.Ms, c.Detail = true, d.Milliseconds(), netdiag.ShortIPs(ips, 3)
			pubMu.Lock()
			goodPublic = append(goodPublic, struct {
				ip string
				ms int64
			}{ns, c.Ms})
			pubMu.Unlock()
			add(c)
		})
	}

	// HTTPS 目標:Docker Hub、已設定的加速地址、Debian 套件源。
	targets := []struct{ id, url string }{
		{"https:dockerhub", "https://registry-1.docker.io/v2/"},
		{"https:debian", "https://deb.debian.org/debian/"},
	}
	cfg, _ := docker.ReadDaemonConfig(s.dockerDaemonJSONPath)
	for _, m := range cfg.RegistryMirrors {
		targets = append(targets, struct{ id, url string }{"https:mirror:" + m, strings.TrimRight(m, "/") + "/v2/"})
	}
	var clockMu sync.Mutex
	var serverDate time.Time
	for _, tg := range targets {
		tg := tg
		goCheck(func() {
			c := netCheck{ID: tg.id, Group: "https", Target: tg.url}
			res, err := netdiag.ProbeHTTP(ctx, tg.url, diagHTTPTimeout)
			if err != nil {
				add(fail(c, err))
				return
			}
			c.OK, c.Ms, c.Detail = true, res.Dur.Milliseconds(), "HTTP "+strconv.Itoa(res.Status)
			if !res.Date.IsZero() {
				clockMu.Lock()
				serverDate = res.Date
				clockMu.Unlock()
			}
			add(c)
		})
	}
	wg.Wait()

	// 時鐘:跟 HTTPS 伺服器的 Date 比較(時間差太多 TLS 憑證驗證會失敗)。
	if !serverDate.IsZero() {
		skew := time.Since(serverDate)
		c := netCheck{ID: "clock", Group: "clock", OK: true, Detail: strconv.Itoa(int(skew.Seconds()))}
		if skew > 5*time.Minute || skew < -5*time.Minute {
			c.OK, c.ErrCode = false, "clock_skew"
		}
		checks = append(checks, c)
	}

	order := map[string]int{"link": 0, "gateway": 1, "internet": 2, "dns": 3, "https": 4, "clock": 5}
	sort.SliceStable(checks, func(i, j int) bool {
		if order[checks[i].Group] != order[checks[j].Group] {
			return order[checks[i].Group] < order[checks[j].Group]
		}
		return checks[i].ID < checks[j].ID
	})

	d := netDiagnosis{Checks: checks, Hints: []string{}}
	byID := map[string]netCheck{}
	for _, c := range checks {
		byID[c.ID] = c
	}
	sort.Slice(goodPublic, func(i, j int) bool { return goodPublic[i].ms < goodPublic[j].ms })
	for _, g := range goodPublic {
		d.SuggestDNS = append(d.SuggestDNS, g.ip)
	}
	switch {
	case !byID["link"].OK:
		d.Hints = append(d.Hints, "noLink")
	case gwErr != nil || !byID["gateway"].OK:
		d.Hints = append(d.Hints, "gatewayDown")
	}
	if byID["internet"].OK && !byID["dns"].OK {
		if len(goodPublic) > 0 {
			d.Hints = append(d.Hints, "dnsBrokenUsePublic")
		} else {
			d.Hints = append(d.Hints, "dnsBroken")
		}
	}
	// 「出不了外網」只在直連 IP 失敗、而且所有 HTTPS 目標也都連不到時才下結論
	// (有些網路只擋對外的 53 埠,HTTPS 照樣能通,不能誤判成斷網)。
	anyHTTPS := false
	for _, c := range checks {
		if c.Group == "https" && c.OK {
			anyHTTPS = true
		}
	}
	if !byID["internet"].OK && !anyHTTPS && (gwErr == nil && byID["gateway"].OK) {
		d.Hints = append(d.Hints, "noInternet")
	}
	if byID["dns"].OK && !byID["https:dockerhub"].OK {
		mirrorOK := false
		for _, c := range checks {
			if strings.HasPrefix(c.ID, "https:mirror:") && c.OK {
				mirrorOK = true
			}
		}
		switch {
		case len(cfg.RegistryMirrors) == 0:
			d.Hints = append(d.Hints, "dockerHubBlockedNoMirror")
		case mirrorOK:
			d.Hints = append(d.Hints, "dockerHubBlockedMirrorOk")
		default:
			d.Hints = append(d.Hints, "dockerHubBlockedMirrorBad")
		}
	}
	if c, ok := byID["clock"]; ok && !c.OK {
		d.Hints = append(d.Hints, "clockSkew")
	}
	if len(d.Hints) == 0 {
		allOK := true
		for _, c := range checks {
			if !c.OK && !strings.HasPrefix(c.ID, "dnspub:") {
				allOK = false
			}
		}
		if allOK {
			d.Hints = append(d.Hints, "allGood")
		}
	}
	return d
}

var pingAvgRe = regexp.MustCompile(`= [\d.]+/([\d.]+)/`)

// checkGateway 先試 ping;沒有 ping 指令或沒權限時退回 TCP(連線被拒也代表
// 閘道活著)。
func (s *Server) checkGateway(ctx context.Context, gw string) netCheck {
	c := netCheck{ID: "gateway", Group: "gateway", Target: gw}
	if _, err := exec.LookPath("ping"); err == nil {
		out, err := s.runner.Run(ctx, "ping", "-c", "3", "-W", "1", "-n", gw)
		if err == nil {
			c.OK = true
			if m := pingAvgRe.FindStringSubmatch(string(out)); m != nil {
				f, _ := strconv.ParseFloat(m[1], 64)
				c.Ms = int64(f + 0.5)
			}
			c.Detail = "ping"
			return c
		}
		if !strings.Contains(strings.ToLower(err.Error()), "permitted") {
			return fail(c, fmt.Errorf("ping %s: no reply", gw))
		}
	}
	for _, port := range []string{"80", "53", "443"} {
		d, err := netdiag.TCPReach(ctx, net.JoinHostPort(gw, port), 2*time.Second)
		if err == nil || strings.Contains(err.Error(), "refused") {
			c.OK, c.Ms, c.Detail = true, d.Milliseconds(), "tcp/"+port
			return c
		}
	}
	return fail(c, fmt.Errorf("gateway %s did not respond", gw))
}

// ---- DNS 設定 ----

type dnsSetRequest struct {
	Servers []string `json:"servers"`
}

// handleNetworkDNSSet:PUT /api/v1/network/dns。
func (s *Server) handleNetworkDNSSet(w http.ResponseWriter, r *http.Request) {
	var req dnsSetRequest
	if !readJSON(w, r, &req) {
		return
	}
	if _, err := netdiag.ValidateServers(req.Servers); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg := s.dnsConfigurator()
	if err := cfg.Set(r.Context(), req.Servers); err != nil {
		s.logger.Error("setting DNS servers failed", "servers", req.Servers, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("DNS servers changed via web UI", "servers", req.Servers)
	writeJSON(w, http.StatusOK, cfg.State(r.Context()))
}

// handleNetworkDNSReset:DELETE /api/v1/network/dns → 回到 DHCP 自動取得。
func (s *Server) handleNetworkDNSReset(w http.ResponseWriter, r *http.Request) {
	cfg := s.dnsConfigurator()
	if err := cfg.Reset(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("DNS servers reset to automatic (DHCP) via web UI")
	writeJSON(w, http.StatusOK, cfg.State(r.Context()))
}

type speedResult struct {
	Target  string `json:"target"`
	OK      bool   `json:"ok"`
	Ms      int64  `json:"ms,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
	ErrCode string `json:"errCode,omitempty"`
	Current bool   `json:"current,omitempty"`
}

func sortSpeed(rs []speedResult) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].OK != rs[j].OK {
			return rs[i].OK
		}
		return rs[i].Ms < rs[j].Ms
	})
}

// handleNetworkDNSTest:POST /api/v1/network/dns/test。對目前的 DNS + 候選
// 公共 DNS 各解析一次 registry-1.docker.io,依速度排序。
func (s *Server) handleNetworkDNSTest(w http.ResponseWriter, r *http.Request) {
	current := s.dnsConfigurator().State(r.Context()).Servers
	seen := map[string]bool{}
	var list []speedResult
	for _, ip := range current {
		if ip != "127.0.0.53" && !seen[ip] {
			seen[ip] = true
			list = append(list, speedResult{Target: ip, Current: true})
		}
	}
	for _, ip := range publicDNSCandidates {
		if !seen[ip] {
			seen[ip] = true
			list = append(list, speedResult{Target: ip})
		}
	}
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ips, d, err := netdiag.ResolveVia(r.Context(), list[i].Target, dnsTestHost, diagDNSTimeout)
			if err != nil {
				list[i].Error, list[i].ErrCode = err.Error(), netdiag.ExplainErr(err)
				return
			}
			list[i].OK, list[i].Ms, list[i].Detail = true, d.Milliseconds(), netdiag.ShortIPs(ips, 3)
		}(i)
	}
	wg.Wait()
	sortSpeed(list)
	writeJSON(w, http.StatusOK, list)
}

// handleNetworkMirrorTest:POST /api/v1/network/mirrors/test。對已設定的與
// 候選的 Docker 加速地址各打一次 /v2/(registry API 的根,正常會回 200 或 401)。
func (s *Server) handleNetworkMirrorTest(w http.ResponseWriter, r *http.Request) {
	cfg, _ := docker.ReadDaemonConfig(s.dockerDaemonJSONPath)
	seen := map[string]bool{}
	var list []speedResult
	addM := func(m string, cur bool) {
		m = strings.TrimRight(strings.TrimSpace(m), "/")
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		list = append(list, speedResult{Target: m, Current: cur})
	}
	for _, m := range cfg.RegistryMirrors {
		addM(m, true)
	}
	for _, m := range mirrorCandidates {
		addM(m, false)
	}
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := netdiag.ProbeHTTP(r.Context(), list[i].Target+"/v2/", diagHTTPTimeout)
			if err != nil {
				list[i].Error, list[i].ErrCode = err.Error(), netdiag.ExplainErr(err)
				return
			}
			list[i].Detail = "HTTP " + strconv.Itoa(res.Status)
			if res.Status == http.StatusOK || res.Status == http.StatusUnauthorized {
				list[i].OK, list[i].Ms = true, res.Dur.Milliseconds()
			} else {
				list[i].ErrCode = "bad_status"
				list[i].Error = list[i].Detail
			}
		}(i)
	}
	wg.Wait()
	sortSpeed(list)
	writeJSON(w, http.StatusOK, list)
}

// ---- 小工具 ----

type netToolRequest struct {
	Tool   string `json:"tool"`   // ping / traceroute / port / dns / http
	Target string `json:"target"` // 主機名稱或 IP(http 工具為網址)
	Port   int    `json:"port,omitempty"`
	Server string `json:"server,omitempty"` // dns 工具:指定 DNS 伺服器(選填)
}

// hostRe 只允許主機名稱/IP 會出現的字元,而且不能以 - 開頭(防止被當成
// 指令參數)。
var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:\-]{0,252}$`)

var errInvalidTarget = errors.New("invalid target: use a hostname or IP address")

// handleNetworkTool:POST /api/v1/network/tool。回傳 {output, ok}。
func (s *Server) handleNetworkTool(w http.ResponseWriter, r *http.Request) {
	var req netToolRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Target = strings.TrimSpace(req.Target)
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()

	if req.Tool == "http" {
		u, err := url.Parse(req.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeError(w, http.StatusBadRequest, errors.New("invalid URL: must start with http:// or https://"))
			return
		}
		res, err := netdiag.ProbeHTTP(ctx, req.Target, 15*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "output": err.Error(), "errCode": netdiag.ExplainErr(err)})
			return
		}
		out := fmt.Sprintf("HTTP %d  %d ms", res.Status, res.Dur.Milliseconds())
		if !res.Date.IsZero() {
			out += "\nServer Date: " + res.Date.UTC().Format(time.RFC1123)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
		return
	}

	if !hostRe.MatchString(req.Target) {
		writeError(w, http.StatusBadRequest, errInvalidTarget)
		return
	}
	switch req.Tool {
	case "ping":
		out, err := s.runner.Run(ctx, "ping", "-c", "4", "-W", "2", "-n", req.Target)
		writeToolResult(w, out, err)
	case "traceroute":
		var out []byte
		var err error
		switch {
		case lookPath("traceroute"):
			out, err = s.runner.Run(ctx, "traceroute", "-n", "-w", "2", "-q", "1", "-m", "20", req.Target)
		case lookPath("tracepath"):
			out, err = s.runner.Run(ctx, "tracepath", "-n", "-m", "20", req.Target)
		default:
			writeError(w, http.StatusBadRequest, errors.New("neither traceroute nor tracepath is installed on this NAS"))
			return
		}
		writeToolResult(w, out, err)
	case "port":
		if req.Port <= 0 || req.Port > 65535 {
			writeError(w, http.StatusBadRequest, errors.New("port must be between 1 and 65535"))
			return
		}
		addr := net.JoinHostPort(req.Target, strconv.Itoa(req.Port))
		d, err := netdiag.TCPReach(ctx, addr, 5*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "output": addr + ": " + err.Error(), "errCode": netdiag.ExplainErr(err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": fmt.Sprintf("%s: open (%d ms)", addr, d.Milliseconds())})
	case "dns":
		server := strings.TrimSpace(req.Server)
		if server != "" && net.ParseIP(server) == nil {
			writeError(w, http.StatusBadRequest, errors.New("DNS server must be an IP address"))
			return
		}
		ips, d, err := netdiag.ResolveVia(ctx, server, req.Target, 6*time.Second)
		via := server
		if via == "" {
			via = "system"
		}
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "output": fmt.Sprintf("%s via %s: %v", req.Target, via, err), "errCode": netdiag.ExplainErr(err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": fmt.Sprintf("%s via %s (%d ms):\n%s", req.Target, via, d.Milliseconds(), strings.Join(ips, "\n"))})
	default:
		writeError(w, http.StatusBadRequest, errors.New("unknown tool"))
	}
}

func lookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }

func writeToolResult(w http.ResponseWriter, out []byte, err error) {
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		} else {
			text += "\n\n" + err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "output": text})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": text})
}
