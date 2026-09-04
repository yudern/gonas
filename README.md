# GoNAS

一套用 Go 打造、Docker 優先、可同時安裝在 **x86_64** 與 **ARM64** 上的自架 NAS 系統。
儲存層採 **SnapRAID + mergerFS**,重現 Unraid「硬碟各自獨立、資料不打散」的陣列模式,
而不是走傳統 RAID 打散條帶的路線。

完整技術路線圖(架構圖 + Phase 0–9 建置順序 + 技術選型說明)見專案交付時附上的路線圖文件。

## 目前狀態:Phase 0 — 專案骨架

已完成:

- Go module 初始化,`cmd/`/`internal/` 分層
- 最小 HTTP server:`GET /api/v1/health`、`GET /api/v1/version`
- 優雅關閉(收到 SIGTERM/SIGINT 時等待進行中的請求完成)
- `Makefile`:一鍵交叉編譯 `linux/amd64` 與 `linux/arm64` 靜態執行檔
- systemd unit(`build/systemd/gonas.service`)

尚未實作(依路線圖排序,接下來的 Phase):

1. 儲存核心 — 硬碟探測、mergerFS 掛載、SnapRAID 校驗排程
2. Docker 引擎整合 — 容器生命週期 API、App 商店範本
3. 檔案共享與帳號 — Samba / NFS / 使用者管理
4. Web 管理介面 — Vue 3 SPA
5. 監控與告警
6. 網路與安全 — WireGuard、HTTPS、2FA
7. 備份與快照
8. 安裝與封裝 — `install.sh`
9. 實機測試與強化

## 開發

需要 Go 1.22 以上(本機驗證於 go1.24.7)。

```sh
make run          # 開發模式,監聽 :8291
make build         # 編譯給目前這台機器用的 binary
make build-all      # 交叉編譯 amd64 + arm64 靜態執行檔到 dist/
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
