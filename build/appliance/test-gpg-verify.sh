#!/bin/sh
# test-gpg-verify.sh 是 lib/verify-gpg-signature.sh 的離線回歸測試。
#
# 這支腳本完全不需要網路、也不需要真的裝 gpg——它在 PATH 前面放一個
# 假的 `gpg` shell script,模擬「驗證成功」跟「驗證失敗」兩種情況下
# gpg 實際會有的 exit code 跟輸出內容,藉此驗證
# gonas_verify_gpg_signature() 判斷「這樣算不算驗證通過」的邏輯本身
# 對不對。
#
# 重要:這支測試*不能*、也沒有打算驗證「真的能不能正確驗證一份 Debian
# 官方簽出來的 SHA256SUMS.sign」這件事本身——那需要真的連得上網路匯入
# Debian 的簽章金鑰,這個開發沙盒完全沒辦法做。這支測試驗證的是「假設
# gpg 這樣回報,我們的判斷邏輯會不會做出正確的結論」,是控制流程層級的
# 驗證,不是密碼學層級的驗證。build-iso.sh 開頭的說明跟
# build/appliance/README.md 對這個邊界有更完整的說明。
#
# 用法(不需要網路):
#   sh build/appliance/test-boot-menu-patch.sh   # 另一支測試,順便一起跑更好
#   sh build/appliance/test-gpg-verify.sh

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# 保留原始 PATH——下面案例 1-3 會把 PATH 指到一個假的 gpg 執行檔,案例
# 4 需要真正的系統 gpg,執行前要記得把 PATH 換回來,不然 case 4 用的
# 會是 case 3 最後留下的那個假 gpg(永遠回報「沒有真的驗證到什麼」),
# 而不是真正的 gpg,整個案例 4 的意義就沒了。
ORIGINAL_PATH="$PATH"
. "$SCRIPT_DIR/lib/verify-gpg-signature.sh"

TEST_WORK_DIR="$(mktemp -d /tmp/gonas-gpg-verify-test.XXXXXX)"
trap 'rm -rf "$TEST_WORK_DIR"' EXIT

FAKE_BIN_DIR="$TEST_WORK_DIR/fakebin"
mkdir -p "$FAKE_BIN_DIR"

FAIL=0

# 隨便建幾個空檔案讓函式簽名可以填——這支測試的假 gpg 完全不看檔案
# 內容,只看呼叫端傳進來的模式決定要回報成功還是失敗。
SIG_FILE="$TEST_WORK_DIR/fake.sig"
DATA_FILE="$TEST_WORK_DIR/fake.data"
KEYRING_FILE="$TEST_WORK_DIR/fake.keyring"
: > "$SIG_FILE"
: > "$DATA_FILE"
: > "$KEYRING_FILE"

# --- 案例 1: gpg exit code 0,輸出裡真的有 "Good signature" ------------
cat > "$FAKE_BIN_DIR/gpg" <<'EOF'
#!/bin/sh
echo "gpg: Signature made Fri 04 Sep 2026 using RSA key ID DEADBEEF" >&2
echo "gpg: Good signature from \"Debian Archive Automatic Signing Key\"" >&2
exit 0
EOF
chmod +x "$FAKE_BIN_DIR/gpg"
LOG1="$TEST_WORK_DIR/case1.log"
PATH="$FAKE_BIN_DIR:$PATH"
if gonas_verify_gpg_signature "$SIG_FILE" "$DATA_FILE" "$KEYRING_FILE" "$LOG1"; then
    echo "PASS: exit 0 + 'Good signature' in output -> treated as verified"
else
    echo "FAIL: exit 0 + 'Good signature' in output should have been treated as verified, log:" >&2
    sed 's/^/    | /' "$LOG1" >&2
    FAIL=1
fi

# --- 案例 2: gpg exit code 非 0(明確的驗證失敗) ------------------------
cat > "$FAKE_BIN_DIR/gpg" <<'EOF'
#!/bin/sh
echo "gpg: Signature made Fri 04 Sep 2026 using RSA key ID DEADBEEF" >&2
echo "gpg: BAD signature from \"Debian Archive Automatic Signing Key\"" >&2
exit 1
EOF
chmod +x "$FAKE_BIN_DIR/gpg"
LOG2="$TEST_WORK_DIR/case2.log"
if gonas_verify_gpg_signature "$SIG_FILE" "$DATA_FILE" "$KEYRING_FILE" "$LOG2"; then
    echo "FAIL: exit 1 + 'BAD signature' should NOT have been treated as verified" >&2
    FAIL=1
else
    echo "PASS: exit 1 + 'BAD signature' -> correctly treated as not verified"
fi

# --- 案例 3: gpg exit code 0,但輸出裡沒有 "Good signature" 這個字串 ----
# (刻意測這個「看起來過但其實沒有」的情境——這正是為什麼判斷邏輯不能
# 只看 exit code,見 lib/verify-gpg-signature.sh 裡的說明。)
cat > "$FAKE_BIN_DIR/gpg" <<'EOF'
#!/bin/sh
echo "gpg: WARNING: nothing conclusive was verified" >&2
exit 0
EOF
chmod +x "$FAKE_BIN_DIR/gpg"
LOG3="$TEST_WORK_DIR/case3.log"
if gonas_verify_gpg_signature "$SIG_FILE" "$DATA_FILE" "$KEYRING_FILE" "$LOG3"; then
    echo "FAIL: exit 0 without 'Good signature' in output should NOT have been treated as verified" >&2
    FAIL=1
else
    echo "PASS: exit 0 without 'Good signature' -> correctly treated as not verified (exit code alone is not trusted)"
fi

# --- 案例 4(可選,需要真的裝 gpg): 用一把當場產生的真測試金鑰,走
# 一次真正的簽章/驗證,不是只用假的 gpg 執行檔模擬控制流程 -----------
# 這是第十輪覆閱時額外補上的:前面 3 個案例只驗證「假設 gpg 這樣
# 回報,我們的判斷邏輯對不對」,不驗證「這整套用法(--no-default-
# keyring --keyring <檔案> --verify)本身在真正的 gpg 底下到底能不能
# 動」——而這支腳本的真正用途正是要驗證真的簽章,只測控制流程不夠。
# 用一把當場產生、只在這次測試裡存在的臨時金鑰(不影響、也不需要
# 使用者自己的 GPG 環境,GNUPGHOME 指到這次測試專用的暫存目錄,
# 跟著 TEST_WORK_DIR 一起在結束時清掉),簽一份測試資料,分別驗證
# 「正常驗證會過」跟「資料被竄改後驗證會正確失敗」——這正是當初
# 覆閱時實際抓到「keyring 要匯出成 binary 格式,不能用 -a/armor
# 格式」跟「keyring 路徑要轉成絕對路徑,不能靠 gpg 自己對相對路徑的
# 解讀方式」這兩個問題的測試方式,把當時的手動驗證過程固定下來,
# 之後這兩個問題不會因為程式碼被改動又悄悄回歸。
#
# 先把 PATH 換回案例 1-3 之前的原始版本,確保這裡呼叫到的是真正的
# 系統 gpg,不是案例 3 留在 $FAKE_BIN_DIR 裡的假 gpg。
PATH="$ORIGINAL_PATH"
if command -v gpg >/dev/null 2>&1; then
    GNUPG_TEST_HOME="$TEST_WORK_DIR/gnupg-home"
    mkdir -m 700 -p "$GNUPG_TEST_HOME"
    REAL_DATA="$TEST_WORK_DIR/real-data.bin"
    REAL_SIG="$TEST_WORK_DIR/real-data.bin.sig"
    REAL_KEYRING_DIR="$TEST_WORK_DIR/keyring-subdir"
    mkdir -p "$REAL_KEYRING_DIR"
    REAL_KEYRING="$REAL_KEYRING_DIR/my.keyring"

    echo "gonas boot menu patch test payload" > "$REAL_DATA"

    if GNUPGHOME="$GNUPG_TEST_HOME" gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
        --quick-generate-key "GoNAS Test Signer <test@example.invalid>" default default 2026-12-31 \
        >"$TEST_WORK_DIR/keygen.log" 2>&1 \
       && GNUPGHOME="$GNUPG_TEST_HOME" gpg --batch --yes --detach-sign -o "$REAL_SIG" "$REAL_DATA" \
        >>"$TEST_WORK_DIR/keygen.log" 2>&1 \
       && GNUPGHOME="$GNUPG_TEST_HOME" gpg --export "GoNAS Test Signer" > "$REAL_KEYRING" 2>>"$TEST_WORK_DIR/keygen.log"; then

        LOG4A="$TEST_WORK_DIR/case4a.log"
        if gonas_verify_gpg_signature "$REAL_SIG" "$REAL_DATA" "$REAL_KEYRING" "$LOG4A"; then
            echo "PASS: real gpg key/signature -> correctly verified (not a mock)"
        else
            echo "FAIL: real gpg key/signature should have verified successfully, log:" >&2
            sed 's/^/    | /' "$LOG4A" >&2
            FAIL=1
        fi

        # 竄改資料之後,同一把金鑰、同一份簽章應該要驗證失敗。
        echo "tampered" >> "$REAL_DATA"
        LOG4B="$TEST_WORK_DIR/case4b.log"
        if gonas_verify_gpg_signature "$REAL_SIG" "$REAL_DATA" "$REAL_KEYRING" "$LOG4B"; then
            echo "FAIL: tampered data should NOT have verified successfully" >&2
            FAIL=1
        else
            echo "PASS: real gpg key, tampered data -> correctly rejected"
        fi
    else
        echo "SKIP: could not generate a test GPG key in this environment (see $TEST_WORK_DIR/keygen.log if it still exists) — skipping the real-key test, control-flow cases above already passed"
    fi
else
    echo "SKIP: gpg not installed in this environment — skipping the real-key test, control-flow cases above already passed"
fi

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all gpg-verify test cases passed"
    echo "    (reminder: cases 1-3 only test the pass/fail decision logic against a fake gpg;"
    echo "    case 4, when it runs, uses a real locally-generated GPG key and a real signature —"
    echo "    but none of this tests verification against Debian's actual official signing key,"
    echo "    which needs real network access this sandbox does not have.)"
    exit 0
else
    echo "==> one or more gpg-verify test cases FAILED — see above" >&2
    exit 1
fi
