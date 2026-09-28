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
#   4. 強制 gonas 這組緊急備援帳號的預設密碼在第一次登入時就要
#      被換掉，而不是只在文件裡提醒使用者自己記得改。
#
# 重要：這支腳本目前完全沒有在真正的 Debian 安裝程式環境裡執行過
# （這個開發沙盒的網路白名單擋掉了 deb.debian.org，連 apt-get update
# 都會被擋下來，見 build/appliance/README.md 的完整說明），是依照
# Debian 官方文件與慣例撰寫、邏輯上自洽，但尚未經過真實驗證——使用者
# 在正式燒錄安裝到硬體之前，務必先在虛擬機（QEMU/VirtualBox）裡完整
# 跑一次，見 README.md「如何驗證」一節。

set -e

# INSTALL_MEDIA 是這支腳本自己（跟 release tarball、debs/、overlay/）
# 實際能找到的路徑。**不是** /cdrom 本身——第二十三輪的實機測試(ESXi)
# 抓到「/cdrom 掛載點不保證在 in-target chroot 裡看得到」這件事之後,
# preseed.cfg 的 late_command 改成先在安裝程式自己的環境裡（此時
# /cdrom 保證還掛著）把整個 gonas/ 目錄複製進 /target/var/lib/
# gonas-install/gonas,這支腳本再用 in-target 從那份複製好的檔案執行
# ——這裡預設值改成那個複製後的位置，不是 /cdrom，理由見 preseed.cfg
# 裡 late_command 那一行完整的說明。用環境變數讓呼叫端可以覆寫，方便
# 手動除錯時指到別的路徑測試,不用寫死。
INSTALL_MEDIA="${GONAS_INSTALL_MEDIA:-/var/lib/gonas-install}"
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
# 第二十四輪覆閱(這次是自己主動再查一輪,不是使用者回報):這一行
# 原本沒有 `|| true`,而下面特地寫的「dpkg 不可用時 fallback 到 uname
# -m」邏輯,其實根本不會被執行到——在 `set -e` 底下,`VAR="$(cmd)"`
# 這種賦值句,如果 cmd 本身失敗,賦值句自己的結束碼就是 cmd 的結束碼,
# 一樣會被 `set -e` 當成「這一行失敗了」直接中止整支腳本,不會等到
# 下面 `if [ -z "$ARCH" ]` 才處理——實測驗證過:`set -e` 底下
# `ARCH="$(false)"` 這一行本身就會讓腳本直接死掉,`echo "got here"`
# 印都印不出來。等於下面整段精心設計的 fallback,只要 dpkg 這次真的
# 不可用,反而完全沒有機會執行到,又是同一支腳本因為某個沒有涵蓋到的
# `set -e` 陷阱,在早期步驟悄悄死掉、後面的品牌化/強制改密碼全部沒
# 機會跑——跟前面幾輪抓到的好幾個 bug 是同一種模式。修法:command
# substitution 裡面自己補一個 `|| true`,讓賦值句本身一定成功,真正
# 「dpkg 失不失敗」的判斷交給下面既有的 `[ -z "$ARCH" ]` 檢查,這樣
# 這段本來就寫好的 fallback 邏輯才真的有機會被執行到。
ARCH="$(dpkg --print-architecture 2>/dev/null || true)"
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
# 所以這裡直接呼叫同一份 install.sh，不需要網路連線 —— gonasd 本體
# 來自 ISO 上內嵌的 release tarball,加上 openssh-server/sudo 由 pkgsel
# 從 DVD-1 的套件庫離線裝好,整個安裝過程完全不需要連網。
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

# --- 1.7. SSH 加固(openssh-server 已由 preseed 的 pkgsel 從 DVD 裝好)---
# 第二十八輪(換成 DVD-1 完整版):openssh-server / sudo 已經由
# preseed.cfg 的 `pkgsel/include` 在安裝階段從 DVD-1 離線裝好了(不再
# 需要前幾輪那套自己打包 .deb 再 dpkg -i 的脆弱做法,那一整段連同
# build-iso.sh 4.5 節、lib/deb-closure.sh 都已刪除)。這裡只做「裝好
# 之後的加固/確認」,不再負責安裝本身。整段仍是 best-effort:SSH 是
# 遠端管理的便利/救援管道,不是 appliance 核心(核心是 gonasd + Web
# 介面 + tty 主控台),每個可能失敗的指令都用 `if`/`|| true` 擋住,
# 不讓它有能力用 `set -e` 拖垮整支 late-command.sh(第十九輪的教訓)。
#
# 用 sshd 這個執行檔存不存在判斷「SSH 到底裝了沒」——理論上 pkgsel
# 一定裝好了,但萬一 DVD 上真的沒有(不該發生)或 pkgsel 出了狀況,
# 這裡優雅跳過、留一行明確的 log,不硬做。
if [ -x /usr/sbin/sshd ] || command -v sshd >/dev/null 2>&1; then
    log "openssh-server present (installed from DVD by pkgsel) — applying SSH hardening"
    # 保險:確保 SSH host key 真的產生了。openssh-server 的 postinst
    # 正常會自己跑 `ssh-keygen -A`,但那是在「沒有真正在跑的 systemd、
    # 裝置節點可能不完整」的 in-target chroot 環境裡執行的,不保證每次
    # 都成功;這裡再明確補跑一次(ssh-keygen -A 是冪等的——已經存在的
    # host key 不會重新產生),沒有 host key 的話 sshd 開機會起不來。
    if command -v ssh-keygen >/dev/null 2>&1; then
        ssh-keygen -A >/dev/null 2>&1 || true
    fi
    # 確保 SSH 服務開機自動啟動(postinst 通常已經 enable 過,這裡再補
    # 一次確保;ssh / sshd 兩種 unit 名稱都試一下)。
    if systemctl enable ssh 2>/dev/null || systemctl enable sshd 2>/dev/null; then
        log "ssh service enabled for boot"
    else
        log "WARNING: could not confirm the ssh service is enabled for boot — check with 'systemctl is-enabled ssh' after first boot"
    fi
    # 第二十七輪(使用者實測回報「IP 通但 SSH 連不上」)補上的加固:寫一份
    # sshd drop-in,明確保證「用密碼登入」跟「密碼過期時在 SSH 上完成強制
    # 改密碼」這兩件事一定開著。背景:第 4 節會用 `chage -d 0 gonas` 把
    # gonas 帳號的密碼標記為過期,強制第一次登入就要改密碼——但「密碼
    # 已過期的帳號透過 SSH 登入時,能不能當場走完『輸入舊密碼→設定新密碼』
    # 這個對話」,取決於 sshd 有沒有開 PasswordAuthentication + PAM +
    # keyboard-interactive。這三個值本來就是 Debian openssh-server 的預設,
    # 但預設值是「可能被其他 drop-in 或未來版本改掉」的東西,而這個帳號
    # 是使用者實體接觸不到機器時、唯一能遠端進去救援的管道,值得明確釘死
    # 一份自己的 drop-in,不賭預設值。放在 /etc/ssh/sshd_config.d/(現代
    # openssh-server 的 sshd_config 預設有 `Include
    # /etc/ssh/sshd_config.d/*.conf`),檔名用數字前綴讓它排在後面、蓋過
    # 其他可能把這幾項關掉的設定。best-effort:寫不成只記警告,不中止。
    if [ -d /etc/ssh ]; then
        mkdir -p /etc/ssh/sshd_config.d 2>/dev/null || true
        if cat > /etc/ssh/sshd_config.d/60-gonas.conf <<'EOF' 2>/dev/null
# GoNAS appliance —— 確保 gonas 這組緊急備援維運帳號能用密碼透過 SSH
# 登入,並且在第一次登入被強制改密碼(late-command.sh 的 chage -d 0)時,
# 能在 SSH 連線上當場完成改密碼流程。這幾個值本來就是 Debian 的預設,
# 明確寫死是為了不被未來預設值變動或其他 drop-in 蓋掉。
PasswordAuthentication yes
KbdInteractiveAuthentication yes
UsePAM yes
EOF
        then
            log "wrote /etc/ssh/sshd_config.d/60-gonas.conf (password + forced-change login over SSH guaranteed)"
        else
            log "WARNING: could not write sshd drop-in — if the first SSH login with the expired 'gonas' password is rejected, log in on tty2 to change it first"
        fi
    fi
    # 確保 gonas 真的在 sudo 群組裡:preseed 的 user-setup 會在建立帳號
    # 時把它加進 `sudo` 群組,sudo 套件也已由 pkgsel 從 DVD 裝好,這裡
    # 再 `usermod -aG` 補一次確保,冪等、失敗也不影響。
    if command -v usermod >/dev/null 2>&1; then
        usermod -aG sudo gonas 2>/dev/null || true
    fi
else
    log "WARNING: openssh-server not found on the target — expected pkgsel to install it from the DVD. Remote SSH will be unavailable until you 'apt install openssh-server' after first boot (needs network)."
fi

# --- 1.8. 預先建立 Web 介面的預設 admin 帳號(gonas/gonas)----------------
# 第十九輪:使用者要求 Web 超級管理員也有一組好記的預設帳密 gonas/gonas。
# gonasd 的 `-seed-default-admin` 會在「還完全沒有任何 Web 管理帳號」時
# 建立一組 admin(帳密皆 gonas),並標記為「第一次登入必須改密碼」——
# 這樣開機後可以直接用 gonas/gonas 登入 Web 介面,不用先走一次首次設定
# 建立帳號流程,但預設密碼一登入就會被強制改掉(見 cmd/gonasd 的
# -seed-default-admin 與 internal/api requireAdmin 的強制邏輯)。冪等:
# 已經有帳號就不動它。best-effort——失敗只記警告,不中止整支腳本
# (Web 帳號使用者也可以開機後自己在瀏覽器首次設定,不是核心開機功能)。
# 明確帶 GONAS_DATA_DIR=/var/lib/gonas,跟 gonas.service 用的資料目錄
# 一致,確保 seed 寫進的 state.json 就是 daemon 開機後會讀的那一份。
if command -v gonasd >/dev/null 2>&1; then
    if GONAS_DATA_DIR=/var/lib/gonas gonasd -seed-default-admin >/dev/null 2>&1; then
        log "seeded default web admin (gonas/gonas, must change password on first login)"
    else
        log "WARNING: 'gonasd -seed-default-admin' failed — no preset web admin; you can still create one via the web UI first-run setup on first boot"
    fi
else
    log "WARNING: gonasd not on PATH — could not seed default web admin"
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

# --- 3.5. 修好安裝後的 apt 套件來源(離線安裝的後遺症) ----------------
# 第十九輪覆閱全面排查時發現的一個真實缺口:這個 appliance 是「完全
# 離線安裝」(preseed 的 apt-setup/use_mirror false),結果 d-i 產生的
# apt 套件來源只會指向安裝媒體(光碟/USB)本身——而 preseed 又設了
# cdrom-detect/eject,裝完重開機之後那份媒體邏輯上已經退出,apt 的
# 來源等於指著一個不存在的光碟。這會直接打臉 README 講的「開機、機器
# 有網路之後,透過 Doctor 頁面或 apt install 補裝 mergerfs/samba/docker
# 這些選用相依套件」——因為在修好套件來源之前,`apt update`/`apt
# install` 會因為讀不到那份已經退出的光碟而失敗。
#
# 修法:裝完之後直接寫一份指向 deb.debian.org 網路鏡像的
# /etc/apt/sources.list(main + updates + security),並把任何還指著
# cdrom 的舊來源檔案移開(Debian 13 預設用 deb822 格式的
# /etc/apt/sources.list.d/debian.sources,舊格式是 /etc/apt/sources.list
# ——用「檔案內容有沒有出現 cdrom:」判斷,兩種格式都涵蓋得到,不必
# 分別處理)。整段 best-effort,失敗只記 log、不中止。
#
# 版本代號直接從目標系統的 /etc/os-release 讀 VERSION_CODENAME(前面
# 品牌化只改了 NAME/PRETTY_NAME,沒動這個欄位),這樣不管裝的是 trixie
# 還是之後的版本都對得上,不用寫死。
GONAS_CODENAME="$(. /etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-}")"
if [ -n "$GONAS_CODENAME" ]; then
    # 把任何還指著 cdrom 的來源檔案移開,避免 apt update 讀不到已退出的
    # 光碟而報錯(兩種格式都靠「內容含 cdrom:」判斷)。
    for _apt_src in /etc/apt/sources.list /etc/apt/sources.list.d/*; do
        [ -f "$_apt_src" ] || continue
        if grep -qi 'cdrom:' "$_apt_src" 2>/dev/null; then
            mv "$_apt_src" "$_apt_src.disabled-by-gonas" 2>/dev/null || true
            log "disabled cdrom-based apt source: $_apt_src"
        fi
    done
    if cat > /etc/apt/sources.list <<EOF
deb http://deb.debian.org/debian $GONAS_CODENAME main contrib non-free-firmware
deb http://deb.debian.org/debian $GONAS_CODENAME-updates main contrib non-free-firmware
deb http://security.debian.org/debian-security $GONAS_CODENAME-security main contrib non-free-firmware
EOF
    then
        log "wrote network apt sources.list for '$GONAS_CODENAME' (offline install left apt pointing only at the ejected install media)"
    else
        log "WARNING: could not write /etc/apt/sources.list — after first boot, 'apt update'/'apt install' may fail until you add a network mirror manually"
    fi
else
    log "WARNING: could not detect VERSION_CODENAME from /etc/os-release — left apt sources as-is; 'apt install' may not work until you configure a network mirror manually"
fi

# --- 3.6. 把內建的離線 .deb 設成本機 apt 來源(離線裝 mergerfs/samba 等)---
# 第五十八輪(使用者:「這幾個軟件你為什麼還需要聯網你不做成離線安裝的?」)。
# build-iso.sh 第 4.8 節會在建置期(Mac 有網路)把 DVD 上沒有的選用套件
# (mergerfs/samba/snapraid/nfs-kernel-server/wireguard-tools/docker.io)連同
# 相依封閉集抓下來,攤平成一個 flat repo 放在安裝媒體的 gonas/debs/(含一份
# Packages 索引)。這裡把它複製到目標系統的 /var/lib/gonas/debs/,並加一條
# 本機 `file://` apt 來源——這樣使用者開機後(不管有沒有網路)在 Web 介面
# Doctor 點「安裝 mergerfs/samba/…」時,apt 就能從這份本機來源離線裝好。
#
# 關鍵設計:
#   - `[trusted=yes]`:本機 flat repo 沒有 GPG 簽章,明確標記為信任,apt 才
#     不會因為「來源未簽章」而拒裝。這只影響這一個本機來源,不動 Debian 官方
#     來源的簽章驗證。
#   - flat repo 寫法 `... /var/lib/gonas/debs ./`(結尾的 `./`)——Packages
#     索引就放在該目錄根部,不是 dists/ 那種階層式結構。
#   - 跟第 3.5 節寫的「網路鏡像來源」並存:離線時走這份本機來源,有網路時
#     apt 也能照樣走網路(對齊使用者要的「兩者都要」)。
#   - 這裡「不」主動跑 apt-get update:Web Doctor 的一鍵安裝在 apt-get install
#     之前本來就會先跑一次 apt-get update(見 internal/api/doctor_handlers.go),
#     那一次就會把這份本機來源的索引讀進 apt(file:// 來源不需要網路,就算
#     同時設定的網路來源因離線而更新失敗,也不影響本機來源被正確索引)。
#     在這個 in-target chroot 階段不主動連網,安裝流程維持完全離線、不會卡在
#     等網路。
# 整段 best-effort:沒有 debs/ 目錄(建置期沒抓成/被跳過)就什麼都不做;
# 任何一步失敗只記警告,不影響安裝結果。
OFFLINE_DEBS_SRC="$GONAS_DIR/debs"
OFFLINE_DEBS_DEST=/var/lib/gonas/debs
if [ -d "$OFFLINE_DEBS_SRC" ] && [ -f "$OFFLINE_DEBS_SRC/Packages" ]; then
    if mkdir -p "$OFFLINE_DEBS_DEST" && cp -a "$OFFLINE_DEBS_SRC/." "$OFFLINE_DEBS_DEST/"; then
        # 讓 apt 的沙盒使用者 _apt 也讀得到(否則 apt 會印一行囉嗦的
        # 「Download is performed unsandboxed as root … Permission denied」提示
        # ——那只是 Notice、apt 會改用 root 讀、安裝照樣成功,但清掉比較乾淨)。
        chmod -R a+rX "$OFFLINE_DEBS_DEST" 2>/dev/null || true
        _deb_n="$(find "$OFFLINE_DEBS_DEST" -name '*.deb' 2>/dev/null | grep -c . || echo 0)"
        log "copied $_deb_n bundled offline .deb(s) to $OFFLINE_DEBS_DEST"
        if printf 'deb [trusted=yes] file://%s ./\n' "$OFFLINE_DEBS_DEST" > /etc/apt/sources.list.d/gonas-offline.list 2>/dev/null; then
            log "registered local offline apt source (/etc/apt/sources.list.d/gonas-offline.list) — mergerfs/samba/etc. can be installed with NO network via the web Doctor"
        else
            log "WARNING: could not write /etc/apt/sources.list.d/gonas-offline.list — the bundled .debs are in $OFFLINE_DEBS_DEST but apt won't see them until you add that source or run 'dpkg -i $OFFLINE_DEBS_DEST/*.deb' manually"
        fi
    else
        log "WARNING: could not copy bundled offline .debs to $OFFLINE_DEBS_DEST — optional packages will need network to install"
    fi
else
    log "no bundled offline .deb repo on the install media (gonas/debs/ absent) — optional packages (mergerfs/samba/etc.) will need network to install; this is expected if the ISO was built with GONAS_SKIP_OFFLINE_DEBS=1 or the build machine had no network"
fi

# --- 4. 強制第一次登入就要換掉 gonas 的預設密碼 -----------------
# preseed.cfg 裡 `gonas` 帳號的密碼是寫死的明文預設值(也是 `gonas`),
# 原本只在文件裡提醒「正式使用前務必自己
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
    if chage -d 0 gonas 2>/dev/null; then
        log "gonas password marked as expired — first login will require setting a new password"
    else
        log "WARNING: 'chage -d 0 gonas' failed — the default placeholder password will NOT be forced to change on first login, change it manually as soon as possible"
    fi
else
    log "WARNING: 'chage' not available — could not force a password change on first login for gonas"
fi

# --- 5. 清掉安裝過程複製到系統上的暫存安裝媒體副本 -------------------
# 第二十五輪覆閱(主動再查一輪,不是使用者回報)發現的問題:第二十三輪
# 修「in-target chroot 裡看不到 /cdrom」那個 bug 時,preseed.cfg 的
# late_command 改成把整個 /cdrom/gonas 先複製一份到
# /target/var/lib/gonas-install/gonas,這支腳本再從那份複製好的檔案
# 執行(見上面 INSTALL_MEDIA 的說明)——當時只顧著讓 late-command.sh
# 找得到檔案來修好「安裝失敗」這個立即可見的症狀,沒有想到這份複製品
# 裝完之後會永久留在目標系統的磁碟上,沒有任何一步會清掉它:裡面有
# gonasd 執行檔(跟已經裝到 /usr/local/bin/gonasd 的那份完全重複)、
# 離線 SSH 用的 .deb 套件(如果有打包的話)、overlay/ 目錄、以及
# preseed.cfg 本身的副本。不是「安裝失敗」那種立即可見的症狀,是每次
# 安裝都會多佔用一些磁碟空間、且在系統上留一份不必要的安裝期檔案的
# 衛生問題——對一台系統碟空間可能吃緊的家用 NAS appliance 而言,不應該
# 平白浪費,裡面那份 preseed.cfg 副本也沒有理由留著(帳號密碼是文件裡
# 公開記載的預設值 gonas/gonas,不是新的外洩,但同樣沒有理由留著多一份)。
#
# 只在 INSTALL_MEDIA 還是預設值時才清——代表這次確實是 late_command
# 自動複製出來的那份;如果使用者手動把 GONAS_INSTALL_MEDIA 指到別的
# 路徑做除錯,通常就是刻意要保留下來事後檢查,不應該被這裡自動清掉。
#
# 刻意放在這支腳本真正的最後一步:這支腳本本身就是從
# $INSTALL_MEDIA/gonas/late-command.sh 被 `sh` 讀取執行的,在 Linux 上
# unlink 一個仍然開著讀取中的檔案是安全的(inode 在檔案描述子關閉前都
# 還在,不會讓正在執行中的這支腳本自己中斷),但保守起見還是等後面
# 沒有任何指令再需要讀這個目錄底下任何檔案(lib/、overlay/、release-
# $ARCH/、debs/ 全部都已經用完)之後才刪,不提早刪。整段 best-effort,
# 失敗只記警告、不影響安裝結果——清不掉暫存檔案不該讓整個安裝被判定
# 為失敗。
if [ "$INSTALL_MEDIA" = "/var/lib/gonas-install" ]; then
    if rm -rf "$INSTALL_MEDIA" 2>/dev/null; then
        log "cleaned up temporary install-media copy at $INSTALL_MEDIA"
    else
        log "WARNING: could not remove temporary install-media copy at $INSTALL_MEDIA — safe to delete manually later (contains a duplicate gonasd binary, any bundled .deb packages, and a copy of preseed.cfg)"
    fi
else
    log "GONAS_INSTALL_MEDIA was overridden to a non-default path ($INSTALL_MEDIA) — leaving it in place (assuming this is a manual debugging run)"
fi

log "GoNAS appliance branding complete."
