# GoNAS 實機測試清單(Phase 9)

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

每一節列出的都是「在沙盒裡已經盡力驗證過設計/邏輯是對的,但沒辦法
證明在真實環境下真的會動」的項目 —— 不是「完全沒做過驗證」的項目。

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

## 3. 儲存(對應 Phase 1)

- [ ] `GET /api/v1/storage/disks` 列出的硬碟跟 `lsblk` 實際輸出比對,
      容量、型號、是否為系統碟等欄位是否正確。
- [ ] SMART 資訊(溫度、健康狀態)跟 `smartctl -a /dev/sdX` 的原始輸出
      比對是否一致。
- [ ] 用兩顆以上的磁碟設定一個 pool(一顆資料碟 + 一顆同位碟),
      `PUT /api/v1/storage/pool`,`POST /api/v1/storage/array/start`,
      確認 mergerFS 真的掛載成功(`mount | grep mergerfs`),寫入檔案
      確認 most-free-space 策略有生效(檔案寫到剩餘空間較多的那顆碟)。
- [ ] 觸發一次 SnapRAID sync,確認同位資訊真的寫進同位碟,`snapraid
      status` 看得到正確的狀態。
- [ ] **模擬硬碟損壞**:陣列停止、直接損毀或抹掉其中一顆資料碟的內容
      (在測試環境裡做,不要在有真實資料的機器上做這步),用
      `snapraid fix` 搭配 GoNAS 產生的設定檔嘗試修復,確認資料真的能
      復原回來。這是整個儲存架構「同位保護到底有沒有用」最關鍵的一次
      驗證,沙盒環境完全沒辦法做這個測試。
- [ ] 陣列運作中重啟 `gonasd`(不是重開機),確認陣列狀態、pool 設定從
      `state.json` 正確載回,不會要求重新設定一次。

## 4. Docker / 應用程式商店(對應 Phase 2,Phase 9.1 已補上部分真實驗證)

Phase 9.1 發現這台沙盒其實有真的 `dockerd` 可以手動啟動,已經用一個
`FROM scratch` 建置、不需要連網就能取得的最小測試映像檔,對著真正的
Docker Engine 完整驗證過:單/多服務自訂安裝、容器與專屬網路的建立、
解除安裝時的清理、ID 碰撞防護、安裝失敗時的 rollback 邏輯(見
README.md 的 Phase 9.1 段落)。下面標記 ✅ 的項目已經這樣驗證過,
不需要在真機上重測;沒標記的項目是這台沙盒的網路政策擋掉了對外部
registry 的存取,依然只能在真機上驗證。

- [x] ✅ `GET /api/v1/docker/ping` 對著真的在跑的 Docker daemon 確認回應
      正常。
- [x] ✅ 安裝/解除安裝一個 app(內建目錄範本或自訂 image 皆可),確認
      容器/網路資源真的被建立/清乾淨,包含多服務 App 的專屬網路。
- [x] ✅ 安裝失敗時(例如某個服務的 image 拉不到)的 rollback:已經
      驗證過會把這次呼叫已建立的容器跟網路清乾淨,不留孤兒容器,也
      不會寫進 `state.json`。
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

## 6. 安全性(對應 Phase 6 與 Phase 9 新增的登入節流)

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

## 7. 備份(對應 Phase 7)

- [ ] 對真實的一批檔案(建議至少幾 GB,包含大檔案跟大量小檔案的混合)
      跑一次備份工作,確認 `rsync -aAX --delete` 真的保留了權限/
      ACL/擴充屬性。
- [ ] 連續跑第二次,用 `stat`/`ls -i` 確認沒有變更的檔案在新舊快照之間
      是同一個 inode(硬連結生效,沒有重複佔用磁碟空間),`du -sh`
      確認整個快照目錄集合的實際磁碟用量遠小於「快照數量 × 單份資料
      大小」。
- [ ] 修改來源的一部分檔案後再跑一次,確認只有變更的檔案被複製,沒變
      的部分還是共用硬連結。
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
