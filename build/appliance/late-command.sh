#!/bin/sh
# late-command.sh 是 Debian 安裝程式跑完基本安裝之後、重開機之前，
# 在剛裝好的目標系統 chroot 裡執行的最後一步（由 preseed.cfg 的
# `d-i preseed/late_command` 呼叫，透過 debian-installer 的 `in-target`
# 機制執行，執行環境已經是目標系統本身，不是安裝程式的臨時環境）。
# 這一步做的事情，就是把一份「已經裝好 Debian」的系統，變成使用者
# 開機後感覺到的是「這是一台 GoNAS」而不是「這是一台裝了 GoNAS 軟體
# 的 Debian」：
#
#   1. 安裝 gonasd 本體（複用既有的 build/install.sh，軟體安裝邏輯
#      完全不重寫一份——appliance 映像檔跟「使用者自己在 Debian 上裝
#      GoNAS」用的是同一套安裝腳本，差別只在誰、什麼時候執行它），
#      並把 uninstall.sh 留一份在系統上（見下面 1.6 節），因為
#      appliance 這條路徑裝完之後安裝媒體會被退出，不像「軟體版」
#      安裝路徑那樣使用者手上自然留著一份 release tarball。
#   2. 佈署 tty1 狀態主控台（gonas-console.service）取代預設的登入
#      提示，這是使用者選的「開機後精簡狀態畫面」路線。
#   3. 品牌化：主機名稱、/etc/motd、/etc/issue、/etc/os-release 的
#      PRETTY_NAME、GRUB 開機選單標題。
#   4. 強制 gonasadmin 這組緊急備援帳號的預設密碼在第一次登入時就要
#      被換掉，而不是只在文件裡提醒使用者自己記得改。
#
# 重要：這支腳本目前完全沒有在真正的 Debian 安裝程式環境裡執行過
# （這個開發沙盒的網路白名單擋掉了 deb.debian.org，連 apt-get update
# 都會被擋下來，見 build/appliance/README.md 的完整說明），是依照
# Debian 官方文件與慣例撰寫、邏輯上自洽，但尚未經過真實驗證——使用者
# 在正式燒錄安裝到硬體之前，務必先在虛擬機（QEMU/VirtualBox）裡完整
# 跑一次，見 README.md「如何驗證」一節。

set -e

# INSTALL_MEDIA 是這個腳本執行當下，安裝媒體（USB/光碟映像）被掛載
# 的路徑——d-i 環境裡通常是 /cdrom，用 in-target 執行時仍然可以透過
# bind mount 存取到，實際路徑由 build-iso.sh 產生 ISO 時決定，這裡
# 用環境變數讓 preseed late_command 那一行可以覆寫，不用寫死。
INSTALL_MEDIA="${GONAS_INSTALL_MEDIA:-/cdrom}"
GONAS_DIR="$INSTALL_MEDIA/gonas"

log() { echo "[gonas-late-command] $*"; }

# 「把 uname -m 的輸出對應回 Debian 慣用架構名稱」這段邏輯獨立成
# lib/detect-arch.sh 的 gonas_uname_to_debian_arch(),原因見那個檔案
# 開頭的說明——來源進來而不是像之前那樣直接寫死在這裡,才能讓
# test-detect-arch.sh 跟這支正式腳本共用同一份對應表,不會不同步。
SCRIPT_DIR_FOR_LIB="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR_FOR_LIB/lib/detect-arch.sh"

# 這裡原本寫成 `dpkg --print-architecture 2>/dev/null || uname -m`——
# 第九輪覆閱時發現這個 fallback 本身的值格式是錯的:`dpkg
# --print-architecture` 回傳的是 Debian 的架構名稱("amd64"/"arm64"),
# 跟 build-iso.sh 建立的目錄名稱("release-amd64"/"release-arm64")
# 一致;但 `uname -m` 回傳的是核心/硬體慣用的名稱("x86_64"/"aarch64"),
# 直接在這台沙盒機器上執行 `uname -m` 得到的就是 "x86_64",不是
# "amd64"——如果哪天 `dpkg` 真的因為某種原因不可用(在一個裝好的
# Debian in-target chroot 裡理論上不該發生,dpkg 本身就是這個系統的
# 一部分,幾乎不可能缺席,但這不代表 fallback 寫錯也沒關係),這個
# fallback 會算出一個跟 `$RELEASE_DIR="$GONAS_DIR/release-$ARCH"`
# 對不起來的路徑,導致下面「找不到 install.sh」直接判定 ISO 建置錯誤
# 並中止——實際上只是這一行本身的 fallback 寫錯,不是真的建置錯誤。
# 修法:fallback 分支額外把 `uname -m` 的輸出對應回 Debian 的架構
# 命名,對不上已知對應表的情況才直接使用原始值(至少不會是一個看起來
# 對、其實是另一種命名慣例的假象)。
ARCH="$(dpkg --print-architecture 2>/dev/null)"
if [ -z "$ARCH" ]; then
    UNAME_M="$(uname -m 2>/dev/null || echo unknown)"
    ARCH="$(gonas_uname_to_debian_arch "$UNAME_M")"
    log "WARNING: 'dpkg --print-architecture' unavailable, fell back to 'uname -m' ($UNAME_M -> $ARCH) — this should not happen inside a Debian in-target chroot; if you see this warning, please report it"
fi

log "install media: $INSTALL_MEDIA"
log "target architecture: $ARCH"

# --- 1. 安裝 gonasd 本體 -----------------------------------------
# build-iso.sh 會把對應架構的 release tarball 解壓到
# $GONAS_DIR/release-$ARCH/ 底下（跟使用者手動下載 tarball 解壓縮後的
# 目錄結構完全一樣：gonasd/install.sh/uninstall.sh/gonas.service）,
# 所以這裡直接呼叫同一份 install.sh，不需要網路連線 —— 這也是為什麼
# 這個 ISO 可以做到「離線安裝」，不像一般 debian-installer netinst
# 映像那樣還需要在安裝過程連網抓套件。
#
# 用 `sh ./install.sh` 明確指定直譯器，不用 `./install.sh` 靠執行位元
# 觸發——理由跟 preseed.cfg 的 late_command 改成 `sh
# /cdrom/gonas/late-command.sh` 一樣(第十三輪覆閱抓到 lib/ 沒塞進 ISO
# 那個 bug之後,順著多想一步發現的另一個潛在風險):這個檔案是透過
# xorriso 重新包裝進 ISO 9660 檔案系統的,執行位元能不能活著留下來,
# 取決於 xorriso 有沒有正確套用 Rock Ridge 擴充屬性,這件事這個沙盒
# 完全沒辦法驗證——與其賭這個假設一定成立,不如讓這裡也完全不依賴
# 執行位元,只需要檔案讀得到就能跑。判斷式也對應改成 `-f`(檔案存在)
# 而不是 `-x`(檔案存在且可執行),不然就算真的改用 `sh` 執行,前面的
# `-x` 檢查還是可能因為執行位元遺失而誤判「找不到 install.sh」,兩處
# 要一起改才有意義。
RELEASE_DIR="$GONAS_DIR/release-$ARCH"
if [ -f "$RELEASE_DIR/install.sh" ]; then
    log "installing gonasd from $RELEASE_DIR"
    ( cd "$RELEASE_DIR" && sh ./install.sh )
else
    log "WARNING: $RELEASE_DIR/install.sh not found — gonasd was NOT installed. This ISO was built incorrectly."
    exit 1
fi

# --- 1.5. 確保 gonas.service 真的被設成開機啟動 --------------------
# 這一步是刻意補上的,理由值得說清楚:install.sh 自己的 systemd 整合
# 邏輯是用 `[ -d /run/systemd/system ]` 判斷「這台機器有沒有一個真正
# 在跑的 systemd」,這是為了在沒有 systemd 的環境(例如某些容器)下
# 優雅跳過,不讓整支腳本因為連不上 systemd 而中止。但這裡(late_command
# 透過 debian-installer 的 in-target 執行)剛好就是這種情況會被誤判的
# 案例:目標系統的 systemd 套件明明已經裝好了,只是這個 chroot 環境
# 本身沒有一個真正在跑的 systemd 實例(那是 d-i 自己的臨時環境,不是
# 裝好的目標系統開機之後的樣子)—— `/run/systemd/system` 因此不存在,
# install.sh 就會整段跳過,包括 `systemctl enable`,不只是它跳過的
# `systemctl restart`(這裡沒有真正在跑的 systemd 可以 restart,跳過
# 是對的)。如果不額外處理,重開機之後 systemd 真的開始跑了,但
# gonas.service 從來沒被 enable 過,使用者看到的會是「開機了,但
# gonasd 沒有自動啟動」——直接違背這個 Phase 的目標。
#
# 解法:不管 install.sh 剛剛有沒有走到它自己的 systemd 分支,這裡都
# 明確再對 gonas.service 做一次 `systemctl enable`。這樣做是安全的,
# 原因是 `systemctl enable` 對一個只有簡單 `[Install] WantedBy=` 的
# unit 而言,純粹是在檔案系統上建立/移除 symlink,不需要真的連上一個
# 在跑的 systemd 執行個體(deb-systemd-helper、dpkg 套件安裝腳本在
# chroot 環境裡設定服務開機啟動,靠的就是同一個機制)——下面對
# `getty@tty1.service`/`gonas-console.service` 做的 disable/mask/
# enable 也是同一個道理,這裡只是把同樣的處理方式也套用在
# gonas.service 上,確保跟 install.sh 本身的行為不衝突、又補上它在
# 這個特定執行環境下漏掉的一步。
GONAS_UNIT_SRC="$RELEASE_DIR/gonas.service"
if [ -f "$GONAS_UNIT_SRC" ] && command -v systemctl >/dev/null 2>&1; then
    cp "$GONAS_UNIT_SRC" /etc/systemd/system/gonas.service
    chmod 0644 /etc/systemd/system/gonas.service
    systemctl daemon-reload 2>/dev/null || true
    if systemctl enable gonas.service 2>/dev/null; then
        log "gonas.service enabled for boot (independent of install.sh's own systemd detection)"
    else
        log "WARNING: 'systemctl enable gonas.service' failed in this chroot — verify manually after first boot with 'systemctl is-enabled gonas.service'"
    fi
else
    log "WARNING: $GONAS_UNIT_SRC not found or systemctl unavailable — could not confirm gonas.service is enabled for boot"
fi

# --- 1.6. 把 uninstall.sh 留一份在系統上 ---------------------------
# 這是回頭檢查「appliance 這條路徑裝完之後,使用者手上到底剩下什麼」
# 時發現的另一個落差:「軟體版」安裝路徑(使用者自己在一台既有的
# Debian 機器上手動下載/解壓縮 release tarball、跑 install.sh)的
# 使用者,release tarball(裡面含 uninstall.sh)自然留在他們自己電腦
# 的某個目錄裡,事後想解除安裝隨時找得到;但 appliance 這條路徑,
# release tarball 只存在於安裝媒體(USB/光碟映像)上的
# `/cdrom/gonas/release-$ARCH/`,而 preseed.cfg 設定了
# `cdrom-detect/eject boolean true`,安裝完成、重開機之後這份安裝
# 媒體邏輯上已經被退出——裝好的系統本身完全沒有 uninstall.sh 這個
# 檔案,使用者除非剛好留著、記得重新插上/掛載當初那支安裝隨身碟並
# 精確找到同一個路徑,不然日後想解除安裝 GoNAS 根本無從下手,只能
# 手動一項一項回想 install.sh 到底改了系統的哪些地方。這在前 13 輪
# 覆閱裡完全沒被提過,是純粹「裝完之後留下什麼給使用者」這個角度才
# 會想到的落差。
#
# 解法:裝完 gonasd 之後,順手把 uninstall.sh 複製一份到系統上一個
# 固定、好記的位置。這裡才是真正把檔案寫到目標系統(已經裝好、開機
# 之後會用的那個 ext4 之類的真實檔案系統),不是留在 ISO 9660 上,
# `chmod` 設的執行位元不會有前面 install.sh/late-command.sh 那兩處
# 改成用 `sh` 執行的顧慮(Rock Ridge 屬性有沒有正確保留),這裡直接
# `chmod 0755` 就是可靠的。
UNINSTALL_DEST_DIR=/usr/local/share/gonas
if [ -f "$RELEASE_DIR/uninstall.sh" ]; then
    mkdir -p "$UNINSTALL_DEST_DIR"
    cp "$RELEASE_DIR/uninstall.sh" "$UNINSTALL_DEST_DIR/uninstall.sh"
    chmod 0755 "$UNINSTALL_DEST_DIR/uninstall.sh"
    log "uninstall.sh saved to $UNINSTALL_DEST_DIR/uninstall.sh for later use (the install media may not be available after this point)"
else
    log "WARNING: $RELEASE_DIR/uninstall.sh not found — no persistent copy could be saved; uninstalling later will require re-mounting the original install media"
fi

# --- 2. tty1 狀態主控台 -------------------------------------------
OVERLAY_DIR="$GONAS_DIR/overlay"
if [ -d "$OVERLAY_DIR" ]; then
    log "applying appliance overlay from $OVERLAY_DIR"
    cp -a "$OVERLAY_DIR/usr/local/sbin/gonas-console" /usr/local/sbin/gonas-console
    chmod 0755 /usr/local/sbin/gonas-console
    cp -a "$OVERLAY_DIR/etc/systemd/system/gonas-console.service" /etc/systemd/system/gonas-console.service
    cp -a "$OVERLAY_DIR/etc/motd" /etc/motd
    cp -a "$OVERLAY_DIR/etc/issue" /etc/issue
else
    log "WARNING: overlay directory $OVERLAY_DIR not found — skipping console/branding files. This ISO was built incorrectly."
fi

# 停用/遮罩預設的 tty1 登入提示，換成我們的狀態主控台——tty2 以後
# 維持系統原本的 getty 登入，保留一個「找一台真機除錯」的正常管道，
# 見 gonas-console.service 的說明。這三個 systemctl 呼叫原本用
# `2>/dev/null || true` 完全吞掉結果——跟前面 gonas.service 那個真的
# 踩到的坑一樣的道理:如果這幾個呼叫在某個 Debian 版本的 in-target
# chroot 環境裡也一樣悄悄失敗,原本的寫法會讓人完全看不出來、只會在
# 開機後發現 tty1 還是一般登入畫面才回頭猜是哪裡的問題。改成明確記錄
# 每一步的成功/失敗,第一次真機/VM 測試時直接看這份 log 就知道是不是
# 也踩到同一類問題,不用用猜的。
if systemctl disable getty@tty1.service 2>/dev/null; then
    log "getty@tty1.service disabled"
else
    log "WARNING: 'systemctl disable getty@tty1.service' failed — tty1 may still show the normal login prompt"
fi
if systemctl mask getty@tty1.service 2>/dev/null; then
    log "getty@tty1.service masked"
else
    log "WARNING: 'systemctl mask getty@tty1.service' failed"
fi
if systemctl enable gonas-console.service 2>/dev/null; then
    log "gonas-console.service enabled"
else
    log "WARNING: 'systemctl enable gonas-console.service' failed — tty1 status console will NOT appear after reboot"
fi

# --- 3. 品牌化 ------------------------------------------------------
echo "gonas" > /etc/hostname
if [ -f /etc/hosts ]; then
    if grep -q '^127\.0\.1\.1[[:space:]]' /etc/hosts; then
        sed -i "s/^127\.0\.1\.1[[:space:]].*/127.0.1.1\tgonas/" /etc/hosts || true
    else
        # 有些最小化安裝的 /etc/hosts 可能根本沒有 127.0.1.1 這一行
        # (原本的 sed 只會在有這一行時才替換,沒有的話就靜默不做
        # 任何事)——這裡改成沒有的話就直接補上一行,而不是假設一定
        # 存在,純粹是本機主機名稱解析的細節,不影響 gonasd 本身監聽
        # 0.0.0.0 對外提供服務。
        echo "127.0.1.1	gonas" >> /etc/hosts
    fi
fi

# /etc/os-release 只改 NAME/PRETTY_NAME 這兩個「給人看」的欄位，
# ID/ID_LIKE 刻意不動——apt、systemd-detect-virt 之類的工具靠 ID/
# ID_LIKE 判斷這是不是 Debian 系統，改了反而可能讓套件管理或未來
# gonasd 自我更新之類的邏輯誤判平台，這裡的目標單純是「使用者打開
# 一個終端機、看 neofetch/hostnamectl 之類的工具時,看到的品牌名稱
# 是 GoNAS」，不是假裝這不是 Debian。
if [ -f /etc/os-release ]; then
    sed -i \
        -e 's/^NAME=.*/NAME="GoNAS"/' \
        -e 's/^PRETTY_NAME=.*/PRETTY_NAME="GoNAS (Debian GNU\/Linux)"/' \
        /etc/os-release || true
fi

# GRUB 開機選單標題品牌化——GRUB_DISTRIBUTOR 是 update-grub 產生選單
# 項目標題時使用的名稱來源（`/etc/grub.d/10_linux` 會呼叫
# `grub-mkconfig`裡的 lsb_release/os-release 邏輯組出「Debian GNU/
# Linux」這種字串，蓋掉 GRUB_DISTRIBUTOR 能直接換成我們要的名稱）。
if [ -f /etc/default/grub ]; then
    if grep -q '^GRUB_DISTRIBUTOR=' /etc/default/grub; then
        sed -i 's/^GRUB_DISTRIBUTOR=.*/GRUB_DISTRIBUTOR="GoNAS"/' /etc/default/grub
    else
        echo 'GRUB_DISTRIBUTOR="GoNAS"' >> /etc/default/grub
    fi
    # 開機選單預設不用等使用者按鍵、也不用刻意顯示選單——一台家用/
    # 小型辦公室 NAS 開機應該直接進系統，不需要每次開機都看到 GRUB
    # 選單，跟 Unraid/TrueNAS 的開機體驗一致。GRUB_TIMEOUT=0 加上
    # GRUB_TIMEOUT_STYLE=hidden 是標準組合，維持一個很短的按鍵視窗
    # （硬體/韌體通常仍會給使用者一個機會用方向鍵中斷，例如 BIOS 機器
    # 在開機當下按住 Shift、或 UEFI 機器按 Esc，是 GRUB 本身的行為，
    # 這裡不用另外處理）。
    #
    # 這裡曾經是一個「上面的中文註解講的是一回事、底下實際設的值是
    # 另一回事」的真 bug:第七輪覆閱之前寫的是 `GRUB_TIMEOUT=3` 加上
    # `GRUB_TIMEOUT_STYLE=menu`——跟註解說要做到的「開機不用看到選單、
    # 直接進系統」剛好相反,`menu` 樣式會讓完整的 GRUB 選單畫面在每次
    # 開機時都顯示 3 秒,而不是隱藏、直接開機。這是靠回頭逐字重讀這段
    # 註解跟緊接著的程式碼、發現兩者互相矛盾才抓到的,不是靠執行測試
    # (這段是真正需要 GRUB/韌體環境才能觀察效果的邏輯,這個沙盒沒有
    # 辦法執行驗證),所以修法直接照註解原本描述的設計意圖修正。
    sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub || echo 'GRUB_TIMEOUT=0' >> /etc/default/grub
    if grep -q '^GRUB_TIMEOUT_STYLE=' /etc/default/grub; then
        sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=hidden/' /etc/default/grub
    else
        echo 'GRUB_TIMEOUT_STYLE=hidden' >> /etc/default/grub
    fi
    update-grub 2>/dev/null || grub-mkconfig -o /boot/grub/grub.cfg 2>/dev/null || \
        log "WARNING: could not regenerate grub.cfg automatically; verify manually on first boot"
fi

# --- 4. 強制第一次登入就要換掉 gonasadmin 的預設密碼 -----------------
# preseed.cfg 裡 `gonasadmin` 帳號的密碼是寫死的明文佔位密碼
# (`gonas-change-me-now`),原本只在文件裡提醒「正式使用前務必自己
# 改掉」,但機器一開機、只要接上網路,這組密碼透過 SSH 就是立刻
# 可以被嘗試的——「文件提醒」跟「技術上強制」是兩回事,重新覆閱時
# 覺得這裡值得做得更確實一點,而且做法完全沒有副作用:用
# `chage -d 0` 把這個帳號的密碼「上次變更日期」設成第 0 天,這是
# shadow/passwd 工具鏈的標準做法(等同 `passwd --expire`),效果是
# 系統會判定這組密碼已經過期,下一次不管是透過 SSH 還是 tty2 主控台
# 登入,都會先被要求立刻設一組新密碼才能拿到 shell——不會擋掉正常的
# 第一次登入,只是把「换掉预设密码」從一個使用者可能忘記做的提醒,
# 變成一個做不到就進不去 shell 的強制步驟。
if command -v chage >/dev/null 2>&1; then
    if chage -d 0 gonasadmin 2>/dev/null; then
        log "gonasadmin password marked as expired — first login will require setting a new password"
    else
        log "WARNING: 'chage -d 0 gonasadmin' failed — the default placeholder password will NOT be forced to change on first login, change it manually as soon as possible"
    fi
else
    log "WARNING: 'chage' not available — could not force a password change on first login for gonasadmin"
fi

log "GoNAS appliance branding complete."
