#!/bin/sh
# test-detect-arch.sh 是 lib/detect-arch.sh 的離線回歸測試。
#
# 背景:late-command.sh 判斷架構原本寫成
# `dpkg --print-architecture 2>/dev/null || uname -m`,第九輪覆閱時
# 發現 fallback 分支算出來的值格式跟主要路徑不一致(`uname -m` 用的是
# "x86_64"/"aarch64" 這種核心/硬體慣用名稱,不是 Debian 慣用的
# "amd64"/"arm64"),修好之後這段對應邏輯抽成
# lib/detect-arch.sh 的 gonas_uname_to_debian_arch()——但抽出來的當下
# 忘記順便補一支對應的回歸測試(跟 patch-boot-menu.sh/
# verify-gpg-signature.sh 都有各自的 test-*.sh 不一樣),第十三輪覆閱
# 重新檢查 CI 設定時發現了這個落差,這支測試就是補上的。
#
# 完全不需要網路,也不需要真的裝什麼工具,純粹是字串比對邏輯。
#
# 用法:
#   sh build/appliance/test-detect-arch.sh

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/lib/detect-arch.sh"

FAIL=0

assert_maps_to() {
    _input="$1"
    _expected="$2"
    _actual="$(gonas_uname_to_debian_arch "$_input")"
    if [ "$_actual" = "$_expected" ]; then
        echo "PASS: uname -m='$_input' -> '$_actual'"
    else
        echo "FAIL: uname -m='$_input' -> got '$_actual', expected '$_expected'" >&2
        FAIL=1
    fi
}

# 這台開發沙盒機器上實際執行 `uname -m` 得到的就是 x86_64——第九輪
# 覆閱時親自驗證過的真實輸入,不是憑空舉例。
assert_maps_to "x86_64" "amd64"
assert_maps_to "aarch64" "arm64"
# 有些系統/工具鏈也會回報 "arm64" 而不是 "aarch64"(例如某些 macOS
# 底下跑的工具),兩種都要能對到同一個結果。
assert_maps_to "arm64" "arm64"
# 不在已知對應表裡的架構(例如 32 位元 ARM、RISC-V)——維持原樣輸出,
# 不要假裝知道怎麼對應,至少不會是一個「看起來對、其實是另一種命名
# 慣例」的假象(這正是原本那個 bug 的問題所在)。
assert_maps_to "armv7l" "armv7l"
assert_maps_to "riscv64" "riscv64"
assert_maps_to "unknown" "unknown"

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all detect-arch test cases passed"
    exit 0
else
    echo "==> one or more detect-arch test cases FAILED — see above" >&2
    exit 1
fi
