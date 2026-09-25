#!/bin/sh
# test-boot-menu-patch.sh 是 lib/patch-boot-menu.sh 的離線回歸測試。
#
# 這支腳本完全不需要網路、xorriso、qemu，也不需要任何一個真正的
# Debian ISO——它只是拿幾份「仿照真實 Debian isolinux/grub.cfg 格式
# 手寫出來」的假設定檔，餵給 build-iso.sh 實際會用到的同一份
# gonas_patch_boot_menu_file() 函式(來源自 lib/patch-boot-menu.sh，
# 不是複製貼上的另一份程式碼)，然後檢查結果是否符合預期。
#
# 這個測試存在的理由:build-iso.sh 裡原本的 grub "---" 注入邏輯，
# 曾經連續兩輪(先假設 "---" 一定在行尾，後來改成「行尾前可以有空白」)
# 都被人工覆閱誤判為「看起來沒問題」，直到真的寫了這種仿真測試資料
# 去跑，才發現真實的 grub.cfg 常見格式是 `--- quiet`("---" 後面接著
# 別的核心參數，不是行尾也不是只有空白)，兩輪的假設都是錯的。這是
# build/appliance/ 目錄裡目前唯一一段真的有可執行測試證據支持的邏輯
# ——把當時的手動測試過程整理成這支固定下來、可以重複執行的腳本，
# 之後不管是改 lib/patch-boot-menu.sh 還是換了 Debian 版本格式，都能
# 立刻重新驗證，不必再手動重新寫一次測試資料。
#
# 用法(在有 /bin/sh、sed、grep 的任何機器/CI 上都能跑，不需要網路):
#   sh build/appliance/test-boot-menu-patch.sh
#
# 執行成功會印出每個案例的 PASS，並以 exit code 0 結束；任何一個案例
# 沒有命中預期結果，會印出 FAIL 訊息並以非 0 結束。
#
# 注意:這支腳本只驗證「開機選單參數注入」這一小段邏輯本身，*不*
# 涵蓋 build-iso.sh 其餘部分(下載/解開/xorriso 重新包裝真正的 ISO)
# ——那些部分需要網路跟外部工具，在這個開發沙盒裡完全沒有辦法執行，
# 詳見 build-iso.sh 檔案開頭與 README.md 的說明。

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# lib/patch-boot-menu.sh 的 gonas_patch_boot_menu_file() 內部會呼叫
# gonas_sed_inplace(見 lib/portable-sed.sh,第十七輪覆閱抓到的
# macOS/BSD sed 相容性修正),要先來源進來。
. "$SCRIPT_DIR/lib/portable-sed.sh"
. "$SCRIPT_DIR/lib/patch-boot-menu.sh"

TEST_WORK_DIR="$(mktemp -d /tmp/gonas-boot-menu-test.XXXXXX)"
trap 'rm -rf "$TEST_WORK_DIR"' EXIT

APPEND_EXTRA="auto=true priority=high preseed/file=/cdrom/gonas/preseed.cfg hostname=gonas domain="
APPEND_MARKER="gonas/preseed.cfg"

FAIL=0

# 檢查一個檔案是否包含 APPEND_MARKER，並回報 PASS/FAIL。
assert_injected() {
    _case_name="$1"
    _file="$2"
    if grep -q "$APPEND_MARKER" "$_file" 2>/dev/null; then
        echo "PASS: $_case_name"
    else
        echo "FAIL: $_case_name — expected to find '$APPEND_MARKER' in patched output, but did not. Actual content:" >&2
        sed 's/^/    | /' "$_file" >&2
        FAIL=1
    fi
}

# --- 案例 1: isolinux 的 "append ..." 一行式參數列 ---------------------
CASE1="$TEST_WORK_DIR/case1-isolinux-append.cfg"
cat > "$CASE1" <<'EOF'
label install
	menu label ^Install
	kernel /install.amd/vmlinuz
	append vga=788 initrd=/install.amd/initrd.gz --- quiet
EOF
gonas_patch_boot_menu_file "$CASE1" "$APPEND_EXTRA"
assert_injected "isolinux append line" "$CASE1"

# --- 案例 2: grub.cfg，"---" 後面接著其他核心參數(最常見的真實格式) ---
CASE2="$TEST_WORK_DIR/case2-grub-dashes-then-params.cfg"
cat > "$CASE2" <<'EOF'
menuentry "Install" {
	set background_color=black
	linux	/install.amd/vmlinuz vga=788 --- quiet
	initrd	/install.amd/initrd.gz
}
EOF
gonas_patch_boot_menu_file "$CASE2" "$APPEND_EXTRA"
assert_injected "grub '--- quiet' (params after triple-dash)" "$CASE2"

# --- 案例 3: grub.cfg，"---" 後面接的是空白字元、不是直接行尾(第二輪
# 覆閱當時「允許尾端空白」的修法設計要涵蓋的情境——這裡故意用 printf
# 而不是 heredoc 寫出這一行,確保 "---" 後面那個空白字元不會被任何
# 工具(編輯器/heredoc 處理)不小心修剪掉,真的測到「有空白」這個情境,
# 不是又跟案例 4 一樣變成沒有任何字元的行尾。之前這裡曾經用 heredoc
# 寫,結果那個空白字元被拿掉了、內容其實跟案例 4 一模一樣,兩個案例名字
# 說要測不同情境,實際上測的是同一份輸入——這是覆閱這支測試腳本本身
# 時，用 `diff` 直接比對兩個案例的檔案內容才抓到的,不是靠看程式碼
# 看出來的。) ---
CASE3="$TEST_WORK_DIR/case3-grub-dashes-trailing-space.cfg"
printf 'menuentry "Install" {\n\tlinux\t/install.amd/vmlinuz vga=788 --- \n\tinitrd\t/install.amd/initrd.gz\n}\n' > "$CASE3"
if ! grep -q -- '--- $' "$CASE3"; then
    echo "FAIL: test setup error — case3 was supposed to contain a trailing space after '---' but does not (got mangled somewhere)" >&2
    FAIL=1
fi
gonas_patch_boot_menu_file "$CASE3" "$APPEND_EXTRA"
assert_injected "grub '---' with trailing whitespace before end of line" "$CASE3"

# --- 案例 4: grub.cfg，"---" 是這一行最後的東西，什麼都不接(模擬 arm64/EFI 常見格式) ---
CASE4="$TEST_WORK_DIR/case4-grub-bare-dashes.cfg"
cat > "$CASE4" <<'EOF'
menuentry "Install" {
	linux	/install.amd/vmlinuz vga=788 ---
	initrd	/install.amd/initrd.gz
}
EOF
gonas_patch_boot_menu_file "$CASE4" "$APPEND_EXTRA"
assert_injected "grub bare '---' with nothing after it" "$CASE4"

# --- 案例 5: 品牌化字樣有沒有真的被換掉(不影響安裝行為,但至少確認邏輯有跑) ---
CASE5="$TEST_WORK_DIR/case5-branding.cfg"
cat > "$CASE5" <<'EOF'
menu title Debian GNU/Linux installer
label install
	menu label ^Install Debian
	kernel /install.amd/vmlinuz
	append vga=788 ---
EOF
gonas_patch_boot_menu_file "$CASE5" "$APPEND_EXTRA"
# 第五十三輪:menu title 現在會整行刪除(避免疊在 splash 上),所以標題那句
# 不再存在;要驗證的是「選單項目」的品牌字有換、且完全沒有 Debian 殘留、
# 且 menu title 行確實被拿掉。
if grep -q 'Install GoNAS' "$CASE5" && ! grep -q 'Debian' "$CASE5" && ! grep -q 'menu title' "$CASE5"; then
    echo "PASS: branding text replaced and menu title line removed"
else
    echo "FAIL: branding text was not replaced as expected, or menu title not removed. Actual content:" >&2
    sed 's/^/    | /' "$CASE5" >&2
    FAIL=1
fi

# --- 案例 6: 擴大後的品牌化——選單項目的 "Debian GNU/Linux" 要變成
# "GoNAS",但「用磁碟標籤找開機檔」那種功能性的 search --label 行裡的
# "Debian" 絕對不能被動到(盲目 s/Debian/GoNAS/g 會讓機器開不了機,見
# lib/patch-boot-menu.sh 的說明)。這個案例同時驗證「該換的換了」跟
# 「不該動的沒動」兩件事。---
CASE6="$TEST_WORK_DIR/case6-branding-safe.cfg"
cat > "$CASE6" <<'EOF'
menu title Debian GNU/Linux installer boot menu
menuentry "Debian GNU/Linux" {
	search --set=root --label 'Debian 13.6.0 amd64 1'
	linux	/install.amd/vmlinuz vga=788 ---
	initrd	/install.amd/initrd.gz
}
EOF
gonas_patch_boot_menu_file "$CASE6" "$APPEND_EXTRA"
# menuentry 的顯示字要換成 GoNAS;menu title 行要被刪掉;而「用磁碟標籤找開機
# 檔」那行 search --label 'Debian 13.6.0 amd64 1' 是功能性字串,絕對不能動。
if grep -q 'menuentry "GoNAS"' "$CASE6" \
   && ! grep -q 'menu title' "$CASE6" \
   && grep -q "label 'Debian 13.6.0 amd64 1'" "$CASE6" \
   && ! grep -q 'label .GoNAS 13' "$CASE6"; then
    echo "PASS: menuentry rebranded, menu title removed, search --label volume label untouched"
else
    echo "FAIL: branding missed the menuentry, left a menu title, or (dangerously) rewrote the search --label volume label. Actual content:" >&2
    sed 's/^/    | /' "$CASE6" >&2
    FAIL=1
fi

# --- 案例 7: 第二十九輪擴大的品牌化——不帶 "GNU/Linux" 的短寫標題
# "Debian installer" / "Debian Installer" 也要換成 GoNAS,同時再次確認
# 帶版本號的 search --label 'Debian 13...' 這種功能性字串沒有被動到
# (短寫規則不含斜線、也不含版本數字,不可能誤傷卷標)。---
CASE7="$TEST_WORK_DIR/case7-branding-shortform.cfg"
cat > "$CASE7" <<'EOF'
menu title Debian installer main menu
menuentry "Debian Installer" {
	search --set=root --label 'Debian 13.6.0 amd64 1'
	linux	/install.amd/vmlinuz vga=788 ---
	initrd	/install.amd/initrd.gz
}
EOF
gonas_patch_boot_menu_file "$CASE7" "$APPEND_EXTRA"
# 短寫標題 "Debian installer main menu" 這一行是 menu title,現在會被整行刪掉;
# menuentry "Debian Installer" 的顯示字要換成 GoNAS;帶版本號的 search --label
# 不能動。
if ! grep -q 'menu title' "$CASE7" \
   && grep -q 'menuentry "GoNAS Installer"' "$CASE7" \
   && grep -q "label 'Debian 13.6.0 amd64 1'" "$CASE7" \
   && ! grep -q 'label .GoNAS 13' "$CASE7"; then
    echo "PASS: short-form menuentry rebranded, menu title removed, version-labeled search left intact"
else
    echo "FAIL: short-form branding missed, left a menu title, or (dangerously) rewrote the search --label volume label. Actual content:" >&2
    sed 's/^/    | /' "$CASE7" >&2
    FAIL=1
fi

# --- 案例 8: 第五十三輪(實機重疊)——vesamenu 的 `menu title` 必須被整行
# 刪掉(它會疊在已有品牌字的 splash 背景圖上,兩層字重疊變亂碼),而選單
# 項目(label/kernel/append)必須原封不動、preseed 參數照樣注入得進去。---
CASE8="$TEST_WORK_DIR/case8-menu-title-removed.cfg"
cat > "$CASE8" <<'EOF'
menu title GoNAS Installer menu (BIOS mode)
label install
	menu label ^Graphical install
	kernel /install.amd/vmlinuz
	append vga=788 initrd=/install.amd/gtk/initrd.gz ---
EOF
gonas_patch_boot_menu_file "$CASE8" "$APPEND_EXTRA"
if ! grep -q 'menu title' "$CASE8" \
   && grep -q 'menu label ^Graphical install' "$CASE8" \
   && grep -q "$APPEND_MARKER" "$CASE8"; then
    echo "PASS: menu title line removed while menu entries and preseed injection are intact"
else
    echo "FAIL: menu title was not removed, or a menu entry / preseed injection was damaged. Actual content:" >&2
    sed 's/^/    | /' "$CASE8" >&2
    FAIL=1
fi

# --- 案例 9: 第五十九輪(改回圖形安裝界面)——gtk initrd 路徑「不」應被改寫,
# 維持指向 gtk 版 initrd(圖形安裝),而 preseed 參數照樣注入得進去。---
CASE9="$TEST_WORK_DIR/case9-keep-gtk-initrd.cfg"
cat > "$CASE9" <<'EOF'
label gtkinstall
	menu label ^Graphical install
	kernel /install.amd/gtk/vmlinuz
	append vga=788 initrd=/install.amd/gtk/initrd.gz ---
EOF
gonas_patch_boot_menu_file "$CASE9" "$APPEND_EXTRA"
if grep -q '/install.amd/gtk/initrd.gz' "$CASE9" \
   && grep -q '/install.amd/gtk/vmlinuz' "$CASE9" \
   && grep -q "$APPEND_MARKER" "$CASE9"; then
    echo "PASS: gtk initrd/vmlinuz paths left intact (graphical install kept), preseed still injected"
else
    echo "FAIL: gtk paths were altered or preseed injection broke. Actual content:" >&2
    sed 's/^/    | /' "$CASE9" >&2
    FAIL=1
fi

echo
if [ "$FAIL" = "0" ]; then
    echo "==> all boot-menu-patch test cases passed"
    exit 0
else
    echo "==> one or more boot-menu-patch test cases FAILED — see above" >&2
    exit 1
fi
