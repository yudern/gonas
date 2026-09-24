#!/bin/sh
# test-offline-packages.sh —— install-offline-packages.sh 的離線控制流測試。
#
# 這支腳本沒辦法真的跑 apt/in-target(那需要真正的 debian-installer chroot 跟
# 一片 DVD),所以改用「打樁」:把 in-target / mount / umount 換成假的、記錄
# 呼叫的樁,再用一棵假的 /cdrom / /target 目錄樹,驗證這支腳本的控制流是對的:
#   - 有 DVD 時:會加臨時 apt 來源、對每個套件呼叫 apt-get install、最後把臨時
#     來源移除、把 bind-mount 卸載乾淨,而且不管 apt 成不成功都 exit 0。
#   - 沒有 DVD 時:直接跳過、exit 0、不留任何臨時來源。
#
# 用法:sh build/appliance/test-offline-packages.sh

set -eu
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FAIL=0

make_sandbox() {
    # 建一棵假的根:$SB/cdrom、$SB/target,並讓腳本裡的絕對路徑 /cdrom /target
    # 指到這裡——做法是把腳本複製一份、把 /cdrom 與 /target 改寫成沙盒路徑,
    # 再把 in-target/mount/umount 樁放進 PATH 最前面。
    SB="$(mktemp -d /tmp/gonas-offpkg.XXXXXX)"
    mkdir -p "$SB/bin" "$SB/target/etc/apt/sources.list.d" "$SB/target/var/lib/apt/lists" "$SB/target/var/log" "$SB/target/media"
    echo 'VERSION_CODENAME=trixie' > "$SB/target/etc/os-release"

    # 樁:in-target 記錄呼叫;apt-get install 依 STUB_FAIL_PKGS 決定成敗。
    cat > "$SB/bin/in-target" <<EOF
#!/bin/sh
echo "in-target \$*" >> "$SB/in-target.calls"
# 解析出「install <pkg>」的 pkg,若在失敗清單裡就回非 0。
for a in "\$@"; do :; done
case "\$*" in
  *"apt-get install"*)
    pkg="\$(echo "\$*" | sed 's/.*install -y --no-install-recommends //')"
    case " \${STUB_FAIL_PKGS:-} " in *" \$pkg "*) exit 100;; esac
    exit 0;;
  *) exit 0;;
esac
EOF
    # mount/umount 樁:只記錄,不真的掛載。
    printf '#!/bin/sh\necho "mount %s" >> "%s/mount.calls"\nexit 0\n' '$*' "$SB" > "$SB/bin/mount"
    printf '#!/bin/sh\necho "umount %s" >> "%s/umount.calls"\nexit 0\n' '$*' "$SB" > "$SB/bin/umount"
    chmod +x "$SB/bin/in-target" "$SB/bin/mount" "$SB/bin/umount"

    # 把腳本裡的 /cdrom 與 /target 改寫成沙盒路徑,產生一份可在沙盒裡跑的副本。
    sed -e "s#/cdrom#$SB/cdrom#g" -e "s#/target#$SB/target#g" \
        "$SCRIPT_DIR/install-offline-packages.sh" > "$SB/run.sh"
}

run() { PATH="$SB/bin:$PATH" STUB_FAIL_PKGS="${1:-}" sh "$SB/run.sh"; echo $?; }

# --- 案例 1:有 DVD,部分套件裝不到(snapraid 失敗)——仍要 exit 0、清理乾淨 ---
make_sandbox
mkdir -p "$SB/cdrom/dists/trixie" "$SB/cdrom/pool"   # 假裝這是一片 DVD
RC="$(run "snapraid")"
if [ "$RC" = "0" ]; then echo "PASS: exits 0 even when a package fails"; else echo "FAIL: expected exit 0, got $RC"; FAIL=1; fi
if grep -q "apt-get install -y --no-install-recommends mergerfs" "$SB/in-target.calls" 2>/dev/null; then echo "PASS: attempts to install mergerfs from DVD"; else echo "FAIL: did not attempt mergerfs install"; FAIL=1; fi
if grep -q "NOT installed (probably not on this DVD): snapraid" "$SB/target/var/log/gonas-offline-packages.log" 2>/dev/null; then echo "PASS: logs the skipped package clearly"; else echo "FAIL: missing skipped-package log"; FAIL=1; fi
if [ ! -f "$SB/target/etc/apt/sources.list.d/gonas-dvd.list" ]; then echo "PASS: temporary DVD apt source removed at the end"; else echo "FAIL: temporary apt source left behind"; FAIL=1; fi
if grep -q "umount" "$SB/umount.calls" 2>/dev/null; then echo "PASS: unmounts the DVD bind-mount"; else echo "FAIL: did not unmount"; FAIL=1; fi
rm -rf "$SB"

# --- 案例 2:沒有 DVD 套件庫 —— 直接跳過、exit 0、不建立任何臨時來源 ---
make_sandbox
# 故意不建立 cdrom/dists 與 cdrom/pool
mkdir -p "$SB/cdrom"
RC="$(run "")"
if [ "$RC" = "0" ]; then echo "PASS: exits 0 when there is no DVD repo"; else echo "FAIL: expected exit 0 without DVD, got $RC"; FAIL=1; fi
if [ ! -f "$SB/target/etc/apt/sources.list.d/gonas-dvd.list" ]; then echo "PASS: no temporary apt source created without a DVD"; else echo "FAIL: created a temp source with no DVD"; FAIL=1; fi
if ! grep -q "apt-get install" "$SB/in-target.calls" 2>/dev/null; then echo "PASS: no install attempted without a DVD"; else echo "FAIL: attempted install without a DVD"; FAIL=1; fi
rm -rf "$SB"

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all offline-packages test cases passed"
    exit 0
else
    echo "==> one or more offline-packages test cases FAILED" >&2
    exit 1
fi
