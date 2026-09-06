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
    #
    # 第十五輪覆閱抓到的問題:`gpg` 的 `--verify` 訊息是會被
    # gettext/語系翻譯的——`gpg: Good signature from ...` 這句話只有在
    # 英文(或沒有對應翻譯包的)語系底下才會真的是這幾個字,如果建置
    # 這支腳本的人的機器語系是德文/法文/日文/中文之類且裝了對應的
    # gnupg 翻譯包,gpg 印出來的會是翻譯過的字串(例如德文是
    # "Korrekte Signatur von ..."),上面這行 `grep -q "Good signature"`
    # 就永遠不會命中——結果是:即使簽章跟金鑰完全正確,這個函式也會
    # 一律回報「驗證失敗」,而 build-iso.sh 把 GPG 驗證失敗當成硬性
    # 中止條件,對一個特地設定 GONAS_DEBIAN_KEYRING、想多做這一層驗證
    # 的使用者來說,會是一個完全摸不著頭緒、看起來像金鑰或簽章本身有
    # 問題、但其實只是機器語系不是英文的假錯誤。這個開發沙盒只裝了
    # C/C.utf8/POSIX 這幾種語系,沒辦法直接裝一個有翻譯包的語系實際
    # 重現(所以沒辦法像 GPG 驗證邏輯本身那樣「真的用一把金鑰整個跑一次
    # 觀察到症狀」),但 gnupg 的訊息會被 gettext 翻譯這件事本身是
    # GnuPG 行之有年、有文件可查的既有行為,不是憑空猜測。修法很直接:
    # 呼叫 gpg 之前明確把 LC_ALL/LANGUAGE 都釘死成 C,強制它輸出英文
    # 訊息,不管使用者機器本身的語系設定是什麼——這只影響這一次呼叫的
    # 環境變數,不會動到呼叫端(build-iso.sh/test-gpg-verify.sh)或
    # 使用者 shell 本身的語系設定。
    if LC_ALL=C LANGUAGE=C gpg --no-default-keyring --keyring "$_vgs_keyring" --verify "$_vgs_sig" "$_vgs_data" >"$_vgs_log" 2>&1; then
        if grep -q "Good signature" "$_vgs_log"; then
            return 0
        fi
    fi
    return 1
}
