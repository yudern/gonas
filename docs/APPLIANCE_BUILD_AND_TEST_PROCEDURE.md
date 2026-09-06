# GoNAS 開機即用映像檔(Phase 19)—— 具體建置 + 虛擬機測試流程

這份文件是給「已經看過 `build/appliance/README.md`,現在要真的動手建置
一次」的人看的操作手冊,把散在 README 各處的資訊整理成一步一步照著做
就好的流程。背景/設計理由/已知限制請看 `build/appliance/README.md`,
這裡只列步驟跟每一步該檢查什麼。

## 為什麼一定要在有網路的機器上做

這整個 `build/appliance/` 目錄下的程式碼,到目前為止只有「開機選單
參數注入」那一小段純字串處理邏輯,跟 tty1 狀態主控台的顯示邏輯,是
真的在開發沙盒裡執行測試過的——下載官方 Debian ISO、用 xorriso 重新
包裝、preseed 被真正的 debian-installer 讀取解析、`late-command.sh`
在真正裝好的系統裡執行,這一整條路徑完全沒有在沙盒裡跑過一次,因為
沙盒的網路白名單直接擋掉 Debian 的套件鏡像。所以這份流程必須在你自己
一台有網路、能裝套件的機器上做——筆電、桌機、雲端 VM 都可以,重點是
要有真正的網際網路連線。

## 步驟 0:解壓縮拿到的程式碼

把這次收到的 zip 解壓縮,或者如果你已經自己 `git clone` 過這個 repo,
直接 `cd` 進去、確認是最新的 commit(`git log --oneline -1` 應該看到
「Phase 19 appliance 第八輪覆閱」這則 commit,或更新的)。

```
cd gonas
```

## 步驟 1:裝建置工具

```
sudo apt update
sudo apt install -y xorriso wget qemu-system-x86 qemu-utils ovmf
```

`qemu-system-x86` 是 x86_64 虛擬機用的;如果之後也想測 arm64 映像檔,
另外裝 `qemu-system-arm`(提供 `qemu-system-aarch64`)——arm64 需要
UEFI 韌體檔案 `edk2-aarch64-code.fd`,`ovmf` 這個套件在大多數
Debian/Ubuntu 上也一併帶了 aarch64 版本的韌體檔案(路徑通常在
`/usr/share/AAVMF/AAVMF_CODE.fd`,實際路徑依發行版而定,用
`dpkg -L ovmf | grep -i aavmf` 找)。**建議先把 amd64 這條路徑完整走完
一次確認沒問題,再考慮花力氣測 arm64**,兩者用的建置/preseed/
late-command 邏輯完全共用,amd64 驗證過的東西大部分也適用於 arm64,
差別主要在 QEMU 開機參數跟 UEFI 韌體這一層。

## 步驟 2:跑一次完全離線的快速自我檢查(不需要網路,幾秒鐘)

在真正花時間下載幾百 MB 的官方 ISO 之前,先確認兩段最容易壞掉的邏輯
本身沒問題:

```
sh build/appliance/test-boot-menu-patch.sh
sh build/appliance/test-gpg-verify.sh
```

第一支應該看到 5 個 `PASS` 跟 `==> all boot-menu-patch test cases
passed`;第二支應該看到 3 個 `PASS` 跟 `==> all gpg-verify
control-flow test cases passed`。如果這裡就失敗了,代表程式碼在傳輸
過程中被改動或損毀,不用往下做,先確認拿到的程式碼是完整的。這兩支
測試也已經寫進 `.github/workflows/ci.yml`,如果你把這個 repo 推到
GitHub,之後每次 push/PR 都會自動跑一次,不用每次都記得手動執行。

## 步驟 3:建置 ISO

```
make iso-amd64
```

這個指令會依序做:

1. `make release`(純 Go 交叉編譯,不需要網路,產出
   `dist/release/gonas-<version>-linux-amd64.tar.gz`)。
2. 下載官方 Debian netinst ISO(需要網路,幾百 MB,第一次會比較慢;
   之後重跑會從 `dist/.cache/debian-iso/` 讀快取,雜湊值對得上才會重用,
   不用每次都重新下載)。
3. 驗證下載回來的 ISO 雜湊值,解開、塞進 gonasd 執行檔跟客製化腳本、
   修改開機選單、重新用 xorriso 包裝。

預設只做 checksum 驗證(完整性,不是真實性)。如果你想多一層 GPG
簽章驗證,自己照 https://www.debian.org/CD/verify 官方說明匯入
Debian 的簽章金鑰、匯出成一個 keyring 檔案,執行前設定
`export GONAS_DEBIAN_KEYRING=/path/to/your.keyring` 就會自動多驗證
一層,失敗直接中止(不想用這個的話什麼都不用做,行為不變)。

過程中終端機會印出目前在做哪一步(`==> ...` 開頭的訊息)。順利的話最後
會看到:

```
==> done: dist/release/gonas-<version>-amd64.iso
==> checksum: ...
```

**如果這一步失敗了**,把完整的錯誤訊息記下來——這是第一次有機會看到
「這個沙盒完全沒辦法驗證的部分」實際跑起來的樣子,錯誤訊息本身就是
最有價值的線索,直接告訴我,我可以照著訊息回頭修對應的邏輯,不需要
你自己排查。

## 步驟 4:建一顆空白測試磁碟

```
qemu-img create -f qcow2 gonas-test-disk.img 20G
```

20GB 對「標準系統 + SSH server」這種最小化安裝來說綽綽有餘。

## 步驟 5:開機測試(QEMU)

```
qemu-system-x86_64 \
    -enable-kvm \
    -m 2048 \
    -cdrom dist/release/gonas-<version>-amd64.iso \
    -boot d \
    -drive file=gonas-test-disk.img,format=qcow2,if=virtio \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0
```

把 `<version>` 換成步驟 3 實際產出的檔名。`-enable-kvm` 需要這台機器
本身支援硬體虛擬化(大多數桌機/筆電的 Linux 都支援;雲端 VM 要看
供應商是否開放巢狀虛擬化)——沒有 KVM 加速的話拿掉這個參數一樣能跑,
只是純軟體模擬會慢非常多(整個安裝過程可能要等數十分鐘而不是幾分鐘),
建置前先用 `kvm-ok`(裝在 `cpu-checker` 套件裡)或直接看
`/dev/kvm` 存不存在確認。如果沒有圖形介面的環境(例如透過 SSH 連進
一台雲端 VM 操作),把整行最後加上 `-nographic`(改用終端機文字模式
顯示 QEMU 的畫面),或者裝 `-vnc :1` 然後用 VNC client 連進去看。

## 步驟 6:照順序確認安裝過程

開機後**不需要按任何鍵**,安裝程式會自動跑完語系/網路/套件安裝,中途
會在以下情況停下來要求手動確認,這些都是**刻意保留、不是 bug**:

1. 如果你這台 VM 掛了不只一顆磁碟(這個範例只掛了一顆,不會遇到這個
   提示;想額外驗證這個路徑的話,`qemu-img create` 兩顆磁碟、QEMU
   指令多加一個 `-drive`),會先問「要對哪一顆磁碟分割」。
2. 不管幾顆磁碟,選定之後最後都會停在「真的要把這個分割配置寫入磁碟
   嗎」的確認畫面——**這裡選擇繼續/是**,才會真的往下走。

如果安裝程式在**這兩種情況以外**的地方停下來問問題(語系、鍵盤、
使用者帳號之類),代表 `preseed.cfg` 某個欄位寫錯了,把畫面上顯示的
問題截圖或抄下來告訴我。

**額外留意套件安裝那一段**(畫面上通常會看到 `tasksel`/`pkgsel` 相關的
進度畫面):`preseed.cfg` 選的是 `standard`(標準系統工具)、
`ssh-server`、`sudo` 這三個套件集/套件,理論上 Debian netinst ISO
本身內附的套件池就夠裝這些,不需要連網——但這件事到目前為止只是
「理論上應該夠用」,這個專案從來沒有實際驗證過。如果這一步順利跑完
沒有停下來報錯,就代表這個假設是對的,不需要你額外做什麼;如果卡在
這裡跳出「找不到套件」或類似的錯誤,把完整訊息帶回來,這會是一個
之前完全沒被抓到過的新問題。

安裝完成後會自動重開機(不會停在「移除安裝媒體」那個畫面)。

**如果安裝過程中途直接失敗、跳回一個錯誤畫面(不是正常走到重開機)**:
`late_command` 那一步失敗時,`preseed.cfg` 裡有寫一個備援機制,會把
錯誤訊息附加到 `/etc/motd`,理論上重開機後不管是 tty1 還是 SSH 登入
都看得到這則訊息(因為這個失敗模式最可能發生在 `late-command.sh`
還沒走到「換掉 tty1 登入畫面」那一步之前,這時候 tty1 應該還是標準的
Debian 登入畫面,登入時會顯示 motd)——這個備援機制本身也還沒有實際
觸發過一次來確認,如果你真的遇到這種情況,務必登入看一下 motd 裡
有沒有出現這則錯誤訊息,並把內容帶回來。

## 步驟 7:第一次開機後的檢查清單

重開機之後,依序確認:

1. **tty1(QEMU 主控台視窗本身)應該幾乎立刻顯示 GoNAS 的 ASCII art
   狀態畫面**,不是 GRUB 選單、也不是 Debian 的登入提示。這裡同時
   驗證了第七輪覆閱修的 GRUB 逾時設定(不應該卡在 GRUB 選單畫面)跟
   之前幾輪修的 tty1 主控台排序問題。畫面上應該看到版本號、主機名稱、
   跟一個或多個 `http://<ip>:8291` 的網址。
2. 在 host 機器的瀏覽器打開畫面上顯示的網址,應該看到 GoNAS 的首次
   設定畫面。**這一步在 VM 測試時無所謂,但真機正式使用時務必注意**:
   這個階段走的是純 HTTP,而且誰先連進去設定誰就拿到管理者帳號,建議
   正式機器第一次開機設定時,接在一個只有自己/信任的人能連進來的
   網路上(直接一條網路線接筆電,或隔離的 VLAN),見
   `build/appliance/README.md`「安全性提醒:第一次開機後,誰先連到
   誰先贏」一節的完整說明。
3. 切到 tty2(QEMU 視窗裡按 `Ctrl+Alt+F2`,或用
   `qemu-system-x86_64` 的 monitor 送對應按鍵),應該看到正常的
   `gonas login:` 提示——用 `gonasadmin` / `gonas-change-me-now`
   登入,應該會被要求立刻設定一組新密碼才能拿到 shell(這是第五輪
   覆閱加的強制機制,務必實際測一次,是目前風險評估最高、最需要
   確認的一項)。
4. 設完新密碼、進到 shell 之後:
   - `systemctl status gonas`:應該是 `active (running)`。
   - `systemctl is-enabled gonas`:應該是 `enabled`(這是第一輪覆閱
     修的 bug,務必確認)。
   - `hostnamectl`:主機名稱應該顯示 `gonas`。
   - `cat /etc/os-release`:`PRETTY_NAME` 應該顯示
     `GoNAS (Debian GNU/Linux)`。
5. 重開機一次(`sudo reboot`),確認 tty1 的狀態畫面在下一次開機一樣
   會自動出現,不需要每次都手動介入。
6. 額外測「開機當下沒有網路」的情境:把 QEMU 指令裡的
   `-netdev .../-device ...` 拿掉重開一次機,tty1 應該**立刻**顯示
   畫面(內容是「尚未偵測到網路連線」),而不是黑畫面卡住等半天——
   這是第四輪覆閱修的問題,值得專門測一次確認。

以上六項全部通過,代表 Phase 19 這條路徑的核心功能都如預期運作。

## 步驟 8(可選):arm64 映像檔

確認 amd64 沒問題之後,如果你的目標硬體是樹莓派或其他 arm64 SBC,
重複同樣的流程,差別在:

```
make iso-arm64
```

以及開機指令換成 `qemu-system-aarch64`,大致像這樣(實際參數請對照
你機器上安裝的 QEMU 版本文件微調,尤其是 UEFI 韌體檔案的路徑):

```
qemu-system-aarch64 \
    -M virt -cpu cortex-a57 \
    -m 2048 \
    -bios /usr/share/AAVMF/AAVMF_CODE.fd \
    -cdrom dist/release/gonas-<version>-arm64.iso \
    -boot d \
    -drive file=gonas-test-disk-arm64.img,format=qcow2,if=virtio \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0
```

這條路徑完全沒有在任何環境測試過(連 QEMU 帶起來的參數都只是照文件
推測的,不保證第一次就能直接開機成功),遇到問題的機率比 amd64 高,
把實際遇到的錯誤訊息告訴我,我可以照著修。

## 步驟 9:全部通過之後才燒到真實硬體

用 `dd`/Rufus/balenaEtcher 之類的工具把 `dist/release/gonas-<version>-
<arch>.iso` 寫到實體 USB。插到真正要當 NAS 用的機器上開機之前:

- **先拔掉/斷開任何已經有資料的磁碟,只留下要裝系統的那顆全新硬碟**
  ——避免萬一分割步驟選錯磁碟,不可逆地清空正式的資料陣列。
- 真實硬體上額外留意有沒有跳出「缺少韌體,請插入另一份媒體」的畫面
  ——這是 `d-i hw-detect/load_firmware boolean false` 沒有完全生效的
  跡象,VM 用 virtio 裝置通常不會遇到,是真機才需要特別留意的差異點。

重複步驟 7 的檢查清單,全部通過之後,這台機器就是一台可以正式使用的
GoNAS 了。

## 如果過程中遇到問題

不管在哪一步卡住,把下面這些資訊帶回來給我,我可以直接照著修:

- 卡在哪一步(哪個畫面、哪個指令)。
- 完整的錯誤訊息(螢幕截圖或直接複製文字都可以)。
- 如果是開機之後的問題,`journalctl -b -1`(上一次開機的 log,通常
  看得到 `late-command` 相關的輸出)或 `/var/log/syslog` 裡跟這次
  安裝相關的內容,特別是 `[gonas-late-command]` 開頭的那幾行——
  `late-command.sh` 每一步都有印 log,失敗訊息會直接告訴你是哪一步、
  為什麼失敗。

這是整個 Phase 19 第一次真的離開「純程式碼覆閱」階段,實際執行結果
(不管是全部通過,還是某幾步失敗)都是目前最有價值的資訊,請盡量
完整帶回來。
