#!/bin/sh
# test-scan-pool-packages.sh —— lib/scan-pool-packages.sh 的離線回歸測試,外加
# 驗證「把找到的套件補進 pkgsel/include」那句 sed 改寫是對的。
#
# 完全不需要真的 DVD/apt:用一棵假的 pool/ 目錄樹(裡面放幾個假的 .deb 檔名)
# 餵給 build-iso.sh 實際會用的同一個 gonas_scan_pool_packages(),檢查:
#   - 找得到的套件要回報(含 docker.io 這種名字裡有點的)
#   - 前綴要精準:pool 裡有 samba-common 但沒有 samba 時,不能把 samba 當成有
#   - 不在 pool 裡的套件不回報
#   - pkgsel/include 那一句 sed 改寫後,確實把找到的套件接在 openssh-server sudo 後面

set -eu
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/lib/portable-sed.sh"
. "$SCRIPT_DIR/lib/scan-pool-packages.sh"

WORK="$(mktemp -d /tmp/gonas-scan-test.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
FAIL=0

# 假 pool:有 mergerfs / samba / nfs-kernel-server / rsync / docker.io / samba-common,
# 故意「沒有」snapraid、wireguard-tools、smartmontools。
mkdir -p "$WORK/pool/main/m/mergerfs" "$WORK/pool/main/s/samba" "$WORK/pool/main/n/nfs-kernel-server" \
         "$WORK/pool/main/r/rsync" "$WORK/pool/main/d/docker.io" "$WORK/pool/main/s/samba-common"
: > "$WORK/pool/main/m/mergerfs/mergerfs_2.33.5-1_amd64.deb"
: > "$WORK/pool/main/s/samba/samba_4.17.12+dfsg-0_amd64.deb"
: > "$WORK/pool/main/n/nfs-kernel-server/nfs-kernel-server_2.6.2-4_amd64.deb"
: > "$WORK/pool/main/r/rsync/rsync_3.2.7-1_amd64.deb"
: > "$WORK/pool/main/d/docker.io/docker.io_20.10.24_amd64.deb"
: > "$WORK/pool/main/s/samba-common/samba-common_4.17.12+dfsg-0_all.deb"

PRESENT="$(gonas_scan_pool_packages "$WORK/pool" mergerfs snapraid samba nfs-kernel-server smartmontools wireguard-tools rsync docker.io)"

check() { # name, condition-already-evaluated via case
    if [ "$2" = "1" ]; then echo "PASS: $1"; else echo "FAIL: $1 (present='$PRESENT')" >&2; FAIL=1; fi
}
has() { case " $PRESENT " in *" $1 "*) echo 1;; *) echo 0;; esac; }

check "mergerfs found" "$(has mergerfs)"
check "samba found" "$(has samba)"
check "nfs-kernel-server found" "$(has nfs-kernel-server)"
check "rsync found" "$(has rsync)"
check "docker.io found (dot in name)" "$(has docker.io)"
# 不在 pool 裡的:
check "snapraid NOT found" "$([ "$(has snapraid)" = 0 ] && echo 1 || echo 0)"
check "wireguard-tools NOT found" "$([ "$(has wireguard-tools)" = 0 ] && echo 1 || echo 0)"
check "smartmontools NOT found" "$([ "$(has smartmontools)" = 0 ] && echo 1 || echo 0)"
# 前綴精準:只有 samba-common 而沒有 samba 的 deb 時,不能誤判 samba 存在。
mkdir -p "$WORK/pool2/x"; : > "$WORK/pool2/x/samba-common_1_all.deb"
P2="$(gonas_scan_pool_packages "$WORK/pool2" samba)"
check "samba NOT matched by samba-common only" "$([ -z "$P2" ] && echo 1 || echo 0)"

# --- pkgsel/include 改寫 ---
cat > "$WORK/preseed.cfg" <<'EOF'
d-i pkgsel/upgrade select none
d-i pkgsel/include string openssh-server sudo
d-i pkgsel/update-policy select none
EOF
gonas_sed_inplace "s#^d-i pkgsel/include string \(.*\)#d-i pkgsel/include string \\1 $PRESENT#" "$WORK/preseed.cfg"
LINE="$(grep '^d-i pkgsel/include string' "$WORK/preseed.cfg")"
case "$LINE" in
    "d-i pkgsel/include string openssh-server sudo "*"mergerfs"*"samba"*) echo "PASS: pkgsel/include appended found packages after openssh-server sudo";;
    *) echo "FAIL: pkgsel/include rewrite wrong: [$LINE]" >&2; FAIL=1;;
esac
# 其他兩行不能被動到
if [ "$(grep -c '^d-i pkgsel/' "$WORK/preseed.cfg")" = "3" ]; then echo "PASS: other pkgsel lines untouched"; else echo "FAIL: pkgsel line count changed" >&2; FAIL=1; fi

echo
if [ "$FAIL" = "0" ]; then echo "==> all scan-pool-packages test cases passed"; exit 0; else echo "==> scan-pool-packages tests FAILED" >&2; exit 1; fi
