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
make iso-amd64          # 只做 x86_64
make iso-arm64          # 只做 arm64(樹莓派 4/5、多數 SBC)
make iso                # 兩個都做

# 或者不透過 Makefile,直接呼叫(等價於上面):
build/appliance/build-iso.sh amd64 1.2.3
build/appliance/build-iso.sh arm64 1.2.3
```

`VERSION` 沒指定的話會用 `git describe`(這個 repo 目前沒有 `.git`,
所以預設會是 `dev`)。輸出在 `dist/release/gonas-<version>-<arch>.iso`
以及對應的 `.sha256`。

整個流程做的事(細節見 `build-iso.sh` 裡逐段的中文註解):

1. 確認/建置 `dist/release/gonas-<version>-linux-<arch>.tar.gz`(沒有
   就自動跑 `make release`——這一步是純 Go 交叉編譯,在任何機器上都
   不需要網路)。
2. 從 `https://cdimage.debian.org/...` 下載官方 netinst ISO(需要
   網路;鏡像位置可用 `GONAS_DEBIAN_ISO_URL`/`GONAS_DEBIAN_RELEASE`
   環境變數覆寫)。
3. 用 `xorriso -osirrox` 解開原始 ISO。
4. 把 gonasd release tarball、`preseed.cfg`、`late-command.sh`、
   `overlay/` 目錄整份塞進解開的目錄樹裡的 `gonas/` 子目錄。
5. 修改開機選單設定檔(isolinux/grub),自動帶入
   `auto=true priority=critical preseed/file=/cdrom/gonas/preseed.cfg`
   等核心參數,讓安裝程式一開機就自動套用 preseed,不需要手動在選單
   按 Enter/輸入指令;順便把看得到的「Debian GNU/Linux installer」
   字樣換成「GoNAS Installer」。
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
   任何鍵,安裝程式自己跑完語系/網路/磁碟分割/套件安裝,中途只會在
   「真的要寫入磁碟」那一步停下來要求確認(見上面的說明,這是刻意
   保留的)。如果安裝程式停下來問其他問題(語系、鍵盤、使用者帳號
   之類),代表 `preseed.cfg` 裡某個欄位的 owner/type/value 寫錯了,
   debian-installer 通常會直接顯示是哪個 debconf 問題在問,對照
   `preseed.cfg` 修正。
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
- `build-iso.sh` 會比對下載回來的官方 ISO 跟 Debian 發布的
  `SHA256SUMS` 是否一致,雜湊不符就直接中止(避免在一份損毀或被
  竄改的 ISO 上繼續動作卻完全沒有任何錯誤訊息)——但這只驗證
  「完整性」,沒有做 GPG 簽章驗證(`SHA256SUMS.sign`)這一層
  「真實性」檢查,因為那需要腳本執行環境事先匯入 Debian 的官方簽章
  金鑰,這支腳本不假設一定有;如果你的信任層級要求更高,建議自己
  另外對 `SHA256SUMS`/`SHA256SUMS.sign` 做一次 GPG 驗證,見
  https://www.debian.org/CD/verify 。
- `gonasadmin` 這組 Unix 帳號的預設密碼寫死在 `preseed.cfg` 裡
  (`gonas-change-me-now`),純粹是給「緊急 SSH/主控台除錯」用的
  備援管道,跟 GoNAS 自己的 Web 介面帳號系統完全無關(見
  `internal/state.AdminAccount`)——正式使用前務必自行修改這組密碼,
  或考慮改成佈署 SSH 公鑰、關閉密碼登入。
- 到目前為止,唯一在這個開發沙盒裡真正執行驗證過的部分,只有
  `overlay/usr/local/sbin/gonas-console` 這支 shell script 本身的
  邏輯(見上面「這個目錄裡的東西在目前這個開發沙盒裡完全沒有執行
  過」一節)。ISO 建置、preseed 自動安裝、late-command 品牌化,全部
  等待使用者在有網路的機器上建置、並在虛擬機裡開機測試後才算數。
