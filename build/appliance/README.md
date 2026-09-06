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
commit 進版本控制。

**在跑真正的建置之前,建議先跑一次快速、完全不需要網路的離線檢查**:

```
sh build/appliance/test-boot-menu-patch.sh
```

這只驗證「開機選單參數注入」這一小段邏輯本身(用假的 isolinux/grub
設定檔測試,幾秒鐘跑完),不會碰到網路、xorriso、真正的 Debian
ISO——不能取代下面「如何驗證」一節真正的 QEMU/VirtualBox 端到端
測試,但可以在完整建置(需要下載幾百 MB 的官方 ISO、跑 xorriso)之前,
先確認這段最容易因為 Debian 版本格式變動而壞掉的邏輯還是好的,尤其是
改過 `build/appliance/lib/patch-boot-menu.sh` 之後,或者換了
`GONAS_DEBIAN_RELEASE` 想升級到不同的 Debian 版本之後。

整個流程做的事(細節見 `build-iso.sh` 裡逐段的中文註解):

1. 確認/建置 `dist/release/gonas-<version>-linux-<arch>.tar.gz`(沒有
   就自動跑 `make release`——這一步是純 Go 交叉編譯,在任何機器上都
   不需要網路)。
2. 抓官方發布的 `SHA256SUMS` 清單,從 `dist/.cache/` 找有沒有雜湊值
   還對得上的快取檔案可以直接重用;沒有的話從
   `https://cdimage.debian.org/...` 下載官方 netinst ISO(需要網路;
   鏡像位置可用 `GONAS_DEBIAN_ISO_URL`/`GONAS_DEBIAN_RELEASE` 環境
   變數覆寫),下載完比對雜湊值,不一致就直接中止、不繼續往下做。
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

## 已知的設計限制/尚未做的事

- 只支援 Debian(bookworm,由 `GONAS_DEBIAN_RELEASE` 環境變數決定)
  netinst 映像,沒有做 Ubuntu Server 或其他發行版的版本。
- 安裝過程完全離線(只吃光碟/USB 媒體本身內附的套件),所以不會在
  裝機時自動安裝 mergerfs/snapraid/samba/docker.io/nfs-common/
  wireguard-tools/rsync 這些 GoNAS 的「選用」外部相依套件——開機、
  機器有網路之後,透過 Web 介面的 Doctor 頁面(或手動 `apt install`)
  補裝,效果跟軟體版安裝路徑完全一樣。
- 沒有做 Secure Boot 簽章相關處理,規劃上假設目標機器的韌體允許
  一般(非簽章)開機或已關閉 Secure Boot。
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
- `build-iso.sh` 會比對下載回來的官方 ISO 跟 Debian 發布的
  `SHA256SUMS` 是否一致,雜湊不符就直接中止(避免在一份損毀或被
  竄改的 ISO 上繼續動作卻完全沒有任何錯誤訊息)——但這只驗證
  「完整性」,沒有做 GPG 簽章驗證(`SHA256SUMS.sign`)這一層
  「真實性」檢查,因為那需要腳本執行環境事先匯入 Debian 的官方簽章
  金鑰,這支腳本不假設一定有;如果你的信任層級要求更高,建議自己
  另外對 `SHA256SUMS`/`SHA256SUMS.sign` 做一次 GPG 驗證,見
  https://www.debian.org/CD/verify 。
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
