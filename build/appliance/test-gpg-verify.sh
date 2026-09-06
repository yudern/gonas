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

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all gpg-verify control-flow test cases passed"
    echo "    (reminder: this only tests the pass/fail decision logic against a fake gpg;"
    echo "    it does NOT test real signature verification against Debian's actual signing key,"
    echo "    which needs real network access this sandbox does not have.)"
    exit 0
else
    echo "==> one or more gpg-verify test cases FAILED — see above" >&2
    exit 1
fi
