# detect-arch.sh 把「把 `uname -m` 的輸出對應回 Debian 慣用的架構
# 名稱」這段小邏輯獨立成一個函式,理由跟同目錄的 patch-boot-menu.sh/
# verify-gpg-signature.sh 一樣:這是 late-command.sh 裡少數幾段完全
# 不需要真的在 Debian in-target chroot 環境裡執行、可以直接用固定輸入
# 驗證的邏輯。
#
# 背景(第九輪覆閱時發現的真 bug):late-command.sh 判斷架構原本寫成
# `dpkg --print-architecture 2>/dev/null || uname -m`——主要路徑
# (`dpkg` 成功)算出來的是 Debian 慣用名稱("amd64"/"arm64"),跟
# build-iso.sh 建的 `release-amd64`/`release-arm64` 目錄名稱一致;但
# fallback 用的 `uname -m` 回傳的是核心/硬體慣用名稱
# ("x86_64"/"aarch64"),兩者不是同一套命名,fallback 一旦被觸發就會
# 讓 late-command.sh 去找一個不存在的目錄。這個函式就是修好之後的
# 對應表本身。
#
# 這個檔案原本沒有對應的回歸測試(跟 patch-boot-menu.sh/
# verify-gpg-signature.sh 不一樣)——第十三輪覆閱重新檢查 CI 設定時
# 發現了這個落差,才把這段邏輯抽出來補上 test-detect-arch.sh。
#
# 用法:
#   . "$(dirname "$0")/lib/detect-arch.sh"
#   arch="$(gonas_uname_to_debian_arch "$(uname -m)")"

gonas_uname_to_debian_arch() {
    case "$1" in
        x86_64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) echo "$1" ;;
    esac
}
