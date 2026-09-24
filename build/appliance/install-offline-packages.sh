#!/bin/sh
# install-offline-packages.sh —— 在「安裝程式環境」(不是 in-target chroot)裡
# 執行,趁安裝 DVD 還掛在 /cdrom 的時候,用 DVD 當一個臨時的 apt 來源,把
# GoNAS 的選用相依套件(mergerfs / snapraid / samba / nfs / smartmontools /
# wireguard-tools / rsync / docker.io)離線裝進剛裝好的目標系統。
#
# 為什麼要這一步(第五十五輪,使用者實機):appliance 是「離線安裝」的
# (preseed apt-setup/use_mirror false),而這台 NAS 開機後常常沒有對外網路。
# 沒有這一步的話,samba/mergerfs 這些要靠開機後用 apt 補裝——離線就裝不了,
# 使用者實測就卡在「E: Unable to locate package samba」跟「mergerfs: executable
# file not found」。改成安裝時就從 DVD 把它們裝好,開機即用、完全不需要網路。
#
# 三個關鍵設計:
#   1) 全程「非致命」:這支腳本一定 exit 0,每一個步驟都吞掉錯誤。任何一個
#      套件裝不起來(例如那個套件剛好不在這片 DVD 上)都只是略過、記 log,
#      絕不讓整個安裝失敗——寧可少裝一個套件,也不要毀掉整台機器的安裝。
#   2) 用 apt(不是自己 dpkg -i):apt 會從 DVD 自動解相依,不用自己算封閉集
#      (第十九輪那套自己算相依的做法很脆、第二十八輪已經廢掉)。
#   3) 只用 DVD、不碰網路:臨時 apt 來源指向 bind-mount 進 chroot 的 DVD,
#      裝完就把這個臨時來源移除、把 DVD 卸載乾淨,不會在開機後留下一個指向
#      已退片 DVD 的壞來源(那會讓開機後的 apt update 出錯)。
#
# 由 preseed.cfg 的 late_command 以 `sh /cdrom/gonas/install-offline-packages.sh`
# 呼叫(在複製 gonas/ 目錄、跑 late-command.sh 之前),此時 /cdrom 保證還掛著。

# 刻意「不」用 set -e:這支腳本的整個精神就是 best-effort,任何一步失敗都要
# 繼續走完、最後乾淨收尾。

PKGS="mergerfs snapraid samba nfs-kernel-server smartmontools wireguard-tools rsync docker.io"

LOG=/target/var/log/gonas-offline-packages.log
mkdir -p /target/var/log 2>/dev/null || true
log() { echo "[gonas-offline-pkgs] $*" >> "$LOG" 2>/dev/null; }

log "starting offline optional-package install from the DVD"

# 沒有 DVD 套件庫就直接跳過(例如用 netinst 建的、或 DVD 已退片)。DVD 安裝
# 媒體的 apt 倉庫結構是 /cdrom/dists/<codename>/... + /cdrom/pool/。
if [ ! -d /cdrom/dists ] || [ ! -d /cdrom/pool ]; then
    log "no DVD package repository at /cdrom (need both dists/ and pool/) — skipping. Install optional packages later from the System Doctor page (needs network)."
    exit 0
fi

# 版本代號:先讀目標系統的,讀不到再退回安裝程式環境的。
CODENAME="$(. /target/etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-}")"
[ -n "$CODENAME" ] || CODENAME="$(. /etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-}")"
if [ -z "$CODENAME" ]; then
    log "could not detect the Debian codename — skipping offline package install"
    exit 0
fi
log "detected codename: $CODENAME"

# 把 DVD bind-mount 進 /target,讓 in-target(會 chroot 進 /target)裡的 apt
# 看得到它(路徑在 chroot 裡是 /media/gonas-dvd)。busybox 的 mount 兩種寫法
# 都試一次。
mkdir -p /target/media/gonas-dvd 2>/dev/null || true
if ! mount --bind /cdrom /target/media/gonas-dvd 2>/dev/null; then
    if ! mount -o bind /cdrom /target/media/gonas-dvd 2>/dev/null; then
        log "could not bind-mount the DVD into the target — skipping offline package install"
        rmdir /target/media/gonas-dvd 2>/dev/null || true
        exit 0
    fi
fi

# 臨時 apt 來源(trusted=yes:本機掛載的 DVD,略過簽章檢查,簡單可靠)。
printf 'deb [trusted=yes] file:/media/gonas-dvd %s main contrib\n' "$CODENAME" \
    > /target/etc/apt/sources.list.d/gonas-dvd.list 2>/dev/null || true

# 讀取 DVD 的套件索引(不抓翻譯檔,快一點)。失敗也繼續。
in-target apt-get -o Acquire::Languages=none update >> "$LOG" 2>&1 \
    || log "apt-get update against the DVD reported an error (continuing anyway)"

INSTALLED=""
SKIPPED=""
for pkg in $PKGS; do
    if in-target env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$pkg" >> "$LOG" 2>&1; then
        log "installed: $pkg"
        INSTALLED="$INSTALLED $pkg"
    else
        log "NOT installed (probably not on this DVD): $pkg — install it later from the System Doctor page (needs network)"
        SKIPPED="$SKIPPED $pkg"
    fi
done
log "summary — installed:${INSTALLED:- none}; skipped:${SKIPPED:- none}"

# 收尾:移掉臨時來源、卸載 DVD bind-mount,不要在開機後留下指向已退片 DVD
# 的壞 apt 來源。late-command.sh 第 3.5 節之後會再寫上網路 apt 來源。
rm -f /target/etc/apt/sources.list.d/gonas-dvd.list 2>/dev/null || true
# 順手把從 DVD 抓來的套件索引清掉,避免開機後 apt update 前殘留指向 DVD 的清單。
rm -f /target/var/lib/apt/lists/*gonas-dvd* 2>/dev/null || true
umount /target/media/gonas-dvd 2>/dev/null || umount -l /target/media/gonas-dvd 2>/dev/null || true
rmdir /target/media/gonas-dvd 2>/dev/null || true

log "offline optional-package install finished"
exit 0
