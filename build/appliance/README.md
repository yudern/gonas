# GoNAS 開機即用映像檔(appliance ISO)

這個目錄產生的東西：一份可以燒到 USB/光碟的 ISO,插上一台空機器開機、
選幾個基本問題(或完全自動)之後,重開機看到的就是「這是一台 GoNAS」
——不是「這是一台裝了 GoNAS 軟體的 Debian」。底層仍然是標準的 Debian
GNU/Linux(這樣才能繼續吃到 Debian 的安全更新、硬體支援跟套件生態),
但開機畫面、tty1 主控台、hostname、`/etc/os-release` 的品牌名稱全部
換成 GoNAS,一般使用者完全不需要知道底下是 Debian。

這**不是**取代既有的「軟體版」安裝路徑(`build/install.sh` + 下載
release tarball,見專案根目錄 README.md 的安裝說明)——兩條路徑並存,
差別只在使用者從哪裡開始:

| | 軟體版(既有,不受影響) | 映像檔版(這個目錄) |
|---|---|---|
| 使用者已經有一台 Debian/相容發行版 | 直接下載 release tarball 執行 `./install.sh` | 不適用 |
| 使用者是一台全新/空機器 | 得自己先裝好 Debian,再跑 `install.sh` | 燒錄映像檔開機,自動裝好 Debian + gonasd |
| gonasd 本體的安裝方式 | `build/install.sh` | 同一份 `build/install.sh`,由 `late-command.sh` 呼叫 |
| 選用相依套件(mergerfs/snapraid/samba/docker.io/…) | 使用者自行安裝,Doctor 頁面會提示 | 同左,映像檔**不會**在安裝過程自動裝這些 |

兩條路徑最終裝出來的 gonasd 是完全一樣的東西,用的是同一份
`install.sh`,只是「誰、什麼時候執行它」不同。

## !!! 這個目錄裡的東西在目前這個開發沙盒裡完全沒有執行過 !!!

這不是保守的免責聲明,是實際測試過的結果,證據如下:

```
$ curl -m 8 https://deb.debian.org/debian/dists/stable/Release
curl: (56) CONNECT tunnel failed, response 403

$ sudo apt-get update
Err:1 http://archive.ubuntu.com/ubuntu noble InRelease
  403  Forbidden
Err:2 http://security.ubuntu.com/ubuntu noble-security InRelease
  403  Forbidden
Err:3 https://download.docker.com/linux/ubuntu noble InRelease
  403  Forbidden
```

也就是說,連這個沙盒自己的 Ubuntu 24.04 都沒辦法 `apt-get update`,
更不用說下載 Debian netinst ISO、安裝 `xorriso`/`qemu-system-x86_64`
這些工具了(查過 `/root/.ccr/README.md` 跟代理狀態端點,允許清單裡
完全沒有任何 OS 套件鏡像)。因此:

- `build-iso.sh` 從來沒有真的下載過 Debian ISO、沒有真的跑過
  `xorriso -osirrox`/`xorriso ... replay`。
- `preseed.cfg` 從來沒有被真正的 debian-installer 讀取、解析過。
- `late-command.sh` 從來沒有在真正裝好的 Debian chroot 環境裡執行過。
- `gonas-console` 這支 shell script**有**在這個沙盒裡直接跑過兩次
  (不是透過 ISO 開機,只是單獨執行這支 shell script 驗證邏輯):一次
  在 PATH 上完全沒有 `gonasd` 時,正確顯示版本「unknown」跟「尚未偵測
  到網路連線」;一次在 PATH 上有一個真的編譯出來的 `gonasd`(用
  `-ldflags -X .../version.Version=dev` 建置)時,正確透過
  `gonasd -version` 抓到版本字串「dev」並顯示出來。這是唯一「有被
  實際跑過」的部分,其餘全部是依照 Debian 官方文件(Installation
  Guide 附錄 B 的 preseed 語法、Debian wiki「RepackBootableISO」的
  ISO 重新包裝手法)撰寫、邏輯上自認為正確,但**沒有**端到端驗證過。

所有 shell scripts 都用 `sh -n <file>` 做過 POSIX 語法檢查、全部通過,
但語法正確不等於行為正確——尤其是 `preseed.cfg` 的欄位名稱/語法
(debconf 的 owner/type/value 三段式)跟 `late-command.sh` 的
`in-target` 執行環境假設,都需要真正的 debian-installer 才能驗證。

## 在真正動手做之前,你需要什麼

一台(或一份雲端)有真正網路連線、可以安裝套件的 Linux 機器,以及:

```
sudo apt install xorriso wget
```

(`build-iso.sh` 開頭也會檢查這兩個工具存不存在,沒有的話會直接報錯
退出。)

## 怎麼建置

```
# 在 repo 根目錄
make iso                # 兩個架構都做(建議用這個)
make iso-amd64          # 只做 x86_64
make iso-arm64          # 只做 arm64(樹莓派 4/5、多數 SBC)

# 或者不透過 Makefile,直接呼叫(等價於上面):
build/appliance/build-iso.sh amd64 1.2.3
build/appliance/build-iso.sh arm64 1.2.3
```

**注意一個容易踩到的坑**:`iso-amd64`/`iso-arm64` 都依賴 `release`
這個 target,而 `release` 一開始會 `rm -rf dist/release`——如果你是
分兩次、各自獨立的 `make` 指令執行(例如先 `make iso-amd64`,隔一段
時間後才另外執行 `make iso-arm64`),第二次執行時 `release` 會把
第一次產出、放在同一個 `dist/release/` 目錄底下的 amd64 ISO 一併
清掉。在**同一次** `make iso` 呼叫裡兩個架構都做,`release` 只會真的
執行一次,不會有這個問題;如果你就是想分開跑,記得先把上一次的 ISO
搬到別的地方保存,或直接兩個都跑 `make iso`。

`VERSION` 沒指定的話會用 `git describe`(即使一開始這份文件的舊版本
寫過「這個 repo 沒有 `.git`」,實際上這個專案在 commit 歷史上是有 git
版本控制的,`git describe` 會拿到真正的版本字串,不會落到 `dev` 這個
後備值)。輸出在 `dist/release/gonas-<version>-<arch>.iso` 以及對應的
`.sha256`。下載回來的官方 Debian ISO 會快取在 `dist/.cache/`(以雜湊
值判斷是否還能重用,不是單純看檔名/時間),重複執行不用每次都重新
下載幾百 MB;`dist/` 整個目錄已經在 `.gitignore` 裡,快取不會被誤
commit 進版本控制。實際要抓哪個檔名,是每次建置當下直接從 Debian
官方的 `SHA256SUMS` 清單裡找出來的(見下面「已知的設計限制」一節
第十八輪覆閱那一條的完整說明),不是寫死在腳本裡——所以不管 Debian
之後出新的 point release(例如 12.11.0 -> 12.12.0)還是換到下一個
穩定版代號,同一個架構下舊版本號的快取檔案都會在下一次成功建置時
自動被清掉,不會一直留著佔磁碟空間(只清「同架構、版本號不是目前這個」
的檔案,不會動到另一個架構的快取,分開跑 `make iso-amd64`/`iso-arm64`
兩次也不會互相清掉對方)。

**在跑真正的建置之前,建議先跑兩支快速、完全不需要網路的離線檢查**:

```
sh build/appliance/test-boot-menu-patch.sh
sh build/appliance/test-gpg-verify.sh
```

第一支驗證「開機選單參數注入」這一小段邏輯本身(用假的 isolinux/grub
設定檔測試);第二支驗證「判斷 GPG 簽章驗不驗得過」這段邏輯的控制
流程(用假的 `gpg` 執行檔測試,見下面「已知的設計限制」一節裡 GPG
驗證那一條的完整說明)。兩支都是幾秒鐘跑完,不會碰到網路、xorriso、
真正的 Debian ISO——不能取代下面「如何驗證」一節真正的 QEMU/
VirtualBox 端到端測試,但可以在完整建置(需要下載幾百 MB 的官方 ISO、
跑 xorriso)之前,先確認這幾段最容易因為 Debian 版本格式變動、或
gpg 版本差異而壞掉的邏輯還是好的。

整個流程做的事(細節見 `build-iso.sh` 裡逐段的中文註解):

1. 確認/建置 `dist/release/gonas-<version>-linux-<arch>.tar.gz`(沒有
   就自動跑 `make release`——這一步是純 Go 交叉編譯,在任何機器上都
   不需要網路)。
2. 抓官方發布的 `SHA256SUMS` 清單,從裡面實際找出符合
   `debian-<版本號>-<arch>-netinst.iso` 格式的那一行(檔名跟版本號
   都是當下從官方清單讀出來的,不是腳本自己猜或寫死的,見下面「已知
   的設計限制」一節第十八輪覆閱那一條),從 `dist/.cache/` 找有沒有
   雜湊值還對得上的快取檔案可以直接重用;沒有的話從
   `https://cdimage.debian.org/...` 下載官方 netinst ISO(需要網路;
   鏡像位置可用 `GONAS_DEBIAN_ISO_URL` 環境變數覆寫,例如想固定用某個
   已經封存的舊版本,可以指到 `cdimage.debian.org` 底下對應的 archive
   路徑),下載完比對雜湊值,不一致就直接中止、不繼續往下做。
3. 用 `xorriso -osirrox` 解開原始 ISO。
4. 把 gonasd release tarball、`preseed.cfg`、`late-command.sh`、
   `overlay/` 目錄整份塞進解開的目錄樹裡的 `gonas/` 子目錄。
5. 修改開機選單設定檔(isolinux/grub),自動帶入
   `auto=true priority=high preseed/file=/cdrom/gonas/preseed.cfg`
   等核心參數,讓安裝程式一開機就自動套用 preseed,不需要手動在選單
   按 Enter/輸入指令(用 `priority=high` 而不是更激進的
   `critical`,是為了讓 `preseed.cfg` 沒有涵蓋到、或刻意留白的
   高優先權問題——尤其是磁碟分割的最終確認——仍然有機會真的顯示
   出來,而不是被 debconf 用預設值悄悄帶過);順便把看得到的
   「Debian GNU/Linux installer」字樣換成「GoNAS Installer」。
6. 重新計算 `md5sum.txt`,用 `xorriso -indev ... -outdev ... -map ...
   -boot_image any replay` 重新包裝成一份新的、一樣可開機的 ISO
   (沿用原始 ISO 的 El Torito/isohybrid 開機目錄結構,這是 Debian
   wiki 建議的標準做法)。

## 如何驗證(在燒到真實硬體之前,務必先做這一步)

**第一步一定是在虛擬機裡開機測試,不要直接燒到真機。** 一台 NAS
機器的磁碟通常裝著使用者真正的資料陣列,`preseed.cfg` 裡的磁碟分割
故意保留了最後一道「真的要清空這顆碟嗎」的確認畫面(見該檔案裡
`partman-md/confirm`/`partman/confirm_nooverwrite` 兩行為什麼故意
維持註解狀態的說明),但在流程本身還沒驗證過之前,唯一安全的作法
就是先在虛擬機裡完整跑一次。

```
# x86_64,分配 2GB 記憶體,掛上剛做好的 ISO 開機:
qemu-system-x86_64 \
    -m 2048 \
    -cdrom dist/release/gonas-1.2.3-amd64.iso \
    -boot d \
    -drive file=gonas-test-disk.img,format=qcow2,if=virtio \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0

# 先建一顆空白測試磁碟(如果還沒有的話):
qemu-img create -f qcow2 gonas-test-disk.img 20G
```

（arm64 映像檔要用 `qemu-system-aarch64` 搭配對應的 `-machine
virt -cpu cortex-a57` 之類的參數,還需要 UEFI 韌體
`edk2-aarch64-code.fd`——實際參數請對照當時可用的 QEMU 版本文件,
這裡不寫死,因為這部分完全沒有在這個沙盒裡測試過,寫死的參數可能
是錯的。）

要確認的事情,按順序:

1. **開機選單有沒有自動套用 preseed** —— 開機後應該完全不用手動按
   任何鍵,安裝程式自己跑完語系/網路/套件安裝,中途會在下面兩種情況
   停下來要求人工確認,這兩種都是刻意保留的,不是 bug:(a)如果這台
   機器(或這個 VM)掛了不只一顆磁碟,guided partitioning 一開始就會
   先問「要對哪一顆磁碟分割」(`preseed.cfg` 故意沒有設定
   `partman-auto/disk`,見該檔案裡的說明);(b)不管幾顆磁碟,選定
   之後最後都會停在「真的要把這個分割配置寫入磁碟嗎」的確認畫面
   (`partman/confirm`/`partman/confirm_nooverwrite` 兩行故意維持
   註解狀態)。如果安裝程式在**這兩種情況以外**的地方停下來問問題
   (語系、鍵盤、使用者帳號之類),代表 `preseed.cfg` 裡某個欄位的
   owner/type/value 寫錯了,或者剛好撞上一個這份 preseed 沒有預期到
   的 debconf 問題——debian-installer 通常會直接顯示是哪個問題在問,
   對照 `preseed.cfg` 修正,修正後也記得把這裡的「预期會停下來的
   地方」清單一併更新。另外如果是在真實硬體(不是 VM)上測,額外
   留意有沒有跳出「缺少韌體,請插入另一份媒體」的畫面——這是
   `d-i hw-detect/load_firmware boolean false` 沒有完全生效的跡象
   (理論上這樣設定就不該再問這一題),VM 用 virtio 裝置通常不會
   遇到這個問題,所以這一項在真機上比在 VM 上更需要特別留意。
2. **安裝完重開機後,tty1 是不是狀態畫面而不是登入提示** —— 應該會
   看到 ASCII art「GONAS」字樣、版本號、一個或多個 `http://<ip>:8291`
   的網址(要看虛擬機的網路是不是有正確拿到 DHCP 位址)。如果還是
   看到標準的 `gonas login:` 提示,代表 `late-command.sh` 裡
   `systemctl disable/mask getty@tty1.service` 或
   `systemctl enable gonas-console.service` 沒有成功執行——先用
   Ctrl+Alt+F2 切到 tty2(應該還是正常的登入提示,用 `gonasadmin` /
   `gonas-change-me-now` 登入)進去看 `systemctl status
   gonas-console.service` 跟 `journalctl -u gonas-console` 找原因。
3. **瀏覽器能不能連到 Web 介面** —— 在 host 機器(或另一台虛擬機)
   瀏覽器打開 tty1 顯示的網址,應該會看到 GoNAS 的首次設定畫面。
   建議額外測一次「開機當下沒有網路」的情境(QEMU 的話開機時先不接
   `-netdev`,或實體機先不插網路線):tty1 應該要**立刻**顯示畫面
   (內容是「尚未偵測到網路連線」),而不是空白畫面等半天——如果是
   黑畫面卡住不動,代表 `gonas-console.service` 的開機排序又不小心
   被某個網路相關的 target 卡住了(這正是這一輪覆閱修掉的問題,見
   `docs/REAL_HARDWARE_TESTING.md` Phase 19 段落的「第四輪覆閱」)。
4. **`gonasd` 是不是真的用 systemd 常駐、而且開機自動啟動有沒有生效**
   —— tty2 登入後 `systemctl status gonas`(unit 名稱是 `gonas`,不是
   `gonasd`),應該是 `active (running)`;另外務必額外確認
   `systemctl is-enabled gonas`回傳 `enabled` —— 這一項特別重要:
   `install.sh` 自己判斷「要不要做 systemd 整合」的邏輯是看
   `/run/systemd/system` 存不存在,而 late-command.sh 執行 install.sh
   當下(debian-installer 的 in-target chroot 環境)這個目錄通常不
   存在,所以 install.sh 那時候會直接跳過整段 systemd 整合(包含
   `enable`)。`late-command.sh` 已經另外補了一步明確
   `systemctl enable gonas.service`來補上這個缺口,但這一步同樣完全
   沒有真正驗證過是否在真實的 in-target chroot 環境裡如預期般成功
   ——如果重開機後 `systemctl is-enabled gonas` 顯示 `disabled`,
   代表這個補救步驟在你的 Debian 版本裡沒有如預期生效,需要另外
   排查(先手動跑 `systemctl enable gonas.service` 確認至少能事後
   補救,再回頭看 late-command.sh 的執行 log,通常在
   `/var/log/syslog` 或 `journalctl -b -1` 裡找 late_command 相關的
   輸出)。
5. **品牌化有沒有生效** —— tty2 登入後檢查 `hostnamectl`(應顯示
   `gonas`)、`cat /etc/motd`、`cat /etc/os-release` 的 `PRETTY_NAME`、
   重開機時 GRUB 選單標題。
6. **確認 tty2 以後還是正常的 shell 登入** —— 這是刻意保留的「進階
   疑難排解」管道,不應該被 tty1 的改動影響到。

全部通過之後,才考慮用 `dd`/Rufus/balenaEtcher 之類的工具把 ISO
寫到實體 USB,插到真正要當 NAS 用的機器上開機——第一次在真機上做,
建議先拔掉/斷開任何已經有資料的磁碟,只留下要裝系統的那顆全新硬碟,
避免 preseed 的分割步驟意外選到錯誤的磁碟。

## 安全性提醒:第一次開機後,誰先連到誰先贏

這一點不是這個 appliance 特有的 bug,兩條安裝路徑(軟體版跟這個映像檔
版)都一樣,但因為映像檔版開機後會主動在 tty1 秀出「請用瀏覽器連到
`http://<ip>:8291`」,更容易讓人直覺以為「開機接上網路」這個動作本身
是安全的,所以特別在這裡提醒一次:

- `gonasd` 預設用**純 HTTP**監聽(`internal/config.ListenAddr` 預設
  `:8291`,不是 HTTPS),`HTTPS` 是登入之後才能在設定頁面手動開啟、
  而且要重啟 daemon 才生效——也就是說,從機器開機到你設定完第一個
  管理者帳號、登入、打開 HTTPS 這整段期間,所有流量(包含你剛剛設定
  的管理者密碼本身)都是明文,同一個網段上能側錄流量的人看得到。
- 建立第一個管理者帳號這支 API(`/api/v1/auth/setup`)在完全沒有任何
  帳號存在時任何人都能呼叫,成功之後直接核發登入 session——程式碼
  層面已經確認過,兩個人同時呼叫也只會有一個人真的建立成功(用
  `sync.Mutex` 序列化,不會有競態條件建出兩個帳號或資料損毀),但
  「兩個人裡面誰先送出這個請求」本身沒有,也沒辦法用軟體邏輯去
  保證是你——機器一開機、接上網路、能被瀏覽器連到那一刻起,同網段上
  第一個連進去、填完使用者名稱密碼的人,就會拿到這台機器唯一的
  管理者帳號。
- 實際建議:**第一次開機設定,盡量讓機器接在一個只有你自己、或你
  信任的人能連進來的網路上**(例如直接一條網路線接自己的筆電、或
  一個獨立/隔離的 VLAN),盡快完成「設定管理員帳號 → 登入 → 開啟
  HTTPS → 重啟 daemon」這一串流程,再把機器接回一般的家用/辦公室
  網路。這跟大多數家用路由器、NAS 產品「第一次設定建議先接
  電腦直連」的慣例是同一種考量,不是 GoNAS 特有的額外負擔。

## 已知的設計限制/尚未做的事

- 只支援 Debian netinst 映像,沒有做 Ubuntu Server 或其他發行版的
  版本。抓的是 `debian-cd/current/` 這個永遠指向「目前最新穩定版」的
  路徑,沒有自己選代號/版本這回事(想固定用某個已經封存的舊版本,
  改設定 `GONAS_DEBIAN_ISO_URL` 指到對應的 archive 路徑)。
- **`build-iso.sh` 原本假設 Debian netinst ISO 的檔名是用版本代號組
  出來的(`debian-bookworm-<arch>-netinst.iso`),這個假設從一開始
  就是錯的,而且是第十八輪覆閱——使用者第一次真的在自己的機器上執行
  這支腳本——才抓到的**:這個開發沙盒完全連不上
  `cdimage.debian.org`,前 17 輪不管做多仔細的靜態審查都沒有辦法
  發現這個問題,因為問題本身要真的發一個 `wget` 請求出去、看官方
  伺服器實際回應什麼東西才會現形。真正的情況是:
  `debian-cd/current/<arch>/iso-cd/` 底下的檔名用的是完整版本號(例如
  `debian-13.6.0-arm64-netinst.iso`),不是版本代號(`bookworm`/
  `trixie` 這種名字只用在 APT 套件庫路徑,是完全不同的命名慣例)——
  照原本的邏輯,不管 Debian 現在的穩定版是哪一個代號,腳本组出來的
  檔名永遠不會出現在真正的 `SHA256SUMS` 清單裡,`make iso-amd64`/
  `iso-arm64` 因此**從第一天開始就不可能成功執行**,只是這個開發
  沙盒完全沒有網路去實際驗證這件事,所以一直沒有人發現。已修正:
  不再自己組一個「猜測的」檔名,改成先把 `SHA256SUMS` 抓下來,直接
  從裡面找出符合 `debian-<版本號>-<arch>-netinst.iso` 格式的那一行,
  檔名跟版本號都來自 Debian 當下真正發布的內容——這樣不管 Debian
  之後從 trixie 換到下一個代號、或同一個穩定版又出新的 point
  release,都不需要回來改這支腳本,原本的 `GONAS_DEBIAN_RELEASE`
  環境變數也因此整個拿掉(它原本要選的東西,`current/` 這個路徑本來
  就只會有一份,沒有代號可選)。這個 bug 目前只在文件層面驗證過邏輯
  (讀真正的 Debian 鏡像站目錄列表確認檔名格式),還沒有被使用者
  實際重跑一次 `make iso-arm64` 確認修好,見
  `docs/REAL_HARDWARE_TESTING.md` 第十八輪的記錄。
- 安裝過程完全離線(只吃光碟/USB 媒體本身內附的套件),所以不會在
  裝機時自動安裝 mergerfs/snapraid/samba/docker.io/nfs-common/
  wireguard-tools/rsync 這些 GoNAS 的「選用」外部相依套件——開機、
  機器有網路之後,透過 Web 介面的 Doctor 頁面(或手動 `apt install`)
  補裝,效果跟軟體版安裝路徑完全一樣。
- 沒有做 Secure Boot 簽章相關處理,規劃上假設目標機器的韌體允許
  一般(非簽章)開機或已關閉 Secure Boot。
- **這個 appliance「開機直接看到 GoNAS 品牌畫面」的整套體驗,是建立在
  目標機器有接一般的 VGA/HDMI 螢幕 + 鍵盤(也就是傳統意義上的
  tty1)的假設上**——`gonas-console.service` 換掉的是 `tty1`(虛擬
  主控台,綁在顯示卡輸出上),完全沒有另外設定序列埠主控台
  (`serial-getty@ttyS0.service` 之類)。如果目標硬體是那種沒有一般
  螢幕輸出、只能用序列埠主控台(常見於某些 mini PC、專用伺服器/NAS
  硬體、或透過 IPMI/BMC 遠端主控台)連線的機器,你完全不會看到 tty1
  那個品牌畫面,也不會有等效的序列埠版本可以看——機器本身還是會正常
  開機、gonasd 還是會正常啟動並監聽網路,只是「開機後立刻知道要連
  到哪個網址」這個體驗完全靠不上,你需要用別的方式找到這台機器的 IP
  (例如看你的路由器/DHCP 伺服器的用戶端清單)。這是覆閱時發現的一個
  文件缺口,不是程式碼 bug——要真的支援序列埠主控台需要另外做一份
  serial-getty 版的品牌畫面服務,目前沒有排進這個 Phase 的範圍,如果
  你的目標硬體屬於這種情況,先用這個方式找到 IP,之後品牌畫面能不能
  用不影響機器本身能不能正常使用。
- 只用 `d-i hw-detect/load_firmware boolean false` 明確告訴安裝程式
  「不用等額外的韌體媒體」,不代表真的解決了缺韌體的問題——如果這台
  機器的網卡/儲存控制器需要非自由韌體才能動作,結果只是安裝程式不會
  卡住等待,但那個裝置本身可能還是用不了。這在 QEMU/VirtualBox 的
  virtio 裝置上不會遇到,是真實硬體上才需要留意的落差,見上面「如何
  驗證」第 1 點。
- 機器上有不只一顆磁碟時,guided partitioning 會停下來問「要對哪一顆
  磁碟分割」(這是刻意的,見 `preseed.cfg` 的說明,不是遺漏),所以
  嚴格來說這不是一份「完全零互動、從開機到裝完中間不用碰鍵盤」的
  preseed——多碟機器上至少會停兩次:選磁碟、確認寫入。單碟機器
  (或只掛一顆測試碟的 VM)只會停在確認寫入那一次。
- `build-iso.sh` 預設會比對下載回來的官方 ISO 跟 Debian 發布的
  `SHA256SUMS` 是否一致,雜湊不符就直接中止(避免在一份損毀或被
  竄改的 ISO 上繼續動作卻完全沒有任何錯誤訊息)——但這只驗證
  「完整性」,不是「真實性」(`SHA256SUMS` 本身有沒有被偽造)。第十輪
  覆閱補上了可選的 GPG 簽章驗證:自己照
  https://www.debian.org/CD/verify 官方說明匯入 Debian 的簽章金鑰,
  **務必用 `gpg --export <key-id> > my.keyring`(不要加 `-a`/
  `--armor`)匯出成 binary 格式**——這是實測時真的踩到的坑:如果匯出
  成 ASCII armor 格式(很多官方文件範例習慣加 `-a` 方便用文字編輯器
  查看),`--keyring` 讀到會直接報 `invalid packet`/`No public key`,
  即使金鑰內容本身完全正確也一樣,armor 格式跟 `--keyring` 要的 binary
  格式對 gpg 來說是兩種不同的檔案格式,不能直接互換。準備好 binary
  格式的 keyring 之後,設定環境變數
  `GONAS_DEBIAN_KEYRING=/path/to/your.keyring`(**要絕對路徑**——這裡
  也是實測抓到的坑:`gpg --keyring` 對相對路徑的解讀方式不是相對於
  目前的工作目錄,而是相對於 gpg 自己的 homedir,兩者常常不是同一個
  地方;`build-iso.sh` 現在會自動把你給的路徑轉成絕對路徑,不管你給
  的是相對還是絕對路徑都不受影響,這裡只是說明背後的原因)再執行
  `build-iso.sh`(或 `make iso-amd64`/`iso-arm64`),就會自動多做這一層
  驗證,失敗直接
  中止建置。**這裡刻意不是腳本自己去某個網址下載金鑰**——金鑰的取得
  管道應該獨立於這支下載腳本本身,不然信任鏈繞了一圈又繞回同一個
  下載來源,沒有真的增加安全性,所以金鑰檔案要由你自己準備好。判斷
  「gpg 說的算不算真的驗證通過」這段邏輯本身(`lib/verify-gpg-
  signature.sh`)不只用假的 `gpg` 執行檔測過控制流程(`sh
  build/appliance/test-gpg-verify.sh`,不需要網路),也已經在這個沙盒
  裡當場產生一把真的測試用 GPG 金鑰、簽一份測試資料,實際走過一次
  完整的「驗證通過」跟「資料被竄改後驗證正確失敗」兩種情境(過程中
  就是這樣抓到上面「keyring 要用 binary 格式匯出」跟「路徑要轉絕對
  路徑」這兩個問題的)。唯一還沒驗證過的,是「用 Debian 真正的官方
  簽章金鑰驗證一份真正的官方 `SHA256SUMS.sign`」這件事本身,那需要
  連得上網路取得 Debian 的官方金鑰/簽章檔案,這個開發沙盒完全沒辦法
  做,需要你自己在真正建置的時候第一次碰到真正的 Debian 簽章資料。
  不想用這一層的話什麼都不用做,預設行為(只做 checksum)不變。
  **第十五輪覆閱另外抓到一個問題**:判斷「驗證通過」的邏輯是 grep
  gpg 輸出裡有沒有出現英文的 `Good signature` 字串,但 gpg 這句訊息
  是會被 gettext 翻譯的——如果你自己建置這支腳本的機器語系不是英文
  (而且裝了對應的 gnupg 翻譯包),真正的 gpg 印出來的會是翻譯過的
  字串(例如德文環境會是「Korrekte Signatur von ...」),`grep` 永遠
  不會命中,即使金鑰跟簽章完全正確,也會被誤判成「驗證失敗」而中止
  建置——對一個特地設定 `GONAS_DEBIAN_KEYRING`、想多做這一層驗證的
  使用者來說,會是一個完全摸不著頭緒、看起來像金鑰有問題但其實只是
  機器語系不是英文的假錯誤。已經修正:呼叫 `gpg` 之前明確把
  `LC_ALL`/`LANGUAGE` 都釘死成 `C`,強制輸出英文訊息,不受你機器本身
  的語系設定影響,`test-gpg-verify.sh` 也補了一個對應的案例(用一個
  會檢查收到的語系變數的假 `gpg` 驗證這個強制設定真的生效)。這個
  開發沙盒只裝了 C/POSIX 這幾種語系,沒辦法直接裝一個有翻譯包的語系
  重現症狀本身,但 gnupg 訊息會被翻譯這件事本身是有文件可查的既有
  行為,不是憑空猜測。
- **`build-iso.sh`(以及它會用到的 `lib/patch-boot-menu.sh`)現在同時
  支援在 Linux 跟 macOS 上執行,不再假設建置這支腳本的機器一定是
  Linux**——第十七輪覆閱是因為使用者實際要在自己的 Mac mini 上建置才
  發現的:`sed -i`(不接參數的 GNU 寫法)在 macOS 內建的 BSD sed 底下
  是完全不同的語法,直接照 Linux 寫法呼叫會讓整條指令的參數解讀錯位;
  `sha256sum`/`md5sum` 這兩個 GNU coreutils 指令在 stock macOS 上根本
  不存在。已修正:新增 `lib/portable-sed.sh` 的 `gonas_sed_inplace()`
  (統一用 `-i.gonas-sed-bak` 這種兩邊都合法的寫法呼叫 `sed`,再手動
  清掉備份檔)跟 `lib/portable-checksum.sh` 的
  `gonas_sha256sum()`/`gonas_md5sum()`(GNU 工具不存在時 fallback 到
  macOS 原生的 `shasum -a 256`/`md5 -r`),`build-iso.sh` 跟
  `lib/patch-boot-menu.sh` 裡所有原本直接呼叫 GNU 工具的地方全部改用
  這兩個函式。`late-command.sh` 因為是在真正的 Debian in-target
  chroot(永遠是 Linux)裡執行,不受影響、不需要修改。這兩個新函式各自
  都有離線回歸測試(`test-portable-sed.sh`/`test-portable-checksum.sh`
  ),CI 也新增了一個 `macos-latest` 的 job 實際在真正的 BSD 工具鏈底下
  跑這些測試,不是只在 Linux runner 上跑過就算數。在 macOS 上建置的
  詳細步驟(Homebrew 裝哪些套件、Apple Silicon 跟 Intel Mac 分別要注意
  什麼)見 `docs/APPLIANCE_BUILD_AND_TEST_PROCEDURE.md`。
- `preseed.cfg` 裡 `d-i pkgsel/update-policy select none` 關掉的是
  「安裝過程順便設定 unattended-upgrades 自動背景更新」這個選項,不是
  真的關掉更新能力——開機之後機器有網路,手動 `apt update && apt
  upgrade` 一樣能拿到 Debian 的安全更新,只是預設不會自動背景執行,
  跟上面「這不是取代既有安裝路徑」一節說的「底層仍然是標準 Debian,
  能繼續吃到安全更新」講的是「有能力吃到」,不是「會自動吃到」——
  這個差異值得說清楚,避免使用者誤以為裝了這份映像檔就等於有自動
  安全更新機制。如果你的使用情境需要自動安全更新,開機、機器連上
  網路之後自己 `apt install unattended-upgrades` 並依 Debian 官方文件
  設定即可,跟軟體版安裝路徑上的既有 Debian 機器完全一樣的做法。
- `gonasadmin` 這組 Unix 帳號的預設密碼寫死在 `preseed.cfg` 裡
  (`gonas-change-me-now`),純粹是給「緊急 SSH/主控台除錯」用的
  備援管道,跟 GoNAS 自己的 Web 介面帳號系統完全無關(見
  `internal/state.AdminAccount`)。`late-command.sh` 會用
  `chage -d 0 gonasadmin` 把這組密碼標記成已過期,第一次登入(不管是
  SSH 還是 tty2)都會被強制要求先設一組新密碼才能拿到 shell——這一步
  是重新覆閱時額外補上的,不是只在文件裡提醒使用者自己記得改,但這個
  強制機制本身也還沒有實際驗證過(需要確認 SSH 客戶端在互動式連線下
  真的會正確跳出「密碼已過期,請設定新密碼」的提示,而不是連線失敗)。
  如果你的使用情境更看重免密碼、直接用 SSH 公鑰登入,還是建議自己
  另外佈署公鑰、關閉密碼登入。
- 到目前為止,在這個開發沙盒裡真正執行驗證過的部分有兩塊:
  `overlay/usr/local/sbin/gonas-console` 這支 shell script 本身的
  邏輯(見上面「這個目錄裡的東西在目前這個開發沙盒裡完全沒有執行
  過」一節);以及開機選單參數注入的邏輯,已經獨立成
  `lib/patch-boot-menu.sh`,並且有一支固定下來、可重複執行的離線
  回歸測試 `test-boot-menu-patch.sh`(不需要網路,任何有 `/bin/sh`
  的機器都能跑:`sh build/appliance/test-boot-menu-patch.sh`)。這支
  測試本身在建置這幾份腳本的過程中就抓到兩個真的存在、光靠人工覆閱
  兩輪都沒發現的 bug:(1)grub.cfg 的 `---` 分隔字元後面常常還接著
  `quiet` 這類參數,不是行尾,原本假設行尾的注入規則會完全沒命中;
  (2)拿來判斷「這一行是不是 grub 的 linux 開機參數列」的 grep guard
  原本寫成比對字面空白鍵,但真實的 grub.cfg 是用 tab 字元分隔
  `linux` 跟核心路徑的,guard 判斷為「不是」,底下真正做注入的 sed
  就整段被跳過,結果是開機參數完全沒被修改卻沒有任何錯誤訊息——這
  兩個都已經修好,而且用 5 種仿真格式的測試資料驗證過,見
  `test-boot-menu-patch.sh` 檔案開頭的完整說明跟 `docs/
  REAL_HARDWARE_TESTING.md` 的「第六輪覆閱」段落。除了這兩塊,ISO
  建置、preseed 自動安裝、late-command 品牌化的其餘部分,全部等待
  使用者在有網路的機器上建置、並在虛擬機裡開機測試後才算數。
- **`late-command.sh`/`install.sh` 這兩個「直接被當成執行檔呼叫」的
  進入點,原本依賴 ISO 上的執行位元有沒有活著留下來——第十三輪覆閱
  抓到 `build-iso.sh` 忘記把 `lib/` 目錄塞進 ISO 那個 bug之後,第
  十四輪回頭多想一步,發現另一個同一類、還沒被驗證過的風險**:
  `build-iso.sh` 是先載入官方 base.iso、再用 xorriso 的 `-map` 疊加
  修改過的目錄樹,新加進去的檔案(`late-command.sh`、
  `release-$ARCH/install.sh`)的 Unix 執行位元,能不能正確透過 Rock
  Ridge 擴充屬性保留到最終的 ISO 9660 檔案系統上,取決於 xorriso 的
  行為細節,這個開發沙盒裝不了 xorriso,完全沒辦法實際驗證。與其賭
  這個假設一定成立(賭錯的症狀會跟前面 `lib/` 沒塞進去那個 bug 幾乎
  一模一樣:機器裝完只是一台陽春 Debian,差別只在失敗訊息從
  「找不到 lib/detect-arch.sh」換成「Permission denied」),第十四輪
  已經把這兩個進入點都改成明確用 `sh 檔案路徑` 執行(`preseed.cfg`
  的 `late_command` 呼叫 `sh /cdrom/gonas/late-command.sh`,
  `late-command.sh` 內部呼叫 `sh ./install.sh`),完全不依賴執行位元
  有沒有被保留下來,只需要檔案讀得到就能跑——這是一個防禦性修正,
  不管 Rock Ridge 屬性實際上有沒有問題都不會有副作用,但真正「執行
  位元到底有沒有被正確保留」這件事本身,到目前為止還是純推導,沒有
  真的建一次 ISO 驗證過。
- **appliance 這條安裝路徑裝完之後,`uninstall.sh` 不會留在系統上**
  ——這是第十四輪從「裝完之後留下什麼給使用者」這個角度回頭檢查才
  發現的落差:「軟體版」安裝路徑的使用者本來就手動下載/解壓縮過
  release tarball,`uninstall.sh` 自然留在自己電腦的某個目錄裡;但
  appliance 這條路徑,release tarball 只存在於安裝媒體上,
  `preseed.cfg` 設定了裝完會退出安裝媒體,原本裝好的系統上完全沒有
  這個檔案。已經修正:`late-command.sh` 現在會在裝完 gonasd 之後,
  順手把 `uninstall.sh` 複製一份到 `/usr/local/share/gonas/
  uninstall.sh`,之後想解除安裝直接
  `sudo /usr/local/share/gonas/uninstall.sh` 就好,不需要重新找回
  當初的安裝媒體。
