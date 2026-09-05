# GoNAS

一套用 Go 打造、Docker 優先、可同時安裝在 **x86_64** 與 **ARM64** 上的自架 NAS 系統。
儲存層採 **SnapRAID + mergerFS**,重現 Unraid「硬碟各自獨立、資料不打散」的陣列模式,
而不是走傳統 RAID 打散條帶的路線。

完整技術路線圖(架構圖 + Phase 0–9 建置順序 + 技術選型說明)見專案交付時附上的路線圖文件。

## 目前狀態:Phase 9 完成 — 實機測試與強化

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

**Phase 6(網路與安全,`internal/security` + `internal/wireguard` + `internal/api`/`internal/state` 擴充 + 內嵌前端)**

在這之前(Phase 0–5)GoNAS 完全沒有身分驗證 —— 任何連得到 `gonasd` 的人都能改
儲存池、裝 App、建使用者、改共享。Phase 6 補上「Web 管理介面本身的門鎖」跟
「不用開防火牆洞就能從外面連回家」這兩塊,跟其他 Phase 一樣維持零第三方
依賴,密碼雜湊、TOTP、TLS 憑證、WireGuard 金鑰全部手刻在標準函式庫上。

- `internal/security`:
  - `HashPassword`/`VerifyPassword`:PBKDF2-HMAC-SHA256(210,000 次疊代、
    16 bytes 隨機 salt、`$` 分隔的自描述編碼字串),沒有用
    `golang.org/x/crypto/bcrypt`(module proxy 被擋,見一貫的「已知取捨」)
    ——PBKDF2 是少數 NIST 認可、標準函式庫的 `crypto/hmac`+`crypto/sha256`
    就能正確實作的密碼雜湊演算法。正確性對照 RFC 7914 的 PBKDF2 測試向量
    (用 Python `hashlib` 獨立算過一次)逐位元組核對。
  - `GenerateSecret`/`GenerateCode`/`ValidateCode`/`ProvisioningURI`:
    RFC 6238 TOTP,手刻 HOTP 動態截斷(RFC 4226)在 `crypto/hmac`+
    `crypto/sha1` 上,正確性對照 RFC 6238 附錄 B 官方測試向量(這是除了
    真的拿其他實作互測之外最強的驗證方式)。`ValidateCode` 容忍前後各
    一個時間窗口的時鐘飄移,沒有這個容忍度使用者手機時鐘慢個幾秒就永遠
    登不進去。
  - `SessionManager`:純記憶體、goroutine-safe 的登入 session(不是
    SMB/NFS 那種檔案存取憑證),daemon 重啟後全部使用者需要重新登入是
    刻意接受的代價,換來不用設計 session 的持久化/加密儲存。
  - `GenerateSelfSignedCert`/`EnsureCertFiles`:ECDSA P-256 自簽憑證(比
    RSA 快很多,對一份使用者自己手動信任的憑證來說安全性已經足夠),
    `EnsureCertFiles` 是 idempotent 的,已經有憑證就不重簽,避免每次啟動
    瀏覽器都跳「憑證變了」的警告。正確性用真正的 `openssl x509 -text`
    對 GoNAS 自己產生的憑證解析過,確認 SAN、演算法都對。
  - 27 個單元測試(密碼 11 個、TOTP 10 個、session 9 個、憑證 6 個,數字
    有重疊是因為部分測試涵蓋多個情境)。
- `internal/wireguard`:
  - `GenerateKeyPair`/`PublicKeyFromPrivate`:WireGuard 的金鑰就是原始的
    X25519 金鑰,Go 1.20 起標準函式庫的 `crypto/ecdh` 內建 X25519 曲線,
    不需要 `golang.org/x/crypto/curve25519` 也能產生格式完全相容
    (標準 base64 編碼的 32 bytes)的金鑰。
  - `GenerateConfig`:`text/template` 產生標準的 wg-quick `.conf` 格式,
    涵蓋 `[Interface]`/多個 `[Peer]` 區塊、選填欄位(PresharedKey/
    Endpoint/PersistentKeepalive)只在有值時才輸出。
  - `Up`/`Down`/`Status`:透過既有的 `cmdrunner.Runner` 抽象呼叫
    `wg-quick`/`wg`,這台開發機沒裝 wireguard-tools,所以套用/查詢這半部
    靠假的 Runner 驗證指令組裝正確,實機上有沒有裝 `wg-quick` 交給
    `internal/api` 做優雅降級(查不到狀態只回警告,不擋 API 本身)。
  - 20 個單元測試(金鑰 5 個、設定檔產生/驗證 9 個、指令組裝 6 個)。
- `internal/state`:新增 `Admin`(`*AdminAccount`,`omitempty` —— 還沒建立
  管理者帳號前完全不出現在 `state.json` 或任何 API 回應裡)、`HTTPS`
  (非 pointer,永遠存在,預設 `{"enabled":false}`)、`WireGuard`
  (`*wireguard.Config`,`omitempty`)三個欄位。`WireGuard.Peers` 這個
  巢狀切片一樣納入既有的 `normalize()` 防護(nil slice → JSON `null` 的
  bug 類別,見 Phase 4/5 的教訓),新增迴歸測試涵蓋這個巢狀情境,以及
  「沒有 admin/WireGuard 時這兩個欄位完全不該出現」的斷言。
- `internal/api` 新增:
  - `GET /api/v1/auth/status`(公開,回傳是不是要走初始設定)、
    `POST /api/v1/auth/setup`(公開,只有完全沒有帳號時才成功 ——
    這道檢查是身分驗證有沒有意義的安全邊界)、`POST /api/v1/auth/login`
    (公開,帳密錯誤一律回同一句「帳號或密碼錯誤」,不區分是哪個錯,
    避免使用者名稱枚舉)、`POST /api/v1/auth/logout`、`GET /api/v1/auth/me`、
    `POST /api/v1/auth/password`(換密碼後撤銷該帳號名下所有 session、
    立刻重新核發一個給目前這個請求)、`POST /api/v1/auth/totp/{setup,enable,disable}`
    (setup 先存密鑰但不啟用,要 enable 帶一次驗證碼才會真的生效,避免
    設定失敗把自己鎖在外面;disable 需要重新輸入密碼)
  - `GET/PUT /api/v1/security/https`(開啟時如果憑證檔案還不存在就順手
    產生,回應附上「需要重啟 gonasd 才會生效」的提醒——實際監聽 HTTP
    還是 HTTPS 只在程序啟動時決定一次)
  - `GET /api/v1/vpn/status`、`PUT /api/v1/vpn/interface`(第一次呼叫才
    產生介面金鑰對,之後只更新位址/埠號、不悄悄輪替私鑰 —— 换私鑰會讓
    所有已核發的用戶端設定檔全部失效)、`GET/POST /api/v1/vpn/peers`、
    `DELETE /api/v1/vpn/peers/{id}`(新增 peer 時伺服器產生一組全新金鑰對,
    只把公鑰存進 state,私鑰連同完整的 wg-quick 用戶端設定檔一起在
    這次 API 回應裡回傳一次就不再保留 —— 這跟 SSH 私鑰、一次性 API token
    是同一種設計,萬一 `state.json` 外洩,受影響的只有伺服器自己這一端,
    不會連帶洩漏所有已核發用戶端的身分)
  - `requireAuth` 中介層包住除了健康檢查/版本資訊跟登入流程本身(status/
    setup/login)以外的所有端點,哪支端點公開、哪支需要登入,從
    `router.go` 的路由註冊就能一眼看完
  - `cmd/gonasd/main.go`:啟動時讀一次 `state.json` 的 HTTPS 設定決定
    `ListenAndServe` 還是 `ListenAndServeTLS`;如果設定說要 HTTPS 但憑證
    檔案不見了(手動刪除、`GONAS_DATA_DIR` 換了位置),寧可退回 HTTP 讓
    daemon 正常啟動、留一條路讓使用者登入後重新開啟 HTTPS,也不要讓
    daemon 直接啟動失敗
- 內嵌前端新增登入閘門(`app.js` 的 `boot()`/`showApp()`/`showLoginGate()`/
  `showSetupGate()`):在使用者通過驗證之前完全不顯示 sidebar,`api.js`
  統一攔截任何 API 呼叫的 401 並跳回登入畫面(session 過期不需要每個
  頁面各自處理);新增「安全」頁面:修改密碼、TOTP 設定/啟用/停用(含
  手動輸入密鑰跟 Provisioning URI 兩種方式)、HTTPS 開關與憑證主機名稱
  設定、WireGuard 介面建立與用戶端管理(新增用戶端時顯示一次性的完整
  `.conf` 內容,只重繪 peer 清單那一小塊、不整頁重繪蓋掉這份內容)。
- 端到端驗證時抓到一個真的會讓整個登入機制看起來「卡住」的 CSS bug:
  瀏覽器 user-agent 樣式表的 `[hidden] { display: none }` 優先權比
  author stylesheet 的一般規則低,`style.css` 裡 `.shell { display:flex }`
  跟 `.auth-gate { display:flex }` 這兩條規則會蓋掉 `[hidden]`,結果
  `element.hidden = true` 這個 JS 呼叫完全生效(屬性確實被移除/加上),
  但畫面上該隱藏的登入卡片還是疊在儀表板上面 —— 用 Playwright 截圖加上
  `page.evaluate` 直接讀 DOM 的 `hidden` 屬性狀態,才確認「JS 邏輯是對的,
  CSS 特異度贏了」這個真正原因。已補上 `.shell[hidden]`/`.auth-gate[hidden]`
  兩條特異度更高的規則明確蓋回 `display:none`,修完後重新截圖驗證整套
  設定→登入→安全頁→TOTP 設定→登出流程都正確渲染。
- 已對這台機器完整跑過一輪端到端驗證(透過 `gonasd` 自己的 HTTP API,
  搭配即時算出來的 RFC 6238 驗證碼,不是靠假資料):初始設定建立管理者
  帳號 → 未登入呼叫受保護端點確認 401 → 登出後確認 session 立刻失效 →
  密碼打錯確認 401 → 開啟 TOTP、用真正算出來的驗證碼完成 enable →
  不帶驗證碼登入確認擋下來、帶正確驗證碼登入成功 → 停用 TOTP 需要重新
  輸入密碼 → 開啟 HTTPS、用 `openssl x509 -text` 驗證產生的憑證 →
  **重啟 daemon**,確認真的用 HTTPS 監聽(`curl -k https://...` 成功,
  純 HTTP 打不通)→ 建立 WireGuard 介面 → 新增一個用戶端,確認回應的
  `.conf` 內容裡 `AllowedIPs`/`Endpoint`/公鑰都正確、伺服器端只存了公鑰 →
  刪除該用戶端確認清單清空、重複刪除回 404 → 用 Playwright 對著真的在
  跑的 `gonasd` 完整截圖初始設定畫面、登入後的儀表板、安全頁面(含 TOTP
  設定中的畫面)、登出後的登入畫面,瀏覽器主控台沒有任何 JS 錯誤。

**Phase 7(備份與快照,`internal/backup` + `internal/api`/`internal/state` 擴充 + 內嵌前端)**

SnapRAID(見 Phase 1)保護的是「同一顆陣列裡的位元衰減/單顆碟故障」,跟
備份要處理的風險完全不同 —— 使用者自己刪錯檔案、勒索軟體把整個陣列的
資料都改寫、陣列所在的地點發生火災或淹水,這些情境下同一份陣列內的
同位校驗完全無能為力,一定要有一份實體上分開、而且能回到「過去某個
時間點」的複本。Phase 7 用 rsync 加硬連結輪替(跟 rsnapshot、macOS Time
Machine 是同一套經典技巧)做到這件事:每次備份看起來像一份完整複本,
但沒有變更的檔案在磁碟上其實只佔一份空間,不需要底層檔案系統支援
btrfs/ZFS 快照(GoNAS 刻意不綁定特定檔案系統)。

- `internal/backup`:
  - `Job`/`Job.Validate`:一份備份工作(來源路徑、目的地路徑、保留份數、
    排程、是否啟用)。特別檢查來源/目的地路徑不能互相包含 —— 不是吹毛
    求疵:如果目的地剛好落在來源底下(或反過來),每次執行會把自己剛
    寫出來的快照當成來源的一部分一起複製進下一層快照,資料量像雪球
    一樣越滾越大,最壞情況把整顆硬碟塞爆。
  - `RunBackup`:組出 `rsync -aAX --delete`(如果已經有既有快照就加
    `--link-dest` 做硬連結增量)、複製到一個 `.partial` 暫存目錄、成功
    後才原子性地 rename 成正式的時間戳記快照目錄並更新 `latest`
    symlink,最後清掉超過保留份數的舊快照。指令組裝一樣透過既有的
    `cmdrunner.Runner` 抽象,這個開發沙盒沒有安裝 rsync(套件源被同一層
    網路白名單擋掉,見一貫的「已知取捨」),所以 rsync 呼叫本身靠假的
    Runner 驗證,但硬連結輪替/清理/`--link-dest` 解析邏輯是純檔案系統
    操作,不依賴 rsync 存不存在,一樣對著這台機器上真正的臨時目錄完整
    測試過。
  - `JobScheduler`:跟 `internal/storage.Scheduler` 幾乎一樣的獨立
    goroutine + `time.Timer` 實作(同樣是「每隔 N 小時、在某個時刻執行」
    的簡化排程,理由也一樣:完整 cron 語法需要第三方套件),刻意不共用
    同一個型別,讓兩個套件的排程邏輯之後各自演化不會互相牽動,跟
    `internal/security` 選擇不依賴其他業務套件是同樣的考量。
  - 30 個單元測試,涵蓋設定驗證(含路徑互相包含的各種情境)、快照
    目錄命名/清單/清理、`.partial` 殘留目錄的容錯處理、`--link-dest`
    參數在有無既有快照兩種情況下的組裝、rsync 失敗時的清理與錯誤回報、
    多次執行後保留份數是否確實生效、排程器的重複觸發與確實停止。
- `internal/state`:新增 `BackupJobs []backup.Job`,一樣納入既有的
  nil slice → JSON null 防護(`normalize()`),新增迴歸測試涵蓋這個
  新欄位。
- `internal/api` 新增:
  - `GET/POST /api/v1/backup/jobs`、`DELETE .../{id}`、
    `POST .../{id}/run`(立刻觸發一次備份,不等它跑完就回應 202 —— 備份
    可能牽涉大量資料、跑好幾分鐘甚至更久,結果反映在下一次 GET 回應的
    `lastRun` 欄位)、`GET .../{id}/snapshots`
  - `Server` 啟動時把既有的、標記成 `Enabled` 的備份工作重新掛回排程,
    daemon 重啟不會讓使用者設定好的排程默默停擺;`Server.Close()` 停掉
    所有排程的背景 goroutine,跟監控輪詢用同一套「優雅關閉」機制
- 內嵌前端新增「備份」頁:工作清單(來源/目的地/保留份數/排程描述、
  啟用/停用燈號、上次執行成功/失敗與時間)、新增表單、立即執行按鈕、
  展開查看快照清單(inline,不需要跳頁)、刪除工作。
- 已對這台機器完整跑過一輪端到端驗證(透過 `gonasd` 自己的 HTTP
  API):建立一份目的地路徑落在來源路徑底下的設定確認 400 擋下來 →
  用一支極簡的假 `rsync` 腳本(這台機器沒裝真正的 rsync)驗證完整流程:
  建立工作 → 手動觸發執行 → 確認檔案真的被複製過去、快照目錄與
  `latest` symlink 都正確產生 → 修改來源內容後連續執行兩次,確認
  `--link-dest` 參數正確帶上第一次的快照路徑 → 連續執行超過保留份數
  的次數,確認最舊的快照被清掉、只留下設定的份數 → **重啟 daemon**,
  確認工作設定跟 `lastRun` 結果都正確從 `state.json` 載入回來,且
  `Enabled` 的工作排程重新掛回去不會讓 daemon 啟動失敗或 panic → 刪除
  工作確認清單清空、重複刪除與觸發不存在的工作 ID 都回 404 → 用
  Playwright 對著真的在跑的 `gonasd` 完整截圖備份頁(空清單、新增後、
  執行後顯示成功狀態、展開快照清單),瀏覽器主控台沒有任何 JS 錯誤。

**Phase 8(安裝與封裝,`internal/doctor` + `build/install.sh` + `build/uninstall.sh`)**

- `internal/doctor`:GoNAS 依賴的選用外部指令(`lsblk`、`smartctl`、`mergerfs`、
  `snapraid`、`useradd`/`userdel`/`chpasswd`/`groupadd`、`smbd`/`testparm`/
  `smbcontrol`/`smbpasswd`、`exportfs`、`wg`/`wg-quick`、`rsync`)一次性檢查,
  只用 `exec.LookPath`(不實際執行任何指令 —— 「裝了沒有」跟「能不能正常運作」
  是兩件事,後者留給使用者實際點下去某個功能時,由各自的 handler 回報)。
  4 個單元測試,透過在 `t.TempDir()` 寫假的可執行 shell script 並整個換掉
  `PATH` 來控制「找不找得到」,不依賴這台機器實際裝了什麼。
- `gonasd` 新增兩個一次性旗標(印完東西就結束,不啟動 daemon):
  - `-version`:印版本/commit/建置時間/GOOS/GOARCH,不用整個啟動 daemon
    再打 `/api/v1/version`
  - `-check-deps`:印 `internal/doctor` 的檢查報告,`install.sh` 裝完會
    自動跑一次,讓使用者馬上知道這台機器還缺哪些選用工具
- `build/install.sh` / `build/uninstall.sh`(純 POSIX `/bin/sh`,不用 bash
  ——這樣在用 dash 當 `/bin/sh` 的精簡 distro 上也能跑,已用 `dash -n` 跟
  `sh -n` 驗證語法):
  - 同時支援兩種佈局:`make release` 產出的 tarball(執行檔跟
    `install.sh` 同一層)跟原始碼 checkout(`build/install.sh` 往上層找
    `dist/gonasd-linux-$ARCH`),也支援 `-bin <path>` 明確指定執行檔
  - 用 `uname -m` 判斷 x86_64/aarch64 → amd64/arm64,不支援的架構直接
    報錯並提示可以用 `-bin`
  - 冪等:重複執行(升級)不會覆蓋 `/etc/gonas/gonas.env`(管理員可能已
    手動改過監聽位址/資料目錄),但執行檔跟 systemd unit 每次都覆蓋成
    最新版本
  - **systemd 偵測**:只有在 `/run/systemd/system` 目錄存在(代表 systemd
    真的是 PID 1)且找得到 `systemctl` 時才做 `daemon-reload`/`enable`/
    `restart`;偵測不到就優雅跳過,印出手動啟動指令,而不是讓整支
    script 中止在「執行檔明明已經裝好了」的狀態 —— 這台開發沙盒本身就是
    這種環境(`systemctl` 這個指令在,但 `/run/systemd/system` 不存在),
    兩條路徑都已在這裡實測跑過(systemd 路徑是暫時建立
    `/run/systemd/system` 目錄、用假的 `systemctl` script 攔截呼叫驗證
    參數正確,測完立刻還原,不影響這台機器本身的狀態)
  - `uninstall.sh` 預設只移除執行檔跟 systemd unit,保留
    `/etc/gonas`(設定)跟 `/var/lib/gonas`(帳號/儲存池/備份工作/
    WireGuard 金鑰等狀態)——跟 `apt remove` vs `apt purge` 的慣例一致；
    要整個刪乾淨要加 `--purge` 並手動輸入 `yes` 確認
  - `build/systemd/gonas.service` 加了
    `EnvironmentFile=-/etc/gonas/gonas.env`(前面的 `-` 表示檔案不存在時
    不報錯),管理員要改監聽位址/資料目錄只要編輯這個檔案,不用碰
    unit file 本身
- `Makefile` 新增 `release` target:跑完 `build-all` 之後,把 amd64/arm64
  兩份執行檔分別跟 `install.sh`/`uninstall.sh`/`gonas.service` 一起打包成
  `dist/release/gonas-$(VERSION)-linux-{amd64,arm64}.tar.gz`,使用者下載
  解壓後 `sudo ./install.sh` 就能裝,不需要自己有 Go 環境
- 已在這台機器上(root、真實檔案系統,但 systemd 不是 PID 1)完整跑過
  端到端驗證,而不只是讀程式碼判斷邏輯對不對:`make release` 產出兩份
  tarball → 解壓其中一份、`./install.sh` 裝起來 → 確認執行檔、
  `/etc/gonas/gonas.env`、`/var/lib/gonas` 都正確建立、systemd 偵測正確
  跳過並印出手動啟動指令 → 手動啟動裝好的執行檔,對 `/api/v1/health`、
  `/api/v1/version` 送真的 HTTP 請求確認可用 → 在 `gonas.env` 裡手動加
  自訂的監聽位址,重新執行 `install.sh` 確認冪等(檔案不被覆蓋、安裝
  摘要正確反映自訂值)→ `uninstall.sh`(預設)確認執行檔被刪、設定/
  資料目錄保留 → 重裝一次、`uninstall.sh --purge` 確認兩個目錄都真的
  被刪除 → 額外測了原始碼 checkout 佈局(`build/install.sh` + `dist/`)、
  非 root 執行會被擋、`-bin` 明確指定路徑、完全找不到執行檔時的錯誤
  訊息、以及用假 `/run/systemd/system` + 假 `systemctl` script 攔截驗證
  「systemd 真的在跑」那條路徑會呼叫正確的 `daemon-reload`/`enable`/
  `restart`/`stop`/`disable` 參數。全部測完已還原這台機器到測試前的
  乾淨狀態(移除所有測試裝上去的檔案跟暫時建立的 `/run/systemd/system`
  目錄)。

**Phase 9(實機測試與強化)**

這台開發沙盒從頭到尾沒有真實硬碟、沒有裝 mergerFS/SnapRAID/Samba/NFS/
WireGuard-tools/rsync,systemd 也不是真正的 PID 1 —— 所以「實機測試」
這部分沒辦法在這裡真的做,改成兩件事:(1) 寫一份具體、可執行的實機
測試清單,讓拿到真實硬體的人知道要測什麼、預期看到什麼結果;(2) 把
這台沙盒裡**能夠**確實驗證、對正式環境有實質幫助的強化項目做掉。

- `docs/REAL_HARDWARE_TESTING.md`:完整的實機測試清單,涵蓋安裝
  (systemd 真的 enable/start 成功的路徑)、systemd 進階沙盒加固
  (`ProtectSystem=strict`/`PrivateDevices=`/`SystemCallFilter=` 這類
  會影響掛載/裝置存取、沒有真硬碟沒辦法驗證安不安全的選項)、儲存
  (含「模擬硬碟損壞、驗證 SnapRAID 真的能修復」這個全專案最關鍵、
  沙盒完全無法測試的項目)、Docker/應用程式商店(含大型映像檔安裝
  會不會逾時)、檔案分享、安全性、備份、長時間穩定性等 8 大類別。
- **panic 復原中介層**(`withRecover`,`internal/api/router.go`):任何
  一支 handler 裡未預期的 panic,現在會被攔下來轉成一個乾淨的 500
  JSON 回應並記進 log,而不是讓那個請求的連線直接斷掉、且錯誤資訊
  只印在 stderr 裡難以追查。用一個刻意觸發 nil map 寫入 panic 的臨時
  測試實際驗證過會被正確攔截,測完即刪除(不留在最終程式碼裡)。
- **登入嘗試節流**(`internal/security/ratelimit.go`,`LoginLimiter`):
  同一個來源 IP 連續 5 次登入失敗(帳號、密碼、TOTP 驗證碼都算)後
  鎖定 5 分鐘,擋掉對管理者密碼的暴力猜測 —— 先前 Phase 6 做完整套
  身分驗證/2FA/HTTPS,但登入端點本身可以無限次重試這件事一直沒補上。
  5 個單元測試涵蓋門檻判斷、鎖定到期後重置、成功登入清除失敗計數、
  不同 key 互不干擾;並對著真的在跑的 `gonasd` 實測連續打錯密碼確認
  第 6 次收到 429 跟正確的 `Retry-After`,鎖定期間連正確密碼都會被拒。
- **請求 body 大小限制**:`readJSON` 現在用 `http.MaxBytesReader` 把
  所有 API 端點的請求 body 限制在 1 MiB,擋掉忘記帶
  Content-Length/惡意送超大 body 撐爆記憶體的請求 —— 已用一個 3 MiB
  的請求實測確認會被乾淨地拒絕(400),正常大小的請求不受影響。
- **HTTP 安全標頭中介層**(`withSecurityHeaders`):`X-Content-Type-
  Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy:
  no-referrer`、`Content-Security-Policy: default-src 'self'; style-src
  'self' 'unsafe-inline'`。CSP 的 `style-src` 部分刻意放寬成允許行內
  樣式 —— 第一版用嚴格的 `default-src 'self'` 時,直接用 Playwright
  對著真的在跑的介面測出整個版面被 CSP 擋壞(前端大量使用行內
  `style="..."` 屬性做動態顏色/版面調整),改成只放寬 style-src、
  script-src 繼續維持嚴格限制之後重新測過全部主要頁面,畫面正常、
  瀏覽器主控台沒有 CSP 相關錯誤。
- **HTTP server 逾時設定**(`cmd/gonasd/main.go`):加上 `ReadTimeout`
  跟 `IdleTimeout` 防 slowloris 類的慢速連線攻擊;刻意**沒有**加
  `WriteTimeout` ——`POST /api/v1/appstore/apps` 會同步等 Docker 映像檔
  拉取完成才回應,在沒有真實 Docker 環境可以驗證「大型映像檔會不會
  被寫入逾時掐斷」之前貿然設一個數字,風險比不設更大,已明確列進
  `docs/REAL_HARDWARE_TESTING.md` 的待驗證清單。
- **systemd unit 加固**(`build/systemd/gonas.service`):補上
  `LimitNOFILE`(調高檔案描述符上限,NAS 常見情境)、
  `ProtectKernelLogs`、`ProtectClock`、`LockPersonality`、
  `RestrictSUIDSGID` 這幾項不影響掛載/裝置存取、可以確定安全的選項,
  用 `systemd-analyze verify` 驗證過語法正確;`ProtectSystem=strict`/
  `PrivateDevices=`/`SystemCallFilter=` 這類有掛載/裝置存取風險的選項
  刻意留白,原因與逐項驗證步驟寫在 unit file 注解跟
  `docs/REAL_HARDWARE_TESTING.md` 裡。
- 全部變更跑過完整的 `gofmt`/`go build`/`go vet`/`go test ./...`,新增
  的 `internal/security/ratelimit_test.go` 全數通過。

**Phase 9.1(修正:這台沙盒其實裝了真的 Docker Engine + 自訂 App 安裝)**

Phase 9 交付之後,重新檢查這台沙盒的環境時發現一個先前的認知錯誤:
`docker`/`dockerd` 執行檔其實已經預裝在這裡,`dockerd` 也真的能以
root 身分手動啟動成功(`docker info` 能連上、`overlayfs` 儲存驅動正常
初始化)——先前所有 Phase 2 的 Docker 整合驗證都只用 HTTP 假伺服器
模擬 Docker Engine API 回應,是因為當時沒有主動確認過這件事,不是
真的沒有 Docker 可用。這個發現值得訂正,也直接拿來把 Docker 相關功能
做一次真正的端到端驗證,同時補上先前找出的一個具體產品缺口(應用
程式商店只有 3 個內建範本、無法安裝任意 image)。

- **應用程式商店支援自訂安裝**:`POST /api/v1/appstore/apps` 現在接受
  `templateId`(原本的內建目錄路徑)或 `template`(使用者自己填的完整
  `appstore.AppTemplate`,不限於 `catalog.go` 裡那 3 個範本)兩者恰好
  一個,對應到 Unraid「不套用任何 Community Applications 範本、直接
  新增容器」的安裝方式。新增的 `resolveInstallTemplate` 會擋掉自訂
  範本 ID 撞到內建目錄或撞到現有已安裝 App 的情況(ID 同時是容器
  命名前綴跟網路名稱,撞名會讓 Uninstall 誤刪不相干的 App)。8 個
  單元測試涵蓋這幾種路徑跟碰撞情境,全數通過。
- **Web UI 新增「自訂安裝」表單**:App ID/名稱/說明/image/埠對應/
  掛載路徑/環境變數,埠/掛載/環境變數用簡易的多行文字格式(而不是
  動態新增列的表單元件)換取實作成本,對「裝一個容器、填幾條設定」
  的情境已經足夠。過程中用 Playwright 抓到一個真的存在的 bug:App ID
  欄位的 `pattern="[a-z0-9][a-z0-9-]*"` 在這個版本的 Chromium 裡會被
  當成無效的正規表示式(新版 HTML 規格用 regex 的 `v` flag/Unicode
  Sets 模式編譯 `pattern` 屬性,這個模式對字元類別裡的連字號比舊模式
  嚴格),導致整個表單完全無法送出且瀏覽器主控台報一個語法錯誤
  ——改成明確跳脫 `[a-z0-9][a-z0-9\-]*` 後修好,已重新用 Playwright
  跑完整個安裝/解除安裝流程確認正常。
- **真的用 Docker Engine 端到端驗證了(不是 HTTP 假伺服器)**:手動
  啟動 `dockerd`,用一個 `FROM scratch` + 靜態編譯的 Go 執行檔建置
  出一個完全不需要連網、不需要向任何 registry 拉取任何東西的最小
  測試映像檔,拿它對著真正在跑的 `gonasd` 做了以下驗證,全部是第一次
  真正對著 Docker Engine API(而非 fake HTTP handler)跑:
  - 單服務自訂安裝:容器真的被建立、啟動,`docker ps` 看得到,
    透過對應的 host port 打 HTTP 請求拿到真實回應。
  - 多服務自訂安裝:專屬 bridge 網路真的被建立、兩個容器都掛在
    上面;解除安裝後容器與網路都真的被清乾淨。
  - ID 碰撞防護(撞內建目錄 / 撞已安裝 App / 同時給兩種安裝方式 /
    兩種都不給)分別回傳正確的 409/400。
  - **安裝失敗時的 rollback 邏輯**:刻意讓多服務範本裡第二個服務指定
    一個真的拉不到的 image(這台沙盒的網路政策擋掉了對 Docker Hub
    registry 的存取,一個現成的失敗情境),確認第一個服務已經建立的
    容器跟共用網路都被正確清乾淨,沒有殘留孤兒容器,也沒有寫進
    `state.json`。
  - 整個「填表單 → 安裝 → docker ps 看到真容器 → 按解除安裝 →
    容器真的消失」流程也透過 Playwright 對著真正的瀏覽器跑過一次
    (不只是 curl 打 API)。
  - 這次驗證仍然有邊界:這台沙盒的網路政策擋掉了 Docker Hub 等
    registry 的存取,所以「拉取一個真實的、之前沒快取過的映像檔」
    這件事本身沒辦法驗證,`cmd/gonasd/main.go` 裡關於
    `WriteTimeout` 為什麼刻意不設的說明因此仍然成立,`docs/
    REAL_HARDWARE_TESTING.md` 裡那一項也維持原樣待驗證。

至此,原始技術路線圖(Phase 0–9)已全部完成,並且比 Phase 9 剛交付時
多驗證了一層:Docker 容器/網路生命週期、rollback 邏輯已經對著真正的
Docker Engine 跑過而不只是邏輯正確性的單元測試。GoNAS 目前是一套零
第三方 Go 依賴、可交叉編譯到 x86_64/ARM64、涵蓋儲存陣列/Docker/檔案
分享/監控告警/備份快照/身分驗證與網路安全/一鍵安裝的完整 NAS 軟體
套件,剩下的工作是 `docs/REAL_HARDWARE_TESTING.md` 清單裡那些依然
需要真實硬體(硬碟、mergerFS/SnapRAID/Samba/NFS/WireGuard-tools 這些
這台沙盒的網路政策擋掉、裝不上的系統套件)才能驗證的項目。

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

## 部署

打包 release tarball(amd64 + arm64 各一份,含 `install.sh`/`uninstall.sh`/
`gonas.service`):

```sh
make release
# 產出 dist/release/gonas-<version>-linux-amd64.tar.gz
#      dist/release/gonas-<version>-linux-arm64.tar.gz
```

在目標機器上解壓、安裝(要 root;會自動判斷 amd64/arm64):

```sh
tar xzf gonas-<version>-linux-amd64.tar.gz
cd gonas-<version>-linux-amd64
sudo ./install.sh
```

`install.sh` 會把執行檔裝到 `/usr/local/bin/gonasd`、建立
`/etc/gonas/gonas.env`(監聽位址/資料目錄的覆寫檔,重複安裝/升級不會
覆蓋)跟 `/var/lib/gonas`;如果這台機器 systemd 真的是 PID 1,會順便裝
`gonas.service` 並開機自動啟動,不是的話會印出手動啟動的指令。裝完會
自動跑一次 `gonasd -check-deps`,列出這台機器還缺哪些選用工具
(mergerFS/SnapRAID/Samba/NFS/WireGuard/rsync)。

移除:

```sh
sudo ./uninstall.sh          # 只移除執行檔跟 systemd unit,保留設定與資料
sudo ./uninstall.sh --purge  # 連 /etc/gonas 跟 /var/lib/gonas 一起刪除(會要求輸入 yes 確認)
```

如果沒有下載 release tarball、是直接從原始碼安裝,`build/install.sh` 跟
`build/uninstall.sh` 也可以直接跑(`make build-all` 之後,會自動去
`dist/` 底下找對應架構的執行檔)。

## 授權

尚未指定,先以私人專案開發。
