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

    # 選單標題/項目品牌化——把安裝媒體開機選單裡看得到的 Debian 字樣
    # 換成 GoNAS,純粹是顯示文字,不影響實際安裝行為。第二十七輪(使用者
    # 要求「不管是安裝還是哪裡,都要換 GoNAS logo」)把涵蓋範圍從原本只有
    # 「Debian GNU/Linux installer」「Install Debian」兩句,擴大到也涵蓋
    # 選單項目標題常見的「Debian GNU/Linux」這個確切字串。
    #
    # 刻意「不」做整檔 `s/Debian/GoNAS/g` 這種盲目全域替換——這會踩到一個
    # 會讓機器開不了機的地雷:部分 Debian ISO 的 grub.cfg 用
    # `search --label 'Debian 13.6.0 amd64 1'` 這類「用磁碟標籤找開機檔」
    # 的寫法,那一行裡的 "Debian" 是真正拿去比對 ISO 卷標(volume label)
    # 的功能性字串,不是顯示文字,一旦被換成 GoNAS,search 就會找不到那個
    # 標籤、grub 直接進不了下一步。所以這裡只替換「確切、且只可能出現在
    # 顯示文字裡」的完整片語:"Debian GNU/Linux" 這個帶斜線的確切字串
    # 不會出現在卷標(卷標長得像 "Debian 13.6.0 amd64 1")或核心路徑
    # (像 /install.amd/vmlinuz)裡,替換它是安全的。順序上「installer」
    # 那句要排在「Debian GNU/Linux」前面,才不會先被較短的片語吃掉。
    #
    # 誠實邊界:這只換得動「開機選單」這一層的字。真正進到 debian-installer
    # 之後那些藍底畫面(選語言、分割磁碟、安裝進度)裡的 "Debian" 字樣是
    # 烙在安裝程式自己的 udeb 模板裡的,要改必須重新編譯整個安裝程式,
    # 這個專案刻意不做(風險高、且跟絕大多數以 Debian 為底的 appliance
    # 一樣:安裝過程中會短暫看到 Debian,裝完重開機後才全面變成 GoNAS
    # 品牌——見 late-command.sh 第 3 節的開機後品牌化)。這個邊界在
    # README/文件裡有說清楚。
    #
    # 第二十九輪(使用者要求「安裝全過程都看不到 Debian」)再補兩句同樣
    # 「確切、且只可能出現在顯示文字」的片語:"Debian Installer" /
    # "Debian installer"(不帶 GNU/Linux 的寫法,某些版本的選單標題會用
    # 這種短寫,例如 `menu title Debian installer main menu`)。這兩句
    # 一樣不可能出現在卷標裡——卷標長得像 "Debian 13.6.0 amd64 1",裡面
    # 沒有 "installer" 這個字,也沒有斜線,所以替換它們不會踩到那條會
    # 讓機器開不了機的 `search --label 'Debian 13.x ...'` 地雷。仍然刻意
    # 「不」做整檔盲目 s/Debian/GoNAS/g,理由見上面那段長註解。
    gonas_sed_inplace 's/Debian GNU\/Linux installer/GoNAS Installer/g; s/Install Debian GNU\/Linux/Install GoNAS/g; s/Debian GNU\/Linux/GoNAS/g; s/Debian Installer/GoNAS Installer/g; s/Debian installer/GoNAS installer/g; s/Install Debian/Install GoNAS/g' "$_pbm_file" || true

    # 第五十三輪(使用者實機拍到):BIOS 開機選單的背景圖(branding/splash.png)
    # 本身已經印上「GoNAS / NETWORK ATTACHED STORAGE」品牌字,但 vesamenu.c32
    # 還會把自己的 `menu title` 這一行文字疊在背景圖的同一塊位置,兩層字重疊
    # 變成一團看不懂的亂碼(標題「GoNAS Installer menu (BIOS mode)」剛好壓在
    # splash 的 tagline 上)。splash 已經完整負責品牌顯示,所以這裡把 `menu
    # title` 整行刪掉,只留背景圖的字,重疊就消失。純顯示調整,不影響開機/
    # 安裝行為;選單項目(Graphical install 等)的位置由 menu vshift/rows 決定,
    # 不受標題行有無影響。grub(UEFI)沒有 `menu title` 這種會疊在 splash 上的
    # 標題,不受影響。
    gonas_sed_inplace '/^[[:space:]]*menu title[[:space:]]/d' "$_pbm_file" || true
}
