#!/bin/sh
# test-find-boot-menu-cfgs.sh 是 lib/find-boot-menu-cfgs.sh 的離線
# 回歸測試。
#
# 這支測試存在的理由:第十八輪覆閱(使用者實測在 Mac mini 上跑
# `make iso-arm64`)真的撞見一個 `find` + `set -e` 交互作用的陷阱——
# `find "$isolinux_dir" "$grub_dir" ...` 給兩個起始路徑,arm64 的官方
# ISO 是 EFI-only、本來就沒有 isolinux/ 目錄,這種情況下 `find` 雖然
# 仍會正確找到 grub 目錄底下的檔案,但自己的 exit code 會因為
# 「isolinux 路徑不存在」變成非 0——在 build-iso.sh 開頭設定的
# `set -eu` 底下,這個非 0 exit code 會讓整支腳本當場中止,而且是在
# 任何一行有意義的錯誤訊息印出來之前就死掉,螢幕上只會看到 make 印出
# 的 `Error 1`,完全看不出真正的原因。這個問題只靠讀程式碼完全看不出
# 來——`find`/`set -e` 這種組合的行為,需要真的在 `sh` 底下執行一次
# 才會現形,這也是為什麼前 17 輪不管審查多少次都沒發現,直到使用者
# 真的用 arm64(沒有 isolinux 目錄)實測才第一次撞見。
#
# 用法(不需要網路,幾秒鐘跑完):
#   sh build/appliance/test-find-boot-menu-cfgs.sh

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/lib/find-boot-menu-cfgs.sh"

TEST_WORK_DIR="$(mktemp -d /tmp/gonas-find-boot-menu-cfgs-test.XXXXXX)"
trap 'rm -rf "$TEST_WORK_DIR"' EXIT

FAIL=0

# --- 案例 1: 兩個目錄都存在,各自有 .cfg 檔案 ---------------------------
CASE1_ISOLINUX="$TEST_WORK_DIR/case1/isolinux"
CASE1_GRUB="$TEST_WORK_DIR/case1/grub"
mkdir -p "$CASE1_ISOLINUX" "$CASE1_GRUB"
echo "x" > "$CASE1_ISOLINUX/isolinux.cfg"
echo "x" > "$CASE1_GRUB/grub.cfg"
CASE1_OUT="$TEST_WORK_DIR/case1.list"
gonas_find_boot_menu_cfgs "$CASE1_ISOLINUX" "$CASE1_GRUB" "$CASE1_OUT"
CASE1_COUNT="$(wc -l < "$CASE1_OUT" | tr -d ' ')"
if [ "$CASE1_COUNT" = "2" ]; then
    echo "PASS: both isolinux and grub dirs exist -> found both .cfg files"
else
    echo "FAIL: expected 2 files found when both dirs exist, got $CASE1_COUNT" >&2
    FAIL=1
fi

# --- 案例 2(最重要,直接重現第十八輪撞見的情境): isolinux 目錄根本
# 不存在(模擬 arm64 EFI-only 的官方 ISO),grub 目錄存在且有 .cfg
# 檔案——函式本身不能因此讓呼叫端在 `set -e` 底下死掉,而且要正確找到
# grub 目錄底下的檔案 ---------------------------------------------------
CASE2_ISOLINUX="$TEST_WORK_DIR/case2/isolinux-does-not-exist"
CASE2_GRUB="$TEST_WORK_DIR/case2/grub"
mkdir -p "$CASE2_GRUB"
echo "x" > "$CASE2_GRUB/grub.cfg"
CASE2_OUT="$TEST_WORK_DIR/case2.list"
# 這裡故意在目前這支測試腳本自己的 `set -eu` 底下直接呼叫,如果
# gonas_find_boot_menu_cfgs 內部沒有正確用 `|| true` 吃掉 find 的
# exit code,這一行執行完,底下的 echo/PASS 就不會被印出來,而是
# 整支測試腳本直接跟著中止,不會印出 FAIL,而是完全沒有任何後續輸出
# ——這正是第十八輪實際發生的症狀,值得特別註明。
gonas_find_boot_menu_cfgs "$CASE2_ISOLINUX" "$CASE2_GRUB" "$CASE2_OUT"
CASE2_COUNT="$(wc -l < "$CASE2_OUT" | tr -d ' ')"
if [ "$CASE2_COUNT" = "1" ]; then
    echo "PASS: isolinux dir missing (arm64-like) -> did not crash under set -e, still found the grub .cfg file"
else
    echo "FAIL: expected 1 file found when isolinux dir is missing but grub dir has one, got $CASE2_COUNT" >&2
    FAIL=1
fi

# --- 案例 3: 兩個目錄都不存在——不應該讓呼叫端崩潰,應該回傳一個空的
# 清單,讓呼叫端自己的「找到 0 個」檢查邏輯來處理 ---------------------
CASE3_ISOLINUX="$TEST_WORK_DIR/case3/isolinux-does-not-exist"
CASE3_GRUB="$TEST_WORK_DIR/case3/grub-does-not-exist"
CASE3_OUT="$TEST_WORK_DIR/case3.list"
gonas_find_boot_menu_cfgs "$CASE3_ISOLINUX" "$CASE3_GRUB" "$CASE3_OUT"
CASE3_COUNT="$(wc -l < "$CASE3_OUT" | tr -d ' ')"
if [ "$CASE3_COUNT" = "0" ]; then
    echo "PASS: both dirs missing -> did not crash under set -e, returned an empty list"
else
    echo "FAIL: expected 0 files found when both dirs are missing, got $CASE3_COUNT" >&2
    FAIL=1
fi

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all find-boot-menu-cfgs test cases passed"
    exit 0
else
    echo "==> one or more find-boot-menu-cfgs test cases FAILED — see above" >&2
    exit 1
fi
