# patch-boot-menu.sh 是「幫一份 isolinux/grub 開機選單設定檔插入
# preseed 自動安裝參數」這段邏輯本身,獨立成一個小函式庫檔案(用
# `.` 來源、不是直接執行),理由是這是整個 build/appliance/ 目錄裡
# 少數幾段完全不需要真的連網、可以在任何沙盒/CI 環境裡直接拿假資料
# 驗證的邏輯——獨立出來之後,`build-iso.sh`(真正建置用)跟
# `test-boot-menu-patch.sh`(離線驗證用,見同目錄)可以共用同一份
# 邏輯,不會出現「測試跑的是一份複製貼上、可能跟正式邏輯不同步」的
# 情況。
#
# 用法:
#   . "$(dirname "$0")/lib/patch-boot-menu.sh"
#   gonas_patch_boot_menu_file "$cfgfile" "$APPEND_EXTRA"

gonas_patch_boot_menu_file() {
    _pbm_file="$1"
    _pbm_append_extra="$2"

    # isolinux 語法用 "append ..." 這一行帶核心參數,直接整行加在最
    # 後面即可——isolinux 的 append 行沒有 grub 那種用 "---" 分隔
    # 「核心參數」跟「init 參數」的慣例,單純是一串扁平的參數列表。
    # sed -i 的呼叫都改用 gonas_sed_inplace(見 lib/portable-sed.sh)
    # 而不是直接 `sed -i "..." 檔案`——第十七輪覆閱抓到的問題:GNU sed
    # 的 `-i`(不接參數)在 macOS 內建的 BSD sed 底下是另一回事,直接
    # 這樣寫在 Mac 上執行會做出完全不對的事,不是單純的指令找不到,
    # 細節見 lib/portable-sed.sh 開頭的說明。
    if grep -q '^[[:space:]]*append ' "$_pbm_file" 2>/dev/null; then
        gonas_sed_inplace "s#^\([[:space:]]*append .*\)\$#\\1 $_pbm_append_extra#" "$_pbm_file"
    fi

    # grub.cfg 用 "linux ... ---" 這種格式,但這裡刻意不假設 "---"
    # 是這一行最後一個字元——這是真的拿仿真測試資料測出來的教訓
    # (見 build-iso.sh 呼叫這個函式那一段的完整說明,或直接跑
    # test-boot-menu-patch.sh 重現):真實的 grub.cfg 常常長得像
    # `linux /install.amd/vmlinuz vga=788 --- quiet`,"---" 後面還接著
    # `quiet` 這類參數,不是行尾。改成不管 "---" 前後有沒有其他字，
    # 只要它出現在這一行的任何位置,一律把要注入的參數插在它「前面」
    # ——這才是語意上正確的位置:"---" 是 d-i 用來分隔「給核心本身
    # 的參數」跟「給開機後 init 行程的參數」的界線,`auto=true`/
    # `priority=high`/`preseed/file=` 這些全部屬於前者。
    # 這個判斷本身之前也是錯的,而且一樣是靠實際寫測試資料才抓到:
    # 原本寫的是 `grep -q '<tab>linux '`(tab 加 "linux" 加「空白」)跟
    # `grep -q '^[[:space:]]*linux '`(行首空白加 "linux" 加「空白」)
    # ——兩個規則都假設 "linux" 後面接的分隔字元是「空白鍵」,但真實的
    # grub.cfg(以及這裡拿來測試的仿真檔案)是用 tab 字元把 "linux" 跟
    # 核心路徑隔開的(`\tlinux\t/install.amd/vmlinuz ...`),不是空白鍵。
    # 用字面 "linux " 去比對,在真實格式底下永遠不會命中,guard 判斷
    # 為「這不是一行 linux 開機參數」,底下真正做注入的 sed 就整段被
    # 跳過——結果是 grub.cfg 完全沒被修改,卻不會有任何錯誤訊息,是
    # 靜默失敗。改用 [[:space:]] 字元類別(在 POSIX BRE 的中括號裡合法,
    # 同時涵蓋空白鍵跟 tab)取代字面空白鍵,兩種分隔字元都認得。
    if grep -q '[[:space:]]linux[[:space:]]' "$_pbm_file" 2>/dev/null || grep -q '^[[:space:]]*linux[[:space:]]' "$_pbm_file" 2>/dev/null; then
        gonas_sed_inplace "s#---#$_pbm_append_extra ---#" "$_pbm_file"
    fi

    # 選單標題品牌化——把看得到的 "Debian GNU/Linux installer" 字樣
    # 換成 "GoNAS Installer",純粹是顯示文字，不影響實際安裝行為。
    gonas_sed_inplace 's/Debian GNU\/Linux installer/GoNAS Installer/g; s/Install Debian/Install GoNAS/g' "$_pbm_file" || true
}
