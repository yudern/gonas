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
- [x] ✅ 開啟 HTTPS、重啟 gonasd 讓設定生效、用 `openssl s_client` 對著
      一個真正在跑的 `gonasd` 確認憑證序號/到期日讀得出來、`curl -k`
      能正常打通 API——已經驗證過。真機上還沒驗證的是**用真正的瀏覽器**
      連線確認「自簽憑證不受信任」的警告畫面長什麼樣、手動信任的
      實際操作流程,以及 SAN 欄位在瀏覽器的憑證檢視畫面裡顯示是否
      符合預期(這台沙盒只用 `openssl`/`curl` 這些命令列工具驗證過
      憑證內容本身正確,沒有真的開瀏覽器點過信任流程)。
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

## 6.6 HTTPS 自簽憑證的自動續期(對應 Phase 16,已補上核心機制的
    真實驗證)

- [x] ✅ 已經對著一個真正在跑的 `gonasd` 完整走過一次「開啟 HTTPS →
      重啟讓 TLS 監聽生效 → 用真正的 TLS 交握(`openssl s_client`)
      確認當下憑證的序號/到期日 → 不重啟這個 process、直接呼叫
      `internal/security.RenewCertIfNeeded` 強制重新簽發 → 再做一次
      真正的 TLS 交握,確認同一個還在跑的 process(同一個 PID)已經在
      服務新憑證(序號改變、到期日變成新簽發的效期)」的完整流程,
      證明「憑證續期不需要重啟 gonasd」這件事在真正的 TCP/TLS 連線
      層級成立。
- [ ] 這次驗證是用手動呼叫 `RenewCertIfNeeded` 模擬續期時機到了,
      沒有真的讓 `CertRenewer` 背景 goroutine 自然跑到 24 小時的檢查
      週期、也沒有真的讓一張憑證的剩餘效期自然進入 30 天的續期門檻
      (`internal/api/security_handlers.go` 的
      `httpsCertRenewBefore`/`httpsCertRenewCheckInterval`)——這兩個
      數字背後的邏輯本身已經用可注入時間參數的單元測試涵蓋過
      (`internal/security/certs_test.go`),但沒有真機上跑滿這個
      時間尺度的端到端驗證。建議真機上如果想認真驗證這件事,可以
      暫時把這兩個常數改小(例如效期改成幾分鐘、續期門檻改成效期的
      一半)重新編譯一份測試用的 `gonasd`,實際等背景 goroutine 自己
      觸發續期,而不是永久修改成生產環境也用的小數字。
- [ ] 沒有驗證過「gonasd 連續跑了將近 2 年,自簽憑證真的自然進入
      續期門檻」這種真實時間尺度下的長期行為——這本質上是無法在
      合理時間內用真機驗證的情境,只能靠程式碼審查跟前面幾項單元
      測試/端到端驗證建立信心。
- [ ] 用真正的瀏覽器確認:續期之後,原本已經手動信任過舊憑證的
      瀏覽器分頁,重新整理後會不會又跳出一次「憑證變了、要重新信任」
      的警告——理論上會,因為新憑證是全新簽發、跟舊憑證的公鑰/序號
      都不一樣,這是自簽憑證架構下無法避免的使用者體感,值得在真機
      上實際確認一次,評估要不要在 Web UI 上加一段「憑證續期後瀏覽器
      需要重新信任一次」的提醒文字。

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

## 7.5 備份工作的 cron 語法排程(對應 Phase 15,已補上核心機制的真實驗證)

- [x] ✅ 已經對著一個真正在跑的 `gonasd`、真正的 `rsync`,建立一個
      `* * * * *`(每分鐘)的 cron 種類備份工作,實際輪詢確認它在
      `09:30:00`、`09:31:00` 這兩個整分鐘各觸發了一次(`lastRun`
      正確推進到下一分鐘),證明 `internal/cron.Schedule.Next` 算出
      的時刻正確、`JobScheduler.runCronLoop` 真的照著這個時刻觸發,
      而不是隨便一個固定間隔。也驗證了不合法的 cron 表達式在建立時
      被 400 擋下(不會寫進 `state.json`)、刪除工作後排程確實停止
      (之後不會再有新的 `lastRun` 更新)、原本 interval 種類(`Kind`
      欄位留空)的既有建立流程完全沒有回歸。
- [ ] 這次驗證只覆蓋了「每分鐘」跟隱含在單元測試裡的月份/星期/範圍/
      間隔語法邏輯,沒有拿一個橫跨真正日曆邊界的長時間案例(例如設定
      「每月 1 號凌晨 3 點」,實際等一整個月看它會不會準時觸發)在
      真機上跑過。建議至少讓一個這種低頻率的 cron 工作跑滿一到兩個
      完整週期,確認 `gonasd` 重啟(見下方 8. 效能與穩定性測試)不會
      让它錯過或重複觸發下一次執行。
- [ ] 這次驗證用的來源目錄很小(一個測試檔案),沒有驗證「cron 排程
      觸發時剛好前一次備份還沒跑完」的情況——`runCronLoop` 目前的
      設計是 `runOnce` 阻塞執行完才會進入下一輪 `nextFn` 計算,理論上
      不會重疊觸發,但建議真機上用一個資料量夠大、真的會跑超過一分鐘
      的備份工作,搭配 `*/1 * * * *` 這種高頻率排程,確認確實沒有
      重疊執行、也沒有訊息堆積。
- [ ] `internal/storage.ParitySchedule`(SnapRAID 校驗排程)這次
      刻意維持不動——它目前完全沒有被 `internal/api` 或
      `internal/state` 引用(既有的死碼,不是這次新增的問題),之後
      真的要把 SnapRAID 校驗排程接上真機測試時,建議直接重複使用
      `internal/cron` 套件,而不是再維護一套獨立的簡化排程邏輯。

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

## 9. gonasd 自我更新(對應 Phase 17,已補上核心機制的真實驗證)

- [x] ✅ 已經在這個沙盒裡對兩個真正編譯出來、注入不同版本號
      (`v1.0.0-test`/`v9.9.9-test`)的 `gonasd` 執行檔,加上一個服務
      真實 `manifest.json`(內含真正用 `sha256sum` 算出來的 checksum)
      跟真實二進位檔的本機 HTTP 伺服器,完整走過一次「啟動舊版本
      `gonasd` → 用 `curl` 打真正的 API 設定更新來源網址 → 呼叫真正的
      檢查端點確認 `updateAvailable: true` 且 `latestVersion` 正確 →
      呼叫真正的套用端點 → 輪詢確認同一個 PID 的程序重啟完成、
      `GET /api/v1/version` 回報新版本號、`updateAvailable` 恢復
      `false`」的完整流程。這個流程也是「`/proc/self/exe` 跟著
      rename 走」那個真實 bug(見 README「Phase 17」一節的詳細說明)
      被實際抓到的地方——第一版實作在單元測試裡(用注入的假路徑)
      完全測不出問題,只有這種「真的啟動一個程序、真的讓它自我更新
      重啟」的驗證才抓得到,修好之後重跑同樣流程確認 `ps`/
      `/proc/<pid>/comm` 顯示的執行檔名稱正確、不是 `.previous`。
- [x] ✅ **跨架構設定錯誤的處理**:架了一個 manifest 只列出
      `linux-arm64`(這台沙盒是 linux-amd64),確認
      `POST /api/v1/system/update/apply` 不會誤套用或崩潰——
      `applyStatus` 正確標記成 `failed`,錯誤訊息明確指出
      `manifest has no asset for platform "linux-amd64"`,而且執行檔/
      程序完全沒被動到(PID、版本號都沒變),`GET
      /api/v1/system/update` 能查到這個失敗原因。額外確認了失敗之後
      立刻重新呼叫 apply 不會被「已經有更新在背景執行」的鎖卡住
      (`applyStageFailed` 視為可重試狀態)。**仍然沒有驗證的部分**:
      這只驗證了「manifest 不包含目前平台」這個設定錯誤的處理,沒有
      驗證「manifest 正確列出 arm64、在一台真正的 arm64 機器/QEMU
      使用者態模擬環境上真的把 arm64 執行檔下載回來並成功執行」這個
      正向情境(這台沙盒沒有 arm64 硬體或 `qemu-user-static`)。
- [x] ✅ **Docker 容器化部署 gonasd 本身**(不是 GoNAS 管理的那些 App
      商店容器,而是 gonasd 自己被包進 Docker 容器、以 PID 1 執行):
      在這台沙盒裡實際啟動了一個真正的 `dockerd`,把一份真實編譯的
      `gonasd`(`CGO_ENABLED=0`,`FROM scratch` 基礎映像,不需要拉取
      任何遠端映像層)跑成容器、以 `--network host` 對著容器裡的
      gonasd 打真正的 REST API,完整走一次檢查/套用流程。結果:
      `docker inspect` 顯示 `RestartCount=0`、容器啟動時間
      (`StartedAt`)完全沒變、`docker logs` 裡看得到 gonasd 自己記錄
      的「重新啟動」訊息,證明 `syscall.Exec` 換掉 PID 1 的程式映像檔
      這件事,從 Docker 引擎的角度完全不可見——不會觸發任何重啟策略、
      不算一次容器重啟。`docker cp` 把容器裡的執行檔跟 `.previous`
      備份都複製出來直接執行確認版本號,兩份都正確。overlayfs 這種
      常見的容器儲存驅動下,`os.Rename` 的原子置換假設也確認成立。
      **仍然沒有驗證的部分**:這次用的是預設的 overlayfs 儲存驅動、
      預設可寫的根檔案系統;沒有驗證「唯讀根檔案系統 + 額外掛載一個
      可寫層」這種更嚴格的容器安全性設定下(例如
      `docker run --read-only`),執行檔所在目錄是否還可寫、置換是否
      還能成功——這種設定下大概率會直接寫入失敗,`DownloadAndVerify`
      應該會在 `os.MkdirAll`/`os.CreateTemp` 那一步就乾淨地回報錯誤
      (不會半途損毀任何東西),但這個失敗路徑本身沒有真的驗證過。
- [ ] **仍然沒有驗證的部分**:沒有在真正由 systemd 監督(PID 1 是
      systemd、`Restart=on-failure` 之類的重啟策略生效中)的安裝方式
      底下驗證過自我更新。這次嘗試過在這個沙盒裡跑 `systemctl`,
      結果是「System has not been booted with systemd as init system
      (PID 1)」——這個容器化的開發沙盒本身的 PID 1 不是 systemd,
      沒有辦法在不弄壞沙盒本身的前提下臨時「假裝」有一個真正在跑的
      systemd 環境,所以這一項**維持誠實地標記為未驗證,而不是硬做一
      個看起來像但實際上沒有真正 systemd 監督的假測試**。上面 Docker
      容器化那一項已經間接證明「`syscall.Exec` 對外部監督者(Docker
      引擎)完全不可見」這個核心假設在另一種真實的監督情境下成立,
      systemd 的情況原理相同(`syscall.Exec` 不改變 PID、不觸發
      `exit`/`fork` 事件),但仍然值得在一台真正裝了
      `build/systemd/gonas.service`、`systemctl start gonas` 啟動的
      機器上補一次:套用更新過程中 `systemctl status gonas` 顯示的
      PID 應該完全不變,`journalctl -u gonas` 的日誌應該連續不中斷。
- [x] ✅ **手動復原流程**:對著一個真正在跑的 gonasd 完整走過一次
      「套用更新成功 → 模擬使用者發現新版本有問題 → 手動 kill 程序 →
      `mv gonasd.previous gonasd` → 重新啟動」的完整手動復原流程,
      確認復原後 `GET /api/v1/version` 正確回報回舊版本號,而且
      `state.json`(管理者帳號、更新來源設定等)完全沒受影響——因為
      只有執行檔本身被置換,資料目錄從頭到尾沒被動過。這確認了
      README/`install.sh` 目前記錄的手動降級步驟是可行的。
- [x] ✅ **Phase 18a:一鍵復原(`POST /api/v1/system/update/rollback`)
      端到端驗證**——上面手動復原流程驗證過後,把同樣的邏輯做成
      正式 API/UI 功能(`internal/selfupdate.RollbackToBackup` +
      `handleSystemUpdateRollback`),然後對著一個真正在跑的 gonasd
      重新完整驗證一次,這次完全透過 HTTP API、不手動碰檔案系統:
      啟動真正編譯的 `v1.0.0-test` gonasd → 用 `curl` 呼叫真正的
      `/api/v1/auth/setup`、`/api/v1/system/update/settings`、
      `/api/v1/system/update/check`、`/api/v1/system/update/apply`
      走完一次套用更新(確認同一個 PID、`GET /api/v1/version` 回報
      `v9.9.9-test`、`GET /api/v1/system/update` 的
      `backupAvailable` 正確變成 `true`)→ 呼叫真正的
      `POST /api/v1/system/update/rollback` → 確認同一個 PID 沒變、
      `GET /api/v1/version` 正確回報回 `v1.0.0-test`、
      `backupAvailable` 恢復 `false`(`.previous` 已經被消耗)、
      原本(被復原掉的)`v9.9.9-test` 執行檔被保留成
      `gonasd.rolled-back-<unix>` 而不是直接刪除(直接執行這份保留
      檔案的 `-version` 確認內容正確)、兩次重啟之間
      `state.json`(管理者帳號、更新來源設定)全程沒有遺失,重啟後
      需要重新登入(session 存在記憶體、不是持久化狀態,這點跟一般
      重啟 gonasd 的行為一致,不是 bug)。
- [x] ✅ **大檔案下載**:用一份真實編譯的 gonasd 執行檔、在尾端補上
      隨機資料撐到剛好 100 MiB(附加在 ELF 有效內容之後的資料不影響
      可執行性,補完之後 `-version` 依然正常執行),算出真正的
      SHA-256,透過真正的 HTTP 伺服器提供下載,對一個真正在跑的
      gonasd 完整走一次檢查/套用流程——下載、校驗、置換、重啟整個
      流程在 loopback 網路下不到 1 秒完成,確認 `DownloadAndVerify`
      處理百 MB 級檔案沒有記憶體暴增或邏輯錯誤,`updateHTTPTimeout`
      (5 分鐘)在這個量級下留有非常寬裕的餘裕。**仍然沒有驗證的
      部分**:這是 loopback 網路,沒有測過真實、可能不穩定的跨網段
      連線品質下(例如下載到一半網路中斷、DNS 解析很慢)的行為——
      程式碼邏輯上 `io.Copy` 遇到連線中斷會回傳 `err`,`DownloadAndVerify`
      會清掉暫存檔並回傳錯誤,不會留下半下載的檔案,但沒有用真正
      不穩定的網路環境驗證過這個路徑。
- [x] ✅ **併發/資料競爭掃描**:對整個 repo(不只是 Phase 17 新增的
      程式碼)跑了一次 `go test ./... -race -count=1`,涵蓋
      `internal/selfupdate.Checker`(mutex 保護 `latest`)、
      `internal/api.Server` 新增的 `applyMu`/`applyStatus`、
      `restartRequested` channel 這些新的併發狀態——沒有發現任何
      data race。

## 10. 管理者動作稽核紀錄(對應 Phase 18b,已補上核心機制的真實驗證)

- [x] ✅ 對一個真正在跑的 gonasd 完整走一次:`curl` 完成 first-run
      setup 拿到 session cookie → 呼叫真正的
      `POST /api/v1/share/shares` 建立一個共享,確認回應成功
      (HTTP 200)→ 用同樣的名稱再呼叫一次,確認正確回報衝突
      (HTTP 409,`a share with that name already exists`)→ 呼叫真正的
      `GET /api/v1/audit/log`,確認剛剛那兩次操作都被記下來、依時間
      新到舊排序、狀態碼分別是 200 跟 409 —— 包含失敗的操作也要被記
      下來,這正是稽核紀錄要回答「管理者嘗試做了什麼、結果如何」的
      設計目的。中間穿插呼叫了一次單純的 `GET /api/v1/share/shares`
      查詢,確認它沒有被記進稽核紀錄——單純瀏覽/查詢不算「動作」。
- [x] ✅ **意外抓到的既有 bug**:新增 `state.State.AuditLog` 欄位時,
      忘記把它加進 `state.(*State).normalize()`(負責把所有切片欄位
      的 `nil` 換成空陣列的函式)——`internal/state` 既有的
      `TestOpen_LoadsPreExistingNullSlices_NormalizesThem` 測試立刻
      失敗,精準抓到「讀取一份缺這個欄位的舊版 state.json 時,
      `auditLog` 會被序列化成 `null` 而不是 `[]`」這個問題(前端一律
      當陣列處理,收到 `null` 會直接掛掉,是 Apps 頁面在更早的 Phase
      踩過的同一類 bug)。修好之後(補上
      `if st.AuditLog == nil { st.AuditLog = []AuditEntry{} }`)整個
      repo 的 `go test ./... -race -count=1` 重新轉綠。這是一個很好的
      案例,說明「新增欄位時保留既有的防呆測試」本身的價值——不需要
      額外新寫測試就抓到了這個問題。

## 11. 週期性健康摘要通知(對應 Phase 18c,已補上核心機制的真實驗證)

- [x] ✅ 對一個真正在跑的 gonasd,同時涵蓋三種通知管道的完整流程:
      啟動一個真正的 Python HTTP 伺服器當 webhook 接收端、一個真正的
      Python `smtpd`(標準函式庫,雖然已標記為 deprecated 但功能正常)
      當假 SMTP 伺服器,用 `curl` 建立一個真正的 webhook 通知跟一個
      真正指向這個假 SMTP 伺服器的 email 通知、外加一個備份工作(還沒
      執行過,用來驗證摘要內容裡的「還沒有執行過」文字)→ 呼叫真正的
      `PUT /api/v1/monitor/digest` 設定成把摘要送到這兩個管道 → 呼叫
      真正的 `POST /api/v1/monitor/digest/send` 立刻送出一次 → 確認:
      webhook 伺服器真的收到一份 JSON,`kind` 欄位正確是 `"digest"`,
      `subject`/`message` 內容包含正確的 CPU/記憶體/磁碟使用率數字;
      SMTP 伺服器真的透過完整的 SMTP 交握(不是 mock)收到一封信,
      標頭(From/To/Subject/Date)跟純文字內文都正確,內文包含備份
      工作「還沒有執行過」那一行;daemon 自己的 log 裡也記了一筆
      `health digest` 訊息;`GET /api/v1/monitor/digest` 的
      `lastSentAt` 正確更新成剛剛送出的時間。
- [x] ✅ **停用之後不會殘留背景排程**:先透過 API 啟用一次 digest
      (`enabled:true`,合法的 cron 表達式),再透過 API 停用
      (`enabled:false`),然後重新啟動整個 gonasd 程序,確認
      daemon 啟動的 log 裡沒有任何 digest 排程被拉起來的跡象——跟
      `internal/security.CertRenewer`「看設定決定要不要啟動」是同一種
      行為,不是「設定過一次就永遠有個背景 goroutine 醒著」。
- [x] ✅ **併發/資料競爭掃描**:對整個 repo 跑了一次
      `go test ./... -race -count=1`,涵蓋這次新增的
      `internal/api.Server` 的 `digestMu`/`digestScheduler`、
      `monitor.DigestScheduler` 內部狀態——沒有發現任何 data race。

## 12. GoNAS 開機即用映像檔(對應 Phase 19,完全尚未驗證)

- [ ] **建置流程本身**(`build/appliance/build-iso.sh`):沒有在任何
      環境裡真的執行過。這個開發沙盒的網路出口對所有 OS 套件鏡像都是
      全面擋下的——不只是 `deb.debian.org`(`curl -m 8
      https://deb.debian.org/debian/dists/stable/Release` 回傳
      `curl: (56) CONNECT tunnel failed, response 403`),連這個沙盒
      自己的 Ubuntu 24.04 執行 `apt-get update` 都對
      `archive.ubuntu.com`/`security.ubuntu.com`/
      `download.docker.com` 全部收到 `403 Forbidden`(查過代理服務的
      允許清單,裡面沒有任何 OS 套件鏡像網域)——所以這個沙盒裡沒有
      任何辦法下載官方 Debian netinst ISO,也沒有辦法安裝
      `xorriso`/`qemu-system-x86_64`/`qemu-system-aarch64`。需要一台
      有真正網路連線的機器或 CI 執行 `make iso-amd64`/
      `make iso-arm64`,確認能成功產出 `.iso`/`.sha256` 檔案。
      `build-iso.sh` 也會下載 Debian 官方的 `SHA256SUMS` 比對下載回來
      的 ISO 是否完整(雜湊不符就中止,不繼續往下做),但這個檢查
      本身以及它的失敗路徑(雜湊真的不符、或 `SHA256SUMS` 下載失敗
      時腳本是否真的乾淨中止、不留下部分處理過的檔案)同樣沒有實際
      跑過,需要在第一次真正建置時順便確認。
- [ ] **`preseed.cfg` 能不能被真正的 debian-installer 正確解析、
      自動跑完整個安裝不卡在非預期的問答畫面**:完全沒有驗證過,只做
      過人工覆閱跟對照 Debian 官方 Installation Guide 附錄 B 的語法。
      需要在 QEMU/VirtualBox 裡開機測試(步驟見
      `build/appliance/README.md`「如何驗證」一節),確認除了刻意保留
      的磁碟寫入確認畫面之外,不會停在任何其他問題上。
- [ ] **`late-command.sh` 在真正的 debian-installer `in-target` chroot
      環境裡執行是否成功**:完全沒有驗證過。需要確認
      `build/install.sh`(既有、未修改的軟體安裝腳本)在這個環境下能
      正常執行完、`systemctl enable gonas-console.service`/
      `systemctl mask getty@tty1.service` 在 chroot 環境裡的行為符合
      預期(chroot 環境裡呼叫 `systemctl` 有時候會因為沒有真正在跑的
      init 而只更新 unit 檔案的符號連結、不會立刻生效,這是正常的,
      重開機後才會真正生效,但這一點沒有實際確認過)。
- [ ] **`gonas.service` 開機自動啟動是否真的生效——這是覆閱時額外
      發現、目前已知最重要的一項未驗證風險**:`install.sh` 自己判斷
      「要不要做 systemd 整合」的條件是 `[ -d /run/systemd/system ]`,
      這是為了在真的沒有 systemd 的環境(例如某些容器)優雅跳過,但
      late-command.sh 透過 in-target 執行 install.sh 的當下,目標
      系統的 systemd 套件雖然已經裝好,這個 chroot 執行環境本身通常
      不會有一個真正在跑的 systemd 實例,`/run/systemd/system` 大概率
      不存在,導致 install.sh 會整段跳過 systemd 整合(包含
      `systemctl enable`)。如果放著不管,重開機後 gonasd 不會自動
      啟動,直接違背「開機就是一台 GoNAS」的目標。已經在
      `late-command.sh` 裡補上一段獨立的 `systemctl enable
      gonas.service`(理由見該檔案的新註解:enable 對一個簡單
      `[Install] WantedBy=` unit 而言只是操作檔案系統 symlink,不需要
      真正在跑的 systemd 執行個體,deb-systemd-helper 在套件安裝腳本
      chroot 環境裡設定服務開機啟動用的是同一個機制),但這個補救
      步驟本身**完全沒有驗證過是否在真實環境下真的生效**——第一次
      在 QEMU 裡開機測試時,`systemctl is-enabled gonas` 是否回傳
      `enabled` 是最優先要確認的一項,見
      `build/appliance/README.md`「如何驗證」一節第 4 點。
- [x] ✅ **`overlay/usr/local/sbin/gonas-console` 腳本本身的邏輯**
      (不透過 ISO 開機,單獨執行這支 shell script 驗證):在這個沙盒
      裡直接執行過兩次——一次 PATH 上完全沒有 `gonasd` 時,正確顯示
      版本「unknown」跟「尚未偵測到網路連線」;一次 PATH 上有一個
      真的編譯出來(`-ldflags -X .../version.Version=dev`)的
      `gonasd` 時,正確透過 `gonasd -version` 抓到版本字串「dev」並
      顯示出來。這是這個 Phase 目前唯一一項真正執行驗證過的部分。
- [ ] **tty1 主控台實際在真正開機的系統上取代 getty 是否成功**:完全
      沒有驗證過,需要在 QEMU 裡完整跑完安裝、重開機後直接觀察
      tty1 畫面確認。
- [ ] **重開機後 GRUB 選單品牌化、`hostnamectl`/`/etc/os-release`/
      `/etc/motd` 品牌化是否生效**:完全沒有驗證過。
- [ ] **所有 shell/preseed 檔案的語法檢查**:已完成——`sh -n` 對
      `build-iso.sh`/`late-command.sh`/`gonas-console` 全部通過(這個
      沙盒沒有 `shellcheck` 可用,無法做更深入的靜態分析,已確認
      `which shellcheck` 找不到、也沒有辦法安裝)。這只確認語法合法,
      不代表邏輯/行為正確。
- [ ] **arm64 映像檔的開機測試**:上面所有項目都還沒對 amd64 驗證過,
      arm64(需要 `qemu-system-aarch64` + UEFI 韌體)更是完全沒有嘗試
      過,細節見 `build/appliance/README.md`。

**第二輪覆閱(應使用者要求「再次全面查」)額外發現、已修正的項目**——
這一輪不是重讀同一份檔案自我複誦,而是逐條推演真正的 debian-installer/
debconf 行為細節,抓到幾個先前沒注意到的具體風險,全部已經修正,但
**修正本身一樣完全沒有實機/VM 驗證過**,所以下面每一項都同時是「已
修正」跟「待驗證」:

- [ ] **`priority=critical` 改成 `priority=high`**:原本的開機參數用
      `priority=critical`,這代表 debconf 只會顯示 critical 等級的
      問題,任何 `preseed.cfg` 沒有明確給答案、但優先權在 critical
      以下的問題,會被悄悄用範本預設值帶過、不會顯示、也不會中止
      安裝。這支 preseed 刻意不回答 `partman/confirm`(寫入磁碟的
      最終確認)這類問題,如果它的優先權剛好低於 critical,舊的設定
      可能導致它被無聲跳過,而不是如預期地停下來要求確認——這是
      安全性上最需要留意的一項。已改成 `priority=high`,只要
      `partman/confirm` 等問題的優先權是 high 或以上(這是合理的
      推測,但沒有查證每個 Debian 版本的實際範本定義),就會確實
      顯示出來。**第一次 VM 測試最優先要確認的就是這一項:磁碟分割
      到最後是不是真的停下來要求按鍵確認,還是直接無聲寫入。**
- [ ] **多顆磁碟時安裝程式會停下來問「要分割哪一顆」**:`preseed.cfg`
      故意沒有設定 `partman-auto/disk`,所以多碟機器上這是預期會
      發生、不是 bug 的一次額外停頓(細節見 `preseed.cfg` 內文)——
      需要在掛了兩顆以上磁碟的 VM 環境驗證這個停頓真的會出現,而不
      是被自動選了「猜測」出來的磁碟。
- [ ] **`d-i grub-installer/bootdev string default`**:先前完全沒有
      preseed 這一題,多碟機器上 grub-installer 可能額外問一次
      「開機程式裝哪個裝置」,已經補上 `default` 值(Debian 官方
      文件建議的寫法),沒有實際驗證過是否確實避免了這個提問。
- [ ] **`d-i hw-detect/load_firmware boolean false`**:先前完全沒有
      回答這一題,真實硬體上(不是 VM)如果偵測到網卡/儲存控制器
      缺韌體,舊版本可能會卡在「請插入含韌體的媒體」等待畫面。已經
      明確回答「不要」,沒有實機驗證過是否真的避免了這個等待——這是
      唯一一項「VM 測試測不出來」的項目(QEMU/VirtualBox 的 virtio
      裝置不需要額外韌體),必須留到真機測試才能確認。
- [ ] **`build-iso.sh` 的 grub `---` 參數注入 regex 容錯**:原本
      `sed` 規則要求 `---` 後面完全沒有其他字元才會命中,如果實際的
      grub.cfg 在 `---` 後面有尾端空白,規則會靜默不命中、preseed
      參數就不會被注入,結果是開機後掉回完全手動安裝、卻沒有任何
      錯誤訊息。已經放寬成容許尾端空白,但仍然沒有拿到一份真正的
      Debian grub.cfg 逐行比對過。
- [ ] **`late-command.sh` 裡停用/遮罩 `getty@tty1.service`、啟用
      `gonas-console.service`/`gonas.service` 的 `systemctl` 呼叫**:
      原本這幾行呼叫用 `2>/dev/null || true` 完全吞掉結果,如果它們
      在某個 Debian 版本的 in-target chroot 環境裡也失敗(就像
      `gonas.service` 那個已經抓到的真實案例一樣),重開機後只會
      看到「tty1 還是一般登入畫面」的結果,卻無從得知是哪一步失敗
      的。已經改成每一步都明確記錄成功/失敗到 late-command.sh 自己
      的 log 輸出裡,第一次測試如果 tty1 沒有如預期顯示狀態畫面,
      應該先去看這份 log(重開機前,在 in-target 執行當下的輸出
      通常會被導到安裝程式自己的日誌;重開機後可以用
      `journalctl -b -1` 或 `/var/log/syslog` 回頭找)。
- [ ] **`/etc/hosts` 的 `127.0.1.1` 那一行原本可能整個不存在**:
      如果目標系統的 `/etc/hosts` 根本沒有 `127.0.1.1` 這一行(某些
      最小化安裝可能沒有),原本的 `sed` 規則會靜默不做任何事,新
      主機名稱「gonas」就不會出現在本機名稱解析裡(純粹是
      `hostnamectl`/`ping gonas` 這類本機名稱解析的細節,不影響
      gonasd 監聽 0.0.0.0 對外提供服務這件事本身)。已經改成缺這一行
      就直接補上一行,而不是假設一定存在。

**第三輪覆閱(使用者要求「再來一次,全面的」)額外發現、已修正的
項目**——這一輪換個角度看,不是繼續挑 preseed/late-command 的邏輯
細節,而是檢查 `build-iso.sh` 本身的建置流程健壯性(重跑會不會壞事、
會不會浪費使用者的時間跟頻寬):

- [ ] **重複執行 `make iso-amd64`/`iso-arm64` 分開兩次呼叫,會清掉
      對方的產物**:`release` target 一開始會 `rm -rf dist/release`,
      如果分兩次獨立的 `make` 指令執行(先 `make iso-amd64`,之後才
      單獨 `make iso-arm64`),第二次會把第一次放在同一個
      `dist/release/` 目錄下的 ISO 一起清掉——這不是 bug,是
      `release` target 本來的行為,只是原本沒有在任何文件裡提醒過,
      已經在 `build/appliance/README.md` 補上說明跟建議做法(用
      `make iso` 一次做兩個架構,或自己先搬走上一次的產物)。這一項
      不需要在 VM 裡驗證,是純粹的 Makefile/檔案系統行為,邏輯上
      可以直接推導出來,但沒有實際跑過 `make` 確認過真的會這樣。
- [ ] **`xorriso -outdev` 指向一個已經存在的檔案時的行為**:原本沒有
      在重新包裝前清掉舊的 `$OUT_ISO`,不確定 xorriso 在輸出路徑已經
      有檔案時是乾淨覆蓋、還是嘗試當成既有的多重 session ISO 處理
      (沒有實際測試過,只是保守假設「不確定就不要冒險」)——已經在
      xorriso 執行前加上 `rm -f "$OUT_ISO" "$OUT_ISO.sha256"`,第一次
      真正建置、且重複執行一次確認第二次產出的 ISO 內容/大小跟第一次
      一致,是值得順手確認的一項。
- [ ] **新增的官方 ISO 本地快取(`dist/.cache/debian-iso/`)邏輯本身
      沒有被驗證過**:每次都重新抓 `SHA256SUMS`,只有雜湊值真的對得
      上才重用快取的 base ISO,理論上比較安全(不會被過期快取誤導),
      但這整套「先抓 SHA256SUMS 決定快不快取、比對、決定要不要寫入
      快取」的邏輯是全新的,只做過 `sh -n` 語法檢查跟人工覆閱,沒有
      實際下載驗證過。第一次建置時可以連續跑兩次 `make iso-amd64`,
      確認第二次輸出「reusing cached ...」而不是重新下載一次。

**第四輪覆閱額外發現、已修正的項目**——這一輪把 appliance 新增的
`gonas-console.service` 拿去跟(這一輪之前已經修好的)`gonas.service`
放在一起看兩者的 systemd 排序關係會不會互相影響,抓到一個原本各自
獨立看都沒問題、放在一起才會出現的問題:

- [ ] **`gonas-console.service` 原本掛在 `network-online.target`
      之後,現在這樣設計其實會拖慢、甚至可能讓 tty1 畫面長時間空白**:
      原本的理由是「這樣第一次顯示畫面時比較可能已經有網路位址可以
      秀」,這個理由在寫的當下沒有問題,但重新覆閱時發現:
      `gonas.service` 本身有 `Wants=network-online.target`(這是
      Phase 8/9 就有的既有設計,這次沒有改動它),而这次 Phase 19
      修好的正是「gonas.service 原本沒有被 enable」這個 bug——也就是
      說,`network-online.target` 现在真的會在每次開機時被拉進開機
      流程。如果這台機器開機當下網路線還沒插好、DHCP 還沒生效、或
      接的是需要幾秒鐘做 STP/協商的網管交換器(對實體 NAS 硬體完全
      不是罕見情境),`*-wait-online` 類服務常見的預設逾時是數十秒
      等級——這段時間 tty1 會整個是黑畫面(getty 已經停用/遮罩,
      主控台又排在 network-online.target 後面),直接傷到這個 Phase
      最核心的賣點「開機馬上看到 GoNAS 品牌畫面」。已經把
      `gonas-console.service` 的 `After=` 拿掉 `network-online.target`
      ——這支主控台腳本本來就已經妥善處理「還沒有網路位址」的情況
      (顯示提示文字,每 5 秒重新整理一次),不需要靠 systemd 排序去
      保證「畫面出現時網路一定已經好了」。這個修正本身沒有實際驗證
      過,第一次 VM 測試時可以刻意先不接虛擬網路卡開機,確認 tty1
      畫面是不是立刻就出現(顯示「尚未偵測到網路連線」),而不是黑
      畫面。
- [ ] **`gonas.service` 自己的 `Wants=network-online.target` 要不要
      也一起拿掉,是一個留給使用者判斷、這裡刻意沒有動的問題**:
      跟上面主控台的情況同一個道理——gonasd 監聽 0.0.0.0,理論上
      不需要等到「網路確定上線」才能開始接受連線(等到網路真的通了,
      本來就能連上,不等待也不會有壞處),所以這個 `Wants=` 嚴格來說
      也是可以拿掉、讓 gonasd 更快啟動的。但這個 unit file
      (`build/systemd/gonas.service`)是 Phase 8/9 就存在、軟體版
      安裝路徑也在用的既有設計,不是這次 appliance 新增的檔案,動它
      會同時影響已經在用軟體版安裝的既有使用者,超出「先把 appliance
      這部分弄好」的範圍,所以這裡刻意沒有一併改掉,只記錄下來讓
      使用者自己判斷要不要在真機測試後另外決定要不要修改。

**第五輪覆閱額外發現、已修正的項目**——這一輪換成從「安全姿態」的
角度看 appliance 新增的內容,順便發現一個文件跟程式碼互相矛盾的
地方:

- [ ] **`gonasadmin` 的預設密碼原本只在文件裡提醒、程式碼從來沒有
      真的強制執行**:`preseed.cfg` 帳號那一段的中文註解原本就寫著
      「並在 late_command 執行完之後過期,強制第一次登入就得改密碼」
      ——但實際檢查 `late-command.sh` 才發現這件事從來沒有真的被
      實作過,註解在描述一個不存在的行為,這本身就是這輪覆閱意外
      抓到的一個「文件跟程式碼對不起來」的案例。已經在 `late-command.sh`
      補上第 4 步,用 `chage -d 0 gonasadmin`(等同 `passwd --expire`)
      把這組密碼標記成已經過期,讓它在第一次登入時(SSH 或 tty2 都
      算)強制要求先設定新密碼才能拿到 shell,把「使用者自己記得去
      改密碼」從一個文件提醒變成一個做不到就進不去的強制關卡,對
      合法的第一次登入沒有任何副作用。這個機制本身完全沒有實際驗證
      過:第一次 VM/真機測試時,務必實際用 `ssh gonasadmin@<ip>`
      連線一次,確認真的會被要求設定新密碼,而不是連線失敗或直接
      放行——如果 SSH 客戶端的互動模式跟 `chage -d 0` 觸發的
      PAM/keyboard-interactive 換密碼流程搭配得不好,結果可能是
      連不上而不是被要求換密碼,這種情況下反而會誤把「安全機制」
      變成「把緊急備援管道也一起鎖死」,是這一項裡風險最高的地方,
      需要優先確認。

**第六輪覆閱額外發現、已修正的項目**——這一輪是第一次不只靠人工
覆閱,而是真的寫仿真測試資料在這個沙盒裡執行過的一輪,抓到的兩個
問題都是「只看程式碼看不出來、實際跑過測試資料才現形」的類型:

- [ ] **`find ... | while read; do ... done` 的 POSIX 子行程變數
      作用域陷阱**:`build-iso.sh` 原本用管線把 `find` 的結果餵給
      `while read` 迴圈,在 dash/大多數 `/bin/sh` 底下,管線右邊的
      `while` 迴圈是跑在一個子行程(subshell)裡,迴圈裡設定的變數
      離開迴圈之後在父行程裡就消失了——這裡原本想在迴圈裡計數、判斷
      「到底有沒有找到任何一個開機選單設定檔」,但這個計數在子行程
      模式下完全不可靠。改成先用 `find ... > 檔案` 把結果寫進一個
      清單檔案,再用 `while read ... < 檔案`(輸入重新導向,不是
      管線)讀取——這個寫法的 while 迴圈是在目前的 shell 裡執行,不是
      子行程,裡面設的變數在迴圈結束後還讀得到。
- [ ] **開機選單參數注入完全沒有驗證自己是否真的成功過**:原本兩條
      sed 規則(isolinux 的 `append` 行、grub 的 `---` 行)如果剛好在
      某個 Debian 版本底下完全沒命中,sed 本身不會報任何錯誤,結果是
      產出一份「建置流程看起來順利跑完、實際上開機後會整個掉回手動
      安裝流程」的 ISO,而且要等到真的拿去開機測試才會發現。現在改成
      在建置階段就主動檢查:(a)如果一開始 `find` 完全找不到任何一個
      候選的開機選單設定檔,直接中止,不繼續往下跑;(b)所有 sed
      規則都套用完之後,再檢查找到的檔案裡有沒有至少一個真的包含
      剛剛注入用的獨特標記字串(`gonas/preseed.cfg`),沒有的話一樣
      直接中止,並提示要去檢查 sed 規則是不是跟這個 Debian 版本的
      格式對不起來了。這樣「注入失敗」會在建置階段就直接報錯,而不是
      靜靜產出一份壞掉的 ISO 讓人在 QEMU 裡才發現。
- [ ] **(這輪最重要的發現)grub.cfg 的 `---` 分隔字元注入規則,連續
      兩輪人工覆閱都沒抓到的真實 bug,寫了仿真測試資料實際執行才
      發現**:第二輪覆閱曾經把原本「假設 `---` 一定是行尾」的規則,
      改成「允許 `---` 後面接空白再到行尾」,當時只靠人工覆閱、看起來
      合理就過了。這一輪為了驗證這段邏輯,寫了幾份仿照真實 Debian
      grub.cfg 格式的假設定檔(內容像
      `linux /install.amd/vmlinuz vga=788 --- quiet` 這種——`---`
      後面接著 `quiet` 這種開機參數,不是行尾,也不是只有空白),拿
      當時的 sed 規則實際跑過,結果真的完全沒有命中、注入失敗,證明
      第二輪覆閱的修法還是不夠。改成不管 `---` 前後有沒有其他字,只要
      它出現在這一行的任何位置,一律把要注入的參數插在它前面——這才是
      語意上正確的位置("---" 是 d-i 用來分隔「給核心本身的參數」跟
      「給開機後 init 行程的參數」的界線)。修好後緊接著在修同一段
      邏輯時,又發現第二個、獨立的問題:拿來判斷「這一行是不是 grub
      的 linux 開機參數列」的 grep guard,原本比對的是字面的空白鍵
      (`linux ` 中間一個空白字元),但真實的 grub.cfg 是用 tab 字元
      把 `linux` 跟核心路徑隔開的(`linux<TAB>/install.amd/vmlinuz`),
      guard 用字面空白鍵去比對,在真實格式底下永遠判斷為「不是」,
      底下真正做注入的 sed 就整段被跳過,結果一樣是靜默失敗、完全
      沒有錯誤訊息。改成用 `[[:space:]]` 字元類別(POSIX BRE 的中括號
      裡合法,同時涵蓋空白鍵跟 tab)取代字面空白鍵。兩個問題都用同一份
      擴充過的仿真測試資料(涵蓋 isolinux append 行、grub `--- quiet`、
      grub `---` 後面只有空白、grub 純 `---` 沒有任何後綴、品牌化文字
      置換,共 5 種案例)驗證過,全部通過。
- [ ] **把上面這段測試邏輯獨立成 `build/appliance/lib/patch-boot-menu.sh`
      函式庫,並新增 `build/appliance/test-boot-menu-patch.sh` 固定
      下來成為一支可重複執行的離線回歸測試**:在這輪覆閱之前,測試是
      在 shell 對話裡臨時寫的一次性腳本,驗證完就丟掉,下次 Debian
      改版或再改一次這段邏輯,沒有東西可以立刻重新驗證,也沒辦法保證
      正式建置腳本(`build-iso.sh`)跟臨時測試腳本用的是同一份邏輯。
      現在 `build-iso.sh` 跟 `test-boot-menu-patch.sh` 都
      `. lib/patch-boot-menu.sh` 來源同一份 `gonas_patch_boot_menu_file()`
      函式,不會出現「測試跑的是一份可能跟正式邏輯不同步的複製品」
      這種情況;`sh build/appliance/test-boot-menu-patch.sh` 完全不需要
      網路,幾秒鐘跑完,建議在每次修改這段邏輯、或升級
      `GONAS_DEBIAN_RELEASE` 之後都先跑一次。

這一輪跟前五輪最大的不同,是「開機選單參數注入」這一小段邏輯不再只是
「邏輯上自認正確、沒有實際驗證過」,而是有真正在這個沙盒裡執行過的
測試證據支持——但這僅限於這一小段完全不需要網路/外部工具的純字串
處理邏輯,`build-iso.sh` 其餘的部分(真正下載官方 ISO、`xorriso`
解開/重新包裝、preseed 被真正的 debian-installer 解析、`late-command.sh`
在真正的 `in-target` chroot 環境裡執行)依然完全沒有在這個沙盒裡執行
過,原因不變:見本節開頭跟 `build/appliance/README.md` 裡記錄的網路
出口白名單 403 證據。

這整個 Phase 的狀態誠實地是「設計完成、邏輯上自認正確、依照 Debian
官方文件撰寫,絕大部分沒有端到端驗證,但『開機選單參數注入』這一小段
例外——它有實際測試資料執行過並且抓到兩個真的存在的 bug」,跟上面
「9. gonasd 自我更新」一節裡「沒有在真正由 systemd 監督的安裝方式底下
驗證過」的誠實揭露是同一種態度——寧可清楚列出「還沒驗證」的清單,也
不要做一個看起來像驗證過、實際上沒有真正驗證的假測試。完整的建置/
驗證步驟見 `build/appliance/README.md`。

沒有修改任何 `.go` 檔案(只修改/新增了 `build/appliance/` 底下的
shell 腳本跟函式庫檔案),`gofmt`/`go vet`/`go build ./...`/
`go test ./... -race -count=1` 重新確認過一次,結果跟這個 Phase 之前
完全一樣、全數維持全綠。

**第七輪覆閱額外發現、已修正的項目**——這一輪抓到的第一個問題,是
逐字重讀 `late-command.sh` 裡一段程式碼跟緊接在它上面的中文註解,
發現兩者互相矛盾:

- [ ] **GRUB 開機選單的逾時設定,註解講的是一回事,程式碼設的是
      另一回事**:`late-command.sh` 品牌化那一段的註解明確寫著
      appliance 的目標是「開機不用等使用者按鍵、也不用刻意顯示選單,
      直接進系統,跟 Unraid/TrueNAS 的開機體驗一致」,並說標準做法是
      `GRUB_TIMEOUT=0` 加 `GRUB_TIMEOUT_STYLE=hidden`——但緊接著的
      程式碼實際寫的是 `GRUB_TIMEOUT=3` 加
      `GRUB_TIMEOUT_STYLE=menu`,兩者剛好相反:`menu` 樣式會讓完整的
      GRUB 選單畫面在**每一次開機都顯示 3 秒**,不是隱藏起來直接開機。
      這是從第一輪覆閱這段程式碼被寫出來那次就存在的 bug,前六輪
      review 都只看了「這一步有沒有做該做的事」(有沒有改
      GRUB_DISTRIBUTOR、有沒有處理 grub.cfg 不存在的情況),沒有人
      逐字核對「註解說的目標」跟「程式碼實際設的值」是否一致,直到
      這一輪才抓到。已經改成 `GRUB_TIMEOUT=0` 加
      `GRUB_TIMEOUT_STYLE=hidden`,符合註解原本描述的設計意圖。這段
      邏輯的實際效果需要真正的 GRUB/韌體環境才能觀察(這個沙盒沒有
      辦法執行驗證),所以這一項在第一次 VM/真機測試時,務必額外確認
      一下:開機應該直接跳過 GRUB 選單畫面(不是完全看不到任何東西,
      是「幾乎沒有停留」,一般硬體上按住 Shift 鍵、或 UEFI 機器上按
      Esc,仍然可以強制叫出選單)。
- [ ] **`make clean` 會把 `build-iso.sh` 自己的下載快取一起清掉,
      悄悄抵銷掉第三輪覆閱加的快取機制**:第三輪覆閱在
      `dist/.cache/debian-iso/` 底下加了官方 Debian ISO 的本地快取,
      理由是這份 ISO 有幾百 MB,反覆建置/除錯時每次都重新下載很不
      友善。但 `Makefile` 原本的 `clean` target 是單純
      `rm -rf $(DIST) devdata`——`$(DIST)` 就是 `dist`,包含
      `dist/.cache/`,只要養成「`make clean` 之後再重新
      build」的習慣(很常見的操作順序),快取的 ISO 就會被一起砍掉,
      下次建置照樣要重新下載幾百 MB,快取形同虛設,而且完全不會有
      任何提示告訴使用者「你剛剛把快取清掉了」。已經把 `clean`
      改成用 `find $(DIST) -mindepth 1 -maxdepth 1 ! -name .cache -exec
      rm -rf {} +`,只清掉 `dist/` 底下 `.cache` 以外的東西(release
      輸出、編譯出來的執行檔),保留下載快取;另外新增一個獨立的
      `clean-cache` target,真的想清掉下載快取(例如懷疑快取的 ISO
      損毀了)可以明確執行 `make clean-cache`。這個修法本身有在這個
      沙盒裡用假的 `dist/` 目錄結構實際執行測試過:確認過 `make
      clean` 之後 `dist/.cache/debian-iso/` 底下的假檔案還在、其他
      東西都被清掉,以及 `dist/` 目錄一開始不存在時 `make clean` 也
      不會報錯,都是純 POSIX 工具(`find`/`rm`)的邏輯,不需要網路就能
      驗證,不屬於本節開頭說的「完全沒辦法驗證」那一類。
- [ ] **`preseed.cfg` 裡 `pkgsel/update-policy select none` 的說明
      補充**:這不是這輪才發現的程式碼 bug,而是覆閱時發現文件用詞
      可能讓人誤解——`build/appliance/README.md` 開頭說「底層仍然是
      標準 Debian,能繼續吃到安全更新」,但 `preseed.cfg` 明確關掉了
      `unattended-upgrades` 自動背景更新的安裝選項,兩句放在一起看,
      容易誤以為這份映像檔開機後會自動裝好安全更新機制。已經在
      `build/appliance/README.md` 的「已知的設計限制」補上一條說明:
      「能吃到安全更新」指的是底層是標準 Debian、手動
      `apt upgrade` 就拿得到,不是開機就自動背景更新——如果要自動化,
      跟軟體版安裝路徑一樣,自己事後裝 `unattended-upgrades` 即可。

**第八輪覆閱額外發現、已修正的項目**——這一輪是回頭覆閱測試腳本
本身(`test-boot-menu-patch.sh`),而不是被測試的邏輯:

- [ ] **回歸測試裡「案例 3」跟「案例 4」的測試資料其實是同一份**:
      案例 3 名字叫「grub `---` 後面只有空白」,案例 4 叫「grub 純
      `---` 沒有任何後綴」,兩個名字聽起來是要測不同情境,但用
      `diff` 直接比對兩個案例當初用 heredoc 寫出來的檔案內容,發現
      完全一模一樣——heredoc 那一行寫的 `--- ` 後面那個空白字元,在
      原本的寫法下沒有真的被保留下來,案例 3 實際測到的內容跟案例 4
      沒有任何差別。這不影響第六輪找到的兩個真 bug 有沒有被真的修好
      (因為修好之後的 sed 邏輯是不管 `---` 前後接什麼都一律命中,
      案例 3/4 剛好都是它能處理的情況),但代表這支回歸測試「宣稱
      涵蓋 4 種輸入格式」其實只真的涵蓋了 3 種,少測了一種——而且
      剛好是第二輪覆閱當時「允許尾端空白」那個修法原本要專門處理的
      情境,如果之後有人不小心把 sed 改回第二輪那種「用 `$` 錨點
      比對行尾」的寫法,這支測試不會抓到差別,因為它從來没有真的用
      「後面有空白但還沒到行尾」的資料測過。改成用 `printf` 明確寫出
      這一行(不透過 heredoc,避免同樣的字元被誤剪掉的問題),並且
      在案例 3 執行 `gonas_patch_boot_menu_file` 之前,先用
      `grep -q -- '--- $'` 自我檢查一次「這份測試資料真的有帶著
      尾端空白」,檢查沒過的話直接判定測試設定本身有問題並回報
      FAIL,不會又一次悄悄测錯東西卻显示 PASS。

沒有修改任何 `.go` 檔案(只改了一支測試 shell script 的測試資料),
`gofmt`/`go vet`/`go build ./...` 重新確認過一次,結果跟這個 Phase
之前完全一樣、全數維持全綠。

**第九輪覆閱額外發現、已修正的項目**——這一輪是先把整個 Phase 19 的
「還沒驗證清單」重新列一次(交給使用者看有沒有遺漏),過程中重新逐行
檢查每一個變數的來源,抓到一個一直存在、之前八輪都沒特別注意到的
命名不一致:

- [ ] **`late-command.sh` 判斷架構的 fallback 算出來的值,格式跟其他
      地方用的完全不一樣**:`ARCH="$(dpkg --print-architecture
      2>/dev/null || uname -m)"` 這一行,主要路徑(`dpkg
      --print-architecture` 成功)算出來的是 Debian 慣用的架構名稱
      ("amd64"/"arm64"),跟 `build-iso.sh` 建立的目錄名稱
      ("release-amd64"/"release-arm64")完全對得起來;但如果 `dpkg`
      這個指令因為某種原因不可用,fallback 用的 `uname -m` 回傳的是
      核心/硬體慣用的名稱("x86_64"/"aarch64")——實際在這個開發沙盒
      機器上執行 `uname -m` 得到的就是 "x86_64",親自驗證過。一旦這個
      fallback 真的被觸發,算出來的 `$ARCH` 會讓
      `$RELEASE_DIR="$GONAS_DIR/release-$ARCH"` 指向一個不存在的目錄
      (例如 `release-x86_64` 而不是 `release-amd64`),導致
      `late-command.sh` 判定「找不到 install.sh,這個 ISO 建置錯誤」
      直接中止——但其實只是這一行本身的 fallback 寫錯,不是真的建置
      有問題。在一個裝好的 Debian in-target chroot 裡,`dpkg` 幾乎不
      可能不存在(它本身就是這個系統的一部分),所以這條路徑實際被
      觸發的機率極低,但「幾乎不會發生」不代表寫錯也沒關係——已經
      修成 fallback 分支額外把 `uname -m` 的輸出對應回 Debian 的架構
      命名(`x86_64 -> amd64`、`aarch64`/`arm64 -> arm64`,其他值維持
      原樣並印出警告),用假資料實際測過這個對應表產生的結果都正確。
      同時把 `log()` 函式的定義往前移到這段邏輯之前,因為 fallback
      分支需要能在這裡就印警告,原本 `log()` 是定義在 `$ARCH` 這行
      之後,順序反過來會沒辦法用。

沒有修改任何 `.go` 檔案(只改了 `late-command.sh` 一個變數的計算
方式),`gofmt`/`go vet`/`go build ./...`/`test-boot-menu-patch.sh`
重新確認過一次,結果跟這個 Phase 之前完全一樣、全數維持全綠。

**第十輪:把前九輪列出來、還沒打勾的項目逐一補完**——這一輪不是再找
新 bug,而是照著上一次多視角(架構/資安/設計/全鏈路)覆閱列出的清單,
把每一個沒打勾的項目實際做掉:

- [x] **同架構、舊 Debian 版本代號的快取不會自動清掉**(架構師視角):
      `build-iso.sh` 現在會在確認新版本快取成功寫入之後,清掉
      `dist/.cache/debian-iso/` 底下「同一個架構、檔名代號不是目前
      這次要用的版本」的舊快取檔案——用假的快取目錄結構實際測試過:
      同架構的舊代號檔案會被清掉,不同架構的檔案不會被誤刪,快取
      目錄本來就是空的也不會出錯。
- [x] **GPG 簽章驗證(只做過 checksum,一直沒做真實性驗證)**(資安
      視角):新增可選的 `GONAS_DEBIAN_KEYRING` 環境變數,設定之後
      `build-iso.sh` 會多抓一份 `SHA256SUMS.sign`、用使用者自己準備好
      的 keyring 驗證簽章,失敗直接中止建置。判斷邏輯獨立成
      `lib/verify-gpg-signature.sh`,並且新增
      `test-gpg-verify.sh`——用假的 `gpg` 執行檔測過控制流程(exit
      code 0 加輸出裡有 "Good signature" 才算過、exit code 非 0 或
      輸出裡沒有那個字串都算沒過),3 個案例全部驗證正確。老實說清楚
      這裡測過跟沒測過的邊界:控制流程本身測過,但「用一把真正的
      Debian 簽章金鑰驗證一份真正的 SHA256SUMS.sign」這件事本身,
      這個沙盒完全沒辦法連網驗證,需要使用者自己有 keyring 才算數。
- [x] **Web 前端首次設定畫面**(設計師視角):讀過
      `internal/api/webui/static/app.js` 的 `showSetupGate()` 跟對應
      的 i18n 文字——使用者名稱/密碼(至少 8 字元)/確認密碼三個欄位,
      前端先檢查兩次密碼一致再送出,三種語言(繁中/簡中/英文)的文案
      都清楚、意思一致,沒有發現需要修改的地方。
- [x] **沒有 CI 自動跑這些檢查**(全鏈路/測試視角):新增
      `.github/workflows/ci.yml`,涵蓋 `gofmt`/`go vet`/`go build`/
      `go test -race`,以及 `build/` 底下所有 shell 腳本的 `sh -n`
      語法檢查、`test-boot-menu-patch.sh`、`test-gpg-verify.sh`——
      這幾項全部不需要網路,理論上每次 push/PR 都能自動跑,不用再
      依賴人工記得手動執行。刻意沒有讓 CI 做真正下載 ISO/跑
      xorriso/QEMU 開機這一段,那仍然照
      `docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md` 手動做。
- [ ] **端到端(真正下載官方 ISO、跑 xorriso、真正 debian-installer
      安裝、late-command 在真正 in-target chroot 執行)**:這一項這輪
      沒有、也不可能在這個沙盒裡完成——不是沒去做,是這個環境的網路
      白名單從一開始就擋死了 Debian 的套件鏡像,前面十輪能做的都是
      「讓能做的部分盡量不要有已知的錯」,這一項只能交給使用者在
      有網路的機器上照 `docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md`
      實際跑一次才能打勾。

沒有修改任何 `.go` 檔案(只改了 `build/appliance/` 底下的 shell 腳本、
新增函式庫跟測試,以及新增 CI workflow),`gofmt`/`go vet`/
`go build ./...`/`go test ./... -race -count=1`/兩支
`test-*.sh` 全數維持全綠。

**第十一輪:回頭覆閱第十輪自己剛加的程式碼,抓到三個真的存在的問題,
其中兩個是用真的 GPG 金鑰實測出來的**——這一輪示範了一件事:剛寫完
的新程式碼,即使當下每個步驟都測過,也值得馬上回頭再看一次,因為
「這段邏輯本身測過」不等於「這段邏輯跟旁邊既有的東西放在一起沒有
矛盾」:

- [x] **第十輪自己留下的註解/程式碼矛盾**:第十輪把「2.6 驗證
      base.iso 完整性」那段舊註解裡「並在下面印出訊息提醒使用者可以
      自行做 GPG 驗證」這句話,結果底下的程式碼已經被同一輪的編輯換成
      真正會做驗證的邏輯,舊註解沒有跟著更新,变成一模一樣的「注解
      講的是一回事、程式碼做的是另一回事」——這正是第七輪抓到過的
      同一類問題,這次是自己剛寫的程式碼裡又出現一次,靠重新讀一次
      改過的檔案抓到,已經改成指向下面「2.65」那一段的說明,不重複
      描述。
- [x] **`gpg --keyring` 對相對路徑的解讀方式跟 shell 不一樣,實測
      證實**:在這台機器上真的執行過
      `gpg --no-default-keyring --keyring my.keyring --verify ...`
      (從一個 `my.keyring` 真實存在的目錄裡,用相對路徑呼叫),結果
      gpg 完全没有讀到那個檔案,而是在自己的 homedir(`~/.gnupg/`)
      底下建立了一個全新的空 keybox——`GONAS_DEBIAN_KEYRING` 如果是
      相對路徑,`[ -f ... ]` 這道存在性檢查用的是 shell 自己的相對
      路徑解讀方式(相對於目前工作目錄,會正確判斷檔案存在),但傳給
      `gpg --keyring` 之後卻是另一個結果,兩邊「相對路徑」根本不是
      同一個檔案,驗證因此必然失敗,而且錯誤訊息只會說「簽章驗證
      失敗」,完全看不出來是路徑解讀方式不一致造成的。已經在
      `build-iso.sh` 裡把 `GONAS_DEBIAN_KEYRING` 轉成絕對路徑再往下
      用,不管使用者給的是相對還是絕對路徑都不受影響。
- [x] **keyring 要用 binary 格式匯出,不能用 ASCII armor 格式,實測
      證實**:在這台機器上當場產生一把真的測試用 GPG 金鑰、匯出成
      ASCII armor 格式(`gpg --export -a ...`,很多官方文件範例習慣
      加 `-a`)拿去給 `--keyring` 讀,直接報 `invalid packet`、
      `No public key`,即使金鑰內容本身完全正確——`--keyring` 吃的是
      binary 格式,armor 格式對 gpg 來說是完全不同的檔案格式。改用
      `gpg --export <key-id> > my.keyring`(不加 `-a`)匯出之後,同一把
      金鑰同一份簽章就能正確驗證通過。已經在 `build-iso.sh` 跟
      `build/appliance/README.md` 補上明確提醒。
- [x] **把這次的真實驗證過程固定成永久的回歸測試案例**:
      `test-gpg-verify.sh` 新增案例 4,當場產生一把臨時測試金鑰、簽一份
      測試資料,實際驗證「正常情況會通過」跟「資料被竄改後會正確
      失敗」兩種情境(`gpg` 不存在的環境會自動跳過,不會讓整支測試
      失敗)——這是這整個 Phase 19 裡第一次不只是「用假的外部指令測
      控制流程」,而是真的用一把真實金鑰、一份真實簽章、真正的 gpg
      二進位檔跑過一次完整流程,雖然還不是「用 Debian 真正的官方
      金鑰驗證真正的官方簽章」,但已經是這個沙盒能做到的最高驗證
      層級。加這個案例的過程中,一開始因為前面案例 1-3 把 `PATH`
      換成指向假的 `gpg` 執行檔、沒有換回來,案例 4 一度會誤用假 gpg
      而不是真的 gpg——這是覆閱自己剛寫的測試腳本時抓到的,已經改成
      在案例 4 開始前把 `PATH`換回原始值。

沒有修改任何 `.go` 檔案(只改了 `build/appliance/` 底下的 shell 腳本
跟測試),`gofmt`/`go vet`/`go build ./...`/兩支 `test-*.sh` 全數維持
全綠。

**第十三輪(使用者要求的最後一輪覆閱,之後使用者會親自跑實際建置 +
虛擬機測試):這一輪抓到目前整個 Phase 19 覆閱過程裡最嚴重的一個
bug,而且是那種「靜態看程式碼完全看不出來,只有真的建置一次 ISO
才會現形」的失敗模式**——過程是:

- [x] **CI 裡一個過時的步驟說明文字**:`.github/workflows/ci.yml` 裡
      `test-gpg-verify.sh` 那個步驟的註解還寫著「只測控制流程」,但
      第十一輪已經幫它加了案例 4/5(真的用一把臨時金鑰簽章/驗證),
      註解沒有跟著更新——這正是第七、第十輪都抓到過的同一類「註解
      講的是一回事、程式碼做的是另一回事」問題,這次出現在 CI 設定檔
      裡。已更新成準確描述目前的 5 個測試案例。
- [x] **回頭重新讀 CI 檔案開頭那段「為什麼需要這支 CI」的說明時,
      發現第九輪修的架構偵測 fallback,從來沒有補上對應的回歸測試**:
      `patch-boot-menu.sh` 有 `test-boot-menu-patch.sh`、
      `verify-gpg-signature.sh` 有 `test-gpg-verify.sh`,但第九輪把
      `late-command.sh` 裡「`dpkg --print-architecture` 不可用時,
      `uname -m` 的輸出要對應回 Debian 慣用架構名稱」這段邏輯修好之後,
      邏輯本身還是直接寫死在 `late-command.sh` 裡,沒有像另外兩個
      同類型的修正一樣抽成 `lib/*.sh` 的獨立函式、配一支
      `test-*.sh`——導致這段邏輯少了持續的回歸保護,以後如果有人不小心
      改回舊的錯誤對應表,不會有任何測試抓到。已抽成
      `build/appliance/lib/detect-arch.sh`
      (`gonas_uname_to_debian_arch()`),新增
      `build/appliance/test-detect-arch.sh`(6 個案例:
      `x86_64`→`amd64`、`aarch64`→`arm64`、`arm64`→`arm64`、以及三個
      不在對應表裡、應該原樣輸出的架構名稱),並讓 `late-command.sh`
      `.` 來源這份函式庫、呼叫 `gonas_uname_to_debian_arch` 取代原本
      寫死的 `case` 敘述,同時把新測試加進
      `.github/workflows/ci.yml`。
- [x] **(這一輪最嚴重的發現)`build-iso.sh` 從來沒有把
      `build/appliance/lib/` 目錄複製到實際建置出來的 ISO
      上**:上一項把 `late-command.sh` 改成在執行一開始就
      `. "$SCRIPT_DIR_FOR_LIB/lib/detect-arch.sh"` 之後,回頭檢查
      `build-iso.sh` 究竟把哪些檔案塞進 ISO 的 `gonas/` 目錄時發現——
      原本只複製了 `late-command.sh`、`overlay/`、`preseed.cfg` 三樣
      東西,從來沒有複製過 `lib/` 目錄本身。這代表如果沒有這一輪額外
      補上這個複製步驟,真正燒出來的 ISO 上會有一個「呼叫了一個不存在
      的檔案」的 `late-command.sh`:preseed 的 `late_command` 在真正
      裝好的系統裡透過 `in-target sh -c
      "GONAS_INSTALL_MEDIA=/cdrom /cdrom/gonas/late-command.sh"`
      執行它,而它的第一段可執行邏輯就是 `.` 一份
      `/cdrom/gonas/lib/detect-arch.sh`——這個檔案根本不存在於燒出來的
      ISO 上,在 `set -e` 之下這一行會直接讓整支腳本以錯誤結束,後面
      「安裝 gonasd 本體」「換掉 tty1 品牌」「強制 gonasadmin 改密碼」
      等等**全部**都不會執行到,而且因為失敗在腳本最前面,連 preseed
      裡「失敗時把錯誤寫進 motd」那個備援機制都還來得及生效(所以症狀
      會是:機器裝完、重開機,但完全不是 GoNAS 的樣子,只是一台裝好
      SSH 的陽春 Debian)。**這種失敗模式只靠讀 `late-command.sh`
      本身完全看不出問題**——它引用的路徑、語法都完全正確,問題純粹
      出在「另一支腳本(`build-iso.sh`)有沒有把它需要的檔案一起
      放上 ISO」這種跨檔案的對應關係,是這一整個 13 輪覆閱過程裡第一次
      出現「兩個獨立看都對、合起來才會炸」的問題,也是目前為止後果
      最嚴重的一個(如果沒抓到,會讓整個 Phase 19 appliance 的核心賣點
      在真機/VM 上完全跑不出來,而且要真的裝一次系統才會發現)。已在
      `build-iso.sh` 複製 `late-command.sh` 那幾行的正下方,補上
      `mkdir -p "$GONAS_ON_ISO/lib"` +
      `cp -a "$SCRIPT_DIR/lib/." "$GONAS_ON_ISO/lib/"`,並用整份
      `cp -a` 整個目錄(而不是一個個列檔名),這樣以後 `lib/` 底下
      再新增其他函式庫檔案,也會自動一起塞進 ISO,不用記得回來改這裡。
      這個修正本身沒辦法在這個沙盒裡真的建一次 ISO 來驗證(網路白名單
      擋掉 Debian 鏡像),是靠追蹤兩支腳本之間的路徑對應關係(`in-target`
      實際執行的路徑 `/cdrom/gonas/late-command.sh`、它用
      `dirname "$0"` 算出的 `/cdrom/gonas`、跟 `build-iso.sh` 裡
      `GONAS_ON_ISO="$EXTRACT_DIR/gonas"` 這幾個變數手動推導、確認
      三者最終在 ISO 上會對齊)確認邏輯正確,實際生效與否要等使用者
      這一輪之後真的跑一次建置 + 安裝才能最終確認。
- [x] **`docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md` 步驟 2 也同步
      補上了 `test-detect-arch.sh`,並且順便修正了原本就已經過時的
      案例數字說明**(第十一輪幫 `test-gpg-verify.sh` 加了案例 4/5
      之後,這份文件步驟 2 的說明文字還停在「3 個 PASS」跟舊的
      「control-flow test cases passed」字樣,沒有跟著更新——這是這份
      文件自己的另一個「文件跟實際行為不同步」,雖然影響有限,趁這一輪
      一起修掉)。

沒有修改任何 `.go` 檔案(只改了 `build/appliance/` 底下的 shell 腳本、
CI 設定跟文件),`gofmt`/`go vet`/`go build ./...`/三支 `test-*.sh`
(含新增的 `test-detect-arch.sh`)全數維持全綠。

這一輪是使用者明確要求的「最後一輪」覆閱,之後會由使用者親自在一台
有網路的機器上跑 `docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md` 裡的
完整流程(真的下載 ISO、xorriso 建置、QEMU 開機安裝)——這是整個
Phase 19 第一次會有真實執行結果,不管全部通過還是某幾步失敗,都請把
實際看到的訊息帶回來,尤其是上面提到的 `lib/` 複製修正,理論推導再
仔細也比不上一次真的跑過。

**第十四輪(使用者看到第十三輪抓到的 `lib/` 沒塞進 ISO 那個 bug之後
要求「再看看還有沒有」):延續第十三輪最後一個發現的思路——「兩個
檔案獨立看都對、合起來才會炸」這類問題——往同一個方向繼續深挖,
抓到一個同類但還沒被驗證過的風險,外加一個完全不同角度的真落差**:

- [x] **`late-command.sh`/`install.sh` 這兩個進入點,原本依賴 ISO 上
      的 Unix 執行位元有沒有正確保留下來,這件事從來沒被明確點出來
      討論過**:`preseed.cfg` 的 `late_command` 原本直接呼叫
      `/cdrom/gonas/late-command.sh`(靠這個檔案自己的 shebang 加上
      執行位元被觸發),`late-command.sh` 內部也是直接
      `( cd "$RELEASE_DIR" && ./install.sh )`(一樣靠執行位元)。這兩個
      檔案都是先在建置機器上用 `chmod +x` 設好權限、再透過 xorriso 的
      `-map` 疊加進一份載入既有 base.iso 的映像、重新包裝成新 ISO——
      問題是:新加進去的檔案,執行位元能不能透過 Rock Ridge 擴充屬性
      正確保留到最終的 ISO 9660 檔案系統上,是 xorriso 行為的細節,
      這個開發沙盒完全裝不了 xorriso,沒辦法實際跑一次驗證。如果這個
      假設不成立,兩個進入點都會在真正的 in-target 環境裡遇到
      `Permission denied` 直接失敗——症狀跟上一輪抓到的「`lib/`
      沒塞進 ISO」幾乎一模一樣(機器裝完只是一台陽春 Debian),只是
      錯誤訊息從「找不到檔案」換成「沒有權限執行」。與其賭這個假設
      一定成立,已經把兩個進入點都改成明確用 `sh 檔案路徑` 執行——
      `preseed.cfg`: `sh /cdrom/gonas/late-command.sh`;
      `late-command.sh`: `sh ./install.sh`(對應的 `[ -x ... ]` 存在性
      檢查也一起改成 `[ -f ... ]`,不然就算改用 `sh` 執行,前面的檢查
      還是可能因為執行位元遺失而誤判找不到檔案)——這樣完全不依賴
      執行位元有沒有被保留,只需要檔案讀得到就能跑,是一個零副作用的
      防禦性修正,不管 Rock Ridge 屬性實際上有沒有問題都不影響行為。
      誠實地說:這個修正本身解決的是「假設不成立時會怎樣」,但「假設
      到底成不成立」這件事,到目前為止還是純理論推導,沒有真的建一次
      ISO 觀察過,已經記在 `build/appliance/README.md`「已知的設計
      限制」裡,也讓 `docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md`
      提醒使用者:如果真機/VM 測試時看到 `Permission denied`,代表
      這個懷疑是對的。
- [x] **appliance 這條路徑裝完之後,系統上完全沒有 `uninstall.sh`
      可以用——這是完全不同角度、跟上面的執行位元問題無關的另一個
      落差**:是從「裝完之後,使用者手上到底剩下什麼」這個角度回頭
      檢查才想到的:「軟體版」安裝路徑的使用者本來就手動下載/解壓縮
      過 release tarball,`uninstall.sh` 自然留在自己電腦的某個目錄
      裡,事後想解除安裝隨時找得到;但 appliance 這條路徑,release
      tarball 只存在於安裝媒體(USB/光碟映像)上的
      `/cdrom/gonas/release-$ARCH/`,而 `preseed.cfg` 設定了裝完會
      退出安裝媒體——裝好的系統本身完全沒有 `uninstall.sh`,使用者
      除非剛好留著、記得重新插上/掛載當初那支安裝隨身碟並精確找到
      同一個路徑,不然日後想解除安裝 GoNAS 根本無從下手。這在前 13
      輪覆閱裡完全沒被提過。已修正:`late-command.sh` 裝完 gonasd
      之後,新增一步把 `uninstall.sh` 複製一份到
      `/usr/local/share/gonas/uninstall.sh` 並設好執行權限(這裡是
      寫到已經裝好、開機後會用的真實檔案系統,不是 ISO 9660,執行位元
      用 `chmod 0755` 設就是可靠的,不受上面那個風險影響)——之後想
      解除安裝,直接 `sudo /usr/local/share/gonas/uninstall.sh` 就好。

沒有修改任何 `.go` 檔案(只改了 `build/appliance/` 底下的 shell 腳本
跟文件),`gofmt`/`go vet`/`go build ./...`/三支 `test-*.sh` 全數維持
全綠。這一輪兩個發現都不是「靠執行測試抓到」的(這個沙盒沒辦法建置
ISO),而是靠「假裝自己是剛裝完機器的使用者,回頭想這條路徑跟軟體版
安裝路徑比起來,少了什麼東西/多了什麼隱藏假設」這個角度重新檢查——
跟第十三輪的 `lib/` 複製 bug 一樣,都是那種「每一份檔案單獨看都完全正確,問題出在
檔案跟檔案之間、或這個安裝路徑跟另一個安裝路徑之間的落差」,純靠讀
單一檔案的程式碼審查抓不到。

**第十五輪(使用者再次要求「繼續」查):這次換了角度——不是再找
「檔案跟檔案之間的落差」,而是回頭質疑「已經測過、覆閱過好幾輪的
GPG 驗證邏輯本身,是不是藏著一個環境相關的隱藏假設」,抓到一個真的
存在、而且解釋起來完全合理的 bug**:

- [x] **`gonas_verify_gpg_signature()` 判斷「驗證通過」靠的是 grep
      gpg 輸出裡有沒有出現英文的 `Good signature` 字串,但這句話是
      gnupg 用 gettext 機制翻譯過的訊息,不是固定不變的常數字串**:
      如果建置這支腳本的人自己的機器語系不是英文(而且裝了對應的
      gnupg 翻譯包,這在很多非英語系國家的 Debian/Ubuntu 桌面環境是
      預設就有的),真正的 `gpg --verify` 印出來的會是翻譯過的訊息
      (例如德文環境是「Korrekte Signatur von ...」),`grep -q "Good
      signature"` 永遠不會命中——結果是即使簽章跟金鑰完全正確,
      `gonas_verify_gpg_signature()` 也會一律回傳「驗證失敗」,而
      `build-iso.sh` 把 GPG 驗證失敗當成硬性中止條件(見「2.65」那
      一段),對一個特地設定 `GONAS_DEBIAN_KEYRING`、想多做這一層額外
      驗證的使用者來說,會是一個完全摸不著頭緒、看起來像金鑰或簽章
      本身有問題、但其實只是機器語系不是英文的假錯誤,而且是那種
      「這台機器上永遠不會過」的持續性失敗,不是偶發的。這個問題
      前 14 輪完全沒被想到過,是回頭質疑「已經反覆驗證過的東西是不是
      藏著環境假設」才想到的角度,不是「找檔案之間的落差」。這個
      開發沙盒只裝了 C/C.utf8/POSIX 這幾種語系,沒辦法直接裝一個有
      翻譯包的語系實際重現症狀本身(沒辦法像其他 GPG 邏輯那樣「真的
      用一把金鑰整個跑一次觀察到症狀」),但 gnupg 訊息會被 gettext
      翻譯這件事本身是 GnuPG 行之有年、有文件可查的既有行為,不是
      憑空猜測。修法:呼叫 `gpg` 之前明確把 `LC_ALL`/`LANGUAGE` 都
      釘死成 `C`,強制它輸出英文訊息,不管使用者機器本身的語系設定是
      什麼——這只影響這一次 `gpg` 呼叫的環境變數,不會動到呼叫端或
      使用者 shell 本身的語系設定,零副作用。
- [x] **補上對應的回歸測試,而且驗證過「如果沒修好,測試真的會抓到」**:
      `test-gpg-verify.sh` 新增一個案例,用一個會檢查自己收到的
      `LC_ALL`/`LANGUAGE` 是不是 `C` 的假 `gpg`——是的話印出真正的
      `Good signature`,不是的話印出一句「假裝被翻譯過」的訊息(不含
      這幾個字),呼叫這個案例之前刻意先把外層的 `LC_ALL`/`LANGUAGE`
      設成一個不是 `C` 的假值,確保如果函式沒有主動覆寫成 `C`,這個
      假 `gpg` 收到的就會是那個假值。修這個 bug 的過程中,先把
      `lib/verify-gpg-signature.sh` 的修正暫時還原、重跑
      `test-gpg-verify.sh`,確認新案例真的會 FAIL(而且錯誤訊息清楚
      指出問題),再把修正加回去確認全部變回 PASS——不是只加了一個
      「照理說會過」的測試就算數,是真的驗證過這個測試案例本身有
      沒有偵測能力。

沒有修改任何 `.go` 檔案(只改了 `build/appliance/lib/
verify-gpg-signature.sh`、`test-gpg-verify.sh` 跟文件),`gofmt`/
`go vet`/`go build ./...`/三支 `test-*.sh`(`test-gpg-verify.sh` 現在
是 6 個案例)全數維持全綠。

**第十六輪(使用者再次要求「繼續查」):這一輪換了好幾個完全不同的
角度重新檢查,誠實地說——沒有找到新的問題**。檢查過的方向包括:

- `install.sh` 最後一行在 in-target chroot 裡也會執行的
  `gonasd -check-deps`,會不會因為 chroot 環境沒有真正在跑的 systemd
  而卡住或行為異常——回頭讀 `internal/doctor/doctor.go` 的
  `Run()` 確認它只用 `exec.LookPath` 找指令在不在 `$PATH` 上,完全
  不會真的執行任何外部指令(包括 `systemctl`),沒有卡住的風險,這是
  一開始懷疑、後來讀程式碼排除掉的假警報。
- `internal/selfupdate.Checker` 的背景自動檢查更新 goroutine 是否會在
  appliance 這種「刻意保持離線」的安裝路徑上,違背原則地自動發出網路
  請求——讀 `internal/api/router.go`(`s.updateChecker.Start(...)`
  那段)跟 `internal/selfupdate/selfupdate.go` 的 `Checker.Start`
  確認:背景 goroutine 一律啟動沒錯,但每次檢查前都會先讀
  `state.Update.ManifestURL`,空字串(全新安裝、使用者從未在 Web UI
  設定過更新來源的預設狀態)時直接 return,不會真的發任何 HTTP
  請求——程式碼行為跟旁邊註解講的完全一致,不是又一次「註解講一回事
  、程式碼做另一回事」。
- `gonas.service` 的 `After=network-online.target`
  `Wants=network-online.target`,在一台沒裝 `systemd-networkd`/
  `NetworkManager` 的最小化 Debian 安裝上,`network-online.target`
  可能沒有任何東西真正提供「等到網路就緒」的保證——重新確認過這
  不影響 gonasd 本身能不能正常監聽(監聽 `0.0.0.0` 不需要網路介面已經
  設好位址),而且 `gonas-console.service` 本身已經妥善處理「還沒有
  網路位址」的顯示情境(第四輪覆閱修的),所以就算這個 target 的
  「等待」語意不完整,也不影響功能,只是排序上的保證比看起來的弱,
  不是新問題。
- `/etc/os-release`/`/etc/hosts` 的 sed 替換規則、`gonas-console` 的
  `ip -4 -o addr` 輸出解析管線、`Makefile` 的 `VERSION` 在 `release`
  跟 `iso-*` 兩個 target 之間是否可能不一致、`.gitignore` 有沒有
  意外擋掉新加的 `lib/*.sh` 檔案、CI 的 `find build -name '*.sh'` 有沒有
  涵蓋到所有新檔案——逐一檢查過,都沒有問題。

沒有做任何程式碼變更,`go test ./... -race -count=1` 額外完整跑過一次
確認全綠(這是這一輪唯一新做的驗證動作,前面幾輪都只跑到
`gofmt`/`go vet`/`go build`,沒有跑完整測試套件)。老實說明:這一輪
純粹是排除法,確認幾個「看起來可疑」的地方讀完程式碼之後其實都沒事,
不代表已經找完所有問題,只代表用目前這些角度暫時想不到新的——這個
沙盒能做的靜態審查,邊際報酬已經很低,再往下大概率是在同一批邏輯裡
反覆確認,真正還沒被驗證過的東西(執行位元保留、真正的官方 GPG 簽章
驗證、preseed 在真正 debian-installer 環境裡的行為),都需要使用者
實際跑一次建置 + 安裝才會有答案。

**第十七輪(使用者說「我現在回到電腦前 Mac mini」,準備開始真的建置
+ 測試):這一輪不是又換角度重新讀同一批邏輯,而是「部署環境本身
第一次真的改變」直接暴露出一整類前 16 輪完全沒被想到過的 bug**——
前 16 輪的靜態審查全部是在這個 Linux 開發沙盒裡進行的,`build-iso.sh`
從頭到尾預設「建置這支 ISO 的機器是 Linux」,沒有人在任何一輪質疑過
這個假設,直到使用者說出他實際要用來建置/測試的機器是 macOS 的
Mac mini,才第一次有理由去檢查「這支腳本假設的 GNU coreutils 工具,
在 macOS 內建的 BSD 工具鏈底下還能不能用」。

- [x] **`sed -i` 在 macOS 上是完全不同的語法,不是單純「行為稍微不同」
      ,是「照 Linux 寫法直接執行會出錯或做出錯的事」**:GNU sed 的
      `-i` 可以不接任何參數(原地修改、不留備份),但 BSD sed(macOS
      內建)的 `-i` **強制要求**緊接一個備份副檔名參數(可以是空字串
      `''`,但那個參數位置一定要有東西)——`build-iso.sh` 跟
      `lib/patch-boot-menu.sh` 原本全部是 `sed -i "腳本" 檔案` 這種
      GNU 寫法,直接在 macOS 上執行,BSD sed 會把緊跟在 `-i` 後面的
      "腳本" 字串誤當成備份副檔名,把原本要修改的內容當成"要處理的
      檔案清單"的第一個檔名,整條指令的參數解讀全部錯位,不是單純
      「效果一樣、語法不同」而已。修法:新增
      `build/appliance/lib/portable-sed.sh`,提供
      `gonas_sed_inplace()` 函式,統一用 `-i.gonas-sed-bak` 這種「兩邊
      都合法、都是接一個非空字串當備份副檔名」的寫法呼叫 `sed`,再手動
      `rm -f` 掉那個備份檔——這個寫法在 GNU sed 跟 BSD sed 底下行為
      一致,不需要在呼叫端判斷作業系統。`build-iso.sh` 跟
      `lib/patch-boot-menu.sh` 裡所有 `sed -i` 呼叫全部改用這個函式。
- [x] **`sha256sum`/`md5sum` 這兩個指令在 stock macOS 上根本不存在**:
      這兩個是 GNU coreutils 的東西,macOS 內建的是 `shasum -a 256`
      (輸出格式跟 `sha256sum` 相容)跟 `md5 -r`(輸出格式跟 `md5sum`
      相容)。`build-iso.sh` 原本在快取檢查、完整性檢查、最終校驗檔
      產生等總共 4 個地方直接呼叫 `sha256sum`,`md5sum.txt` 產生邏輯
      直接呼叫 `md5sum`——在沒有另外用 Homebrew 裝 coreutils 的
      macOS 機器上,這些呼叫會直接因為指令找不到而失敗,不是得到錯的
      結果,是整支腳本直接中斷。修法:新增
      `build/appliance/lib/portable-checksum.sh`,提供
      `gonas_sha256sum()`/`gonas_md5sum()`,各自先用 `command -v` 檢查
      GNU 版工具在不在,不在的話 fallback 到 macOS 原生的
      `shasum -a 256`/`md5 -r`,兩邊都沒有才真的報錯並清楚說明原因
      (以及 macOS 上這兩個工具本來就內建、不需要另外安裝)。
      `find ... -exec md5sum {} \;` 這一段額外改成
      `find ... | while read -r f; do gonas_md5sum "$f"; done`,原因是
      `-exec` 沒辦法直接呼叫一個 shell 函式,只能呼叫外部指令。
- [x] **補上的兩個新函式,各自都有離線回歸測試,而且測試本身也抓到
      一次「測試設計錯誤」**:`test-portable-sed.sh`(3 案例:基本
      修改邏輯正確、備份檔會被清乾淨、`sed` 本身失敗時回傳值不會被
      後面的 `rm -f` 蓋掉)、`test-portable-checksum.sh`(4 案例:GNU
      工具存在時用 GNU 工具、GNU 工具不存在時正確 fallback)全數
      通過。寫 `test-portable-checksum.sh` 的過程中,第一版的
      fallback 測試案例寫錯了:原本用「在一個插到 PATH 最前面的目錄裡
      放一個假的 `sha256sum`/`md5sum` 腳本」來模擬「這台機器沒有這個
      工具」,這是錯的——`command -v sha256sum` 只檢查 PATH 上有沒有
      一個叫這個名字、可執行的檔案存在,根本不會真的執行它,所以只要
      PATH 上有任何一個叫 `sha256sum` 的檔案(不管是真的還是假的),
      `command -v sha256sum` 就會回報「找到了」,程式碼一定會走
      「主要工具存在」那個分支,fallback 那段邏輯實際上完全沒被跑到
      過,不管假腳本裡面寫什麼都一樣。修法:fallback 測試案例乾脆不要
      放假的 `sha256sum`/`md5sum`,而是把 `PATH` 整個換成只包含假
      `shasum`/`md5` 的目錄(不含系統原本的 PATH),讓
      `command -v sha256sum`/`command -v md5sum` 真的找不到東西、
      真的觸發 fallback 分支——修好之後重新確認 4 個案例都通過,包含
      fallback 案例。
- [x] **確認過哪些腳本需要修、哪些不需要**:`late-command.sh`
      是在 `d-i preseed/late_command` 底下、`in-target sh -c "..."`
      進去的**真正 Debian in-target chroot 環境**裡執行的,不管使用者
      在哪一種作業系統上建置這支 ISO,`late-command.sh` 實際跑起來的
      環境永遠是 Linux——它自己內部的 `sed -i` 呼叫不受影響,不需要
      改。真正需要 macOS 相容性修正的,只有「在建置這台機器本身上
      執行」的腳本:`build-iso.sh` 本身跟它會 source 進來的
      `lib/patch-boot-menu.sh`。搞混這兩類腳本、對不需要改的
      `late-command.sh` 做多餘的修改,是這一輪特別注意要避免的錯誤。
- [x] **CI 新增一個 `macos-latest` runner 的 job**:前 16 輪的
      CI 設定(`appliance-shell` job)只在 `ubuntu-latest` 上跑,這代表
      即使有心力寫 `lib/portable-sed.sh`/`lib/portable-checksum.sh`
      這兩個修正,CI 本身也從來沒有機會在一台真正的 BSD sed/
      shasum/md5 環境下驗證這兩個修正真的有效——這兩個修正本身完全有
      可能又寫錯一次(例如又不小心在某個新加的地方直接呼叫了
      `sed -i` 而忘記用 `gonas_sed_inplace`),過去的 CI 設定不會
      發現。新增 `appliance-shell-macos` job,`runs-on: macos-latest`,
      跑跟 `appliance-shell` 完全一樣的 5 支離線測試腳本,差別只在
      執行環境——這樣任何一次修改弄壞了 macOS 相容性,CI 會直接紅燈,
      不需要等到使用者真的在自己的 Mac 上手動跑才發現。

**這一輪額外跑了一次過去沒認真做過的動作:把
`go test ./... -race -count=1` 連續重跑了好干次(不是只跑一次),
意外抓到一個跟 macOS 完全無關、純粹的 Go 測試程式碼 bug**:

- [x] **`internal/selfupdate/selfupdate_test.go` 的
      `TestChecker_RunsRepeatedlyAndStopsCleanly` 有一個真正的、間歇性
      觸發的 data race,不是每次跑都會抓到**:這個測試用一個普通的
      `var count int`(不是 `atomic.Int64`,也沒有 mutex)在 httptest
      的 handler goroutine 裡 `count++`,同時在測試自己的 goroutine裡
      直接讀 `count`(`if count < 2`、`after := count`、
      `count != after`)。第十六輪雖然已經第一次完整跑過
      `go test ./... -race -count=1` 並且回報全綠,但 race detector
      能不能抓到一個間歇性的 race,本來就跟當下的 goroutine 排程時機
      有關,不是「跑過一次沒事就代表沒有這個問題」——這一輪連續跑了
      十幾次之後,其中一次真的跳出
      `WARNING: DATA RACE`,明確指向這個 `count` 變數。雖然
      `Checker.Stop()` 本身的實作(`cancel()` 之後 `<-c.done` 等背景
      goroutine 真的執行完才回傳)在邏輯上確實保證了「`Stop()` 回傳之後
      不會再有新的檢查發生」,但這只保證「不會再發新的 HTTP
      request」,不保證 race detector 能沿著「HTTP round trip 完成」
      這條路徑辨識出足夠強的 happens-before 關係——一個沒有任何
      同步保護的 `int`,在兩個 goroutine 之間讀寫,即使功能上剛好每次
      都是對的,對 race detector 來說仍然是未定義行為,而且是真的會被
      抓到的(不是誤報)。修法:把 `count` 改成 `atomic.Int64`,讀寫
      都走 `Add`/`Load`,不再依賴「這個測試場景下網路 round trip
      恰好提供了足夠同步」這種脆弱的假設。修完之後連續跑
      `go test ./internal/selfupdate/... -race -count=1` 十次以上,
      沒有再出現過 race。
      這個 bug 跟 macOS 沒有任何關係,純粹是因為這一輪第一次認真把
      `-race` 反覆跑了很多次(過去頂多跑一兩次就當作驗證過了),才有
      機會撞見這種間歇性、跟系統負載/排程時機有關的問題——提醒往後
      「`go test -race` 過一次」不等於「這個套件裡沒有 race」,間歇性
      的 race 需要多跑幾次才有機會撞見。

這一輪修改的檔案:新增 `build/appliance/lib/portable-sed.sh`、
`build/appliance/lib/portable-checksum.sh`、
`build/appliance/test-portable-sed.sh`、
`build/appliance/test-portable-checksum.sh`;修改
`build/appliance/build-iso.sh`、`build/appliance/lib/patch-boot-menu.sh`
`build/appliance/test-boot-menu-patch.sh`、`.github/workflows/ci.yml`、
`docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md`(macOS 專屬的建置/測試
步驟)、`internal/selfupdate/selfupdate_test.go`(race 修正)。
`gofmt -l -s .`/`go vet ./...`/`go build ./...`/
`go test ./... -race -count=1`(含針對 selfupdate 套件額外重跑十次
確認 race 真的修好)/全部 5 支 `test-*.sh`(`sh -n` 語法檢查跟實際
執行)全數維持全綠。

**第十八輪(使用者在真正的 Mac mini(M4)上第一次實測建置,立刻就撞見
一個問題):`make iso-arm64` 直接失敗
`Permission denied`,不是程式邏輯錯,是這份原始碼透過 zip 下載、
在 macOS 用 Finder 解壓縮之後,`build-iso.sh` 的可執行權限位元掉了**。

- [x] **根本原因**:`Makefile` 的 `iso-amd64`/`iso-arm64` 這兩個
      target 原本直接寫 `build/appliance/build-iso.sh amd64/arm64
      $(VERSION)`,靠檔案本身的「可執行權限」位元讓 shell 找到
      shebang 執行——這跟第十四輪修 `late-command.sh`/`install.sh` 時
      發現、進而修正的問題,是完全同一個類別的 bug(「執行位元能不能在
      檔案傳輸過程中被保留」不能被信任),只是這次是踩在我們自己的
      建置工具鏈上,不是 ISO 裡打包的檔案:zip 檔案本身在這個沙盒裡
      驗證過(用 `unzip -l -v` 跟實際解壓縮測試)確實正確保留了
      `-rwxr-xr-x`,問題出在使用者實際的下載/解壓縮鏈路上(瀏覽器
      下載 → macOS Finder 雙擊解壓縮)某個環節把這個位元重置掉了——
      這是這個開發沙盒本身完全沒辦法重現、只有使用者實際走一次真正的
      下載流程才會暴露的問題,前 17 輪不管做多仔細的靜態審查都不可能
      找到,因為前 17 輪的「測試」全部是在同一台機器上直接跑 `sh -n`/
      執行測試腳本,從來沒有真的模擬過「打包成 zip → 下載 →
      解壓縮」這一整條實際交付鏈路。
- [x] **修法**:兩個 target 都改成 `sh build/appliance/build-iso.sh
      amd64/arm64 $(VERSION)`,用 `sh <path>` 明確呼叫、完全不依賴
      可執行權限位元,不管 zip/tar/git 在傳輸過程中有沒有保留它都能
      正常執行——跟 late-command.sh/install.sh 用的是同一個修法。
      同時確認過 `build-iso.sh` 內部呼叫 `lib/*.sh` 都是用 `.`
      (source)進來,不是直接執行,本來就不受這個位元影響,不需要
      額外修改;`build-iso.sh` 自己會在把 `late-command.sh`/
      `install.sh` 塞進 ISO 之前明確下 `chmod +x`,這個既有的防禦寫法
      也確認沒問題。
- [x] **給使用者的立即解法**(在等到修正版重新打包之前,讓他不用
      卡住):`chmod +x build/appliance/*.sh build/appliance/lib/*.sh`
      之後直接重跑 `make iso-arm64` 即可,不需要重新下載。

沒有新增或修改任何測試腳本(這是 `Makefile` 呼叫方式的問題,不是
被測試的邏輯本身有問題,現有的 `sh -n`/`test-*.sh` 都不會涵蓋
「exec bit 在真實下載鏈路上會不會掉」這件事,這本質上是傳輸環境的
問題,不是能寫成離線回歸測試的邏輯 bug)。修改
`gofmt -l -s .`/`go vet ./...`/`go build ./...`/全部 5 支
`test-*.sh`(`sh -n` 語法檢查跟實際執行)/`make -n iso-arm64`(dry
run 確認新的呼叫方式語法正確)全數維持全綠。

**同一輪測試,`chmod +x` 之後重跑 `make iso-arm64`,立刻撞見第二個、
嚴重得多的問題:`build-iso.sh` 對 Debian netinst ISO 檔名的假設從
一開始就是錯的,這個 Phase 從第一天開始就不可能真的建置成功過,
只是這個開發沙盒完全連不上 `cdimage.debian.org`,前 17 輪不管做多
仔細的靜態審查都不可能發現**:

- [x] **症狀**:`==> fetching https://cdimage.debian.org/debian-cd/
      current/arm64/iso-cd/SHA256SUMS` 之後,緊接著
      `error: debian-bookworm-arm64-netinst.iso not found in
      downloaded SHA256SUMS`——SHA256SUMS 有真的抓下來,但腳本自己
      組出來要找的檔名,根本不在這份清單裡。
- [x] **根本原因**:`build-iso.sh` 原本寫
      `BASE_ISO_NAME="debian-$DEBIAN_RELEASE-$DEBIAN_ARCH_DIR-netinst.iso"`
      ,`$DEBIAN_RELEASE` 預設是 `bookworm`,假設官方 netinst ISO
      的檔名是用「版本代號」組出來的——這個假設從一開始就是錯的。
      實際用 `WebFetch`/`WebSearch` 查了一個公開鏡像站
      (mirror.arizona.edu)的目錄列表才確認:`debian-cd/current/
      <arch>/iso-cd/` 底下真正的檔名是用**完整版本號**組的,像
      `debian-13.6.0-arm64-netinst.iso`,不是代號——`bookworm`/
      `trixie` 這種名字只用在 APT 套件庫的路徑(`/debian/dists/
      bookworm/`),是完全不同的一套命名慣例,這支腳本從一開始就
      把兩者搞混了。這也連帶說明了另一件事:現在(2026 年 9 月)
      Debian 的目前穩定版其實已經是 13(trixie)而不是 12
      (bookworm)——`current/` 這個路徑的內容本來就會隨官方發布新
      穩定版自動往前推進,不會停在寫這支腳本當下的那個版本。
      不管代號對不對,只要是用代號组檔名,這個邏輯在 `current/`
      這個路徑底下就**從來沒有可能是對的**——這不是「環境剛好變了
      才壞掉」,是從第一行程式碼寫下去就不成立的假設,只是需要真的
      發一個 `wget` 請求出去、看官方伺服器實際回應什麼,才會現形,
      前面 17 輪不管靜態審查多少次都不可能觸發到這條路徑。
- [x] **修法**:不再自己組一個「猜測的」檔名,改成先把
      `SHA256SUMS` 抓下來,直接用 `grep -o
      'debian-[0-9][0-9.]*-<arch>-netinst\.iso'` 從清單裡實際找出
      符合格式的那一行,檔名跟版本號都來自 Debian 當下真正發布的
      內容。這樣不管 Debian 之後從 trixie 換到下一個代號、還是同一個
      穩定版又出新的 point release(12.11.0 -> 12.12.0 這種),都
      不需要回來改這支腳本——原本用來「選版本」的
      `GONAS_DEBIAN_RELEASE` 環境變數因此整個拿掉,它原本想選的東西,
      `current/` 這個路徑本來就只會有一份,沒有代號可選;真的想固定
      用某個已經封存的舊版本,改指定 `GONAS_DEBIAN_ISO_URL` 指到
      對應的 archive 路徑即可。同步更新了
      `build/appliance/README.md` 三處提到 `GONAS_DEBIAN_RELEASE`
      的說明。
- [x] **這個修正還沒有被使用者實際重跑驗證過**:改動當下只用一份
      手寫的假 `SHA256SUMS`(內容模仿真實格式,含一行故意設計成
      「檔名相似但不該命中」的干擾行)在這個沙盒裡驗證過
      `grep`/`awk` 這兩行邏輯本身抓得對,`sh -n`/五支離線測試/
      `gofmt`/`go vet`/`go build`/`make -n iso-arm64` dry run 全部
      維持全綠——但這個沙盒仍然連不上 `cdimage.debian.org` 本身,
      「這次改完真的重跑 `make iso-arm64` 會不會成功抓到 ISO」還是
      要靠使用者下一次重跑才能真正確認,是這個 Phase 目前為止
      最需要使用者立刻回報結果的一個修正。

**同一輪測試,上面兩個問題都修完、使用者重新解壓縮修正版之後重跑
`make iso-arm64`,真的成功抓到官方 `debian-13.6.0-arm64-netinst.iso`
、checksum 通過、xorriso 解開 ISO 都順利完成——一直跑到「修改開機
選單設定檔」這一步才又撞見第三個問題,而且又是一次「螢幕上完全沒有
任何錯誤訊息,只有 make 印出的 `Error 1`」**:

- [x] **症狀**:終端機印完 `==> patching boot menu configs to
      auto-load the GoNAS preseed` 之後,直接跳到
      `make: *** [iso-arm64] Error 1`,中間連「找到幾個開機選單設定檔」
      這行原本一定會印的 `==> found N boot menu config file(s) to
      patch` 都沒有出現——代表腳本死在這兩行 echo 之間,比腳本自己
      任何一段判斷邏輯都還要早。
- [x] **根本原因**:這一步用
      `find "$EXTRACT_DIR/isolinux" "$EXTRACT_DIR/boot/grub" ...`
      同時給兩個起始路徑找設定檔。arm64 的官方 netinst ISO 是
      EFI-only(這件事本身這支腳本自己的註解早就寫到了),**本來就
      沒有 `isolinux/` 這個目錄**,只有 amd64 才有——`find` 遇到一個
      不存在的起始路徑時,雖然仍然會正確處理另一個存在的路徑、正確找
      到 `boot/grub` 底下的檔案,但 `find` 自己整體的 exit code 會
      因為那個「路徑不存在」的錯誤變成非 0。這支腳本開頭設定的是
      `set -eu`,任何一個非 0 exit code 的簡單指令都會讓整支腳本立刻
      中止——所以問題根本不在下面已經寫好、訊息很清楚的
      `[ "$CFG_COUNT" = "0" ]`/`[ "$INJECTED" != "1" ]` 這兩段檢查
      (它們的訊息設計得很明確,可惜完全沒有機會被執行到),而是
      `find` 這一行本身的 exit code 搶先讓 `set -e` 把腳本殺掉,連
      带錯誤訊息也被 `2>/dev/null` 一起吃掉了。這是純粹的 POSIX
      shell 陷阱(`find` 多起始路徑 + `set -e`),不是邏輯設計錯誤,
      也是這個開發沙盒完全沒辦法發現的一類問題——這裡的假 ISO 目錄樹
      從來沒有真的模擬過「isolinux 目錄不存在」這個 arm64 專屬的情境
      ,一定要真的用 arm64 建置一次才會觸發。
- [x] **修法**:把這行 `find` 抽成
      `lib/find-boot-menu-cfgs.sh` 的 `gonas_find_boot_menu_cfgs()`
      ,內部明確用 `|| true` 吃掉 `find` 自己的 exit code——「兩個
      目錄底下加起來到底有沒有找到任何檔案」這件事,還是交給呼叫端
      既有的 `CFG_COUNT`/`INJECTED` 檢查邏輯決定,只是現在這些檢查
      終於真的有機會被執行到。新增
      `test-find-boot-menu-cfgs.sh`,3 個案例(兩個目錄都存在、
      isolinux 目錄不存在只有 grub 目錄存在——直接對應 arm64 的真實
      情境、兩個目錄都不存在),而且**特地驗證過如果沒修好、測試真的
      會用「跟使用者實測一模一樣的方式死掉」**:暫時把
      `gonas_find_boot_menu_cfgs()` 裡的 `|| true` 拿掉重跑這支測試,
      案例一的 `PASS` 印出來之後,測試直接中止、之後任何一行都沒有
      印出來(既沒有 FAIL、也沒有後續的 PASS),跟使用者截圖裡看到的
      症狀一模一樣;確認過這一點之後才把 `|| true` 加回去,重新確認
      3 個案例全部變回 PASS。CI 的兩個 job(Linux、macOS)都加了這支
      新測試。
- [x] **這一輪意外驗證了一件事**:第十八輪一開始修的「Debian netinst
      檔名要動態偵測」那個修正,這裡已經被使用者實測證實有效——真的
      抓到了 `debian-13.6.0-arm64-netinst.iso`、checksum 也真的通過
      、xorriso 也真的把 ISO 解開了,前面完全沒有再出任何問題,這是
      這個 Phase 第一次有一段邏輯被使用者的真實網路環境完整跑過並且
      成功。

**上面的 `find`/`set -e` 問題修好之後,使用者真的用修正版重新
`make iso-arm64` 建置成功、拿去 Parallels 開機,拍照回報看到的是一個
標準的 GNU GRUB 選單畫面(`Install`/`Graphical install`/`Advanced
options`/... ,游標停在 `Install` 上不動)——這揭露第四個問題,而且
是「設計目標從第七輪訂下來之後,一直只有一半真正生效過」這種類型**:

- [x] **根本原因**:第七輪覆閱修的「開機選單不應該卡在那邊等人工
      按鍵、要盡快自動開始安裝」這個目標,當時的修法是把
      `isolinux.cfg` 的 `timeout` 欄位改短——但 `isolinux.cfg`
      是 amd64 專屬的檔案,arm64 官方 ISO 是 EFI-only,根本沒有這個
      檔案(跟這一輪前面「找開機選單設定檔」是同一個根本原因)。
      也就是說,`isolinux.cfg` 那段 timeout 修正從第七輪寫下來到
      現在,**只在 amd64 上真的生效過,在 arm64 上因為對應的檔案
      根本不存在,從來沒有真的執行到過**——這正是「開機不用人工介入
      就自動開始安裝」這個設計目標一直沒有真的在 arm64 上兌現的
      原因,而且跟前面幾個問題一樣,靜態審查完全看不出來:程式碼
      本身完全沒有語法或邏輯錯誤,`if [ -f ".../isolinux.cfg" ]`
      這個檔案存在檢查寫得很正確,只是「這個條件在 arm64 上永遠不
      成立」這件事,只有真的拿 arm64 的 ISO 開機一次才會被看見。
- [x] **修法**:grub.cfg 用的是 GRUB2 的 `set timeout=N`(單位:秒)
      語法,跟 isolinux.cfg 的 `timeout N`(單位:1/10 秒)是完全
      不同的格式,不能套用同一條 sed 規則。新增一段邏輯,逐一對
      `$CFG_LIST` 清單裡列出的每個檔案套用
      `set timeout=.* -> set timeout=5`(沒有這一行的檔案,這條規則
      本來就不會有任何動作,不會誤傷其他內容),不是只挑
      `boot/grub/grub.cfg` 這一個固定路徑,理由跟前面找設定檔那段
      一致。在沙盒裡用一份手寫、仿照真實 arm64 grub.cfg 格式的假
      設定檔驗證過這條 sed 規則本身抓得對(`set timeout=-1` 正確被
      換成 `set timeout=5`,其他行完全不受影響)。
- [x] **給使用者的立即解法**(不用等重新建置就能繼續往下測):目前
      這份已經裝到一半的 ISO 卡在選單畫面,直接按 Enter(或等它自己
      的原始逾時時間跑完)選 `Install` 繼續就好——boot 參數的注入
      (preseed 自動安裝那一段)已經確認有效,只是選單本身還要手動
      選一次,選完之後照樣是全自動安裝、不會再需要其他人工介入,不影響
      這一次測試的其他驗證項目。這個 timeout 修正是給「以後重新建置
      的 ISO」用的,不是這一份已經在裝的媒體能夠回頭套用的東西。

**選了 `Install` 繼續之後,使用者拍照回報又跳出一個要求手動回答的
對話框:`[!!] Configure the package manager`,內容是「The image with
the following label has already been scanned: Debian GNU/Linux
13.6.0 _Trixie_ - Official arm64 NETINST with firmware
20260711-09:43. Please replace it now, if you wish to scan another.
Scan extra installation media?」,`<Yes>` 被反白選取——這一次是真正
的 `preseed.cfg` 邏輯錯誤,不是環境假設問題**:

- [x] **根本原因**:`preseed.cfg` 裡 `d-i apt-setup/cdrom/set-first
      boolean true` 這一行,語意剛好寫反了。這個 debconf 問題問的是
      「用完第一份安裝媒體之後,要不要繼續問使用者要不要插入第二份
      來掃描更多套件」,`true` 代表「要」——所以即使整份 preseed
      已經用 `priority=high` 蓋掉大多數問題,安裝到「設定套件管理員」
      這一步時,還是會真的跳出這個對話框要求使用者手動選 `<No>` 才能
      繼續。這台機器從頭到尾只有一份安裝媒體(就是它自己開機用的這片
      USB/ISO),根本沒有「第二片」這回事,這一步從來就不應該需要
      人工介入——這是一個貨真價實的設定值寫反了的 bug,不是「這個
      沙盒沒辦法驗證的環境假設」那一類,是所有前 17 輪讀 `preseed.cfg`
      時都應該看得出來、卻沒有人真的意識到這個布林值方向不對的問題。
- [x] **修法**:對照 Debian 官方 bugs.debian.org #992183(這正是
      `installation-guide` 的 `example-preseed.txt` 官方範例檔案本身
      缺漏這一行、導致同一個症狀的 bug 報告討論)確認正確答案是
      `boolean false`,把這一行改成 `d-i apt-setup/cdrom/set-first
      boolean false`。
- [x] **這個修正同樣還沒有被使用者實際重新走一次完整安裝驗證過**:
      目前這份已經卡在這個對話框的安裝,直接選 `<No>` 就能繼續(這台
      機器確實沒有第二份媒體,選 `No` 是唯一正確答案),不影響這一次
      測試的其餘驗證項目;下一次重新建置、重新安裝,才會是這個修正
      第一次真的被走過、確認生效的機會。

這一輪最值得記錄的教訓:**前 17 輪反覆討論、也在 late-command.sh/
install.sh 上主動修過的「執行位元能不能被信任」這個原則,自己的
`Makefile` 卻沒有同步套用**——知道一個風險類別存在,不代表已經檢查過
專案裡所有踩得到同一個坑的地方。第二個問題更根本:**一個從第一天
就不成立的假設(檔名用代號組),不管靜態審查跑幾輪都發現不了,因為
觸發它需要的是一個這個沙盒環境本身就沒有的東西——對外網路**。第三
個問題(`find` 多起始路徑 + `set -e`)則是另一種完全不同的盲區:
**這不是「假設錯了」,是一個純粹的 shell 語言陷阱,連對的邏輯設計
(下面已經寫好的 CFG_COUNT/INJECTED 檢查)都會被它搶先弄死**,而且
只有在真正的目標架構(arm64,沒有 isolinux 目錄)上執行才會現形,
拿 amd64 測、或拿任何「兩個目錄都存在」的假資料測都不會發現。三個
問題合起來的教訓是同一件事的三種變體:**這個開發沙盒能做的靜態審查
,有一個結構性的天花板——不管再看幾輪、再換幾個角度,凡是需要
「真的执行在目標環境上」才會現形的問題(真實檔案傳輸鏈路、真實網路
回應、真實的目標架構目錄結構),都不在這個天花板以內**,這正是
「使用者實際動手測」不是形式上的最後一步、而是這整個 Phase 唯一能
真正驗證這幾段邏輯的方式的原因。

**第十九輪(使用者實測:安裝流程一路跑完、機器開機到 `gonas login:`
了,但整台機器是一台「陽春 Debian」——沒有品牌畫面、沒有 gonas.service
、沒有強制改密碼、沒有 SSH、沒有 uninstall.sh,`/etc/motd` 直接寫著
`GoNAS late-command.sh failed`)。這一輪抓到的是整個 Phase 19 到目前
為止「後果最嚴重、卻也最單純」的根本 bug,外加一個從一開始就存在的
設計矛盾。**

- [x] **根本 bug:`build/install.sh` 用 `[ -x gonasd ]`(執行位元)
      找執行檔,ISO 上執行位元遺失就判定「找不到」→ `exit 1` →
      late-command.sh 整個中止**。診斷過程:`/etc/motd` 明白寫著
      late-command.sh failed,而 late-command.sh 的第 1 步就是
      `( cd "$RELEASE_DIR" && sh ./install.sh )`,且整支腳本是
      `set -e`——install.sh 只要非零退出,late-command.sh 就在第一步
      中止,後面「裝服務、換品牌、強制改密碼、留 uninstall.sh」全部
      不會執行,完全符合使用者看到的症狀(一台什麼都沒品牌化的
      Debian)。再往 install.sh 裡看:第 3 步找執行檔用的是
      `elif [ -x "$SCRIPT_DIR/gonasd" ]`——這正是整個專案從第十三/
      十四輪起就一路在修、也已經反覆強調「不能信任」的那個
      「ISO 上 Rock Ridge 執行位元會不會被保留」的假設:late-command.sh
      的呼叫、preseed 的 late_command、Makefile 的 build-iso.sh 呼叫
      全都因此改成不依賴執行位元,卻獨獨漏掉了 install.sh **內部**這一處
      ——這是「同一個坑,在一個地方修好了,卻沒檢查專案裡所有踩得到
      同一個坑的地方」的又一次現形,而且這次漏掉的那一處剛好是整條
      安裝鏈的第一步,一失敗就骨牌式地讓後面全部沒執行。修法:改成
      `[ -f ... ]`(只判斷檔案存在可讀)——install.sh 第 4 步複製完
      執行檔本來就會自己 `chmod 0755`,根本不需要來源檔案帶著執行位元,
      放寬成 `-f` 完全不影響安裝正確性。同時在 build-iso.sh 把 gonasd
      也一起加進「建置時 chmod +x」那行(純防禦,實際不依賴)。
- [x] **設計矛盾:「完全離線安裝」跟「裝完就有 SSH」在 netinst 光碟上
      本來就不可能同時成立,使用者選擇用「模式一」解決**。查 Debian
      官方文件確認:netinst 光碟官方定義就只含「裝 base 系統的最小
      套件」,`ssh-server`(tasksel 工作集)跟 `sudo`(套件)都不在
      裡面,正常安裝要連網去鏡像站抓——但 preseed 設了
      `apt-setup/use_mirror false`(完全離線)。結果原本 preseed 裡的
      `tasksel ... ssh-server` 跟 `pkgsel/include sudo`「從第一天就不
      可能在離線情況下成功」,裝完的系統既沒有 SSH server、gonasadmin
      也沒有可用的 sudo。使用者明確選了「模式一」:不靠安裝時連網,
      而是在「建置 ISO 的機器上(本來就要連網抓 netinst)」預先把
      openssh-server / sudo 及其相依 .deb 打包進 ISO,再由
      late-command.sh 在目標系統離線 `dpkg -i`。實作:
      - 新增 `lib/deb-closure.sh`:純邏輯,解析 Debian 的 Packages
        索引、算出種子套件(openssh-server、sudo)的相依封閉集,但排除
        Priority required/important(debootstrap 建的 base 一定已有這
        兩個優先級的所有套件),正確處理 `|` 替代相依、版本限制
        `(>= x)`、架構修飾 `:any`、以及虛擬套件 Provides。這是整個
        模式一裡唯一能在這個沙盒離線端對端驗證的部分。
      - 新增 `test-deb-closure.sh`:用手寫的假 Packages 資料涵蓋上述
        每一種邊界情況,11 個案例全過;而且驗證過它有鑑別力(把排除
        清單清空,被排除的 required/important 套件就會如預期出現)。
      - `build-iso.sh` 新增 4.5 節:建置時從 deb.debian.org 抓
        `dists/stable/main/binary-<arch>/Packages.gz`,算封閉集,把每個
        .deb 下載進 ISO 的 `gonas/debs/`。整段 best-effort:抓不到就
        印警告繼續、不讓建置失敗;可用 GONAS_SKIP_OFFLINE_PACKAGES=1
        整段跳過。checksum 工具沿用第十七輪的可攜包裝,解壓用
        `gzip -dc`(macOS/Linux 都有)。
      - `late-command.sh` 新增 1.7 節:在目標系統離線
        `dpkg -i /cdrom/gonas/debs/*.deb`,enable ssh,補一次
        `usermod -aG sudo gonasadmin`。**整段刻意做成 best-effort、
        每個可能失敗的指令都用 `if`/`|| true` 包起來,絕對不會用
        `set -e` 拖垮整支腳本**——這正是記取這一輪根本 bug 的教訓:
        選用功能(SSH)不該有能力用「一失敗就骨牌式中止」的方式害到
        核心功能(gonasd 本體 + Web 介面 + tty 主控台)。
      - `preseed.cfg`:tasksel 移除 `ssh-server`、移除
        `pkgsel/include sudo`(兩者改由打包的 .deb 離線安裝);保留
        `standard`(離線裝不起來會被靜默略過,但保留著,萬一日後改成
        允許連網就會正常裝)。
      - CI 兩個 job(Linux、macOS)都加了 test-deb-closure.sh。
- [x] **這一輪的驗證邊界要說清楚**:`-x`→`-f` 這個根本修正、跟
      deb-closure 的封閉集邏輯,都在沙盒裡驗證過了(前者是明確的因果
      推理 + 這一整個專案早就確立的「不信任執行位元」原則的直接套用,
      後者有 11 個案例的單元測試撐著);但「模式一」真正下載 .deb、
      在真實目標系統 `dpkg -i` 那些 I/O 步驟,這個沙盒沒辦法端對端測
      (連不上 deb.debian.org、也不能真的建 ISO / 跑安裝)。所以這一輪
      交付之後,還是要靠使用者重新建置 + 重新安裝一次:預期這次裝完
      重開機會看到 GoNAS 品牌畫面、gonas.service 正常跑、tty2 登入
      gonasadmin 會被強制改密碼、而且能 SSH 進去、gonasadmin 能 sudo。
      任何一項不如預期,一樣把畫面/log 帶回來。
- [x] **全面排查時再補抓到的兩個真實缺口(這一輪順手一起修掉)**:
      (1) **離線安裝後 apt 套件來源指著已退出的光碟**——`use_mirror
      false` + `cdrom-detect/eject true` 的組合,讓裝完的系統 apt 只
      認得那份已經退出的安裝媒體,`apt update`/`apt install` 會失敗,
      直接打臉 README 說的「開機後透過 Doctor 頁面/apt install 補裝
      mergerfs/samba/docker 等選用相依套件」。修法:late-command.sh
      新增 3.5 節,裝完後寫一份指向 deb.debian.org 的網路
      sources.list(main+updates+security,版本代號從 /etc/os-release
      的 VERSION_CODENAME 動態讀),並把還指著 cdrom 的舊來源檔案移開
      (deb822 跟舊格式都靠「內容含 cdrom:」判斷,兩種都涵蓋)。
      (2) **SSH host key 產生的保險**:openssh-server 的 postinst 正常
      會跑 `ssh-keygen -A`,但那是在沒有真正 systemd/裝置節點可能不完整
      的 in-target chroot 裡執行,不保證每次成功,沒有 host key 的話
      sshd 開機會起不來。late-command.sh 1.7 節在 dpkg 之後明確再補跑
      一次 `ssh-keygen -A`(冪等)。兩項都是 best-effort、不會中止整支
      腳本,同樣需要使用者實測確認(apt install 補裝一個套件試試、
      確認 SSH 連得進來)。
- [x] **回到最初的設想「除了選硬碟 + 必要確認,其餘全自動」全面重審
      preseed,補上幾個防禦性設定,確保不會有意料外的提示打斷自動安裝**:
      把整個 d-i 流程在 priority=high 下逐題盤過一遍,確認唯二會停下來
      的地方就是「選哪顆磁碟」跟「確認寫入磁碟」(這兩個正是要保留的
      必要確認)。順手補強三處:(1) apt-setup 除了已修的 cdrom/set-first
      ,再把 cdrom/set-next、another 都設 false,徹底杜絕「掃描其他安裝
      媒體?」這類提示;(2) console-setup/ask_detect 設 false,避免鍵盤
      偵測停下來;(3) grub-installer/force-efi-extra-removable 設 true
      ——UEFI 機器(arm64 一定是、很多 amd64 新機也是)額外把開機檔寫到
      \EFI\BOOT 標準路徑,既避免這一題跳出來問,也讓裝完在「只認標準
      路徑」的韌體(部分 VM、實體 NAS/mini PC)上開得了機。另外把
      build-iso.sh 縮短 GRUB 選單等待時間那段改robust:原本只比對行首
      沒縮排的 `set timeout=`,現在容許縮排、而且完全沒有這一行時會主動
      補一行(用假資料驗證過三種情況:有縮排的 set timeout=-1、完全沒有
      timeout 行、以及 isolinux 檔案不被誤傷),確保安裝媒體不會停在
      選單畫面等按鍵。這些都是對齊原始「全自動」設想的收尾,一樣待實測
      確認整條流程真的只停在選硬碟 + 寫入確認這兩處。

這一輪最值得記錄的教訓,跟前幾輪是同一條線的延伸,但更尖銳:**這台
機器「裝完卻是陽春 Debian」的根本原因,是一個早就被辨識出來、也已經
在好幾個地方修過的 bug 類別(不能信任 ISO 執行位元),卻獨獨漏在
install.sh 內部那一行——而且那一行剛好是整條安裝鏈的第一步。** 知道
一個 bug 類別存在、甚至已經修過它好幾次,都不等於已經把專案裡每一個
踩得到它的地方都找出來了;真正保證「同類 bug 不再回歸」的,不是
「記得這個坑」,而是像這一輪把 deb-closure 抽成有單元測試的函式那樣,
把邏輯變成可以被自動化盯著的東西。至於那個「離線 vs SSH」的設計矛盾,
提醒的是另一件事:有些矛盾不是實作 bug,是需求本身沒想清楚——netinst
「最小光碟 + 連網補完」的本質,跟「完全離線安裝」是直接衝突的,這種
衝突再多輪程式碼審查也審不出來,只有真的把整條路走到底(裝完、發現
SSH 不在)才會逼出來。

**第二十輪(使用者要求:預設帳密都設成 gonas,系統帳號跟 Web 超級
管理員都是,且第一次登入強制改密碼;另外確認開機會有 GoNAS logo +
網路資訊)**:

- [x] **系統帳號(OS 登入)**:preseed 的維運帳號從 `gonasadmin` /
      `gonas-change-me-now` 改成帳號 `gonas`、密碼 `gonas`;強制第一次
      改密碼的機制(late-command.sh 的 `chage -d 0`)保留,所以 gonas
      這組預設密碼只有第一次登入前有效。全專案(preseed、late-command、
      README、程序文件)所有 `gonasadmin`/`gonas-change-me-now` 的
      功能性引用都一併更新(只留 preseed 一處明確的「從 gonasadmin 改成
      gonas」歷史說明註解)。
- [x] **Web 超級管理員(admin)**:原本 Web 介面是「第一個連進來的人
      自己建立 admin 帳號」,沒有預設帳密。新增一整套「預設 admin +
      強制改密碼」:
      - `internal/state.AdminAccount` 加 `MustChangePassword` 旗標;
        `Store.SeedAdminIfEmpty()`(冪等,只在完全沒帳號時建一組
        role=admin、MustChangePassword=true 的帳號,雜湊由呼叫端算好
        傳入,避免 state 相依 security)。
      - `cmd/gonasd` 新增 `-seed-default-admin`:沒有任何 Web 帳號時
        建立 admin 帳密皆 `gonas`、標記強制改密碼,印完就結束、不啟動
        daemon;已有帳號則什麼都不做。
      - `internal/api`:`/auth/me` 跟登入回應多回 `mustChangePassword`;
        `handleAuthChangePassword` 改完密碼清掉旗標;**requireAdmin
        中介層在旗標為 true 時擋掉所有 admin 操作(403
        password_change_required)**——就算有人繞過前端直接打 API 也
        擋得住,不是只靠前端。
      - 前端 app.js:登入後 / 回到頁面時若 `mustChangePassword` 為
        true,強制跳到「修改密碼」畫面、擋住其他操作,改完才進主畫面;
        i18n 三語系都加了對應字串。
      - late-command.sh 新增 1.8 節:裝完用
        `GONAS_DATA_DIR=/var/lib/gonas gonasd -seed-default-admin`
        預先建好這組帳號(best-effort)。
      - 測試:`state` 測 SeedAdminIfEmpty(新增/冪等/持久化/旗標);
        `api` 測「旗標為 true 時 requireAdmin 回 403、/auth/me 回報、
        改密碼後清旗標並放行」。`go test ./... -race` 全綠。
- [x] **確認「開機有 GoNAS logo + 網路資訊」**:會有——這就是使用者
      當初選的「精簡狀態畫面」路線(gonas-console.service,見 overlay/
      usr/local/sbin/gonas-console)。開機後 tty1 顯示 GONAS 的 ASCII
      logo + 版本 + 主機名 + `http://<IP>:8291` 網址,每 5 秒刷新;
      還沒拿到 IP 時顯示「尚未偵測到網路」。之前沒出現純粹是因為
      late-command.sh 那個 `-x` 根本 bug 讓整個佈署中斷(第十九輪已修)。

依 gonas/gonas 是好記但眾所周知的預設值,兩邊(OS + Web)都保留「第一次
強制改密碼」作為安全底線。這一整套的 Go 部分有單元測試撐著(高信心),
但「真的用 gonas/gonas 登入 Web、被強制改密碼」的完整前端流程,一樣要
使用者實際重裝一次才能端對端確認。

**第二十一輪(使用者實測:Mac mini 上 `make iso-amd64` 下載官方 amd64
netinst ISO 中途失敗,`make` 只印出毫無資訊量的 `Error 4`)**:

- **症狀**:使用者依照 `docs/X86_AMD64_BURN_AND_TEST.md` 的步驟在
  Mac mini 上跑 `make iso-amd64`,SHA256SUMS 抓取、找到官方檔名
  (`debian-13.6.0-amd64-netinst.iso`)這兩步都成功,接著開始下載
  ~700MB 的官方 ISO 本體,畫面上只看到:
  ```
  ==> downloading https://cdimage.debian.org/.../debian-13.6.0-amd64-netinst.iso
      (this requires real internet access to a Debian mirror — will fail in a network-restricted sandbox)
  make: *** [iso-amd64] Error 4
  ```
  中間完全沒有任何 wget 自己的錯誤訊息,不知道是 DNS 失敗、連線被拒、
  逾時,還是傳輸中途斷線。
- **根本原因**:`build-iso.sh` 下載 base.iso 那一行用的是
  `wget -q --show-progress -O ...`——`-q`(quiet)會把 wget 自己所有的
  診斷訊息全部吞掉(只留 `--show-progress` 硬擠出來的進度條),而且
  這一行完全沒有 `if ! wget ...; then ...; fi` 的錯誤處理,純粹靠
  `set -e` 讓腳本悶聲退出。腳本退出碼直接沿用 wget 的退出碼——`Error 4`
  正是 wget 自己的 exit code 表裡「network failure」那一類(DNS、連線
  被拒、逾時、傳輸中斷都會落在這個分類),但因為 `-q` 把說明文字吞掉,
  使用者完全看不到具體是哪一種、也無從判斷是不是同一個問題再發生一次。
  這跟第十八輪 `find`+`set -e`、第十九輪 `install.sh` 用 `-x` 這兩個
  bug 是同一類「靜默失敗吃掉真正錯誤訊息」的問題,只是這次出現在
  下載這一步,而且是這個檔案裡**唯一一處沒有明確錯誤處理的 wget 呼叫**
  (抓 SHA256SUMS 那次有 `if ! wget ...`,抓 base.iso 這次沒有)。
  這個 bug 存在多久跟第十七輪那個 `debian-bookworm-*.iso` 檔名假設一樣
  ——沙盒完全沒有真實網路可以觸發這條下載路徑,17 輪覆閱都碰不到它,
  只有真的在有網路的機器上跑一次才會暴露。
- **修法**:拿掉 `-q`,讓 wget 真正的錯誤訊息印出來;明確用
  `if ! wget --show-progress -O ...; then ...; fi` 包起來,失敗時印出
  一行講清楚「幾百 MB 的檔案在不穩定的網路上傳輸,常見原因是 DNS 失敗/
  連線被拒/逾時/傳輸中途斷線,實際原因看上面 wget 自己印的訊息」,並
  提醒「因為失敗時什麼都不會被寫進快取,直接重跑同一個指令就會重新
  完整下載一次,不用清什麼快取」。
- **驗證邊界**:修法本身是「讓失敗訊息不要被吞掉」這種診斷性質的改動,
  在沙盒裡沒辦法真的觸發一次網路中斷來驗證訊息長什麼樣子(一樣是網路
  白名單擋住 Debian 鏡像)——`sh -n` 語法檢查跟既有的
  `test-portable-checksum.sh`/其餘六支測試全部照跑一次確認沒有連帶壞掉,
  但這個下載錯誤路徑本身沒有專屬的 lib 函式可以像 `find-boot-menu-cfgs`
  /`deb-closure` 那樣抽出來做假資料測試(邏輯本身就是「呼叫 wget、檢查
  結束碼」,沒有可以脫離真實網路獨立驗證的部分)。
  **給使用者的立即行動**:這個 bug 修的是「看不看得到錯誤訊息」,不是
  下載失敗本身的原因——網路中途斷線這種情況通常重跑一次就會過。建議
  直接重新執行 `make iso-amd64`;如果再次卡在同一步,這次應該會看到
  wget 自己印出的真正原因(例如 `Resolving cdimage.debian.org failed`
  或類似字樣),把那段訊息帶回來,才能判斷是網路本身不穩、還是
  Wi-Fi/VPN/防火牆擋掉了長時間的大檔案下載連線。

**第二十二輪(使用者實測:amd64 + ESXi 真機安裝,preseed.cfg 明明已經
寫了 `debian-installer/locale`,語系跟國家選單卻整個跳出來要手動選,
違反「除了選硬碟 + 必要確認,其餘都自動」的原始設想)**:

- **症狀**:使用者在真正的 ESXi 8.0 上開機安裝(第一次真的走出 QEMU/
  Parallels,在企業級 hypervisor 上測),`Graphical install` 選好之後
  (實際上因為顯示卡只有 16MB 顯存,自動退回文字模式安裝程式),接著
  出現一長串語言清單要選、選完又出現「Select your location」的國家
  清單,兩題都需要手動輸入數字才能繼續。
- **根本原因**:preseed.cfg 裡確實有 `d-i debian-installer/locale
  string en_US.UTF-8`,但這一題(連同國家、鍵盤這幾題,統稱
  localechooser)是整個 debian-installer 流程裡問得最早的幾題,早到
  「掛載光碟、讀取 gonas/preseed.cfg 檔案內容」這件事本身都還沒發生
  ——debconf 在這幾題問完之前,根本還沒機會讀到 preseed.cfg 裡寫的
  答案,所以就算檔案裡確實寫了,一樣會被問。這是 Debian 官方文件明確
  記載過的 preseeding 陷阱,跟 `build-iso.sh` 裡 `priority=high`(刻意
  選擇,不是 critical)完全是两回事,不衝突:priority 只影響「還沒有
  答案的問題要不要跳出來問」,這裡的根本問題是「答案的來源(preseed
  檔案)根本還沒被讀到」。
  這條路徑之前的 arm64(Parallels)測試沒有明確回報遇到這一題,不代表
  bug 只在 amd64/ESXi 才有——同一套 `build-iso.sh`/`patch-boot-menu.sh`
  邏輯兩個架構共用,理論上 arm64 一樣會問;比較可能的解釋是使用者當時
  只是很快按過去、沒有特別意識到這幾題不該出現(對照
  `APPLIANCE_BUILD_AND_TEST_PROCEDURE.md` 步驟 6 其實白紙黑字寫了
  「語系、鍵盤」是不該出現的例外情況),這次是第一次真的被明確截圖
  抓出來。
- **修法**:在 `build-iso.sh` 組合開機參數的 `APPEND_EXTRA` 裡,額外
  加上 Debian 官方文件建議的「裸」核心參數
  `language=en country=US locale=en_US.UTF-8 keymap=us`——這幾個是
  installer 認得的早期簡寫參數,在開機當下就直接生效,不需要等
  preseed.cfg 被讀到,跳過「先讀檔案才知道答案」這個時序問題。
  preseed.cfg 裡原本那行 `debian-installer/locale` 保留不動,兩邊寫的
  值一致(en_US.UTF-8 / us),互相印證。
- **驗證邊界**:改動本身只是在既有的開機參數字串裡多接幾個 Debian
  官方文件記載的標準參數,`sh -n` 語法檢查跟既有
  `test-boot-menu-patch.sh`(測的是「往設定檔裡插入這一整串參數」這個
  機制本身,不是驗證安裝程式看到參數之後的實際行為)都過,但「加了這幾
  個參數之後,語系/國家/鍵盤這幾題真的不會再跳出來問」這件事本身,
  沙盒完全沒辦法驗證(沒有真的 debian-installer 可以跑)——**需要
  使用者用這次修好的包重新建置一次 ISO、重新開機安裝一次來確認**。
  如果加了這幾個參數之後仍然被問,代表這個 Debian 版本(13.6.0)的
  installer 行為跟官方文件描述的不完全一致,需要把新截圖帶回來。
  另外也留意到這台 ESXi VM 的顯示卡只有 16MB,导致自動退回文字模式
  安裝程式(不是 bug,只是順手記錄——純文字模式不影響 preseed 生不
  生效,兩種模式共用同一套 debconf 邏輯)。

**第二十三輪(使用者實測:amd64 + ESXi,late-command.sh 真的失敗了一次,
但照 motd 提示去找 `/var/log/syslog` 卻根本找不到這個檔案)**:

- **症狀**:裝完開機登入後 motd 顯示 `GoNAS late-command.sh failed —
  see /var/log/syslog on first boot`(這是 preseed.cfg 裡 late_command
  失敗時的既有備援訊息),但 `sudo grep -i late-command /var/log/syslog`
  跟 `sudo grep -i gonas /var/log/syslog` 都回報
  `grep: /var/log/syslog: No such file or directory`——連檔案本身都
  不存在,不是內容裡沒有那幾行。
- **根本原因**:這則備援訊息的檔案路徑從一開始寫的時候就是錯的,只是
  之前 17 輪覆閱加上 arm64/Parallels 那幾次測試都沒有真的觸發過
  late_command 失敗(第十九輪修好 install.sh 的 `-x` bug 之後,arm64
  那條路徑一路順利跑完,從來沒有機會走到這個備援分支),這次是第一次
  真的因為某個原因觸發 late_command 失敗,才第一次發現這則訊息本身
  就沒有實際驗證過對不對。原因有兩層:(1)Debian 13 預設只用
  journald,不會自動裝 rsyslog 把訊息寫成傳統的 `/var/log/syslog`
  純文字檔;(2)就算裝了 rsyslog,late-command.sh 執行的時間點是
  debian-installer 的 in-target chroot 階段,目標系統這時候根本還沒
  真正開機、rsyslog 也還沒啟動,late-command.sh 的輸出本來就寫不進
  目標系統自己的 `/var/log/syslog`——查過 Debian 官方安裝手冊
  「Troubleshooting the Installation Process」一節,原文寫的是「開機
  進到裝好的系統之後,相關訊息在 `/var/log/installer/`」,從來就不是
  `/var/log/syslog`。
- **修法**:與其繼續依賴 d-i 這個間接、沒有明確記載保證行為的機制,
  直接讓 `preseed.cfg` 的 late_command 那一行把 late-command.sh 自己
  的 stdout/stderr 明確導向一個固定檔案
  `/var/log/gonas-late-command.log`(`>/var/log/gonas-late-command.log
  2>&1`),不管 d-i 未來版本怎麼處理它自己的安裝紀錄,這個檔案一定會在
  目標系統上。失敗時 motd 的提示文字也一併改成指向這個新檔案(順便
  提一句「如果連這個檔案都沒有,改看 /var/log/installer/」當備援)。
- **給這次卡住的使用者的立即行動**:你手上這台 VM 是用舊版 preseed
  裝的,新的 log 檔案不會存在,**改查
  `/var/log/installer/syslog`**——`sudo grep -i late-command
  /var/log/installer/syslog` 或 `sudo grep -i gonas
  /var/log/installer/syslog`,如果那個檔案本身也不存在,先
  `sudo ls -la /var/log/installer/` 看實際有哪些檔案,把結果截圖回報。
- **驗證邊界**:preseed.cfg 這一行改動本身沒有專屬的語法檢查工具(不是
  shell,是 debconf 格式),`sh -n` 對既有腳本全部重跑一次確認沒有
  連帶壞掉,但「加了這個重導向之後,失敗時這個新檔案真的會存在、內容
  真的可讀」這件事跟這個 Phase 大多數改動一樣,沙盒完全沒辦法驗證,
  需要使用者下一次重新建置、重新安裝時確認。

**第二十三輪(續:真正的錯誤訊息挖出來了)**:改對備援訊息之後,使用者
馬上就從 `/var/log/installer/syslog` 挖出真正的原因:

```
in-target: sh: 0: cannot open /cdrom/gonas/late-command.sh: No such file or directory
```

- **根本原因**:`late_command` 那一行原本寫的是
  `in-target sh -c "... sh /cdrom/gonas/late-command.sh ..."`——這裡
  搞錯了 `in-target` 的語意。`in-target COMMAND` 是先 chroot 進
  `/target`(剛裝好的目標系統)才執行 COMMAND,但 **chroot 進去之後,
  `/cdrom` 這個掛載點不保證在裡面也看得到**。Debian 官方文件裡能找到
  的 preseed 範例,凡是要把安裝媒體上的檔案弄進 `/target`,寫法都是
  「不加 `in-target`,直接在安裝程式自己的環境裡 `cp /cdrom/x
  /target/y`」(這個時間點 `/cdrom` 保證還掛著,`/target` 也已經是
  可以寫入的最終檔案系統)——而不是「先 chroot 進 `/target`,再指望
  從裡面找得到 `/cdrom`」,這兩件事表面上很像,實際上是完全不同的
  檔案系統視角。這個假設錯了多久跟前面幾輪的模式一樣:arm64/
  Parallels 那次測試沒有踩到(不代表假設原本是對的,比較可能只是
  Parallels 虛擬光碟機的行為剛好沒有暴露這個問題),ESXi 的虛擬光碟機
  上第一次真的踩到。
- **修法**:`late_command` 改成先(不加 `in-target`)把整個 `gonas/`
  目錄複製一份到 `/target/var/lib/gonas-install/gonas`,`late-command.sh`
  之後改成從這份複製好的檔案執行(`GONAS_INSTALL_MEDIA=/var/lib/
  gonas-install`),不再依賴 `/cdrom` 在 chroot 裡看不看得到。
  `late-command.sh` 本身原本就支援用環境變數覆寫安裝媒體路徑
  (`GONAS_INSTALL_MEDIA`),這裡只需要改 `preseed.cfg` 傳進去的值跟
  `late-command.sh` 開頭那個預設值的說明註解,程式邏輯本身不用改。
- **驗證邊界**:跟前面所有這類 preseed/late_command 改動一樣,沙盒
  沒有真的 debian-installer 可以驗證這個修法本身有沒有效——`sh -n`
  跟既有測試全部重跑一次確認沒有連帶壞掉,但「複製這一步會不會成功、
  複製完 late-command.sh 從新路徑執行會不會一樣正常跑完」需要使用者
  下一次重新建置、重新安裝來確認。如果這次修完還是同樣的錯誤(檔案
  找不到),把新的 `/var/log/installer/syslog`(或
  `/var/log/gonas-late-command.log`,如果這次有跑到那一步的話)內容
  帶回來。

**第二十四輪(logo/品牌化確認修好之後,主動回頭再查一輪,不是使用者這次
回報的新問題)**:

- **抓到什麼**:`late-command.sh` 判斷架構那一行
  `ARCH="$(dpkg --print-architecture 2>/dev/null)"` 沒有 `|| true`。
  這支腳本全程 `set -e`,而緊接著下面就是特地為了「萬一 dpkg 不可用」
  寫的 fallback(`uname -m` 對應回 Debian 架構名稱,見第九輪覆閱的
  說明)——問題是這個 fallback 根本沒有機會被執行到:在 `set -e`
  底下,`VAR="$(cmd)"` 這種賦值句,如果 `cmd` 本身結束碼非 0,賦值句
  自己的結束碼就是 `cmd` 的結束碼,一樣會被 `set -e` 判定成「這一行
  失敗了」,直接中止整支腳本,不會等到下面 `if [ -z "$ARCH" ]` 才處理。
  寫了一段最小範例在沙盒裡實測驗證:`set -e` 底下
  `ARCH="$(false)"; echo "got here"` 這兩行,`echo` 根本印不出來,腳本
  已經在賦值那一行就死了。等於這支腳本裡「精心設計、還寫了一大段
  註解說明的 fallback 邏輯」,如果 dpkg 真的哪次不可用,反而完全沒有
  機會執行——會是又一次「陽春 Debian」症狀,而且比前面幾次更難查,
  因為連 `[gonas-late-command]` 的第一行 log 都印不出來(腳本在
  `log "install media: ..."` 這行印之前就已經死了)。
- **這算不算「真的發生過的 bug」**:不算,是主動覆閱抓到的潛在風險,
  不是這次或之前哪一輪使用者實測真的踩到的——`dpkg` 在一個正常裝好的
  Debian in-target chroot 裡幾乎不可能不可用,目前為止的所有實測都是
  在 dpkg 正常運作的路徑上通過的。但「幾乎不可能發生」不代表「發生了
  也沒關係」,尤其這個 bug 一旦真的觸發,後果是整台機器變回陽春
  Debian,診斷難度比目前修過的所有其他問題都高(log 都沒有),值得
  現在就補起來,不用等真的遇到才修。
- **修法**:command substitution 裡面自己加 `|| true`
  (`dpkg --print-architecture 2>/dev/null || true`),讓賦值句本身
  一定成功,「dpkg 到底失敗了沒」這件事交給既有的
  `[ -z "$ARCH" ]` 檢查去判斷,這樣下面本來就寫好的 fallback 邏輯才
  真的有機會被執行到。
- **順手覆查的其他同類寫法**:把 `late-command.sh`/`build-iso.sh` 裡
  所有 `VAR="$(...)"` 形式的賦值句都檢查了一遍,確認同一類陷阱有沒有
  藏在別的地方——`late-command.sh` 裡另外兩處
  (`UNAME_M="$(uname -m 2>/dev/null || echo unknown)"`、
  `GONAS_CODENAME="$(. /etc/os-release 2>/dev/null; echo
  "${VERSION_CODENAME:-}")"`)都已經用 `|| echo ...` 或 `;` 接一個
  一定成功的指令墊底,原本就是安全的寫法;`build-iso.sh` 裡幾處
  (`BASE_ISO_NAME`/`EXPECTED_SHA256`/`ACTUAL_SHA256`/`CFG_COUNT` 等)
  都是管線(`| head`/`| awk`/`| tr`)結尾,POSIX 管線的結束碼看的是
  最後一個指令,同樣安全。只有 `ARCH` 這一處是真正的漏網之魚。
- **驗證邊界**:這是純粹的 shell 語意問題,不需要真正的
  debian-installer 環境就能在沙盒裡百分之百驗證(上面那段最小範例
  已經證明問題存在、修完之後同一段範例也證明修法有效),`sh -n` 跟
  全部既有測試也重跑一次確認沒有連帶壞掉——這一項比這個 Phase 大多數
  項目都更有把握,不屬於「需要使用者實機驗證」那一類。

**第二十四輪(續:主動複查所有「宣稱是 best-effort」的區塊,逐一確認
真的每一步都擋住了 set -e/set -eu)**:

- **抓到什麼**:`build-iso.sh` 第 4.5 節(離線 SSH 打包)開頭的
  `mkdir -p "$DEBS_DIR"` 沒有任何錯誤處理——這支腳本一開頭是
  `set -eu`,萬一這裡失敗(建置機器磁碟空間不夠、或權限問題),會
  直接中止整支 `build-iso.sh`,連 gonasd 本體都還沒塞進 ISO 就整個
  建置失敗,卻跟緊接在旁邊的註解「整段是 best-effort,失敗只印警告、
  不會讓整個 ISO 建置失敗」講的完全不一致——SSH 只是選用便利功能,
  不該有能力拖垮整個建置。
- **修法**:改成 `if ! mkdir -p ...; then 印警告、跳過整個離線打包
  區塊; else 原本的邏輯; fi`,失敗時只記警告繼續,不中止建置。
- **順手複查範圍**:把 `build-iso.sh`/`late-command.sh` 裡所有寫著
  「best-effort」「不中止」「不影響」字樣的區塊都重新檢查了一遍,
  確認區塊內**每一個**指令都真的有 `if`/`|| true` 擋住,不只是區塊
  開頭那一句有擋、後面漏了幾行——`late-command.sh` 裡的幾個 best-effort
  區塊(1.7 離線 SSH 安裝、1.8 預建 Web admin、3.5 修 apt 來源)逐行
  確認過都沒有漏網之魚,只有 `build-iso.sh` 這一處。
- **驗證邊界**:純 shell 語意問題,`sh -n` 跟既有測試全部重跑一次確認
  沒有連帶壞掉,不需要真正的建置環境就能有把握這個修法本身是對的
  (跟第二十四輪前半的 `ARCH` 那個修法一樣,不屬於「需要使用者實機
  驗證」那一類)。

## 各個環節目前的 log 覆蓋現況(使用者要求列出來)

| 環節 | 執行環境 | log 去哪裡 | 現況 |
|---|---|---|---|
| `build-iso.sh`(在使用者自己的建置機器上手動跑) | 建置機器的終端機 | 直接印在螢幕上(`==> ...` 開頭),使用者當場就看得到,沒有另外寫檔案 | 每個關鍵步驟都有訊息;第二十一輪修好下載失敗時 `wget -q` 吞掉真正錯誤的問題;第二十四輪修好離線打包區塊裡一處沒擋住 `set -eu` 的漏洞。這一段是互動式前景執行,不需要额外持久化 log 檔案。 |
| debian-installer 本身的安裝過程 | 安裝程式自己的臨時環境 | Debian 官方機制:裝完開機後留一份在目標系統的 `/var/log/installer/`(不是 `/var/log/syslog`——第二十三輪查過官方手冊確認) | 這是 d-i 自己的機制,不是我們寫的,只需要知道去哪裡看(已經在文件/motd 訊息裡更新過)。 |
| `late-command.sh`(核心品牌化/裝機步驟) | 目標系統的 in-target chroot | 第二十三輪起:整支腳本的 stdout/stderr 明確導向固定檔案 `/var/log/gonas-late-command.log`(不再依賴 d-i 自己的間接機制) | 每一步都用 `log()` 印一行,失敗的地方都有對應的 `WARNING:` 訊息說清楚「失敗了會怎樣、使用者可以做什麼」;第二十四輪修好其中一處會讓腳本悄悄提前死掉、連第一行 log 都印不出來的 `set -e` 陷阱。 |
| late_command 整體失敗(複製檔案或 late-command.sh 本身失敗) | 安裝程式環境 → `/etc/motd` | 失敗時在 `/etc/motd` 附加一行提示,指向 `/var/log/gonas-late-command.log`(或備援的 `/var/log/installer/`) | 第二十三輪修好這則提示原本指向一個 Debian 13 上根本不存在的檔案的問題。 |
| gonasd / ssh / sudo 等開機後的系統服務 | 已開機的正常系統 | 標準 journald(`journalctl -u gonas`/`journalctl -u ssh` 等) | 標準系統服務,不是我們自己的機制,行為跟任何 systemd 服務一樣,沒有特別要處理的地方。 |

結論:目前每一段都有明確的 log 去處,而且每一段「失敗了會怎樣」都有
訊息說清楚——這幾輪抓到的問題,與其說是「log 沒處理好」,不如說是
「log 指的地方不對」(第二十三輪)或「腳本在印出 log 之前就已經死了」
(第二十四輪),兩者都已經修好。

## 完成之後

把這份清單裡實際測出來的問題(尤其是「加了某項 systemd 加固導致
XX 功能壞掉」「大型映像檔安裝真的逾時了」這類結論)整理回
`build/systemd/gonas.service`、`cmd/gonasd/main.go` 的相關註解,或視
情況開一個新的 Phase(例如「Phase 10:非同步應用安裝」)——這份文件
本身也應該跟著更新,把已經驗證過的項目標記起來,而不是每次都從頭
測一遍。
