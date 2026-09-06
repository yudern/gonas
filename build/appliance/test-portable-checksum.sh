#!/bin/sh
# test-portable-checksum.sh 是 lib/portable-checksum.sh 的離線回歸測試。
#
# 背景:build-iso.sh 原本直接呼叫 `sha256sum`/`md5sum`,這兩個 GNU
# coreutils 工具在 Linux 上到處都有,但 macOS 內建的 BSD 使用者空間
# 完全沒有——第十七輪覆閱是因為使用者真的要在自己的 Mac mini 上跑
# build-iso.sh,回頭檢查這支腳本用到的每一個外部指令在 macOS 上在不在
# 才發現這個問題,前 16 輪都是在 Linux 沙盒裡覆閱,從來沒被抓到過。
# gonas_sha256sum()/gonas_md5sum() 就是修好之後的版本:優先用
# sha256sum/md5sum(Linux 環境),不存在的話 fallback 到 macOS 原生的
# shasum -a 256 / md5 -r。
#
# 這支測試在這台開發沙盒(Linux)上只能驗證「sha256sum/md5sum 存在時,
# 走的是第一個分支,輸出格式正確」——沒辦法在這裡直接驗證 macOS 那個
# fallback 分支的實際行為(這個沙盒沒有 shasum/md5 這兩個 macOS 指令),
# 但可以驗證 fallback 分支「選擇哪個指令」的判斷邏輯本身:用一個假的
# `sha256sum`/`md5sum`(讓 `command -v` 都找不到),搭配一個假的
# `shasum`/`md5`,確認函式在找不到 GNU 版本時真的會改用 macOS 版本、
# 而且傳的參數/旗標(`-a 256`、`-r`)是對的。
#
# 完全不需要網路。
#
# 用法:
#   sh build/appliance/test-portable-checksum.sh

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/lib/portable-checksum.sh"

TEST_WORK_DIR="$(mktemp -d /tmp/gonas-checksum-test.XXXXXX)"
trap 'rm -rf "$TEST_WORK_DIR"' EXIT

FAIL=0
TEST_FILE="$TEST_WORK_DIR/payload.bin"
echo "gonas portable checksum test payload" > "$TEST_FILE"

# --- 案例 1: 這台機器本來就有真正的 sha256sum/md5sum(這個沙盒的情況,
# 大部分 Linux 機器也是)——驗證直接呼叫真正的指令,輸出格式正確、
# 雜湊值真的對得上手動算的結果。
if command -v sha256sum >/dev/null 2>&1; then
    EXPECTED="$(sha256sum "$TEST_FILE" | awk '{print $1}')"
    ACTUAL="$(gonas_sha256sum "$TEST_FILE" | awk '{print $1}')"
    if [ "$ACTUAL" = "$EXPECTED" ]; then
        echo "PASS: gonas_sha256sum uses the real sha256sum when present, hash matches"
    else
        echo "FAIL: gonas_sha256sum hash ($ACTUAL) does not match sha256sum ($EXPECTED)" >&2
        FAIL=1
    fi
else
    echo "SKIP: this machine has no 'sha256sum' to compare against — the case below covers the fallback path instead"
fi

if command -v md5sum >/dev/null 2>&1; then
    EXPECTED_MD5="$(md5sum "$TEST_FILE" | awk '{print $1}')"
    ACTUAL_MD5="$(gonas_md5sum "$TEST_FILE" | awk '{print $1}')"
    if [ "$ACTUAL_MD5" = "$EXPECTED_MD5" ]; then
        echo "PASS: gonas_md5sum uses the real md5sum when present, hash matches"
    else
        echo "FAIL: gonas_md5sum hash ($ACTUAL_MD5) does not match md5sum ($EXPECTED_MD5)" >&2
        FAIL=1
    fi
else
    echo "SKIP: this machine has no 'md5sum' to compare against — the case below covers the fallback path instead"
fi

# --- 案例 2: 模擬 macOS 環境(sha256sum/md5sum 都找不到,只有
# shasum/md5)——確認函式真的會改呼叫 macOS 原生指令、而且旗標正確
# (`-a 256`、`-r`),不是假設 GNU 工具一定存在。用假的
# shasum/md5(印出固定字串,不做真的雜湊運算)驗證「呼叫到了誰、帶了
# 什麼參數」,不驗證雜湊演算法本身對不對(那是系統工具的責任,不是
# 這幾行 wrapper 邏輯要驗證的東西)。
#
# 這裡刻意「不」在假的 PATH 裡放一個空的、只會 exit 127 的假
# sha256sum/md5sum——那樣做完全沒有用:`command -v` 只檢查 PATH 上
# 有沒有一個名字叫這個、可執行的檔案,根本不會真的執行它,只要那個
# 檔案存在(不管內容是什麼、會不會失敗),`command -v sha256sum` 就會
# 回報「找到了」,gonas_sha256sum 就會照樣走第一個分支去呼叫它,永遠
# 不會走到 fallback,等於沒測到 fallback 邏輯。真正要讓
# `command -v sha256sum` 找不到,PATH 裡必須完全沒有任何一個叫這個
# 名字的檔案——所以底下呼叫這兩個函式之前,把 PATH 整個換成只指向
# 這個乾乾淨淨、只放了 shasum/md5 兩個假指令的目錄,不接原本的 PATH
# (這兩個函式本身不需要呼叫任何其他外部指令,不會因為 PATH 變窄而
# 出問題)。
ORIGINAL_PATH="$PATH"
FAKE_BIN_DIR="$TEST_WORK_DIR/fakebin"
mkdir -p "$FAKE_BIN_DIR"

cat > "$FAKE_BIN_DIR/shasum" <<'EOF'
#!/bin/sh
if [ "$1" = "-a" ] && [ "$2" = "256" ]; then
    echo "FAKE_SHASUM_A256_OK  $3"
else
    echo "FAKE_SHASUM_WRONG_ARGS: $*" >&2
    exit 1
fi
EOF
cat > "$FAKE_BIN_DIR/md5" <<'EOF'
#!/bin/sh
if [ "$1" = "-r" ]; then
    echo "FAKE_MD5_R_OK $2"
else
    echo "FAKE_MD5_WRONG_ARGS: $*" >&2
    exit 1
fi
EOF
chmod +x "$FAKE_BIN_DIR/shasum" "$FAKE_BIN_DIR/md5"

# 注意:這裡是 `PATH="$FAKE_BIN_DIR"`,不是
# `PATH="$FAKE_BIN_DIR:$ORIGINAL_PATH"`——後面接原始 PATH 的話,這台
# 沙盒機器上真正的 sha256sum/md5sum 還是找得到(只是排在假指令後面),
# `command -v sha256sum` 一樣會回報「找到了」,還是測不到 fallback。
PATH="$FAKE_BIN_DIR"

OUT_SHA="$(gonas_sha256sum "$TEST_FILE" 2>&1)"
if [ "$OUT_SHA" = "FAKE_SHASUM_A256_OK  $TEST_FILE" ]; then
    echo "PASS: gonas_sha256sum falls back to 'shasum -a 256' (macOS-style) when sha256sum is unavailable"
else
    echo "FAIL: gonas_sha256sum did not correctly fall back to 'shasum -a 256', got: $OUT_SHA" >&2
    FAIL=1
fi

OUT_MD5="$(gonas_md5sum "$TEST_FILE" 2>&1)"
if [ "$OUT_MD5" = "FAKE_MD5_R_OK $TEST_FILE" ]; then
    echo "PASS: gonas_md5sum falls back to 'md5 -r' (macOS-style) when md5sum is unavailable"
else
    echo "FAIL: gonas_md5sum did not correctly fall back to 'md5 -r', got: $OUT_MD5" >&2
    FAIL=1
fi

PATH="$ORIGINAL_PATH"

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all portable-checksum test cases passed"
    exit 0
else
    echo "==> one or more portable-checksum test cases FAILED — see above" >&2
    exit 1
fi
