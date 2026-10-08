package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

// 模擬使用者遇到的情況:同一個容器同時發布「BT 埠(接受連線但不講 HTTP)」與
// 「網頁埠」,只有網頁埠該被認成網頁。
func TestProbeWebPortDistinguishesNonHTTPPorts(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized) // 像網頁服務要求登入
		w.Write([]byte("Unauthorized"))
	}))
	defer web.Close()

	// 非 HTTP 埠:接受連線後送一段 BT 風格的二進位再關閉。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte{0x13, 'B', 'i', 't', 'T', 'o', 'r', 'r', 'e', 'n', 't'})
			c.Close()
		}
	}()

	ctx := context.Background()
	info, ok := probeWebPort(ctx, "127.0.0.1", portOf(t, web.Listener.Addr().String()), time.Second)
	if !ok || info.Scheme != "http" || info.Status != 401 || !info.HTML {
		t.Fatalf("網頁埠應被認成 http(401, html),得到 %+v ok=%v", info, ok)
	}
	if _, ok := probeWebPort(ctx, "127.0.0.1", portOf(t, ln.Addr().String()), time.Second); ok {
		t.Fatal("不講 HTTP 的埠不該被認成網頁埠")
	}
}

func TestProbeWebPortDetectsHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	info, ok := probeWebPort(context.Background(), "127.0.0.1", portOf(t, strings.TrimPrefix(srv.URL, "https://")), time.Second)
	if !ok || info.Scheme != "https" {
		t.Fatalf("自簽 HTTPS 埠應被認成 https,得到 %+v ok=%v", info, ok)
	}
}

func TestProbeWebPortClosedPort(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := portOf(t, ln.Addr().String())
	ln.Close()
	if _, ok := probeWebPort(context.Background(), "127.0.0.1", port, 300*time.Millisecond); ok {
		t.Fatal("沒有人在聽的埠不該被認成網頁埠")
	}
}

func TestProbeHost(t *testing.T) {
	for in, want := range map[string]string{"": "127.0.0.1", "0.0.0.0": "127.0.0.1", "::": "127.0.0.1", "192.168.1.5": "192.168.1.5"} {
		if got := probeHost(in); got != want {
			t.Errorf("probeHost(%q)=%q want %q", in, got, want)
		}
	}
}
