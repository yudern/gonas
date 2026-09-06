# portable-checksum.sh 把「算 SHA256/MD5」這兩個 build-iso.sh 需要的
# 小動作獨立成函式,理由跟同目錄其他 lib/*.sh 一樣:build-iso.sh 跟
# test-portable-checksum.sh(離線回歸測試,見同目錄)共用同一份邏輯,
# 不會出現「測試跑的是一份可能跟正式邏輯不同步的複製品」的情況。
#
# 背景(第十七輪覆閱抓到的問題,而且是這一次因為使用者真的要在自己的
# Mac 上執行 build-iso.sh,才第一次會真正踩到的問題):build-iso.sh
# 原本直接呼叫 `sha256sum`/`md5sum` 這兩個指令——這兩個是 GNU coreutils
# 附帶的工具,Debian/Ubuntu 這類 Linux 發行版本來就有,但 macOS 內建的
# BSD 使用者空間完全沒有這兩個指令(命令列會直接報 "command not
# found"),macOS 對應的原生指令是 `shasum -a 256`(算 SHA256)跟
# `md5 -r`(算 MD5,`-r` 是「反過來,先印雜湊再印檔名」,輸出格式才會
# 跟 GNU 版本一致)。這整個 Phase 19 appliance 之前 16 輪覆閱都是在一台
# Linux 開發沙盒裡進行的,從來沒有人在 macOS 上真的執行過
# build-iso.sh,這個問題完全沒有被任何一輪覆閱發現過,是使用者告知
# 「要在自己的 Mac mini 上跑」之後,回頭檢查這支腳本用到的每一個外部
# 指令在 macOS 上是否存在,才發現的。
#
# 用法:
#   . "$(dirname "$0")/lib/portable-checksum.sh"
#   gonas_sha256sum "$file"     # 輸出格式跟 `sha256sum "$file"` 相容
#                                 (兩個空白分隔雜湊值跟檔名),可以直接
#                                 接 `| awk '{print $1}'` 取雜湊值,也
#                                 可以直接重導向存成 .sha256 驗證檔。
#   gonas_md5sum "$file"        # 同上,輸出格式跟 `md5sum "$file"` 相容。

gonas_sha256sum() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1"
    elif command -v shasum >/dev/null 2>&1; then
        # macOS 內建 Perl 的 Digest::SHA 包出來的 shasum,`-a 256` 指定
        # 演算法——輸出格式(雜湊值、兩個空白、檔名)跟 GNU sha256sum
        # 相容,呼叫端不需要另外處理。
        shasum -a 256 "$1"
    else
        echo "gonas_sha256sum: error: neither 'sha256sum' nor 'shasum' found on this machine — cannot verify checksums (on macOS, shasum ships with the OS by default; on Linux, install coreutils)" >&2
        return 1
    fi
}

gonas_md5sum() {
    if command -v md5sum >/dev/null 2>&1; then
        md5sum "$1"
    elif command -v md5 >/dev/null 2>&1; then
        # macOS 內建的 `md5` 預設輸出格式是 "MD5 (檔名) = 雜湊值",跟
        # GNU md5sum 的 "雜湊值  檔名" 完全不同——`-r` 這個旗標會把輸出
        # 順序反過來、改成先印雜湊值再印檔名,格式才會跟 GNU 版本相容。
        md5 -r "$1"
    else
        echo "gonas_md5sum: error: neither 'md5sum' nor 'md5' found on this machine — cannot compute md5sum.txt" >&2
        return 1
    fi
}
