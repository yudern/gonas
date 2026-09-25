# scan-pool-packages.sh —— 判斷「一片 Debian 安裝 DVD 的 pool/ 底下,到底有
# 哪些指定的套件」。第五十七輪(使用者第三次實機:離線裝套件一個都沒進去)
# 之後獨立出來的小邏輯,理由跟 lib/ 其他檔案一樣:這是唯一能在沙盒/CI 裡用
# 假資料驗證的部分(不需要真的 DVD、不需要 apt),把它跟 build-iso.sh 分開,
# 才能寫一支離線回歸測試(test-scan-pool-packages.sh)。
#
# 用法:
#   . "$(dirname "$0")/lib/scan-pool-packages.sh"
#   present="$(gonas_scan_pool_packages "$POOL_DIR" mergerfs samba docker.io)"
#
# 回傳(印到 stdout,空白分隔):candidates 裡「pool 底下真的有對應 .deb 檔」
# 的那些,順序照傳入順序。.deb 檔名格式是 <pkg>_<version>_<arch>.deb,所以用
# "<pkg>_" 這個前綴精準比對——這樣 "samba" 不會誤命中 "samba-common"
# ("samba-common_..." 前綴不同),也不會誤命中 "libsamba"。
gonas_scan_pool_packages() {
    _spp_pool="$1"
    shift
    _spp_idx="$(mktemp 2>/dev/null || echo /tmp/gonas-pool-idx.$$)"
    # 一次把 pool 底下所有 .deb 的「檔名」列出來,之後用 grep 比對,避免對每個
    # 套件各走一次上萬檔案的 pool 樹。GNU find 用 -printf 最快;BSD find
    # (macOS)沒有 -printf,退回 find | sed 去掉路徑。
    if ! find "$_spp_pool" -name '*.deb' -printf '%f\n' > "$_spp_idx" 2>/dev/null; then
        find "$_spp_pool" -name '*.deb' 2>/dev/null | sed 's#.*/##' > "$_spp_idx"
    fi
    _spp_out=""
    for _spp_pkg in "$@"; do
        if grep -q "^${_spp_pkg}_" "$_spp_idx" 2>/dev/null; then
            _spp_out="$_spp_out $_spp_pkg"
        fi
    done
    rm -f "$_spp_idx"
    # 去掉開頭空白後印出。
    printf '%s\n' "$_spp_out" | sed 's/^ *//;s/ *$//'
}
