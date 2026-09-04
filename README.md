# GoNAS

一套用 Go 打造、Docker 優先、可同時安裝在 **x86_64** 與 **ARM64** 上的自架 NAS 系統。
儲存層採 **SnapRAID + mergerFS**,重現 Unraid「硬碟各自獨立、資料不打散」的陣列模式,
而不是走傳統 RAID 打散條帶的路線。

完整技術路線圖(架構圖 + Phase 0–9 建置順序 + 技術選型說明)見專案交付時附上的路線圖文件。

## 目前狀態:Phase 5 完成 — 監控與告警

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

**Phase 3(檔案共享與帳號,`internal/share`)**

- `internal/cmdrunner`:把「執行外部指令」從 storage 套件抽成獨立共用套件
  (新增 stdin 支援,給 chpasswd/smbpasswd 這種要從標準輸入餵密碼的工具用),
  storage 套件的既有 API 靠型別別名保持相容,不用動到已經寫好的程式碼
- Samba:共享設定產生(寫成獨立的 include 檔,不覆蓋系統的 smb.conf)、
  `testparm` 語法驗證、`smbcontrol` 熱重載、`smbpasswd` 密碼同步(密碼一律
  走 stdin,不會出現在指令列參數、不會被其他使用者用 `ps` 看到)
- NFS:exports 設定產生(寫進 `/etc/exports.d/gonas.exports`)、`exportfs -ra`
  熱重載,設計上不允許產生「任何人都能掛」的無限制匯出
- 使用者/群組:`useradd`(無 shell、無 home 目錄,NAS 帳號不該有互動式登入)、
  `userdel`、`chpasswd`、`groupadd`
- 設定檔一律原子寫入(先寫暫存檔、`rename` 换過去),不會有寫到一半留下半份
  設定檔的情況
- 這個沙盒沒有安裝 Samba/NFS server(套件源被同一層網路白名單擋掉),
  所以這兩塊靠完整的單元測試覆蓋(組出來的設定檔內容、指令參數、stdin 內容
  逐一斷言)。使用者/群組管理則不受這個限制 —— 已經對這台機器上真正的
  `useradd`/`groupadd`/`chpasswd`/`userdel` 實際跑過一輪:建帳號、確認
  `-M` 真的沒建立 home 目錄、確認殼層是 nologin、設密碼、用 `getent shadow`
  確認密碼雜湊真的寫進去了、刪帳號、確認真的刪乾淨。驗證腳本沒有留在版本庫裡。
- 目前沒有掛 HTTP API:共享/匯出清單需要先有設定持久化與陣列路徑選擇
  (Phase 4 的 Web UI 範圍),現在開放端點沒有介面把關容易被誤用,
  跟 Phase 2 App 商店安裝/解除安裝端點延後的理由一樣

**Phase 4(Web 管理介面 + 設定持久化,`internal/state` + `internal/api` 擴充 + 內嵌前端)**

> **技術路線圖的修正**:原規劃是 Vue 3 + Vite,但這個沙盒的 npm registry
> 也被同一層網路白名單擋掉了(`npm view vue version` → 403),連建置工具都
> 拉不下來。與其等一個可能拉得到套件的環境,不如正視一件事:**NAS 管理介面
> 本來就不該依賴外部 CDN 或建置鏈才能動** —— 使用者的 NAS 可能就在一個
> 斷網、隔離的區網裡。所以 Phase 4 改成純手寫的 vanilla HTML/CSS/JS
> (ES module、fetch API,無任何第三方依賴),用 Go 1.16+ 的 `embed.FS`
> 直接內嵌進 `gonasd` 執行檔 —— 使用者拿到的仍然是「一個檔案、複製過去就能跑」,
> 瀏覽器打開就有完整介面,不需要 Node.js、不需要建置步驟、不需要網路。
> 這跟 Phase 0 開始就堅持的「零第三方依賴」是同一個方向,只是連前端也一併
> 貫徹了。

- `internal/state`:唯一的設定持久化層,一份原子寫入的 `state.json`(pool
  設定、共享、匯出、使用者中繼資料、已安裝的 App),沒有資料庫(理由跟
  Docker SDK/robfig-cron 一樣:module proxy 被擋,而且對這個資料量與存取
  型態,flat file 已經足夠——這跟 Unraid 本身的做法一致)
- `internal/api` 新增:
  - `PUT /api/v1/storage/pool`、`POST /api/v1/storage/array/{start,stop}`
  - `GET /api/v1/appstore/catalog`(內建 3 個示範範本:單服務的 Portainer/
    code-server、多服務的 WordPress+MySQL)、`GET/POST /api/v1/appstore/apps`、
    `DELETE /api/v1/appstore/apps/{id}`
  - `GET/POST /api/v1/share/shares`、`DELETE .../{name}`、
    `GET/POST /api/v1/share/exports`、`GET/POST /api/v1/share/users`、
    `DELETE .../{username}` —— 套用系統設定(smbd/exportfs 重載)失敗時只回
    警告,不會讓「存設定」這件事跟著失敗
- 內嵌前端(`internal/api/webui/static`):儀表板、儲存(硬碟列表 + 設定
  pool + 啟停陣列)、應用程式(已安裝列表 + 商店目錄 + 安裝表單)、共享
  (SMB/NFS)、使用者,五個頁面,純 hash routing,無框架
- 修正一個真的會讓前端讀錯資料的 bug:`appstore.InstallResult` 原本沒有
  json tag,序列化出來是 `ContainerIDs`/`NetworkID`(PascalCase),前端寫的
  是 `containerIds`/`networkId` —— 是在做端對端驗證、真的用瀏覽器會呼叫的
  同一組 API 跑一次安裝流程時發現的,已修正並在單元測試/live 驗證都過。
- 已對這台機器完整跑過一輪端對端(透過 gonasd 自己的 HTTP API,不是繞過
  API 直接呼叫 Go 套件):啟動 daemon → 首頁與 `/app.js` 正確以
  `text/html`/`text/javascript` 回應 → 設定並驗證一份合法 pool 設定 →
  透過 `/api/v1/appstore/apps` 對真正的 dockerd 安裝一個本機建置的測試映像
  (見前面 Phase 2 的說明,不需要外部網路)→ 確認容器真的在跑 → 解除安裝 →
  新增一個 SMB 共享(這台機器沒裝 Samba,確認回應是「設定已存、套用失敗」
  的優雅降級,不是整個請求失敗)→ 透過 API 建立一個真正的系統使用者、
  用 `getent passwd` 確認存在 → **重啟 daemon**,確認 pool 設定、共享、
  使用者清單都從 `state.json` 正確載入回來,已解除安裝的 App 確實不在列表裡。

- 又修正一個真的會讓前端整頁掛掉的 bug,一樣是端對端驗證抓到的:用
  Playwright 對著真的在跑的 `gonasd` 截圖檢查每一頁,結果「應用程式」
  頁面只顯示一條錯誤訊息 `Cannot read properties of null (reading 'length')`,
  什麼都沒渲染出來。追下去發現是 `internal/state.State` 全新安裝時
  (或是舊版 state.json 剛好缺欄位/欄位是 `null`)的零值切片欄位
  (`Shares`/`Exports`/`Users`/`InstalledApps`)是 Go 的 nil slice,
  `encoding/json` 會把它序列化成 JSON `null` 而不是 `[]`;前端
  `app.js` 對這些端點的呼叫用 `.catch(() => [])` 接錯誤,但 HTTP 200
  加上回應主體 `null` 根本不會進到 `.catch()`,`installed.length` 就直接
  對著 `null` 炸掉。已在 `internal/state/store.go` 新增 `State.normalize()`,
  在 `Open()`(不管是全新安裝還是讀到舊檔案)、`Update()` 寫檔前、以及
  `Snapshot()` 這三個會把資料交出去的地方一律確保這四個欄位不是 nil,
  並新增兩個迴歸測試直接斷言序列化出來的原始 JSON 位元組裡不會出現
  `"shares":null` 這類字串(而不是只檢查 Go 型別層面的 `len()==0`,因為
  bug 本身就是「Go 看起來沒事,但序列化出來是 null」)。修完後重新對著
  這台機器真正在跑的 `gonasd` 走一次:建一個真的系統使用者、確認
  `/api/v1/share/users` 回應是 `[]` 不是 `null`、用 Playwright 截圖確認
  應用程式/共享/使用者三個頁面都正常渲染、刪除該使用者並用 `getent passwd`
  確認真的清乾淨。

已知但刻意不修的小地方:`internal/docker` 的 Container/Image/Network 型別
為了直接對應 Docker Engine API 自己的 JSON 格式,用的是 PascalCase 標籤
(`"Id"`、`"Names"`……),這跟 GoNAS 其他端點的 camelCase 風格不一致;
目前的前端沒有直接渲染這幾支端點的原始資料所以不影響功能,但之後如果
要在 Web UI 上直接列 Docker 原始容器清單,值得加一層轉換成一致的 DTO。

**Phase 5(監控與告警,`internal/monitor` + `internal/api`/`internal/state` 擴充 + 內嵌前端)**

- `internal/monitor`:跟其他套件一樣零第三方依賴 —— 不用 gopsutil,
  直接解析 `/proc/stat`(CPU)、`/proc/meminfo`(記憶體)、`/proc/uptime`,
  用 `syscall.Statfs` 算磁碟使用率,這些正是 top/free/df 實際的資料來源
  - `Collector`:單次取樣,CPU 使用率靠保存上一次讀數算差值(第一次呼叫
    沒有基準點,回傳 0,由週期性輪詢自然補上)
  - `History`:固定容量的取樣紀錄(預設 180 筆、間隔 10 秒,約 30 分鐘),
    刻意不落地到磁碟 —— 這是「最近走勢」,跟 state.json 那種設定遺失會
    讓人困擾的資料不是同一回事,daemon 重啟後重新累積是可接受的
  - `Poller`:背景 goroutine 週期性取樣、寫入 History,並把結果交給
    告警規則引擎評估,啟動時立刻取樣一次(不用空等一個 interval)
  - `AlertRule`/`Facts`/`evaluateRule`:規則可以是連續數值指標
    (CPU/記憶體/磁碟使用率 + 比較方式 + 門檻值)或布林狀態指標
    (陣列變成 failed、任一顆碟 SMART 沒過),同一個型別涵蓋兩種,
    因為對使用者來說「選一個指標、決定觸發條件」是同一個心智模型
  - `AlertEngine`:只在規則的觸發狀態真的「轉換」時才通知一次(防抖),
    持續超標不會每次輪詢都重複發通知,解除時也會通知一次
  - `Notifier`:可插拔介面,內建 `LogNotifier`(永遠可用的保底管道,
    寫進 daemon 的 log)跟 `WebhookNotifier`(標準函式庫 `net/http` POST
    JSON,不透過任何第三方 HTTP 用戶端),`MultiNotifier` 讓兩者(或多個
    webhook)同時掛著,其中一個失敗不影響其他的送達
  - 43 個單元測試,涵蓋 `/proc` 格式解析(用這台機器真實的 `/proc/stat`
    等內容當 fixture)、CPU 使用率計算(含計數器倒退的邊界情況)、History
    容量裁切、Poller 的啟動/停止/取樣失敗處理、規則驗證與評估、
    AlertEngine 的防抖/解除通知/規則刪除清理,以及對著真的 httptest
    server 驗證 WebhookNotifier 送出的請求方法/Header/Body 都正確
- `internal/state`:新增 `AlertRules`/`Notifiers` 兩個持久化欄位,一併
  納入 Phase 4 那個 nil slice → JSON null 的 `normalize()` 防護裡
  (新增規則/通知管道時如果忘記這麼做,就會重演應用程式頁面那個 bug,
  所以這次直接把迴歸測試也一起擴充覆蓋這兩個新欄位)
- `internal/api` 新增:
  - `GET /api/v1/monitor/system`(最新一筆快照,History 還沒有資料時
    直接同步取樣一次,不會回「還沒有資料」的錯誤)、
    `GET /api/v1/monitor/history`(最近的走勢)
  - `GET/POST /api/v1/monitor/alerts`、`DELETE .../{id}`(規則清單會
    附上 AlertEngine 記錄的「目前是否觸發中」)
  - `GET/POST /api/v1/monitor/notifiers`、`DELETE .../{id}`(新增/刪除
    後重建 AlertEngine 實際在用的 `MultiNotifier`)
  - `Server.Close()`:daemon 優雅關閉時停掉監控輪詢的背景 goroutine,
    `cmd/gonasd/main.go` 改成保留 `api.New` 回傳的 `*Server` 並 `defer`
    呼叫,而不是像 Phase 0-4 那樣直接丟棄
  - 使用者設定/變更 pool 時,監控要看的磁碟路徑會跟著換成新陣列的
    掛載點(`Collector.SetDiskPath`),不會繼續顯示舊路徑或預設的 `/`
  - 告警評估的 SMART 檢查(`anyDiskSmartFailed`)對每顆碟的 `smartctl`
    呼叫都有 5 秒逾時,單顆碟檢查失敗(裝置不支援、smartctl 沒裝)只記
    debug log 略過,不會被誤判成「SMART 故障」而亂觸發告警
- 內嵌前端新增「監控」頁:即時 CPU/記憶體/磁碟/執行時間統計卡、用
  Canvas 手寫的三色走勢折線圖(顏色直接讀取 CSS 變數,深色/淺色主題
  切換不用另外處理)、告警規則清單(附觸發中/監控中/已停用燈號)與
  新增表單(依指標是連續數值還是布林狀態動態顯示/隱藏比較方式欄位)、
  通知管道清單與新增表單
- 已對這台機器完整跑過一輪端到端驗證(透過 `gonasd` 自己的 HTTP API):
  啟動 daemon → 確認 `/monitor/system`/`/monitor/history` 回傳這台機器
  真實的 CPU/記憶體/磁碟數字、且 History 隨著時間累積 → 架一個真的在
  跑的本機 HTTP 監聽器當 webhook 端點,新增一個保證會觸發的告警規則
  (`memPercent > 0`)跟一個指到這個監聽器的 webhook 通知管道 → 確認
  規則觸發後 `firing` 變成 `true`、webhook 真的收到一次 JSON 通知 →
  等過至少一次輪詢間隔,確認持續觸發不會重複發送(防抖生效)→ 刪除
  規則與通知管道確認清單清空 → 新增另一組規則/通知管道後**重啟
  daemon**,確認兩者都從 `state.json` 正確載入回來 → 用 Playwright
  截圖確認監控頁完整渲染(統計卡、走勢圖、規則列表含真實的「監控中」
  燈號、通知管道列表)、瀏覽器主控台沒有任何 JS 錯誤。

尚未實作(依路線圖排序,接下來的 Phase):

1. 網路與安全 — WireGuard、HTTPS、2FA
2. 備份與快照
3. 安裝與封裝 — `install.sh`
4. 實機測試與強化

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

啟動後,瀏覽器打開 `http://<主機位址>:8291/` 就是 Web 管理介面(內嵌在執行檔裡,
不需要另外部署前端)。

## 部署(之後 Phase 8 會做成 install.sh,目前先手動)

```sh
sudo install -m 0755 dist/gonasd-linux-amd64 /usr/local/bin/gonasd   # ARM 機器改用 -linux-arm64
sudo install -m 0644 build/systemd/gonas.service /etc/systemd/system/gonas.service
sudo systemctl daemon-reload
sudo systemctl enable --now gonas
```

## 授權

尚未指定,先以私人專案開發。
