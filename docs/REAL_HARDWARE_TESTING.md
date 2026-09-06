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

## 完成之後

把這份清單裡實際測出來的問題(尤其是「加了某項 systemd 加固導致
XX 功能壞掉」「大型映像檔安裝真的逾時了」這類結論)整理回
`build/systemd/gonas.service`、`cmd/gonasd/main.go` 的相關註解,或視
情況開一個新的 Phase(例如「Phase 10:非同步應用安裝」)——這份文件
本身也應該跟著更新,把已經驗證過的項目標記起來,而不是每次都從頭
測一遍。
