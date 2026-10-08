package api

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 第六十九輪(使用者實機):應用頁的「打開」原本只看 Docker 回報的埠號猜網頁埠,
// qBittorrent 的 BT 埠 6881 被當成網頁埠 → 連線被重置。任何容器(不管是不是 GoNAS
// 商店裝的、是不是官方映像)都該用「實際探測」決定:對每個已發布的 TCP 埠真的
// 發一個 HTTP 請求,有 HTTP 回應(含 401/403/重新導向)才算網頁埠;BT/DNS/資料庫
// 這類不講 HTTP 的埠不會回應。HTTP 不通再試 HTTPS(自簽憑證也算)。

type webPortInfo struct {
	Private int    `json:"private"`
	Port    int    `json:"port"`
	Scheme  string `json:"scheme"` // "http" / "https"
	Status  int    `json:"status"`
	HTML    bool   `json:"html"` // Content-Type 是 text/html:最像「給人看的網頁」
}

// probeClient 不走環境變數的 proxy(探測的是本機/區網位址)、不跟隨轉址、不驗證憑證
// (自簽的 HTTPS 也要認得出來;只發 GET /,不送任何憑證/資料,不影響安全性)。
func probeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: timeout,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 只為判斷「這個埠講不講 HTTPS」
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func probeOnce(ctx context.Context, scheme, host string, port int, timeout time.Duration) (webPortInfo, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+net.JoinHostPort(host, strconv.Itoa(port))+"/", nil)
	if err != nil {
		return webPortInfo{}, false
	}
	req.Header.Set("User-Agent", "GoNAS-webport-probe")
	resp, err := probeClient(timeout).Do(req)
	if err != nil {
		return webPortInfo{}, false
	}
	resp.Body.Close()
	return webPortInfo{
		Port:   port,
		Scheme: scheme,
		Status: resp.StatusCode,
		HTML:   strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html"),
	}, true
}

// probeWebPort 判斷 host:port 是不是網頁埠。回傳 (資訊, 是否網頁)。
func probeWebPort(ctx context.Context, host string, port int, timeout time.Duration) (webPortInfo, bool) {
	httpRes, httpOK := probeOnce(ctx, "http", host, port, timeout)
	// HTTP 正常回應(非 400)就是明文網頁。400 常是「對 HTTPS 埠講了 HTTP」,再試 HTTPS。
	if httpOK && httpRes.Status != http.StatusBadRequest {
		return httpRes, true
	}
	if tlsRes, ok := probeOnce(ctx, "https", host, port, timeout); ok {
		return tlsRes, true
	}
	if httpOK {
		return httpRes, true // 真的就是回 400 的網頁服務
	}
	return webPortInfo{}, false
}

func probeHost(ip string) string {
	switch ip {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return ip
}

// GET /api/v1/docker/web-ports → { "<容器id>": [webPortInfo, ...] }
// 只含「探測到講 HTTP/HTTPS」的埠;容器剛啟動、服務還沒起來時為空陣列,前端會退回
// 依範本/常見埠號的推測。
func (s *Server) handleDockerWebPorts(w http.ResponseWriter, r *http.Request) {
	containers, err := s.docker.ListContainers(r.Context(), false)
	if err != nil {
		s.logger.Error("listing docker containers for web-port probe failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type job struct {
		id      string
		private int
		host    string
		port    int
	}
	var jobs []job
	for _, c := range containers {
		seen := map[int]bool{}
		for _, p := range c.Ports {
			if p.PublicPort == 0 || (p.Type != "tcp" && p.Type != "") || seen[p.PublicPort] {
				continue
			}
			seen[p.PublicPort] = true
			jobs = append(jobs, job{id: c.ID, private: p.PrivatePort, host: probeHost(p.IP), port: p.PublicPort})
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	out := map[string][]webPortInfo{}
	for _, c := range containers {
		out[c.ID] = []webPortInfo{}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			if info, ok := probeWebPort(ctx, j.host, j.port, 1500*time.Millisecond); ok {
				info.Private = j.private
				info.Port = j.port
				mu.Lock()
				out[j.id] = append(out[j.id], info)
				mu.Unlock()
			}
		}(j)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}
