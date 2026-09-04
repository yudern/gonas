package api

import "github.com/bng147/gonas/internal/appstore"

// builtinCatalog 是內建的示範 App 目錄。真正的 App 商店應該從一個可更新的
// 遠端目錄拉範本清單(類似 Unraid Community Applications 的做法),但那需要
// 額外的網路存取與目錄格式協商,超出目前這個階段的範圍。這裡先內建三個
// 有代表性的範本，刻意涵蓋單一服務跟多服務兩種情況，證明 appstore 套件的
// 安裝流程兩種都撐得住,之後要接上真正的遠端目錄時,這份清單只是換成
// 從網路抓來的資料，Install/Uninstall 的邏輯完全不用變。
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
}
