package api

import (
	"strconv"
	"testing"
)

// 第六十八輪:qBittorrent 的 WEBUI_PORT 必須等於容器埠,且主機埠必須等於容器埠
// (它會比對 Host 標頭的埠,不一致回 401 Unauthorized)。鎖住不讓之後被改回去。
func TestCatalogQBittorrentWebUIPortConsistent(t *testing.T) {
	for _, tmpl := range builtinCatalog {
		if tmpl.ID != "qbittorrent" {
			continue
		}
		svc := tmpl.Services[0]
		var webEnv string
		for _, e := range svc.Env {
			if e.Key == "WEBUI_PORT" {
				webEnv = e.Default
			}
		}
		if len(svc.Ports) == 0 {
			t.Fatal("qbittorrent 沒有埠對應")
		}
		web := svc.Ports[0]
		if strconv.Itoa(web.ContainerPort) != webEnv {
			t.Errorf("WEBUI_PORT=%s 與容器埠 %d 不一致", webEnv, web.ContainerPort)
		}
		if web.HostPort != web.ContainerPort || !web.SamePort {
			t.Errorf("網頁埠主機埠(%d)必須等於容器埠(%d)且標 SamePort", web.HostPort, web.ContainerPort)
		}
		return
	}
	t.Fatal("找不到 qbittorrent")
}
