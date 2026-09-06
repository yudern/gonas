# verify-gpg-signature.sh 把「用 gpg 驗證一份 detached signature」這段
# 邏輯獨立成一個小函式庫檔案(用 `.` 來源、不是直接執行),理由跟同目錄
# 的 patch-boot-menu.sh 一樣:這是 build-iso.sh 裡少數幾段「不需要真的
# 連上 Debian 的官方 keyserver/鏡像,可以用假的 gpg 執行檔在任何沙盒/CI
# 環境裡驗證控制流程對不對」的邏輯——獨立出來之後,build-iso.sh(真正
# 建置用)跟 test-gpg-verify.sh(離線驗證用,見同目錄)可以共用同一份
# 判斷邏輯。
#
# 老實說清楚這支函式庫「測過」跟「沒測過」的邊界在哪裡:test-gpg-verify.sh
# 只驗證這個函式在「gpg 回報成功/失敗」兩種情況下,回傳值跟輸出訊息是否
# 正確——用的是一個假的 gpg 執行檔,不是真的 Debian 簽章金鑰,不驗證
# 「真的能不能正確驗證一份 Debian 官方簽出來的 SHA256SUMS.sign」這件事
# 本身。這一層真正的驗證,需要真的連得上網路匯入 Debian 的簽章金鑰、
# 抓到一份真的 SHA256SUMS.sign,這個開發沙盒完全沒辦法做,只能等
# build-iso.sh 在一台有網路的機器上真的執行過一次才算數。
#
# 用法:
#   . "$(dirname "$0")/lib/verify-gpg-signature.sh"
#   if gonas_verify_gpg_signature "$sig_file" "$data_file" "$keyring_file" "$log_file"; then
#       echo "簽章驗證通過"
#   else
#       echo "簽章驗證失敗,詳細內容在 $log_file"
#   fi

gonas_verify_gpg_signature() {
    _vgs_sig="$1"
    _vgs_data="$2"
    _vgs_keyring="$3"
    _vgs_log="$4"

    # 這裡刻意不是只看 gpg 的 exit code——gpg 的 exit code 語意在不同
    # 版本/情境下沒有想像中乾淨(例如某些警告類的情況 exit code 仍然是
    # 0),比較保守可靠的做法是 exit code 成功「而且」輸出裡真的出現
    # "Good signature" 這個字串,兩個條件都成立才算數。`--verify` 的
    # 輸出預設是寫到 stderr,這裡用 2>&1 把 stdout/stderr 都導進同一份
    # log 檔,方便呼叫端事後檢查完整內容。
    if gpg --no-default-keyring --keyring "$_vgs_keyring" --verify "$_vgs_sig" "$_vgs_data" >"$_vgs_log" 2>&1; then
        if grep -q "Good signature" "$_vgs_log"; then
            return 0
        fi
    fi
    return 1
}
