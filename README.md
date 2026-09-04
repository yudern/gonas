# GoNAS

一套用 Go 打造、Docker 優先、可同時安裝在 **x86_64** 與 **ARM64** 上的自架 NAS 系統。
儲存層採 **SnapRAID + mergerFS**,重現 Unraid「硬碟各自獨立、資料不打散」的陣列模式,
而不是走傳統 RAID 打散條帶的路線。

完整技術路線圖(架構圖 + Phase 0–9 建置順序 + 技術選型說明)見專案交付時附上的路線圖文件。

## 目前狀態:Phase 2 完成 — Docker 整合

**Phase 0(專案骨架)**

- Go module 初始化,`cmd/`/`internal/` 分層
- 最小 HTTP server:`GET /api/v1/health`、`GET /api/v1/version`
- 優雅關閉(收到 SIGTERM/SIGINT 時等待進行中的請求完成)
- `Makefile`:一鍵交叉編譯 `linux/amd64` 與 `linux/arm64` 靜態執行檔
- systemd unit(`build/systemd/gonas.service`)

**Phase 1(儲存核心,`internal/storage`)**

- 硬碟探測:`GET /api/v1/storage/disks`(呼叫 `lsblk -J`,已在本機容器驗證可正確列出真實區塊裝置)
- SMART 健康/溫度解析(`smartctl -a` 輸出解析,尚未掛到 API,邏輯已有單元測試)
- `PoolConfig`:嚴格驗證(強制要有同位碟,不允許無保護的池)
- mergerFS 掛載參數產生(most-free-space 建立策略)
- SnapRAID 設定檔範本產生 + sync/diff/scrub 呼叫
- 陣列啟停狀態機:`GET /api/v1/storage/array`(stopped/starting/started/stopping/failed)
- 同位校驗排程器(標準函式庫實作,細節見下方「已知取捨」)
- 20 個單元測試全數通過,涵蓋設定驗證、指令組裝、狀態轉換、排程時機邏輯(靠假的 Runner,不需要真硬碟)

> **已知取捨**:排程器原本規劃用 `robfig/cron`,但這個開發沙盒的網路白名單擋掉了
> `proxy.golang.org`,無法 `go get` 第三方套件,所以先用標準函式庫寫了一個「每天固定
> 時刻執行一次」的簡化排程器。等你在自己的機器上開發、或這裡的網路權限開放後,可以
> 直接換成完整 cron 語法的套件,對外的 `Scheduler` 介面不需要變動。

**Phase 2(Docker 整合,`internal/docker` + `internal/appstore`)**

- `internal/docker`:不依賴官方 SDK,直接用標準函式庫打 Docker Engine API 的
  Unix socket(見套件註解「已知取捨」——網路白名單擋掉了 Go module proxy,
  這同時也讓 GoNAS 維持零第三方依賴)。涵蓋容器 CRUD/啟停、映像列表與拉取
  (含串流進度、正確處理「HTTP 200 但錯誤藏在串流裡」這個 Docker API 的坑)、
  本機映像存在檢查(避免不必要的網路存取)、網路列表與建立/移除
- `internal/appstore`:App 範本 schema(單一/多容器)、Install/Uninstall
  生命週期(多容器 App 自動建立共用網路、安裝失敗會自動回滾已建立的資源、
  Uninstall 靠 Docker 標籤重新找回整個 App,即使 daemon 重啟過也能清乾淨)
- 新增 `GET /api/v1/docker/{ping,containers,images,networks}`(唯讀;
  安裝/解除安裝需要 Web UI 讓使用者選陣列路徑、填環境變數,留到 Phase 4)
- 30 個單元測試(全部用 httptest 假伺服器,不需要真的 Docker),另外用一支
  臨時腳本對著這台開發機**真正在跑的 Docker daemon**(本機建置的
  `FROM scratch` 測試映像,不需要外部網路)驗證過完整流程:ping → 安裝
  單容器 App → 確認執行中 → 解除安裝 → 確認清乾淨;同樣流程也用多容器 App
  驗證過共用網路的建立與清除。腳本本身沒有留在版本庫裡。

尚未實作(依路線圖排序,接下來的 Phase):

1. 檔案共享與帳號 — Samba / NFS / 使用者管理
2. Web 管理介面 — Vue 3 SPA
3. 監控與告警
4. 網路與安全 — WireGuard、HTTPS、2FA
5. 備份與快照
6. 安裝與封裝 — `install.sh`
7. 實機測試與強化

## 開發

需要 Go 1.22 以上(本機驗證於 go1.24.7)。

```sh
make run          # 開發模式,監聽 :8291
make build         # 編譯給目前這台機器用的 binary
make build-all      # 交叉編譯 amd64 + arm64 靜態執行檔到 dist/
go test ./...       # 跑全部單元測試(不需要真硬碟/root)
```

驗證健康檢查:

```sh
curl -s localhost:8291/api/v1/health | jq
curl -s localhost:8291/api/v1/version | jq
```

## 部署(之後 Phase 8 會做成 install.sh,目前先手動)

```sh
sudo install -m 0755 dist/gonasd-linux-amd64 /usr/local/bin/gonasd   # ARM 機器改用 -linux-arm64
sudo install -m 0644 build/systemd/gonas.service /etc/systemd/system/gonas.service
sudo systemctl daemon-reload
sudo systemctl enable --now gonas
```

## 授權

尚未指定,先以私人專案開發。
