#!/bin/sh
# test-portable-sed.sh 是 lib/portable-sed.sh 的離線回歸測試。
#
# 背景:build-iso.sh/lib/patch-boot-menu.sh 原本直接用
# `sed -i 'script' file`(不帶參數的 `-i`)——這是 GNU sed 的用法,在
# macOS 內建的 BSD sed 底下,`-i` 語法上一定要接一個備份副檔名參數,
# 這樣寫會讓 BSD sed 把 `'script'` 這個字串本身當成備份副檔名、把
# `file` 當成 sed 的操作腳本去執行,完全不是原本的意思,而且不會是
# 「指令找不到」這種容易發現的錯誤。gonas_sed_inplace() 是修好之後的
# 版本,見 lib/portable-sed.sh 開頭的完整說明。
#
# 這支測試在這台開發沙盒(Linux/GNU sed)上驗證:
#   1. gonas_sed_inplace 真的能正確修改檔案內容(基本功能)。
#   2. 修改完之後,備份檔案(<file>.gonas-sed-bak)真的有被清掉,不會
#      留下垃圾檔案。
#   3. 回傳值反映的是 sed 本身的結果,不是被後面清理備份檔案的 rm 蓋過去
#      (用一個「這個檔案不存在」的情境,確認失敗有正確回報)。
#
# 這支測試沒辦法在這裡驗證「换成 BSD sed 之後,`-i.bak` 這個寫法是不是
# 真的兩邊都相容」這件事本身(這個沙盒只有 GNU sed)——但 `-i` 後面接
# 一個非空副檔名,是兩者唯一共同支援、行為一致的語法,這是 sed 本身
# 文件記載的行為,不是猜測。
#
# 完全不需要網路。
#
# 用法:
#   sh build/appliance/test-portable-sed.sh

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/lib/portable-sed.sh"

TEST_WORK_DIR="$(mktemp -d /tmp/gonas-sed-test.XXXXXX)"
trap 'rm -rf "$TEST_WORK_DIR"' EXIT

FAIL=0

# --- 案例 1: 基本功能——內容真的被改了 ------------------------------
CASE1="$TEST_WORK_DIR/case1.txt"
printf 'hello world\n' > "$CASE1"
gonas_sed_inplace 's/world/gonas/' "$CASE1"
if [ "$(cat "$CASE1")" = "hello gonas" ]; then
    echo "PASS: gonas_sed_inplace correctly modifies the file in place"
else
    echo "FAIL: expected 'hello gonas', got: $(cat "$CASE1")" >&2
    FAIL=1
fi

# --- 案例 2: 備份檔案有被清掉,不會留垃圾 -----------------------------
if [ -e "$CASE1.gonas-sed-bak" ]; then
    echo "FAIL: backup file $CASE1.gonas-sed-bak was left behind, should have been cleaned up" >&2
    FAIL=1
else
    echo "PASS: the .gonas-sed-bak backup file left by -i is cleaned up afterward"
fi

# --- 案例 3: 回傳值反映 sed 本身的結果,不是被後面的 rm 蓋過去 ---------
# 對一個不存在的檔案呼叫,sed 本身一定會失敗(exit 非 0)——如果函式的
# 回傳值被後面清理備份檔案的 `rm -f`(對不存在的檔案是安全、成功的)
# 蓋過去,這裡就會錯誤地回報「成功」。
NONEXISTENT="$TEST_WORK_DIR/does-not-exist.txt"
if gonas_sed_inplace 's/a/b/' "$NONEXISTENT" 2>/dev/null; then
    echo "FAIL: gonas_sed_inplace should have failed for a nonexistent file, but reported success (return value is being masked by the cleanup rm)" >&2
    FAIL=1
else
    echo "PASS: gonas_sed_inplace correctly reports sed's own failure, not masked by the backup-file cleanup"
fi

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all portable-sed test cases passed"
    exit 0
else
    echo "==> one or more portable-sed test cases FAILED — see above" >&2
    exit 1
fi
