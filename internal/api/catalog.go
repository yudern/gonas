package api

import "github.com/bng147/gonas/internal/appstore"

// builtinCatalog 是內建的 App 目錄 —— 一組不需要連任何外部伺服器就能用的
// 常見自架 App 範本。真正的「社群目錄」(類似 Unraid Community Applications)
// 透過遠端目錄(internal/appstore.FetchCatalog + handleAppstoreCatalogSourceSet)
// 另外接上;兩者最後都餵給同一組 Install/Uninstall,安裝邏輯完全不用區分
// 一個範本是內建的還是遠端目錄來的。
//
// 這裡的範本刻意涵蓋 NAS 最常見的幾類用途(媒體串流、下載、相簿、密碼管理、
// 家庭自動化、監控、網路),而且涵蓋單一服務與多服務兩種結構。每個範本的
// 掛載路徑一律留空 HostPath(由安裝精靈預填/讓使用者選陣列上的路徑),埠給
// 常見預設值、使用者可在安裝時改,避免多個 App 搶同一個埠。
var builtinCatalog = []appstore.AppTemplate{
	{
		ID:          "portainer",
		Name:        "Portainer CE",
		Description: "Docker 容器的圖形化管理介面，可以在 GoNAS 自己的 App 商店之外，直接管理所有容器。",
		Category:    "utilities",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "portainer/portainer-ce:latest",
				Ports: []appstore.PortMapping{
					{ContainerPort: 9000, HostPort: 9000},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/data"}, // HostPath 留空：安裝時由使用者在陣列上選一個路徑
				},
			},
		},
	},
	{
		ID:          "jellyfin",
		Name:        "Jellyfin",
		Description: "免費開源的媒體串流伺服器(電影、影集、音樂、相片),類似 Plex/Emby,但完全自架、無任何付費功能牆。把你的媒體共享掛進去就能在各種裝置上播放。",
		Category:    "media",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "jellyfin/jellyfin:latest",
				Env: []appstore.EnvVar{
					{Key: "TZ", Default: "Etc/UTC", Description: "時區,例如 Asia/Taipei"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 8096, HostPort: 8096},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/config"},
					{ContainerPath: "/media", ReadOnly: true}, // 媒體來源唯讀掛載,避免 App 誤改/刪你的原始檔
				},
			},
		},
	},
	{
		ID:          "qbittorrent",
		Name:        "qBittorrent",
		Description: "網頁介面的 BitTorrent 下載器,下載完成的檔案直接落在 NAS 上。常跟媒體伺服器搭配使用。",
		Category:    "downloads",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "lscr.io/linuxserver/qbittorrent:latest",
				Env: []appstore.EnvVar{
					{Key: "PUID", Default: "1000", Description: "執行容器的使用者 ID"},
					{Key: "PGID", Default: "1000", Description: "執行容器的群組 ID"},
					{Key: "TZ", Default: "Etc/UTC", Description: "時區,例如 Asia/Taipei"},
					{Key: "WEBUI_PORT", Default: "8080", Description: "網頁介面埠(要跟下面對應)"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 8080, HostPort: 8081}, // 8080 常被其他 App 佔用,預設給 8081
					{ContainerPort: 6881, HostPort: 6881},
					{ContainerPort: 6881, HostPort: 6881, Protocol: "udp"},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/config"},
					{ContainerPath: "/downloads"},
				},
			},
		},
	},
	{
		ID:          "nextcloud",
		Name:        "Nextcloud",
		Description: "自架的雲端硬碟/協作平台(檔案同步、行事曆、通訊錄、線上文件),自己掌握資料的 Dropbox/Google Drive 替代品。這裡用 Nextcloud + MariaDB 兩個容器。",
		Category:    "productivity",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "db",
				Image: "mariadb:10.11",
				Command: []string{
					"--transaction-isolation=READ-COMMITTED", "--log-bin=ROW", // Nextcloud 官方建議的 MariaDB 參數
				},
				Env: []appstore.EnvVar{
					{Key: "MYSQL_ROOT_PASSWORD", Required: true, Description: "MariaDB root 密碼"},
					{Key: "MYSQL_DATABASE", Default: "nextcloud"},
					{Key: "MYSQL_USER", Default: "nextcloud"},
					{Key: "MYSQL_PASSWORD", Required: true, Description: "nextcloud 資料庫使用者的密碼"},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/var/lib/mysql"},
				},
			},
			{
				Name:  "app",
				Image: "nextcloud:latest",
				Env: []appstore.EnvVar{
					{Key: "MYSQL_HOST", Default: "nextcloud-db"}, // appstore 以 "<appID>-<serviceName>" 命名容器,同網路內可用容器名互相解析
					{Key: "MYSQL_DATABASE", Default: "nextcloud"},
					{Key: "MYSQL_USER", Default: "nextcloud"},
					{Key: "MYSQL_PASSWORD", Required: true, Description: "要跟 db 服務的 MYSQL_PASSWORD 填一樣的值"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 80, HostPort: 8082},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/var/www/html"},
				},
			},
		},
	},
	{
		ID:          "vaultwarden",
		Name:        "Vaultwarden",
		Description: "輕量的 Bitwarden 相容密碼管理伺服器,自架自己的密碼庫,手機/瀏覽器用官方 Bitwarden 用戶端連過來即可。",
		Category:    "security",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "vaultwarden/server:latest",
				Env: []appstore.EnvVar{
					{Key: "ADMIN_TOKEN", Required: true, Description: "管理後台(/admin)的存取權杖,請填一段夠長的隨機字串"},
					{Key: "SIGNUPS_ALLOWED", Default: "false", Description: "是否開放任何人自行註冊帳號(家用建議 false,自己建好帳號後關閉)"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 80, HostPort: 8083},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/data"},
				},
			},
		},
	},
	{
		ID:          "homeassistant",
		Name:        "Home Assistant",
		Description: "開源的家庭自動化中樞,整合各家智慧家電/感測器,本地運作不依賴雲端。",
		Category:    "smart-home",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "ghcr.io/home-assistant/home-assistant:stable",
				Env: []appstore.EnvVar{
					{Key: "TZ", Default: "Etc/UTC", Description: "時區,例如 Asia/Taipei"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 8123, HostPort: 8123},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/config"},
				},
			},
		},
	},
	{
		ID:          "uptime-kuma",
		Name:        "Uptime Kuma",
		Description: "自架的服務監控面板,定時檢查你的網站/服務/NAS 上其他 App 是否在線,斷線時可推播通知。",
		Category:    "monitoring",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "louislam/uptime-kuma:1",
				Ports: []appstore.PortMapping{
					{ContainerPort: 3001, HostPort: 3001},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/app/data"},
				},
			},
		},
	},
	{
		ID:          "pihole",
		Name:        "Pi-hole",
		Description: "全家網路層級的廣告/追蹤器攔截 DNS 伺服器,把路由器的 DNS 指到 NAS 即可全屋生效。注意會佔用 53 埠。",
		Category:    "network",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "pihole/pihole:latest",
				Env: []appstore.EnvVar{
					{Key: "TZ", Default: "Etc/UTC", Description: "時區,例如 Asia/Taipei"},
					{Key: "WEBPASSWORD", Required: true, Description: "Pi-hole 管理網頁的登入密碼"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 53, HostPort: 53},
					{ContainerPort: 53, HostPort: 53, Protocol: "udp"},
					{ContainerPort: 80, HostPort: 8084},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/etc/pihole"},
					{ContainerPath: "/etc/dnsmasq.d"},
				},
			},
		},
	},
	{
		ID:          "code-server",
		Name:        "code-server",
		Description: "瀏覽器裡的 VS Code，方便直接在 NAS 上編輯設定檔、寫小腳本，不用另外 SSH 進去。",
		Category:    "utilities",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "lscr.io/linuxserver/code-server:latest",
				Env: []appstore.EnvVar{
					{Key: "PUID", Default: "1000", Description: "執行容器的使用者 ID"},
					{Key: "PGID", Default: "1000", Description: "執行容器的群組 ID"},
					{Key: "TZ", Default: "Etc/UTC", Description: "時區，例如 Asia/Taipei"},
					{Key: "PASSWORD", Required: true, Description: "登入 code-server 網頁介面的密碼"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 8443, HostPort: 8443},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/config"},
				},
			},
		},
	},
	{
		ID:          "wordpress",
		Name:        "WordPress",
		Description: "WordPress + MySQL 的多容器範例，示範一個 App 底下多個服務如何透過共用網路互通。",
		Category:    "web",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "db",
				Image: "mysql:8",
				Env: []appstore.EnvVar{
					{Key: "MYSQL_ROOT_PASSWORD", Required: true, Description: "MySQL root 密碼"},
					{Key: "MYSQL_DATABASE", Default: "wordpress"},
					{Key: "MYSQL_USER", Default: "wordpress"},
					{Key: "MYSQL_PASSWORD", Required: true, Description: "wordpress 資料庫使用者的密碼"},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/var/lib/mysql"},
				},
			},
			{
				Name:  "app",
				Image: "wordpress:latest",
				Env: []appstore.EnvVar{
					{Key: "WORDPRESS_DB_HOST", Default: "wordpress-db"}, // appstore 用 "<appID>-<serviceName>" 命名容器，同網路內可以直接用容器名稱互相解析
					{Key: "WORDPRESS_DB_USER", Default: "wordpress"},
					{Key: "WORDPRESS_DB_PASSWORD", Required: true, Description: "要跟 db 服務的 MYSQL_PASSWORD 填一樣的值"},
					{Key: "WORDPRESS_DB_NAME", Default: "wordpress"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 80, HostPort: 8080},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/var/www/html"},
				},
			},
		},
	},
	{
		// 第六十三輪(使用者:「我需要建一個自建私有倉庫」)。官方 registry:2 +
		// 一個網頁瀏覽介面(joxit/docker-registry-ui,經由它內建的 nginx 反代到
		// registry,瀏覽器不用處理 CORS)。
		ID:          "registry",
		Name:        "Docker 私有鏡像倉庫(Registry)",
		Description: "在 NAS 上自建私有 Docker 鏡像倉庫,區網內的電腦可以把自己的鏡像 push 上來、再從這裡 pull,不必經過 Docker Hub。附網頁介面(預設埠 5080)可瀏覽/刪除鏡像。倉庫走 HTTP、沒有帳號密碼,只建議在區網內使用。用法:NAS 本機用 localhost:5000/名稱;其他電腦用「NAS的IP:5000/名稱」,並需在那台電腦(以及本機要用 IP 拉取時,在應用頁「Docker 鏡像加速」)的 Insecure registry 加上「NAS的IP:5000」。",
		Category:    "utilities",
		Services: []appstore.ServiceTemplate{
			{
				Name:  "app",
				Image: "registry:2",
				Env: []appstore.EnvVar{
					{Key: "REGISTRY_STORAGE_DELETE_ENABLED", Default: "true", Description: "允許刪除鏡像(網頁介面的刪除按鈕需要)"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 5000, HostPort: 5000},
				},
				Volumes: []appstore.VolumeMapping{
					{ContainerPath: "/var/lib/registry"}, // 鏡像資料,建議放在陣列上
				},
			},
			{
				Name:  "ui",
				Image: "joxit/docker-registry-ui:latest",
				Env: []appstore.EnvVar{
					{Key: "SINGLE_REGISTRY", Default: "true"},
					{Key: "REGISTRY_TITLE", Default: "GoNAS Registry"},
					{Key: "NGINX_PROXY_PASS_URL", Default: "http://registry-app:5000"}, // 同 App 網路內用容器名找到 registry
					{Key: "DELETE_IMAGES", Default: "true"},
					{Key: "SHOW_CONTENT_DIGEST", Default: "true"},
				},
				Ports: []appstore.PortMapping{
					{ContainerPort: 80, HostPort: 5080},
				},
			},
		},
	},
}
