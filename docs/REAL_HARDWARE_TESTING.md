# GoNAS 實機測試清單(Phase 9 / 9.1 / 9.2 / 9.3 / 10)

這份清單存在的理由很直接:GoNAS 從 Phase 0 到 Phase 8 的所有開發跟驗證,
都是在一個**沒有真實硬碟、沒有裝 mergerFS/SnapRAID/Samba/NFS/WireGuard-
tools/rsync、systemd 也不是真正 PID 1** 的雲端沙盒裡完成的(細節見各
Phase 在 README.md 裡的「已知取捨」段落跟 `internal/doctor` 套件的註解)。
每個功能都已經用假的 Runner/rsync/systemctl 腳本做過端到端驗證,單元
測試也涵蓋了指令組裝、狀態機、資料驗證這些跟「有沒有真硬體」無關的
邏輯 —— 但終究有一整類 bug 只有在真正的硬體、真正的磁碟、真正的
systemd 環境上跑過才驗證得出來。這份清單就是把「還缺這一塊」講清楚,
讓拿到實機的人知道具體要測什麼、預期看到什麼結果,而不是含糊地說
「上機測一下」。

**Phase 9.1 補充/訂正**:後來發現這台沙盒其實預裝了 `docker`/`dockerd`,
`dockerd` 也真的能手動啟動成功(只是網路政策擋掉了對 Docker Hub 等
registry 的存取,pull 真實映像檔會失敗)——所以「儲存陣列相關的外部
工具全部無法安裝/驗證」這件事仍然成立,但「完全沒有真實 Docker
Engine 可以測」這個前提已經不成立了。第 4 節已經更新,標出哪些部分
已經用真正的 `dockerd` 驗證過、哪些仍然卡在 registry 存取被擋。

**Phase 9.2 補充/訂正**:再往下查網路政策的邊界,發現 GitHub Releases
的檔案下載網址(`releases/download/...`)沒有被擋,跟一般的 GitHub
存取(分支/tag 壓縮包、`api.github.com`)不一樣。用這條路徑真的裝上了
mergerfs(`.deb`)跟編譯出 snapraid、rsync 的執行檔,補上了先前完全
無法驗證的「SnapRAID 資料復原能力」這一項——第 3、7 節已經更新,標出
哪些部分已經用真正的工具驗證過。Samba/NFS/WireGuard-tools 沒有類似的
例外路徑可用(apt 跟官方下載管道都被擋),這三項依然只驗證過邏輯
正確性,沒有真正的伺服器/介面可以測,詳見 README.md 的 Phase 9.2
段落。

每一節列出的都是「在沙盒裡已經盡力驗證過設計/邏輯是對的,但沒辦法
證明在真實環境下真的會動」的項目 —— 不是「完全沒做過驗證」的項目。

## 0. 準備

- 一台 x86_64 或 ARM64(例如樹莓派 4/5)的機器,乾淨安裝 Debian 12 /
  Ubuntu Server 22.04+ / Raspberry Pi OS(64-bit)其中一種,systemd 真的
  是 PID 1(這是絕大多數主流 Linux distro 的預設狀態,沙盒環境才是
  例外)。
- 至少 2 顆空的(可以損毀資料的)磁碟或分割區,拿來測資料碟+同位碟的
  陣列組合;理想上再多一顆熱備用/第二個同位碟測雙同位碟情境。
- 網路能連到套件源,安裝:`mergerfs`、`snapraid`、`smartmontools`、
  `samba`、`nfs-kernel-server`、`wireguard-tools`、`rsync`、`docker.io`
  或 Docker CE。裝完之後跑 `gonasd -check-deps` 應該全部顯示已安裝。
- 一台另外的裝置(筆電/手機)當「客戶端」,測 SMB/NFS 掛載、WireGuard
  連線、瀏覽器打開 Web UI。

## 1. 安裝與封裝(對應 Phase 8)

Phase 8 在沙盒裡已經完整測過 install.sh/uninstall.sh 的邏輯跟
「systemd 不是 PID 1」那條優雅降級路徑(用假的 `/run/systemd/system`
目錄跟假的 `systemctl` 腳本驗證過參數正確),**沒測過的是 systemd 真的
可以 `enable`/`start` 成功的那條路徑**。

- [ ] `make release` 之後,把 `dist/release/gonas-<version>-linux-<arch>.tar.gz`
      傳到這台機器上,解壓、`sudo ./install.sh`。
- [ ] 確認印出「已安裝並啟動 systemd 服務 gonas.service」而不是優雅降級
      的訊息。
- [ ] `systemctl status gonas.service` 確認是 `active (running)`。
- [ ] `curl localhost:8291/api/v1/health`、瀏覽器打開 Web UI 確認能連上。
- [ ] `sudo reboot`,重開機後不用手動做任何事,`gonasd` 應該自己啟動
      (`systemctl is-enabled gonas.service` 應該是 `enabled`)。
- [ ] 重新打包一個「新版本」(隨便改個字串、`make release`)、`sudo
      ./install.sh` 覆蓋安裝,確認是升級行為:`/etc/gonas/gonas.env`
      沒被覆蓋、既有的 `state.json`(帳號、陣列設定等)還在、服務有
      重新啟動成新版本(`gonasd -version` 確認)。
- [ ] `sudo ./uninstall.sh`,確認服務停止、`/etc/gonas`、`/var/lib/gonas`
      都還在;`sudo ./uninstall.sh --purge` 確認兩個目錄都被刪除。

## 2. systemd 進階沙盒加固(對應 Phase 9 刻意留白的部分)

`build/systemd/gonas.service` 裡的註解列了一組刻意先不加的 systemd
沙盒選項(`ProtectSystem=`、`PrivateDevices=`、`ProtectKernelModules=`、
`SystemCallFilter=`、`RestrictNamespaces=`),理由是這些選項會限制掛載
檔案系統/存取裝置節點/系統呼叫,而 gonasd 執行 mergerfs/snapraid 等子
行程時會繼承同一組限制,錯誤設定可能讓陣列相關功能以難以預期的方式
壞掉。這一節就是把這件事在真機上實際試出來:

- [ ] 在 `gonas.service` 加一行 `ProtectSystem=strict` 加上對應的
      `ReadWritePaths=`(至少要包含 `/var/lib/gonas`、陣列掛載點、
      `/etc/gonas`),`systemctl daemon-reload && systemctl restart gonas`,
      確認陣列還能正常啟動/停止、SnapRAID sync 還能寫入同位碟。
      如果壞了,記錄下是哪個路徑被擋掉,通常代表少加了一條
      `ReadWritePaths=`。
- [ ] 加 `SystemCallFilter=@system-service`,確認 mergerfs 掛載、
      SnapRAID sync/scrub、Samba/NFS 啟停都還正常。FUSE 掛載(mergerFS)
      如果失敗,通常代表 `mount`/`umount` 相關的系統呼叫不在
      `@system-service` 這個 group 裡,需要額外加 `@mount`。
- [ ] 加 `PrivateDevices=true`,確認 mergerFS 掛載(需要 `/dev/fuse`)
      是否還能成功 —— 這個選項理論上會拿掉大部分裝置節點的存取權,
      `/dev/fuse` 可能因此消失,預期這一項很可能會直接壞掉,先確認
      壞了再決定要不要另外加規則放行 `/dev/fuse`。
- [ ] 每項都是**單獨測試、單獨決定要不要保留**,不要一次全加上去再
      除錯 —— 加固的目的是「盡量收緊,但不能犧牲核心功能」,一次測
      一項才知道具體是哪個選項造成的影響。確定安全的項目,回頭補到
      `build/systemd/gonas.service` 並更新對應的註解說明。

## 3. 儲存(對應 Phase 1,Phase 9.2 已補上關鍵的真實驗證)

Phase 9.2 用 GitHub Releases 的例外網路路徑裝上了真正的 mergerfs
(v2.40.2)跟編譯出真正的 snapraid(v12.3),用三個 `fallocate` +
`mkfs.ext4` + `mount -o loop` 建出的獨立區塊裝置模擬三顆硬碟,直接
套用 GoNAS 自己的 `storage.GenerateSnapraidConfig`/`BuildMergerfsArgs`
產生的設定檔跟參數(不是手刻的等效版本)驗證過。下面標記 ✅ 的項目
已經這樣驗證過;沒標記的項目(SMART、硬碟熱插拔偵測)這台沙盒沒有
真實硬碟,依然只能在真機上驗證。

- [ ] `GET /api/v1/storage/disks` 列出的硬碟跟 `lsblk` 實際輸出比對,
      容量、型號、是否為系統碟等欄位是否正確。
- [x] ✅ `GET /api/v1/storage/disks/smart` 這支 API 本身、以及 Storage
      頁面新增的 SMART 欄位(Phase 12):對每顆硬碟平行呼叫
      `smartctl -a <device>`、單顆碟查詢失敗不會拖垮整支 API(用假
      Runner 模擬「其中一顆碟查不到 SMART」的單元測試,以及對這台
      沙盒 7 個真實 `/dev/vd*` 區塊裝置——沙盒本身沒裝 `smartctl`、
      也裝不了(套件庫連不到)——實際打 API 驗證過:回傳 200,每顆
      碟各自帶著清楚的「查詢失敗」錯誤訊息,不是整支 500)、Web UI
      三語系下正確顯示「無法讀取/无法读取/Unavailable」都已驗證。
- [ ] 上面驗證的是「管線本身」(API 設計、錯誤處理、UI 呈現)——真正
      有意義的 SMART 數值(溫度、健康狀態 PASSED/FAILED)跟
      `smartctl -a /dev/sdX` 的原始輸出比對是否一致,這台沙盒沒有
      真實硬碟、也沒辦法裝 `smartctl`,完全沒測過,真機上必須補測。
- [x] ✅ 用兩顆以上的磁碟設定一個 pool(一顆資料碟 + 一顆同位碟),
      確認 mergerFS 真的掛載成功(`mount | grep mergerfs`)。**已知
      異常**:透過 mergerFS 掛載點建立全新檔案在這台沙盒裡會失敗、
      回傳 ENOSPC,即使底層碟跟掛載點都還有足夠空間;用 `strace`
      確認是 `openat(..., O_CREAT)` 本身回傳 ENOSPC,懷疑是這個容器
      沙盒的 FUSE 環境特有的狀況、不是 GoNAS 的 bug,但**沒有完全
      根因**,真機上請額外確認透過 mergerFS 掛載點正常建立新檔案
      沒有問題(讀取、對底層碟直接寫入在沙盒裡都驗證正常)。
      most-free-space 建立策略是否生效也請一併在真機上確認(沙盒裡
      因為上述異常沒辦法透過掛載點驗證這件事)。
- [x] ✅ 觸發一次 SnapRAID sync,確認同位資訊真的寫進同位碟;也驗證過
      `snapraid diff`(異動偵測,注意真正的 snapraid 在「有異動」時
      是用 **exit code 2** 結束、不是 0——`internal/storage/
      snapraid.go` 的 `RunSnapraid` 已經修正成把這個狀況當成正常
      結果,不是錯誤)跟 `snapraid scrub`(位元腐化偵測)。
- [x] ✅ **模擬硬碟損壞並復原——全專案最關鍵的一項驗證,已經完整
      證實可行**:陣列停止、直接清空其中一顆資料碟底層目錄模擬整顆
      硬碟報銷,用 `snapraid fix` 搭配 GoNAS 產生的設定檔復原,逐一
      字對字比對復原後的檔案內容跟原始內容完全一致。真機上建議至少
      重複一次這個流程求個安心,但邏輯本身(GoNAS 產生的設定檔格式
      是否正確、`fix` 指令組裝是否正確)已經不是未知數。
- [ ] 陣列運作中重啟 `gonasd`(不是重開機),確認陣列狀態、pool 設定從
      `state.json` 正確載回,不會要求重新設定一次。

## 4. Docker / 應用程式商店(對應 Phase 2,Phase 9.1/9.3 已補上部分真實驗證)

Phase 9.1 發現這台沙盒其實有真的 `dockerd` 可以手動啟動,已經用一個
`FROM scratch` 建置、不需要連網就能取得的最小測試映像檔,對著真正的
Docker Engine 完整驗證過:單/多服務自訂安裝、容器與專屬網路的建立、
解除安裝時的清理、ID 碰撞防護、安裝失敗時的 rollback 邏輯(見
README.md 的 Phase 9.1 段落)。Phase 9.3 用同樣的手法額外驗證了容器
log/exec 這兩個新端點。下面標記 ✅ 的項目已經這樣驗證過,不需要在
真機上重測;沒標記的項目是這台沙盒的網路政策擋掉了對外部 registry
的存取,依然只能在真機上驗證。

- [x] ✅ `GET /api/v1/docker/ping` 對著真的在跑的 Docker daemon 確認回應
      正常。
- [x] ✅ 安裝/解除安裝一個 app(內建目錄範本或自訂 image 皆可),確認
      容器/網路資源真的被建立/清乾淨,包含多服務 App 的專屬網路。
- [x] ✅ 安裝失敗時(例如某個服務的 image 拉不到)的 rollback:已經
      驗證過會把這次呼叫已建立的容器跟網路清乾淨,不留孤兒容器,也
      不會寫進 `state.json`。
- [x] ✅ **容器 log/exec(Phase 9.3 新增)**:`GET .../containers/{id}/logs`
      跟 `POST .../containers/{id}/exec` 已經用一個內含長駐程式(印
      stdout/stderr)跟一支探針程式(印一行訊息、可選擇以失敗結束)的
      `FROM scratch` 測試映像檔,對著真正的 Docker Engine 完整驗證
      過:多工串流格式正確解開、`tail`/`timestamps` 參數生效、exec
      成功/失敗的結束碼正確回傳、容器不存在時回傳有意義的錯誤,並且
      用 Playwright 對著真正的瀏覽器跑過「查看 Log」「執行指令」兩個
      按鈕的完整互動。
- [ ] **仍待真機驗證**:從真正的 registry 拉一個先前沒快取過的映像檔
      安裝一個 App(內建範本或自訂 image 皆可),觀察安裝過程中 Web UI
      有沒有明顯卡住或逾時的跡象 —— 這個端點目前是**同步**等映像檔
      拉取完成才回應(見 `internal/api/appstore_handlers.go` 跟
      `cmd/gonasd/main.go` 裡關於 `WriteTimeout` 為什麼刻意沒設的
      註解),這台沙盒的網路政策擋掉了對 Docker Hub 等 registry 的
      存取,沒辦法驗證真實的拉取行為。
- [ ] **重點驗證項目**:改用一個比較大的映像檔(例如幾百 MB 以上),在
      較慢的網路環境下安裝,確認 Web UI/瀏覽器端的 fetch 請求不會在
      映像檔還沒拉完時就先逾時斷線。如果真的會斷線:
      1. 先確認是瀏覽器端 fetch 有沒有自己的逾時設定(`internal/api/webui/static/api.js`)造成的,不是伺服器端問題;
      2. 如果是伺服器端在映像檔拉完前就把連線關掉,需要把 `handleAppstoreInstall`
         改成非同步(比照 `internal/backup` 備份工作「先回 202、結果
         之後用輪詢或狀態查詢拿」的模式),這是一個需要真機才能確認
         「到底要不要做」的架構決定,不建議在沒有真實 registry 存取的
         情況下猜測性地先改。

## 5. 檔案分享(對應 Phase 3)

- [ ] 建立一個 SMB 分享跟一個系統使用者,從 Windows/macOS 客戶端用
      該帳號密碼掛載,確認讀寫權限符合設定。
- [ ] 建立一個 NFS 匯出,從 Linux 客戶端 `mount -t nfs` 掛載,確認權限
      跟 `exportfs -v` 顯示的設定一致。
- [ ] 修改/刪除分享跟使用者之後,確認 `testparm` 沒有回報設定錯誤,
      Samba 有透過 `smbcontrol` 正確 reload 而不需要重啟整個 `smbd`。

## 5.5 網頁版檔案總管(對應 Phase 10,已補上完整的真實驗證)

跟 Samba/NFS/WireGuard 不一樣,檔案總管只依賴檔案系統本身(不需要另外
安裝的伺服器軟體),所以這台雲端容器沙盒裡已經可以做到接近完整的真實
驗證,不是只靠單元測試——細節見 README.md 的 Phase 10 段落,包含兩次
真的路徑穿越/symlink 逃逸攻擊嘗試,以及過程中抓到並修正的一個真的
bug(複製到不存在的目的資料夾時洩漏伺服器路徑並回 500,已改成乾淨的
404)。以下只列出**這台沙盒沒辦法涵蓋、需要真實硬體才能驗證**的部分:

- [x] ✅ 瀏覽/上傳/下載(含 Range)/搬移/複製/重新命名/刪除+回收桶+
      復原/搜尋/文字檔線上編輯 —— 已用真正掛載的 mergerFS 池 + 真正
      的瀏覽器(Playwright)完整驗證,見 README.md Phase 10。
- [x] ✅ 路徑穿越與 symlink 逃逸防禦 —— 已用真的攻擊請求驗證擋下。
- [ ] **數 GB 等級的大檔上傳/下載**:這台沙盒的磁碟配額跟網路頻寬都
      不適合真的傳幾 GB 的檔案,只驗證過幾十 byte 的小檔案。真機上
      需要確認:上傳一個大檔案時,`cmd/gonasd/main.go` 裡對這個端點
      豁免 `ReadTimeout` 的做法是否還夠(目前是直接關掉這個請求的讀取
      期限,理論上沒有上限,但值得跑一次實測);下載大檔案搭配瀏覽器
      的續傳(中斷連線後重新整理,確認用 Range 接續而不是從頭重下)。
- [ ] **陣列接近寫滿時的行為**:mergerFS 的 mfs 建立策略在所有底層
      磁碟都快滿的時候如何選盤、上傳到剩餘空間不足時 GoNAS 回報的
      錯誤訊息是否清楚,這台沙盒的假磁碟太小,沒辦法真實重現「陣列
      真的快滿了」這種情境。
- [ ] **真的不穩定的網路**:模擬 Wi-Fi/行動網路那種會中途斷線、
      重新連線的環境,確認上傳進度條在斷線後的行為合理(目前沒有做
      自動續傳,斷線後需要使用者重新選檔上傳一次,值得在真機上確認
      這個限制是否需要優先解決)。
- [ ] 多使用者同時透過檔案總管操作同一個陣列。這一項的前提——
      GoNAS 支援多個各自獨立的管理帳號、並區分「管理者/檢視者」
      權限——已經在 Phase 13 做掉了(見下面第 6 節),但「兩個不同
      帳號同時在檔案總管操作同一個陣列,會不會有沒考慮到的競爭情況」
      本身還沒有真的用兩個瀏覽器分頁同時測過,列在這裡提醒之後補測。

## 6. 安全性(對應 Phase 6、Phase 9 新增的登入節流,Phase 13 新增的
   多帳號/權限)

- [x] ✅ **多管理帳號 + 管理者/檢視者權限**(Phase 13):新增第二、
      第三個帳號、指定角色、刪除帳號、不能刪自己、不能刪最後一個
      管理者、RoleViewer 對所有會新增/修改/刪除東西的端點收到 403、
      RoleViewer 可以正常改自己的密碼/設定 TOTP、帳號被刪除後對應的
      session 立刻失效——上面這些都已經用真正在跑的 `gonasd`(不是
      單元測試裡的假 Runner)實際打過 API 驗證過,也用 Playwright
      在三種語言下驗證過「帳號管理」畫面本身能正常新增/列出/刪除
      帳號、角色文字正確翻譯。真機上還沒驗證的是**多個真人同時各自
      用自己的帳號登入同一台 NAS 操作**這種更貼近實際多人使用情境的
      案例(這台沙盒的驗證都是同一個腳本依序呼叫,不是真的兩個瀏覽器
      同時操作)。
- [ ] 用真正的瀏覽器完成首次設定精靈、登入、修改密碼(確認其他分頁的
      session 真的失效)。
- [ ] 用真正的驗證器 App(Google Authenticator/Authy 等)掃描/手動輸入
      TOTP 設定,確認驗證碼能正常登入,時間誤差在合理範圍內容忍
      (`internal/security/totp.go` 允許前後各一個時間窗口)。
- [ ] 開啟 HTTPS,瀏覽器連線確認出現「自簽憑證不受信任」的警告是預期
      行為,手動信任後能正常使用;`openssl s_client` 確認憑證的
      SAN(主機名稱/IP)欄位正確。
- [ ] 建立 WireGuard 介面跟一個 peer,把產生的用戶端設定檔匯入手機或
      筆電的 WireGuard App,確認真的能連進來、AllowedIPs 範圍正確。
- [ ] **登入節流**(Phase 9 新增,見 `internal/security/ratelimit.go`):
      故意連續打錯密碼 5 次,確認第 6 次收到 HTTP 429 跟 `Retry-After`
      標頭;等鎖定時間過後,確認能重新用正確密碼登入。也確認鎖定期間
      *正確*密碼一樣會被拒絕(節流是鎖 IP,不是鎖「猜對猜錯」)。
- [ ] 從外部掃描端口(例如 `nmap`)確認除了 GoNAS 自己監聽的埠之外,
      沒有意外多開的服務。

## 6.5 監控告警通知管道(對應 Phase 5 的 webhook、Phase 14 新增的 email)

- [x] ✅ **Email 通知**(Phase 14,`internal/monitor/email.go`):這台
      沙盒連不到任何真正的外部郵件服務(Gmail、Office 365 等網域都在
      網路白名單之外),沒辦法對著真正的第三方信箱驗證,但已經用一個
      跑在 127.0.0.1、講真正 SMTP 文字協定的假伺服器(不是繞過網路層
      的 mock)做過完整驗證:建立一個真正在跑的 `gonasd`、設定一條會
      立刻觸發的告警規則、指向這個假伺服器的 email 通知管道,確認
      `AlertEngine` 的輪詢真的在規則觸發的當下透過真實 TCP 連線完成
      EHLO/AUTH PLAIN/MAIL FROM/RCPT TO/DATA 的完整交握,而且信件標題
      跟內文正確帶有規則名稱、指標、數值、FIRING 狀態。真機上還需要
      補測的是**對著真正的郵件服務商(Gmail 的應用程式專用密碼、公司
      Exchange/Office 365 等)**送信是否順利——特別是 STARTTLS 升級
      這一段(假伺服器的測試刻意沒有廣播 STARTTLS 擴充,只驗證了明文
      AUTH 這條路徑,真正的郵件服務商幾乎都要求先 STARTTLS 才准許
      AUTH,這條路徑目前只有程式碼審查過、沒有真的連線驗證過),以及
      不同服務商對「寄件人網域跟 SMTP 帳號網域不一致」這類反垃圾信
      規則的實際反應。
- [ ] Webhook 通知(Phase 5):對著一個真正可以連到的外部 HTTP 端點
      (例如 Slack Incoming Webhook、Discord Webhook,或自己架的一個
      小型 HTTP 伺服器)驗證規則觸發/解除時真的送出 POST,JSON 內容
      跟預期一致。這台沙盒的網路白名單同樣連不到這些外部服務,目前
      只在單元測試層級(用 `httptest.Server`)驗證過。

## 7. 備份(對應 Phase 7,Phase 9.2 已補上核心機制的真實驗證)

- [ ] 對真實的一批檔案(建議至少幾 GB,包含大檔案跟大量小檔案的混合)
      跑一次備份工作,確認 `rsync -aAX --delete` 真的保留了權限/
      ACL/擴充屬性。**注意**:這台沙盒編譯出來的 rsync 3.4.1 因為
      缺少 `libacl1-dev` 標頭檔而不支援 `-A`,已經透過真正在跑的
      `gonasd` API 驗證過這種情況會乾淨地失敗(`state.json` 正確標記
      工作失敗、錯誤訊息完整記下、沒有殘留半成品目錄),但沒辦法驗證
      ACL 真的有沒有被保留下來——真機上一般 `apt install rsync` 裝
      出來的版本都有完整 ACL 支援,請在真機上確認這件事。
- [x] ✅ 連續跑第二次,用 `stat -c "%i"`(inode)確認沒有變更的檔案在
      新舊快照之間是同一個 inode 且 link count 變成 2(硬連結生效,
      沒有重複佔用磁碟空間)——已經用真正的 rsync(`-aX`,拿掉沙盒
      裝不上的 `-A`)直接驗證過這個核心機制成立。真機上建議額外用
      `du -sh` 確認整個快照目錄集合的實際磁碟用量遠小於「快照數量 ×
      單份資料大小」。
- [x] ✅ 修改來源的一部分檔案後再跑一次,確認只有變更的檔案被複製
      (拿到全新的 inode)、沒變的部分還是共用硬連結——已經用真正的
      rsync 驗證過這個核心機制成立。
- [ ] 連續執行超過保留份數,確認最舊的快照被刪除,且刪除最舊快照不會
      不小心把還在用的硬連結資料弄丟(這在沙盒用假 rsync 已經驗證過
      邏輯,實機用真正的 rsync 跑一次求個安心)。
- [ ] 在陣列有其他讀寫負載(例如 Docker 容器正在寫資料、有人在用
      SMB 傳檔案)的情況下跑備份,確認不會因為檔案被佔用/鎖定而整個
      工作失敗,或至少失敗訊息清楚可辨識。

## 8. 效能與長時間穩定性

- [ ] 讓 `gonasd` 連續跑至少 72 小時(有陣列、有排程備份、監控輪詢
      正常運作的情況下),觀察記憶體使用量是否穩定(用
      `systemctl status gonas` 或 `ps` 看 RSS)——沙盒裡的驗證都是短時間
      的手動測試,goroutine/記憶體洩漏這類問題通常要長時間運行才會
      顯現。
- [ ] 監控頁面(`GET /api/v1/monitor/history`)在長時間運行後,History
      的容量上限(見 `internal/api/router.go` 的 `monitorHistoryCapacity`)
      是否確實把記憶體用量鎖在固定範圍,沒有隨時間無限增長。
- [ ] 在陣列有明顯讀寫負載時,觀察 Web UI 回應速度、SMART 檢查(會
      實際存取硬碟)有沒有讓一般 API 請求出現明顯延遲。

## 完成之後

把這份清單裡實際測出來的問題(尤其是「加了某項 systemd 加固導致
XX 功能壞掉」「大型映像檔安裝真的逾時了」這類結論)整理回
`build/systemd/gonas.service`、`cmd/gonasd/main.go` 的相關註解,或視
情況開一個新的 Phase(例如「Phase 10:非同步應用安裝」)——這份文件
本身也應該跟著更新,把已經驗證過的項目標記起來,而不是每次都從頭
測一遍。
