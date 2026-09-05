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
#      GoNAS」用的是同一套安裝腳本，差別只在誰、什麼時候執行它）。
#   2. 佈署 tty1 狀態主控台（gonas-console.service）取代預設的登入
#      提示，這是使用者選的「開機後精簡狀態畫面」路線。
#   3. 品牌化：主機名稱、/etc/motd、/etc/issue、/etc/os-release 的
#      PRETTY_NAME、GRUB 開機選單標題。
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
ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"

log() { echo "[gonas-late-command] $*"; }

log "install media: $INSTALL_MEDIA"
log "target architecture: $ARCH"

# --- 1. 安裝 gonasd 本體 -----------------------------------------
# build-iso.sh 會把對應架構的 release tarball 解壓到
# $GONAS_DIR/release-$ARCH/ 底下（跟使用者手動下載 tarball 解壓縮後的
# 目錄結構完全一樣：gonasd/install.sh/uninstall.sh/gonas.service）,
# 所以這裡直接呼叫同一份 install.sh，不需要網路連線 —— 這也是為什麼
# 這個 ISO 可以做到「離線安裝」，不像一般 debian-installer netinst
# 映像那樣還需要在安裝過程連網抓套件。
RELEASE_DIR="$GONAS_DIR/release-$ARCH"
if [ -x "$RELEASE_DIR/install.sh" ]; then
    log "installing gonasd from $RELEASE_DIR"
    ( cd "$RELEASE_DIR" && ./install.sh )
else
    log "WARNING: $RELEASE_DIR/install.sh not found or not executable — gonasd was NOT installed. This ISO was built incorrectly."
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
# 見 gonas-console.service 的說明。
systemctl disable getty@tty1.service 2>/dev/null || true
systemctl mask getty@tty1.service 2>/dev/null || true
systemctl enable gonas-console.service 2>/dev/null || true

# --- 3. 品牌化 ------------------------------------------------------
echo "gonas" > /etc/hostname
if [ -f /etc/hosts ]; then
    sed -i "s/127.0.1.1.*/127.0.1.1\tgonas/" /etc/hosts || true
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
    # （硬體/韌體通常仍會給使用者一個機會用方向鍵中斷）。
    sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=3/' /etc/default/grub || echo 'GRUB_TIMEOUT=3' >> /etc/default/grub
    if grep -q '^GRUB_TIMEOUT_STYLE=' /etc/default/grub; then
        sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=menu/' /etc/default/grub
    else
        echo 'GRUB_TIMEOUT_STYLE=menu' >> /etc/default/grub
    fi
    update-grub 2>/dev/null || grub-mkconfig -o /boot/grub/grub.cfg 2>/dev/null || \
        log "WARNING: could not regenerate grub.cfg automatically; verify manually on first boot"
fi

log "GoNAS appliance branding complete."
