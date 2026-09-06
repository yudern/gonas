# find-boot-menu-cfgs.sh 把「找出所有可能需要注入 preseed 開機參數的
# isolinux/grub 設定檔案」這個小動作獨立成一個函式,理由是這是第十八輪
# 覆閱(使用者實測 arm64 建置)才真的撞見的一個 `find` + `set -e` 交互
# 作用的陷阱,值得像其他純邏輯問題一樣,寫一支不需要真的建置 ISO 就能
# 重複驗證的離線回歸測試,避免以後又在同一個地方摔一次。
#
# 用法:
#   . "$(dirname "$0")/lib/find-boot-menu-cfgs.sh"
#   gonas_find_boot_menu_cfgs "$ISOLINUX_DIR" "$GRUB_DIR" "$OUTPUT_FILE"
#
# 把 $ISOLINUX_DIR、$GRUB_DIR 底下所有 *.cfg/txt.cfg 檔案的路徑,一行
# 一個,寫進 $OUTPUT_FILE。
#
# 問題的origin:build-iso.sh 一開頭就設定了 `set -eu`,而 `find` 同時
# 給兩個起始路徑時,如果其中一個根本不存在——這正是 arm64 官方 ISO的
# 正常情況,arm64 是 EFI-only,本來就沒有 isolinux/ 目錄,只有 amd64
# 才有——`find` 仍然會正確找到另一個存在的路徑底下的檔案、正確寫進
# 輸出檔案,但 `find` 自己的 exit code 會因為那個「路徑不存在」的
# 錯誤變成非 0。在 `set -eu` 底下,呼叫端完全沒有機會檢查「到底有沒有
# 找到檔案」,腳本會在這一行當場死掉,而且因為
# `2>/dev/null` 把 find 自己的錯誤訊息也吃掉了,螢幕上不會出現任何
# 一行看得懂的錯誤訊息,只會看到 make 印出的 `Error 1`——這正是這段
# 邏輯的呼叫端後來想避免的「沉默失敗」,諷刺的是問題出在 `find`
# 本身的 exit code,不是呼叫端寫的判斷邏輯。
#
# 修法:這個函式內部用 `|| true` 明確吃掉 `find` 自己的 exit code——
# 「兩個目錄底下加起來到底有沒有找到任何檔案」這件事,交給呼叫端
# 事後自己讀輸出檔案的行數判斷(build-iso.sh 本來就有這一段檢查,問題
# 只出在它根本沒有機會執行到),不是靠 `find` 的 exit code 間接猜測。
gonas_find_boot_menu_cfgs() {
    _fbmc_isolinux_dir="$1"
    _fbmc_grub_dir="$2"
    _fbmc_outfile="$3"
    find "$_fbmc_isolinux_dir" "$_fbmc_grub_dir" -type f \( -name '*.cfg' -o -name 'txt.cfg' \) 2>/dev/null > "$_fbmc_outfile" || true
}
