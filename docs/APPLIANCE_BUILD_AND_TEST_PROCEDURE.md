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

### Linux(Debian/Ubuntu)

```
sudo apt update
sudo apt install -y xorriso wget qemu-system-x86 qemu-utils ovmf
```

`qemu-system-x86` 是 x86_64 虛擬機用的;如果之後也想測 arm64 映像檔,
另外裝 `qemu-system-arm`(提供 `qemu-system-aarch64`)——arm64 需要
UEFI 韌體檔案 `edk2-aarch64-code.fd`,`ovmf` 這個套件在大多數
Debian/Ubuntu 上也一併帶了 aarch64 版本的韌體檔案(路徑通常在
`/usr/share/AAVMF/AAVMF_CODE.fd`,實際路徑依發行版而定,用
`dpkg -L ovmf | grep -i aavmf` 找)。硬體加速用的是 KVM,`-enable-kvm`
這個 QEMU 參數見步驟 5。

### macOS(例如你現在用的 Mac mini)

先確認你的 Mac 是哪一種晶片,這決定了下面該用哪個架構、哪個硬體加速
方式:

```
uname -m
```

看到 `arm64` 代表是 Apple Silicon(M1/M2/M3/M4 系列);看到
`x86_64` 代表是 Intel 晶片。這件事很重要,原因見下面「該測 amd64
還是 arm64」的說明。

裝建置工具(用 [Homebrew](https://brew.sh),如果還沒裝過先照官網
指示裝好):

```
brew install xorriso wget qemu
```

macOS 上不需要另外裝 `qemu-utils`/`ovmf`——Homebrew 的 `qemu` 套件
本身就含 `qemu-img`、`qemu-system-x86_64`、`qemu-system-aarch64`
跟 arm64 開機要用的 UEFI 韌體檔案(路徑通常是
`$(brew --prefix qemu)/share/qemu/edk2-aarch64-code.fd`,步驟 8 會
用到)。

macOS 的硬體加速用的是 Apple 自己的 Hypervisor.framework(QEMU 的
`-accel hvf` 參數,取代 Linux 的 `-enable-kvm`,見步驟 5),但
**HVF 只能加速跟主機同架構的 VM**——這跟 Linux 上的 KVM 不一樣(KVM
也只能加速同架構,但 x86_64 機器本來就幾乎不會想在裡面跑 arm64
VM,不會特別感覺到這個限制)。這對你接下來要測哪個架構有直接影響,
見下面的說明。

**該測 amd64 還是 arm64:**

- **Apple Silicon Mac(`uname -m` 顯示 `arm64`)→ 建議測 arm64 映像檔
  (`make iso-arm64`,對應步驟 3/5/8 都選 arm64 那個指令)。** 這樣
  QEMU 才吃得到 HVF 加速,開機/安裝過程是正常速度(幾分鐘等級)。
  如果你反而想測 amd64 映像檔,QEMU 會用純軟體模擬(TCG)跑一顆
  x86_64 虛擬 CPU,完全沒有加速,整個安裝過程可能要等上數十分鐘到
  更久,不建議當第一次測試——GoNAS 的建置/preseed/late-command 邏輯
  兩個架構完全共用,arm64 測過一次,邏輯上的問題(preseed 有沒有
  正確生效、tty1 品牌畫面對不對、gonasd 有沒有開機自動啟動)跟 amd64
  是同一套,不需要兩個都測。
- **Intel Mac(`uname -m` 顯示 `x86_64`)→ 測 amd64 映像檔**
  (`make iso-amd64`,文件裡預設的指令),QEMU 用 HVF 加速跑
  x86_64 VM,速度正常。

下面步驟 3/5 的指令預設寫的是 amd64(對應多數 Linux/Intel 機器的
情境);如果你是 Apple Silicon Mac,照著做但把 `amd64` 換成
`arm64`、`qemu-system-x86_64` 換成 `qemu-system-aarch64`,詳細的
arm64 QEMU 開機參數在步驟 8。

**建議先把你機器對應的那個架構完整走完一次確認沒問題**,兩者用的
建置/preseed/late-command 邏輯完全共用,差別主要在 QEMU 開機參數跟
UEFI 韌體這一層。

**磁碟空間**:第二十八輪起底層 ISO 從 netinst(~700MB)換成 DVD-1
完整版(~3.7GB),所以這裡的估計要大幅上修。建置一個架構的過程中
同時間可能佔用到:官方 DVD-1 ISO 一份快取在 `dist/.cache/`(約
3.7GB)、同一份 ISO 的工作副本在系統暫存目錄(`/tmp`,同樣約
3.7GB)、解開後的完整目錄樹(約 3.7GB)、最後包裝出來的成品 ISO 在
`dist/release/`(約 3.7GB)——保守估計建置過程中**暫時**需要 12GB
左右的可用空間才不會卡在中途(如果 `/tmp` 跟這個 repo 所在的磁碟是
同一個分割區,兩邊加起來一起算)。加上步驟 4 的 20GB 測試磁碟映像
(那個是稀疏檔案,實際用量通常遠小於 20GB),建議至少預留 20GB 以上
的可用空間再開始,免得建到一半才發現 `No space left on device`。

## 步驟 2:跑一次完全離線的快速自我檢查(不需要網路,幾秒鐘)

在真正花時間下載 ~3.7GB 的官方 DVD-1 ISO 之前,先確認幾段最容易壞掉的
邏輯本身沒問題(在 Linux 或 macOS 上都一樣執行,不用另外做什麼):

```
sh build/appliance/test-boot-menu-patch.sh
sh build/appliance/test-gpg-verify.sh
sh build/appliance/test-detect-arch.sh
sh build/appliance/test-portable-checksum.sh
sh build/appliance/test-portable-sed.sh
sh build/appliance/test-find-boot-menu-cfgs.sh
```

第一支應該看到 5 個 `PASS` 跟 `==> all boot-menu-patch test cases
passed`;第二支應該看到 6 個 `PASS`(其中一個案例驗證「呼叫 gpg 時有
沒有強制用英文語系」——gpg 的驗證訊息會被翻譯,建置機器語系不是英文
的話,沒有這個強制設定會讓每一次驗證都被誤判成失敗,見
`lib/verify-gpg-signature.sh` 的說明;另外兩個案例會用一把真的臨時
GPG 金鑰簽章/驗證,不是純粹的假 `gpg`)跟 `==> all gpg-verify
test cases passed`;第三支(檢查 `late-command.sh` 判斷架構時,
`dpkg --print-architecture` 不可用而 fallback 到 `uname -m` 的對應表)
應該看到 6 個 `PASS` 跟 `==> all detect-arch test cases passed`;
第四、五支(第十七輪覆閱新增,專門為了你在 macOS 上執行這件事補的
——`build-iso.sh` 原本直接用 GNU 專屬的 `sha256sum`/`md5sum`/
`sed -i`,macOS 內建的 BSD 版本這幾個指令要不是不存在、要不是語法
不一樣,已經改成會自動判斷環境的版本)應該分別看到 4 個跟 3 個
`PASS`,以及 `==> all portable-checksum test cases passed`/
`==> all portable-sed test cases passed`;第六支(第十八輪覆閱新增
——就是你這次實測 `make iso-arm64` 撞到「patching boot menu configs
之後 make 直接印 Error 1、沒有任何錯誤訊息」的那個問題:`find` 找
isolinux/grub 設定檔時,如果 isolinux 目錄不存在(arm64 官方 ISO
本來就沒有這個目錄),`find` 自己的 exit code 在 `set -e` 底下會讓
整支腳本沉默死掉)應該看到 3 個 `PASS` 跟
`==> all find-boot-menu-cfgs test cases passed`。
(第七支 `test-deb-closure.sh` 已在第二十八輪隨著離線 .deb 打包邏輯
一併刪除——換成 DVD-1 完整版之後,openssh-server/sudo 改由 preseed
的 `pkgsel/include` 直接從 DVD 離線裝,不再需要自己算相依封閉集。)
如果這裡就失敗了,代表程式碼在傳輸過程中被改動或損毀,不用往下做,
先確認拿到的程式碼是完整的。這六支測試也已經寫進
`.github/workflows/ci.yml`(而且特地也在 macOS 的 GitHub Actions
runner 上跑一次,不是只在 Linux 上跑),如果你把這個 repo 推到
GitHub,之後每次 push/PR 都會自動跑一次,不用每次都記得手動執行。

## 步驟 3:建置 ISO

```
make iso-amd64
```

這個指令會依序做:

1. `make release`(純 Go 交叉編譯,不需要網路,產出
   `dist/release/gonas-<version>-linux-amd64.tar.gz`)。
2. 下載官方 Debian DVD-1 完整版 ISO(需要網路,~3.7GB,第一次會相當慢;
   之後重跑會從 `dist/.cache/debian-iso/` 讀快取,雜湊值對得上才會重用,
   不用每次都重新下載)。
3. 驗證下載回來的 ISO 雜湊值,解開、塞進 gonasd 執行檔跟客製化腳本、
   修改開機選單、重新用 xorriso 包裝。

預設只做 checksum 驗證(完整性,不是真實性)。如果你想多一層 GPG
簽章驗證,自己照 https://www.debian.org/CD/verify 官方說明匯入
Debian 的簽章金鑰,**用 `gpg --export <key-id> > my.keyring`(不要加
`-a`/`--armor`)匯出成 binary 格式**(這是實測踩過的坑:armor 格式
會讓 `--keyring` 報 `invalid packet`,即使金鑰本身完全正確),執行前
設定 `export GONAS_DEBIAN_KEYRING=/path/to/my.keyring`(相對、絕對
路徑都可以,腳本會自動轉成絕對路徑)就會自動多驗證一層,失敗直接
中止(不想用這個的話什麼都不用做,行為不變)。

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

### Linux

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

### macOS(Apple Silicon,例如 Mac mini M 系列——步驟 1 判斷過
`uname -m` 是 `arm64` 的情況)

```
qemu-system-aarch64 \
    -M virt \
    -cpu host \
    -accel hvf \
    -m 2048 \
    -bios "$(brew --prefix qemu)/share/qemu/edk2-aarch64-code.fd" \
    -cdrom dist/release/gonas-<version>-arm64.iso \
    -boot d \
    -drive file=gonas-test-disk.img,format=qcow2,if=virtio \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0 \
    -device virtio-gpu-pci -display default,show-cursor=on
```

把 `<version>` 換成步驟 3 實際產出的檔名。`-accel hvf` 是 macOS 的
硬體加速(相當於 Linux 的 KVM),`-cpu host` 讓虛擬 CPU 直接使用主機
CPU 的完整特性——這個組合只在**虛擬機架構跟主機架構相同**時有效
(arm64 虛擬機 + Apple Silicon 主機),這正是步驟 1 建議 Apple
Silicon Mac 測 arm64 映像檔而不是 amd64 的原因。`-device
virtio-gpu-pci -display default,show-cursor=on` 是給 QEMU 開一個
真正的視窗顯示畫面(在 Mac 上通常不需要另外裝 X11/VNC,QEMU 自己會
跳出一個視窗);如果你想在 Terminal 裡直接看文字輸出,把這兩個參數
換成 `-nographic`。

### macOS(Intel Mac——`uname -m` 是 `x86_64` 的情況)

```
qemu-system-x86_64 \
    -accel hvf \
    -m 2048 \
    -cdrom dist/release/gonas-<version>-amd64.iso \
    -boot d \
    -drive file=gonas-test-disk.img,format=qcow2,if=virtio \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0
```

跟 Linux 版本幾乎一樣,差別只在加速參數從 `-enable-kvm` 換成
`-accel hvf`(Intel Mac 上跑 x86_64 虛擬機,架構跟主機相同,一樣吃得
到硬體加速)。

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
進度畫面):第二十八輪換成 DVD-1 完整版之後,`preseed.cfg` 選的是
`standard`(標準系統工具)加上 `pkgsel/include` 的 `openssh-server`
跟 `sudo`——這三樣 DVD-1 的套件庫裡都有,`apt-setup/use_mirror false`
(離線)下直接從 DVD 裝,不需要連網。**這一段正是這次換 DVD-1 最想
驗證的地方**:如果這一步順利跑完沒停下來報錯,代表「DVD-1 上確實有
這些套件、pkgsel 真的能離線從 DVD 裝好」這個核心假設成立;如果卡在
這裡跳出「找不到套件 / package not found」或類似錯誤,把完整訊息帶
回來——那代表要嘛 DVD 沒被正確當成 apt 來源、要嘛某個套件其實不在
DVD-1 上,是需要回頭調整的新問題。

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
2. 在 host 機器的瀏覽器打開畫面上顯示的網址,應該看到 GoNAS 的**登入
   畫面**(不是「首次建立帳號」畫面——第十九輪起 appliance 會預先建好
   一組預設 admin)。用帳號 `gonas` / 密碼 `gonas` 登入,登入後應該
   **立刻被強制要求修改密碼**才能進到主畫面(這是預設密碼只有第一次
   有效的機制,伺服器端 requireAdmin 也會擋住所有 admin 操作直到改完,
   見 internal/api)。改完密碼就會進到 GoNAS 主控台。
   **這一步在 VM 測試時無所謂,但真機正式使用時務必注意**:這個階段
   在你開啟 HTTPS 之前走的是純 HTTP,而且預設密碼是眾所周知的 gonas,
   建議正式機器第一次開機設定時,接在一個只有自己/信任的人能連進來的
   網路上(直接一條網路線接筆電,或隔離的 VLAN),盡快登入改掉預設
   密碼、開啟 HTTPS,再接回一般網路,見 `build/appliance/README.md`
   「安全性提醒:第一次開機後,誰先連到誰先贏」一節的完整說明。
3. 切到 tty2(QEMU 視窗裡按 `Ctrl+Alt+F2`,或用
   `qemu-system-x86_64` 的 monitor 送對應按鍵),應該看到正常的
   `gonas login:` 提示——用 `gonas` / `gonas`
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
   - `ls -l /usr/local/share/gonas/uninstall.sh`:應該存在,而且有
     執行權限(這是第十四輪覆閱補上的——appliance 裝完之後安裝媒體
     會被退出,系統上原本完全沒有 `uninstall.sh` 可以用,現在
     `late-command.sh` 會順手留一份在這裡)。
   - `systemctl is-enabled ssh`:應該是 `enabled`(第二十八輪起
     openssh-server 由 preseed 的 `pkgsel/include` 在安裝時從 DVD-1
     離線裝上,`late-command.sh` 1.7 節再補確認開機啟動、產生 host key、
     寫 sshd drop-in)。接著從你的 Mac 用 `ssh gonas@<這台機器的IP>`
     應該連得進來,第一次登入會被要求立刻改掉預設密碼 `gonas`。
   - `sudo -v` 或 `sudo id`:gonas 應該能用 sudo(sudo 這個套件
     同樣由 pkgsel 從 DVD-1 離線裝上,gonas 也已經被加進 sudo 群組)。
5. 重開機一次(`sudo reboot`),確認 tty1 的狀態畫面在下一次開機一樣
   會自動出現,不需要每次都手動介入。
6. 額外測「開機當下沒有網路」的情境:把 QEMU 指令裡的
   `-netdev .../-device ...` 拿掉重開一次機,tty1 應該**立刻**顯示
   畫面(內容是「尚未偵測到網路連線」),而不是黑畫面卡住等半天——
   這是第四輪覆閱修的問題,值得專門測一次確認。

以上六項全部通過,代表 Phase 19 這條路徑的核心功能都如預期運作。

**額外特別留意這一項(第十四輪新加,目前風險評估最高)**:如果第 3
項(`gonas` 登入強制改密碼)或步驟 6 這裡的
`/usr/local/share/gonas/uninstall.sh` 檢查失敗、或整台機器開機後
根本不是 GoNAS 的樣子(看起來像一台單純裝了 SSH 的陽春 Debian)——
先檢查 `journalctl -b -1` 或 `/etc/motd` 裡有沒有
`[gonas-late-command]` 開頭的 log、或 `GoNAS late-command.sh
failed` 這則備援訊息,尤其留意有沒有出現 `Permission denied`:
第十四輪把 `late-command.sh`/`install.sh` 這兩個直接被安裝程式呼叫
的進入點都改成用 `sh 檔案路徑` 執行,理由是懷疑 xorriso 重新包裝
ISO 的過程可能沒有正確保留這兩個檔案的 Unix 執行位元(Rock Ridge
擴充屬性的細節,這個沙盒完全沒辦法驗證),如果你真的看到相關的
`Permission denied` 訊息,代表這個懷疑是對的,務必把完整訊息帶
回來——這會是這個 Phase 目前唯一一個「理論推導出問題、但修法本身
也還沒被真正驗證過」的地方。

## 步驟 8(可選):另一個架構的映像檔

**如果你是 Apple Silicon Mac,你在步驟 5 測的已經是 arm64**——這一步
對你來說是「反過來,可選地也測一下 amd64」,順序相反但道理一樣:
GoNAS 的建置/preseed/late-command 邏輯兩個架構完全共用,通常不需要
兩個都測,除非你想額外確認一下(amd64 在 Apple Silicon Mac 上跑
QEMU 的純軟體模擬,沒有 HVF 加速,建置/開機都會慢很多)。

**如果你是 Linux 或 Intel Mac**,確認 amd64 沒問題之後,如果你的
目標硬體是樹莓派或其他 arm64 SBC,重複同樣的流程,差別在:

```
make iso-arm64
```

Linux 上開機指令換成 `qemu-system-aarch64`,大致像這樣(實際參數請
對照你機器上安裝的 QEMU 版本文件微調,尤其是 UEFI 韌體檔案的路徑):

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

在 Intel Mac 上則是步驟 5 的 macOS 指令,但把 `-accel hvf` 拿掉
(Intel 主機跑 arm64 虛擬機,架構不同,吃不到 HVF 加速,一樣是純
軟體模擬)、`-cpu host` 換成 `-cpu cortex-a57`(host CPU 特性只有
架構相同時才能直接透傳)、`-bios` 路徑照樣用
`"$(brew --prefix qemu)/share/qemu/edk2-aarch64-code.fd"`。

這條路徑完全沒有在任何環境測試過(連 QEMU 帶起來的參數都只是照文件
推測的,不保證第一次就能直接開機成功),遇到問題的機率比另一個架構
高,把實際遇到的錯誤訊息告訴我,我可以照著修。

## 步驟 9:全部通過之後才燒到真實硬體

用 `dd`/Rufus/balenaEtcher 之類的工具把 `dist/release/gonas-<version>-
<arch>.iso` 寫到實體 USB。在 macOS 上用 `dd` 的話,裝置路徑跟 Linux
不一樣(`diskutil list` 找到 USB 隨身碟對應的 `/dev/diskN`,先
`diskutil unmountDisk /dev/diskN` 卸載、再用
`/dev/rdiskN`——加了 `r` 的「raw disk」路徑寫入速度快很多——執行
`sudo dd if=dist/release/gonas-<version>-<arch>.iso of=/dev/rdiskN
bs=4m`);裝置路徑選錯會直接覆寫到錯的磁碟,務必用 `diskutil list`
仔細確認是那支隨身碟,不是你電腦本身的硬碟。也可以省去命令列,直接
用 balenaEtcher(有 macOS 版 GUI,選好 ISO 跟目標隨身碟,介面上會
清楚標示,比較不容易選錯)。插到真正要當 NAS 用的機器上開機之前:

- **先拔掉/斷開任何已經有資料的磁碟,只留下要裝系統的那顆全新硬碟**
  ——避免萬一分割步驟選錯磁碟,不可逆地清空正式的資料陣列。
- 真實硬體上額外留意有沒有跳出「缺少韌體,請插入另一份媒體」的畫面
  ——這是 `d-i hw-detect/load_firmware boolean false` 沒有完全生效的
  跡象,VM 用 virtio 裝置通常不會遇到,是真機才需要特別留意的差異點。
- **如果你的目標硬體沒有一般的 VGA/HDMI 螢幕輸出**(只能用序列埠
  主控台,或透過 IPMI/BMC 這類遠端管理介面連線)——tty1 那個 GoNAS
  品牌畫面完全靠不上,見 `build/appliance/README.md`「已知的設計
  限制」對應那一條。機器還是會正常開機、gonasd 一樣會啟動,只是找
  不到品牌畫面上顯示的網址,改看你的路由器/DHCP 伺服器的用戶端清單
  找到這台機器的 IP。

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
