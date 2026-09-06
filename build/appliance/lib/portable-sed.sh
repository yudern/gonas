# portable-sed.sh 把「原地修改一個檔案的內容」這個小動作獨立成一個
# 函式,理由是這是第十七輪覆閱抓到的、影響範圍最廣的一個 macOS 相容性
# 問題:`sed -i 'script' file` 這個寫法(不帶任何參數的 `-i`,GNU sed
# 的用法,代表「原地修改、不留備份」)在 macOS 內建的 BSD sed 底下,
# 完全是另一回事——BSD sed 的 `-i` 語法上「一定」要接一個參數當備份
# 副檔名(可以給空字串,但一定要是獨立的一個引數),`sed -i 'script'
# file` 在 BSD sed 眼裡,`-i` 吃掉的下一個引數是 `'script'`(被當成
# 備份副檔名,不是 sed 的操作腳本),然後 `file` 反而被當成 sed 的
# script 去執行——這不是「指令找不到」這種容易發現的錯誤,是「執行了,
# 但做的完全不是原本要做的事」,而且這個專案裡好幾支腳本(build-iso.sh
# 本身、lib/patch-boot-menu.sh)都用到 `sed -i`,原本全部都是這個寫法。
#
# 修法:`-i` 後面直接接一個(非空的)備份副檔名,例如 `-i.bak`——這是
# 唯一在 GNU sed 跟 BSD sed 底下語法完全一致、行為也一致的寫法,兩邊
# 都會先寫一份 `<檔名>.bak` 備份、再原地修改原始檔案。這個函式把「用
# `-i.gonas-sed-bak` 呼叫 sed、確認真正的 sed 結果(不是被後面清理
# 備份檔案的 rm 蓋過去)、清掉備份檔案」這一整套動作包起來,呼叫端不用
# 每個呼叫點都重複這幾行。
#
# 用法:
#   . "$(dirname "$0")/lib/portable-sed.sh"
#   gonas_sed_inplace 's/foo/bar/' "$file"
#   # 回傳值就是 sed 本身的 exit code(不是後面清理備份檔案那個 rm 的),
#   # 呼叫端要忽略失敗的話,一樣照平常寫法接 `|| true`。

gonas_sed_inplace() {
    _gsi_script="$1"
    _gsi_file="$2"
    _gsi_status=0
    sed -i.gonas-sed-bak "$_gsi_script" "$_gsi_file" || _gsi_status=$?
    # 不管 sed 本身成功還是失敗都要清掉備份檔案——`rm -f` 對「檔案本來
    # 就不存在」是安全的(不會報錯),所以就算 sed 因為某些原因根本沒
    # 建立備份檔案(理論上不應該發生),這裡也不會出錯。故意不讓這個
    # rm 的結果影響函式的回傳值,見上面的用法說明。
    rm -f "$_gsi_file.gonas-sed-bak"
    return "$_gsi_status"
}
