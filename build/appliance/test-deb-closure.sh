#!/bin/sh
# test-deb-closure.sh 是 lib/deb-closure.sh 的離線回歸測試。
#
# 完全不需要網路、也不需要真的一份 Debian Packages 索引——用一份
# 手寫、刻意涵蓋各種邊界情況的假 Packages 資料,餵給 build-iso.sh 真正
# 會用到的同一份 gonas_deb_closure()(來源自 lib/deb-closure.sh),
# 檢查算出來的相依封閉集對不對。
#
# 這是「模式一:離線 SSH」裡唯一能在這個開發沙盒裡端對端驗證的部分
# ——真正下載 .deb、在目標系統 dpkg -i 那些 I/O 步驟沒辦法在這裡測,
# 但「解析 Packages + 算封閉集 + 排除 base 已有優先級 + 處理替代相依/
# 版本限制/架構修飾/虛擬套件」這段純邏輯可以,而且這正是最容易寫錯、
# 最需要回歸測試盯著的部分。
#
# 用法:sh build/appliance/test-deb-closure.sh

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/lib/deb-closure.sh"

TEST_WORK_DIR="$(mktemp -d /tmp/gonas-deb-closure-test.XXXXXX)"
trap 'rm -rf "$TEST_WORK_DIR"' EXIT

FAIL=0

# 一份手寫的假 Packages 索引,涵蓋:
#  - 種子套件(pkg-seed),相依裡故意放了版本限制 "(>= 1.0)"、架構修飾
#    ":any"、`|` 替代相依、Pre-Depends、以及 Priority required/important
#    (應該被當成 base 已有而排除)。
#  - 一層轉一層的相依(pkg-real -> pkg-leaf),確認會遞迴展開。
#  - 虛擬套件:pkg-virtual 沒有實體,由 pkg-provider 用 Provides 提供,
#    種子相依寫的是 "pkg-virtual | pkg-fallback",應該解成 pkg-provider,
#    而且**不**該把 pkg-fallback 也拉進來(取第一個替代方案)。
PKGS="$TEST_WORK_DIR/Packages"
cat > "$PKGS" <<'EOF'
Package: pkg-seed
Priority: optional
Filename: pool/main/s/seed/pkg-seed_1.0_arm64.deb
Depends: pkg-real (>= 1.0), pkg-required, pkg-important, pkg-virtual | pkg-fallback, pkg-arch:any
Pre-Depends: pkg-predep

Package: pkg-real
Priority: standard
Filename: pool/main/r/real/pkg-real_2.0_arm64.deb
Depends: pkg-leaf

Package: pkg-leaf
Priority: optional
Filename: pool/main/l/leaf/pkg-leaf_3.0_arm64.deb

Package: pkg-required
Priority: required
Filename: pool/main/r/required/pkg-required_1.0_arm64.deb

Package: pkg-important
Priority: important
Filename: pool/main/i/important/pkg-important_1.0_arm64.deb
Depends: pkg-should-not-be-pulled

Package: pkg-should-not-be-pulled
Priority: optional
Filename: pool/main/x/xxx/pkg-should-not-be-pulled_1.0_arm64.deb

Package: pkg-provider
Priority: optional
Filename: pool/main/p/provider/pkg-provider_1.0_arm64.deb
Provides: pkg-virtual

Package: pkg-fallback
Priority: optional
Filename: pool/main/f/fallback/pkg-fallback_1.0_arm64.deb

Package: pkg-arch
Priority: optional
Filename: pool/main/a/arch/pkg-arch_1.0_arm64.deb

Package: pkg-predep
Priority: optional
Filename: pool/main/p/predep/pkg-predep_1.0_arm64.deb
EOF

OUT="$TEST_WORK_DIR/closure.out"
gonas_deb_closure "$PKGS" "required,important" pkg-seed > "$OUT" 2>"$TEST_WORK_DIR/closure.err"

# 檢查某個 Filename 有沒有出現在輸出裡
assert_present() {
    _name="$1"; _needle="$2"
    if grep -qF "$_needle" "$OUT"; then
        echo "PASS: $_name"
    else
        echo "FAIL: $_name — expected '$_needle' in closure output, actual output:" >&2
        sed 's/^/    | /' "$OUT" >&2
        FAIL=1
    fi
}
assert_absent() {
    _name="$1"; _needle="$2"
    if grep -qF "$_needle" "$OUT"; then
        echo "FAIL: $_name — did NOT expect '$_needle' in closure output, actual output:" >&2
        sed 's/^/    | /' "$OUT" >&2
        FAIL=1
    else
        echo "PASS: $_name"
    fi
}

assert_present "seed package itself is included" "pool/main/s/seed/pkg-seed_1.0_arm64.deb"
assert_present "direct dep with version constraint (pkg-real) included" "pool/main/r/real/pkg-real_2.0_arm64.deb"
assert_present "transitive dep (pkg-leaf via pkg-real) included" "pool/main/l/leaf/pkg-leaf_3.0_arm64.deb"
assert_present "arch-qualified dep (pkg-arch:any) resolved and included" "pool/main/a/arch/pkg-arch_1.0_arm64.deb"
assert_present "Pre-Depends (pkg-predep) included" "pool/main/p/predep/pkg-predep_1.0_arm64.deb"
assert_present "virtual dep resolved via Provides (pkg-provider) included" "pool/main/p/provider/pkg-provider_1.0_arm64.deb"

assert_absent "Priority:required dep (pkg-required) excluded as base-provided" "pool/main/r/required/pkg-required_1.0_arm64.deb"
assert_absent "Priority:important dep (pkg-important) excluded as base-provided" "pool/main/i/important/pkg-important_1.0_arm64.deb"
assert_absent "excluded package's own deps are NOT recursed into" "pool/main/x/xxx/pkg-should-not-be-pulled_1.0_arm64.deb"
assert_absent "second alternative (pkg-fallback) NOT pulled when first alternative resolves" "pool/main/f/fallback/pkg-fallback_1.0_arm64.deb"

# --- 一個不存在的種子套件:應該印警告到 stderr、不崩潰、輸出裡沒有它 ---
OUT2="$TEST_WORK_DIR/closure2.out"
ERR2="$TEST_WORK_DIR/closure2.err"
gonas_deb_closure "$PKGS" "required,important" pkg-does-not-exist > "$OUT2" 2>"$ERR2"
if grep -qi "not found in Packages index" "$ERR2"; then
    echo "PASS: nonexistent seed produces a clear warning on stderr, does not crash"
else
    echo "FAIL: nonexistent seed should warn on stderr; stderr was:" >&2
    sed 's/^/    | /' "$ERR2" >&2
    FAIL=1
fi

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all deb-closure test cases passed"
    exit 0
else
    echo "==> one or more deb-closure test cases FAILED — see above" >&2
    exit 1
fi
