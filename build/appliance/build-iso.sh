#!/bin/sh
# build-iso.sh 把官方 Debian netinst ISO 改造成一份「插上就能自動裝好
# GoNAS」的映像檔:下載官方 ISO -> 解開 -> 塞進 preseed.cfg / 這個目錄
# 裡的其他檔案 / 已經編譯好的 gonasd release tarball -> 修改開機選單
# 的標題跟預設開機參數(自動套用 preseed，不需要在安裝畫面手動選)
# -> 用 xorriso 重新包裝成一份新的、一樣可開機的 ISO。
#
# !!! 這支腳本沒有辦法在目前這個開發沙盒裡執行過一次 !!!
# 這個環境的網路出口白名單會直接擋掉 deb.debian.org(用
# `curl https://deb.debian.org/...` 或甚至沙盒自己的
# `apt-get update` 都會收到 403，已經實際測試確認過，不是猜測)，
# 也沒有安裝 xorriso/qemu 這類工具、也沒有辦法臨時安裝(套件鏡像
# 一樣被擋)。這支腳本是依照 Debian 官方文件（Installation Guide
# 附錄 B 的 preseed 語法、Debian wiki 的「RepackBootableISO」重新
# 包裝 ISO 的標準做法）撰寫，邏輯上力求正確，但在正式用到真實硬體
# 之前，請務必先在一台有網路、能裝軟體的機器上完整跑一次，並且先在
# QEMU 或 VirtualBox 這類虛擬機裡開機測試過安裝流程，見這個目錄下
# README.md「如何驗證」一節的詳細步驟。
#
# 用法:
#   ./build-iso.sh amd64 [version]
#   ./build-iso.sh arm64 [version]
#
# 需要的工具:
#   - Debian/Ubuntu:`apt install xorriso wget`——sha256sum/md5sum
#     隨 coreutils 本來就有,不用另外裝。
#   - macOS(例如用 Homebrew):`brew install xorriso wget`——checksum
#     工具改用系統內建的 shasum/md5(見 lib/portable-checksum.sh),
#     一樣不用另外裝。
# 需要能連上網路下載官方 netinst ISO 跟(可選)驗證 GPG 簽章。

set -eu

ARCH="${1:-}"
VERSION="${2:-$(cd "$(dirname "$0")/../.." && git describe --tags --always --dirty 2>/dev/null || echo dev)}"

case "$ARCH" in
    amd64|arm64) ;;
    *)
        echo "usage: $0 <amd64|arm64> [version]" >&2
        exit 1
        ;;
esac

command -v xorriso >/dev/null 2>&1 || { echo "error: xorriso is required (apt install xorriso)" >&2; exit 1; }
command -v wget >/dev/null 2>&1 || { echo "error: wget is required (apt install wget)" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORK_DIR="$(mktemp -d /tmp/gonas-iso-build.XXXXXX)"
trap 'rm -rf "$WORK_DIR"' EXIT

# gonas_sed_inplace(見 lib/portable-sed.sh)要先於
# lib/patch-boot-menu.sh 來源進來——那個檔案裡的
# gonas_patch_boot_menu_file() 會呼叫這個函式,順序顛倒的話會在真正
# 呼叫到之前都沒事,一直到執行到那一行才會出現「找不到指令」。
. "$SCRIPT_DIR/lib/portable-sed.sh"
# 「幫一份 isolinux/grub 開機選單設定檔插入 preseed 自動安裝參數」這段
# 邏輯獨立成 lib/patch-boot-menu.sh,理由是這是整個 build/appliance/
# 目錄裡少數幾段完全不需要真的連網、可以在沙盒/CI 環境裡直接拿假資料
# 驗證的邏輯——來源(source)進來而不是複製貼上一份,這樣這支正式建置
# 腳本跟 test-boot-menu-patch.sh(離線回歸測試)才會共用同一份邏輯，
# 不會出現「測試跑的是一份可能跟正式邏輯不同步的複製品」這種情況。
. "$SCRIPT_DIR/lib/patch-boot-menu.sh"
. "$SCRIPT_DIR/lib/verify-gpg-signature.sh"
# 第十七輪覆閱抓到的問題:這支腳本下面原本直接呼叫 `sha256sum`/
# `md5sum`,這兩個是 GNU coreutils 工具,Linux 上到處都有,但 macOS
# 內建的 BSD 使用者空間完全沒有——如果直接在一台 Mac 上執行這支腳本
# (而不是在 Linux 機器/VM 上),會在第一次驗證 ISO 雜湊值時就直接
# 「command not found」中止。`gonas_sha256sum`/`gonas_md5sum` 是修好
# 之後的版本,理由/實作見 lib/portable-checksum.sh 開頭的說明。
. "$SCRIPT_DIR/lib/portable-checksum.sh"
# 第十八輪覆閱(使用者實測 arm64 建置)抓到的問題:找開機選單設定檔的
# `find` 呼叫,理由/實作見 lib/find-boot-menu-cfgs.sh 開頭的說明。
. "$SCRIPT_DIR/lib/find-boot-menu-cfgs.sh"
# 第十九輪覆閱「模式一:離線 SSH」——算 openssh-server 相依封閉集、
# 決定要打包哪些 .deb 進 ISO 的核心邏輯,見 lib/deb-closure.sh 開頭的
# 說明。真正的下載步驟在下面 4.5 節,是 best-effort(失敗只記警告、
# 不會讓整個建置或 appliance 壞掉)。
. "$SCRIPT_DIR/lib/deb-closure.sh"

echo "==> building GoNAS appliance ISO for $ARCH (version $VERSION)"
echo "==> work dir: $WORK_DIR"

# --- 1. 準備 gonasd release tarball ---------------------------------
# 直接複用既有的 `make release` 產物（見 repo 根目錄的 Makefile）：
# 跨平台編譯本身在這個開發沙盒裡完全沒問題（純 Go 交叉編譯，不需要
# 網路），只有「抓官方 Debian ISO」跟「跑 xorriso/qemu」這幾步才需要
# 這個沙盒沒有的東西——這也是為什麼 gonasd 本體的建置沒有被擋在這支
# 腳本外面，而是假設呼叫端已經跑過 `make release`。
RELEASE_TARBALL="$REPO_ROOT/dist/release/gonas-$VERSION-linux-$ARCH.tar.gz"
if [ ! -f "$RELEASE_TARBALL" ]; then
    echo "==> release tarball not found at $RELEASE_TARBALL, building it now"
    ( cd "$REPO_ROOT" && make release VERSION="$VERSION" )
fi
if [ ! -f "$RELEASE_TARBALL" ]; then
    echo "error: still could not find $RELEASE_TARBALL after 'make release' — check the Makefile's VERSION handling" >&2
    exit 1
fi

# --- 2. 下載官方 Debian netinst ISO ----------------------------------
# 第十八輪覆閱(使用者實測)才真的抓到的問題:這裡以前假設
# cdimage.debian.org 的 netinst 檔名是用「版本代號」組出來的(例如
# `debian-bookworm-amd64-netinst.iso`),這個假設從一開始就是錯的,
# 而且錯得很基本——這個開發沙盒完全連不到 cdimage.debian.org,前
# 17 輪不管做多仔細的靜態審查都不可能發現。使用者實測第一次真的呼叫
# `wget`/`awk` 去比對真正的 SHA256SUMS 內容,才第一次真正暴露:
# `debian-cd/current/<arch>/iso-cd/` 底下的檔名實際上是用完整版本
# 號組出來的(例如 `debian-13.6.0-arm64-netinst.iso`),不是代號,
# 「代號」只用在 APT 的套件庫路徑(`/debian/dists/bookworm/`)這種
# 完全不同的地方,兩者是兩套不相干的命名慣例,不能套用同一個假設。
# 修法:不再自己組出一個「猜測的」檔名,而是先把 SHA256SUMS 抓下來,
# 再從裡面實際找出符合 `debian-<版本號>-<arch>-netinst.iso` 這個格式
# 的那一行,檔名跟版本號都直接來自 Debian 官方當下真正發布的內容,
# 不管 Debian 之後從 trixie 換到下一個代號、或同一個穩定版又出新的
# point release(12.11.0 -> 12.12.0 這種),都不需要回來改這支腳本。
case "$ARCH" in
    amd64) DEBIAN_ARCH_DIR="amd64" ;;
    arm64) DEBIAN_ARCH_DIR="arm64" ;;
esac
BASE_ISO_URL="${GONAS_DEBIAN_ISO_URL:-https://cdimage.debian.org/debian-cd/current/$DEBIAN_ARCH_DIR/iso-cd}"

# 先抓 SHA256SUMS(每次都重新抓,不快取——這份清單很小,而且要用它來
# 判斷「快取的 base.iso 還算不算數」,快取 SHA256SUMS 本身會讓這個判斷
# 失去意義)。
SHA256SUMS_URL="$BASE_ISO_URL/SHA256SUMS"
echo "==> fetching $SHA256SUMS_URL"
if ! wget -q -O "$WORK_DIR/SHA256SUMS" "$SHA256SUMS_URL"; then
    echo "error: could not download $SHA256SUMS_URL to verify the base ISO's checksum — refusing to continue with an unverified ISO" >&2
    exit 1
fi

# 從 SHA256SUMS 實際列出的檔名裡,找符合
# debian-<版本號,例如 13.6.0>-<arch>-netinst.iso 格式的那一行——不是
# 事先假設好一個檔名再回頭比對。`grep -o` 在 GNU 跟 BSD(macOS 內建)
# grep 底下都支援,不需要額外的相容性包裝。`head -n1` 是防呆:目前
# 這個目錄底下就只會有一個 netinst 映像檔,理論上只會有一個結果,
# 但如果 Debian 未來改變目錄結構、同時列出多個候選,寧可明確只取第一個
# 也不要整個比對邏輯壞掉。
BASE_ISO_NAME="$(grep -o 'debian-[0-9][0-9.]*-'"$DEBIAN_ARCH_DIR"'-netinst\.iso' "$WORK_DIR/SHA256SUMS" | head -n1)"
if [ -z "$BASE_ISO_NAME" ]; then
    echo "error: could not find a 'debian-<version>-$DEBIAN_ARCH_DIR-netinst.iso' entry in $SHA256SUMS_URL — Debian's directory layout or netinst filename convention may have changed; open the URL in a browser to see what's actually there" >&2
    exit 1
fi
echo "==> found official netinst image: $BASE_ISO_NAME"

EXPECTED_SHA256="$(awk -v name="$BASE_ISO_NAME" '$2 == name || $2 == "*"name {print $1}' "$WORK_DIR/SHA256SUMS")"
if [ -z "$EXPECTED_SHA256" ]; then
    echo "error: $BASE_ISO_NAME not found in downloaded SHA256SUMS — refusing to continue with an unverified ISO" >&2
    exit 1
fi

# --- 2.5 下載官方 ISO(有本地快取就重用,雜湊對得上才算數)------------
# 這支腳本在第一次真的拿去跑之前,大概率會被反覆執行很多次(preseed/
# late-command 邏輯只要哪裡出錯就要重跑整個建置流程再進 QEMU 測一次)
# ——netinst ISO 有幾百 MB,每次都重新下載對「反覆測試、反覆修正」這種
# 使用情境很不友善,所以在 repo 外的 $REPO_ROOT/dist/.cache/ 底下留一份
# 快取。快取是否可以重用完全看雜湊值是否還跟官方最新的 SHA256SUMS
# 一致——不是看檔名或下載時間,這樣即使 Debian 之後把同一個檔名的
# netinst ISO 換成新的內容(小版本更新常有這種情況),也不會誤用一份
# 過期的快取。
CACHE_DIR="$REPO_ROOT/dist/.cache/debian-iso"
mkdir -p "$CACHE_DIR"
CACHED_ISO="$CACHE_DIR/$BASE_ISO_NAME"
if [ -f "$CACHED_ISO" ] && [ "$(gonas_sha256sum "$CACHED_ISO" | awk '{print $1}')" = "$EXPECTED_SHA256" ]; then
    echo "==> reusing cached $CACHED_ISO (checksum matches current SHA256SUMS)"
    cp "$CACHED_ISO" "$WORK_DIR/base.iso"
else
    echo "==> downloading $BASE_ISO_URL/$BASE_ISO_NAME"
    echo "    (this requires real internet access to a Debian mirror — will fail in a network-restricted sandbox)"
    # 這支下載曾經在真實測試中失敗過(wget exit 4 = network failure,
    # 通常是幾百 MB 的大檔案傳輸中途網路斷線/逾時),但因為原本用的是
    # `wget -q`,wget 自己的錯誤訊息(DNS 失敗?連線被拒?逾時?)整個
    # 被吞掉,使用者只會在 `make` 那一層看到毫無資訊量的 `Error 4`,
    # 完全不知道該怎麼辦、也沒辦法判斷是不是同一種問題再發生一次。改成
    # 不加 `-q`(保留 `--show-progress` 顯示下載進度)讓 wget 真正的
    # 錯誤訊息印出來,並且明確檢查結束碼、給一個看得懂的提示,而不是讓
    # `set -e` 直接把腳本悶聲弄死。
    if ! wget --show-progress -O "$WORK_DIR/base.iso" "$BASE_ISO_URL/$BASE_ISO_NAME"; then
        echo "error: download of $BASE_ISO_NAME failed (see the wget error above for the real reason — DNS, connection refused, timeout, or the connection dropped mid-transfer are the common causes for a ~700MB file over an unstable network)" >&2
        echo "  nothing was cached, so simply re-running this command will retry the full download from scratch" >&2
        rm -f "$WORK_DIR/base.iso" 2>/dev/null || true
        exit 1
    fi
fi

# --- 2.6 驗證 base.iso 完整性 -------------------------------------------
# 這一步刻意不是可有可無的——這支腳本後面會把整個目錄樹解開、修改、
# 重新包裝，如果 base.iso(不管是剛下載的還是重用快取的)本身就已經
# 損毀或被竄改，後面所有步驟都是在一個不可信的基礎上動作，卻完全不會
# 有任何錯誤訊息，因為 xorriso/後續流程不會去檢查「這份 ISO 本來長
# 怎樣」。
#
# 這只驗證「完整性」（下載過程沒有被截斷/損毀），不是「真實性」（沒有
# 被中間人竄改成惡意版本）——真正的真實性驗證是下面「2.65」那一段
# 可選的 GPG 簽章驗證,預設不開啟(維持只做 checksum),設定
# GONAS_DEBIAN_KEYRING 才會真的去驗證簽章,細節見那一段的說明。
ACTUAL_SHA256="$(gonas_sha256sum "$WORK_DIR/base.iso" | awk '{print $1}')"
if [ "$EXPECTED_SHA256" != "$ACTUAL_SHA256" ]; then
    echo "error: checksum mismatch for $BASE_ISO_NAME" >&2
    echo "  expected: $EXPECTED_SHA256" >&2
    echo "  actual:   $ACTUAL_SHA256" >&2
    echo "  the download may be corrupt or the mirror may be serving something unexpected — refusing to continue" >&2
    rm -f "$CACHED_ISO" 2>/dev/null || true
    exit 1
fi
echo "==> checksum OK ($ACTUAL_SHA256)"
echo "    (this confirms the download is intact, not that it is authentic)"

# --- 2.65 (可選)GPG 簽章驗證 ------------------------------------------
# 上面的 checksum 比對只驗證「完整性」(下載過程沒有被截斷/損毀),不是
# 「真實性」(SHA256SUMS 本身沒有被中間人偽造)——真正的真實性驗證要另外
# 抓 SHA256SUMS.sign 用 gpg 驗證簽章,而這一步需要一把可信的 Debian
# 簽章金鑰。這支腳本刻意不去自動下載/匯入金鑰:金鑰應該透過一個獨立於
# 這個下載流程本身的管道取得(見 https://www.debian.org/CD/verify 官方
# 說明的建議做法),如果腳本自己去某個固定網址抓一把「聲稱是 Debian
# 官方金鑰」的檔案再拿來驗證,等於信任鏈繞了一圈又繞回同一個下載
# 管道,沒有真的增加安全性。
#
# 所以這裡改成:預設完全不做(維持原本只有 checksum 這一層,不改變
# 既有行為),使用者可以自己照官方文件匯入金鑰、匯出成一個獨立的
# keyring 檔案,再用 GONAS_DEBIAN_KEYRING 環境變數指到那個檔案路徑,
# 才會啟用這一層驗證——而且一旦啟用,驗證失敗就直接中止建置(不是
# 印個警告就算了),因為使用者主動選擇了「我要更高的信任層級」,失敗
# 卻放行會比完全不做這層檢查更糟。
#
# 準備 keyring 檔案時務必用 `gpg --export <key-id-或名字> > my.keyring`
# (不要加 `-a`/`--armor`)——這裡真的用一把測試用的 GPG 金鑰、一份
# 真的簽章走過一次完整流程才發現:如果匯出成 ASCII armor 格式
# (`gpg --export -a ... > my.keyring`,很多人的直覺會這樣做,官方
# 文件裡常見的範例也習慣加 `-a` 方便用文字編輯器看/貼上),`--keyring`
# 讀到這個檔案會直接報 `invalid packet`、`No public key`,即使金鑰內容
# 本身完全正確——`--keyring` 吃的是 binary 格式,不是 armor 格式,兩者
# 對 gpg 來說是不同的檔案格式,不是同一份資料的兩種呈現方式,不能直接
# 混用。
#
# 誠實的邊界:這一段判斷邏輯本身(gonas_verify_gpg_signature,見
# lib/verify-gpg-signature.sh)不只用假的 gpg 執行檔測過控制流程(見
# test-gpg-verify.sh),也已經在這個沙盒裡用一把真的、當場產生的測試用
# GPG 金鑰、一份真的簽章,實際跑過一次完整的「產生金鑰 → 匯出 keyring
# → 簽一份測試資料 → 驗證通過」跟「資料被竄改 → 驗證正確失敗」兩種
# 情境,確認邏輯本身是對的(過程中也是這樣抓到上面「keyring 路徑要轉
# 絕對路徑」跟「keyring 要用 binary 格式匯出」這兩個問題的)。唯一還沒
# 驗證過的,是「用 Debian 真正的官方簽章金鑰驗證一份真正的官方
# SHA256SUMS.sign」這件事本身,那需要連得上網路取得 Debian 的官方
# 金鑰/簽章檔案,這個開發沙盒完全沒辦法做,需要使用者在真正建置的時候
# 才會第一次碰到真正的 Debian 簽章資料。
GONAS_DEBIAN_KEYRING="${GONAS_DEBIAN_KEYRING:-}"
if [ -n "$GONAS_DEBIAN_KEYRING" ]; then
    if ! command -v gpg >/dev/null 2>&1; then
        echo "error: GONAS_DEBIAN_KEYRING is set but 'gpg' is not installed — install gnupg, or unset GONAS_DEBIAN_KEYRING to fall back to checksum-only verification" >&2
        exit 1
    fi
    if [ ! -f "$GONAS_DEBIAN_KEYRING" ]; then
        echo "error: GONAS_DEBIAN_KEYRING is set to '$GONAS_DEBIAN_KEYRING' but that file does not exist — see https://www.debian.org/CD/verify for how to prepare a keyring" >&2
        exit 1
    fi
    # 轉成絕對路徑再往下用——實際測試過確認 gpg 的 `--keyring` 對「相對
    # 路徑」的解讀方式跟 shell 本身不一樣:在這台機器上真的執行過
    # `gpg --no-default-keyring --keyring my.keyring --verify ...`(從
    # 一個 my.keyring 真實存在的目錄裡執行,用相對路徑),結果 gpg
    # 完全沒有讀到那個檔案,而是在自己的 homedir(`~/.gnupg/`)底下
    # 建立了一個全新的空 keybox,等於憑空冒出一份跟原本準備好的 keyring
    # 完全無關、什麼金鑰都沒有的「keyring」——上面 `[ -f ... ]` 這道
    # 檢查是用 shell 自己的相對路徑解讀方式(相對於目前工作目錄),會
    # 正確判斷檔案存在,但傳給 gpg 之後卻是另一個結果,兩邊「相對路徑」
    # 指的根本不是同一個檔案,驗證因此必然失敗,而且錯誤訊息只會說
    # 「簽章驗證失敗」,完全看不出來是路徑解讀方式不一致造成的,非常
    # 容易誤導使用者去懷疑金鑰或簽章本身有問題。轉成絕對路徑之後,
    # shell 跟 gpg 兩邊看到的都是同一個檔案,不會有這個落差。
    GONAS_DEBIAN_KEYRING="$(cd "$(dirname "$GONAS_DEBIAN_KEYRING")" && pwd)/$(basename "$GONAS_DEBIAN_KEYRING")"
    SIGN_URL="$BASE_ISO_URL/SHA256SUMS.sign"
    echo "==> GONAS_DEBIAN_KEYRING is set — fetching $SIGN_URL for GPG signature verification"
    if ! wget -q -O "$WORK_DIR/SHA256SUMS.sign" "$SIGN_URL"; then
        echo "error: could not download $SIGN_URL — GONAS_DEBIAN_KEYRING was set, so this is treated as a hard failure (unset it to fall back to checksum-only verification)" >&2
        exit 1
    fi
    GPG_LOG="$WORK_DIR/gpg-verify.log"
    if gonas_verify_gpg_signature "$WORK_DIR/SHA256SUMS.sign" "$WORK_DIR/SHA256SUMS" "$GONAS_DEBIAN_KEYRING" "$GPG_LOG"; then
        echo "==> GPG signature OK — SHA256SUMS is authentically signed by a key in $GONAS_DEBIAN_KEYRING"
    else
        echo "error: GPG signature verification failed — refusing to continue with an ISO whose SHA256SUMS could not be authenticated. Full gpg output:" >&2
        sed 's/^/  /' "$GPG_LOG" >&2
        exit 1
    fi
else
    echo "    (for full authenticity, separately verify SHA256SUMS.sign with gpg against Debian's"
    echo "    signing key, see https://www.debian.org/CD/verify — set GONAS_DEBIAN_KEYRING to a"
    echo "    local keyring file path to have this script do that check automatically)"
fi

# 通過驗證才寫進快取(或更新快取)——避免一份沒通過驗證的檔案被誤存
# 起來,下次又被當成「快取命中」重用。
if [ ! -f "$CACHED_ISO" ] || [ "$(gonas_sha256sum "$CACHED_ISO" 2>/dev/null | awk '{print $1}')" != "$ACTUAL_SHA256" ]; then
    cp "$WORK_DIR/base.iso" "$CACHED_ISO"
fi

# --- 2.7 清掉同架構、不同 Debian 版本號的舊快取 -----------------------
# 只在確認新版本快取成功寫入之後才做,清的對象限定在「同一個架構、
# 檔名不是目前這個 $BASE_ISO_NAME」的檔案——第十八輪覆閱把 BASE_ISO_NAME
# 改成直接從 SHA256SUMS 動態抓真正的檔名之後,這一段反而變得更常會
# 真的用到:Debian 每次發布新的 point release(例如 12.11.0 ->
# 12.12.0,或直接換到下一個穩定版代號),`current/` 底下的檔名都會
# 跟著換,舊版本號的那份快取永遠不會再被用到(BASE_ISO_NAME 已經變了)
# ,但也永遠不會自動消失,一直佔用磁碟空間卻沒有任何提示。故意不去動
# 「其他架構」的快取檔案(例如建 amd64 的時候不會去動 arm64 的快取)
# ——避免使用者分開兩次 `make iso-amd64`/`make iso-arm64` 時,這一步
# 反而互相刪掉對方仍然有效的快取,那樣就違背了當初做這個快取機制的
# 本意。
for old_cached in "$CACHE_DIR"/debian-*-"$DEBIAN_ARCH_DIR"-netinst.iso; do
    [ -e "$old_cached" ] || continue
    [ "$(basename "$old_cached")" = "$BASE_ISO_NAME" ] && continue
    echo "==> removing stale cached ISO for a different Debian release ($DEBIAN_ARCH_DIR): $old_cached"
    rm -f "$old_cached"
done

# --- 3. 解開原始 ISO --------------------------------------------------
EXTRACT_DIR="$WORK_DIR/iso"
mkdir -p "$EXTRACT_DIR"
echo "==> extracting base ISO"
xorriso -osirrox on -indev "$WORK_DIR/base.iso" -extract / "$EXTRACT_DIR" >/dev/null

chmod -R u+w "$EXTRACT_DIR"

# --- 4. 塞進 GoNAS 專屬的檔案 -----------------------------------------
GONAS_ON_ISO="$EXTRACT_DIR/gonas"
mkdir -p "$GONAS_ON_ISO/release-$ARCH" "$GONAS_ON_ISO/overlay"

echo "==> embedding gonasd release tarball and appliance overlay"
tar -xzf "$RELEASE_TARBALL" -C "$GONAS_ON_ISO/release-$ARCH" --strip-components=1
cp "$SCRIPT_DIR/late-command.sh" "$GONAS_ON_ISO/late-command.sh"
# 這裡設執行位元純粹是「如果 xorriso/Rock Ridge 真的保留得住,那就順便
# 帶著」的防禦性做法——實際的安裝路徑完全不依賴它(late-command.sh 用
# `sh` 呼叫、install.sh 用 `-f` 找 gonasd,見各自檔案的說明),第十九輪
# 覆閱把 gonasd 也一起加進來,理由同上:多帶一層保險,少一個「萬一哪天
# 又改回依賴執行位元」的隱患。
chmod +x "$GONAS_ON_ISO/late-command.sh" "$GONAS_ON_ISO/release-$ARCH/install.sh" "$GONAS_ON_ISO/release-$ARCH/gonasd" 2>/dev/null || true
cp -a "$SCRIPT_DIR/overlay/." "$GONAS_ON_ISO/overlay/"
cp "$SCRIPT_DIR/preseed.cfg" "$GONAS_ON_ISO/preseed.cfg"
# late-command.sh 第十三輪覆閱之後會 `. `一份 lib/detect-arch.sh 來源
# 檔案(理由見 late-command.sh 開頭的說明)——這裡務必把整個
# build/appliance/lib/ 目錄也一起塞進 ISO,放在跟 late-command.sh
# 同一層(late-command.sh 用 `dirname "$0"` 找這個目錄,兩者的相對
# 位置要對得起來)。這是這一輪新增的一步:如果漏掉這一步,
# late-command.sh 在真正的 in-target chroot 裡執行時,`.` 一個不存在
# 的檔案會直接觸發 `set -e` 中止,整個 GoNAS 的安裝/品牌化流程完全
# 不會執行——這種失敗只有真的建置一次 ISO、實際跑一次安裝流程才會
# 現形,靜態看 late-command.sh 本身完全看不出少了這一步,務必特別
# 小心維護 late-command.sh 跟這裡「塞了什麼檔案進 ISO」兩者的對應
# 關係一致。
mkdir -p "$GONAS_ON_ISO/lib"
cp -a "$SCRIPT_DIR/lib/." "$GONAS_ON_ISO/lib/"

# --- 4.5 (模式一:離線 SSH)把 openssh-server 及其相依 .deb 打包進 ISO ---
# netinst 光碟官方定義就只含「裝 base 系統的最小套件」,openssh-server
# 這種東西不在裡面,正常安裝流程要連網去鏡像站抓——但這個 appliance
# 的設計是「安裝過程完全離線」(preseed.cfg 的 apt-setup/use_mirror
# false)。矛盾的解法(使用者選的「模式一」):在「建置 ISO 的這台機器
# 上(本來就需要網路去抓 netinst ISO)」順便把 openssh-server 以及它
# 需要的所有相依套件的 .deb 抓下來、放進 ISO 的 gonas/debs/,之後
# late-command.sh 在目標系統裡直接 `dpkg -i` 這些本地檔案,安裝當下
# 完全不需要網路。
#
# 整段是 best-effort:抓不到套件索引、或某個 .deb 下載失敗,都只印警告
# 繼續,不讓整個 ISO 建置失敗——SSH 是選用便利功能,不是 appliance
# 的核心(核心是 gonasd 本體 + Web 介面 + tty 主控台,那些完全不依賴
# 這一步)。設定 GONAS_SKIP_OFFLINE_PACKAGES=1 可以整段跳過。
if [ -n "${GONAS_SKIP_OFFLINE_PACKAGES:-}" ]; then
    echo "==> GONAS_SKIP_OFFLINE_PACKAGES set — skipping offline package bundling (SSH will NOT be preinstalled)"
else
    # 套件鏡像跟前面抓 netinst ISO 的 cdimage.debian.org 是兩個不同的
    # 東西:cdimage 放的是「光碟映像」,套件本身在一般的 apt 鏡像
    # (deb.debian.org/debian)。suite 用 `stable`——deb.debian.org 上
    # `dists/stable` 永遠指向目前的穩定版,跟前面 `debian-cd/current/`
    # 抓到的 netinst 是同一個穩定版,兩者版本一致(穩定版內只有 ABI
    # 相容的安全性更新,不會動到 openssh-server 相依的 base 函式庫的
    # 主版本,所以就算鏡像上的 openssh-server 比 ISO 的 base 稍新也裝
    # 得起來)。都可以用環境變數覆寫。
    DEB_MIRROR="${GONAS_DEBIAN_PKG_MIRROR:-https://deb.debian.org/debian}"
    DEB_SUITE="${GONAS_DEBIAN_SUITE:-stable}"
    # 預設打包 openssh-server(遠端管理)跟 sudo(gonas 帳號被加進
    # sudo 群組,但 sudo 這個指令本身也不在 netinst 光碟裡,一樣要
    # 離線打包才能用)。可用環境變數覆寫成別的清單。
    SEED_PACKAGES="${GONAS_APPLIANCE_SEED_PACKAGES:-openssh-server sudo}"
    DEBS_DIR="$GONAS_ON_ISO/debs"
    PKG_INDEX_URL="$DEB_MIRROR/dists/$DEB_SUITE/main/binary-$DEBIAN_ARCH_DIR/Packages.gz"

    echo "==> (offline SSH) fetching package index $PKG_INDEX_URL"
    if wget -q -O "$WORK_DIR/Packages.gz" "$PKG_INDEX_URL" 2>/dev/null && \
       gzip -dc "$WORK_DIR/Packages.gz" > "$WORK_DIR/Packages" 2>/dev/null; then
        # gzip -dc 在 GNU 跟 macOS(BSD)底下都存在、行為一致,不需要
        # 額外的相容性包裝(不像 xz 在 stock macOS 上沒有)。
        mkdir -p "$DEBS_DIR"
        CLOSURE_LIST="$WORK_DIR/deb-closure.list"
        echo "==> (offline SSH) computing dependency closure for: $SEED_PACKAGES"
        # 排除 required/important——debootstrap 建的 base 一定已經有這兩個
        # 優先級的所有套件,不需要我們再打包一份,見 lib/deb-closure.sh。
        # shellcheck disable=SC2086
        gonas_deb_closure "$WORK_DIR/Packages" "required,important" $SEED_PACKAGES > "$CLOSURE_LIST" 2>"$WORK_DIR/deb-closure.err" || true
        if [ -s "$WORK_DIR/deb-closure.err" ]; then
            sed 's/^/    /' "$WORK_DIR/deb-closure.err" >&2
        fi
        _deb_ok=0
        _deb_fail=0
        while IFS= read -r _relpath; do
            [ -n "$_relpath" ] || continue
            _out="$DEBS_DIR/$(basename "$_relpath")"
            if wget -q -O "$_out" "$DEB_MIRROR/$_relpath" 2>/dev/null; then
                _deb_ok=$((_deb_ok + 1))
            else
                echo "    WARNING: failed to download $DEB_MIRROR/$_relpath — SSH may be incomplete" >&2
                rm -f "$_out"
                _deb_fail=$((_deb_fail + 1))
            fi
        done < "$CLOSURE_LIST"
        echo "==> (offline SSH) bundled $_deb_ok package(s) into $DEBS_DIR ($_deb_fail failed)"
        if [ "$_deb_ok" = "0" ]; then
            echo "    WARNING: no packages were bundled — the installed system will NOT have a preinstalled SSH server" >&2
            rmdir "$DEBS_DIR" 2>/dev/null || true
        fi
    else
        echo "    WARNING: could not fetch/decompress the package index from $PKG_INDEX_URL — skipping offline package bundling; SSH will NOT be preinstalled. (set GONAS_DEBIAN_PKG_MIRROR to a reachable mirror, or GONAS_SKIP_OFFLINE_PACKAGES=1 to silence this)" >&2
    fi
fi

# --- 5. 修改開機選單:自動套用 preseed、品牌化標題 ---------------------
# 不同 Debian 版本的 isolinux/grub 選單檔案結構偶爾會變(例如選單項目
# 是直接寫在 isolinux.cfg 還是被 include 進 txt.cfg/gtk.cfg)，這裡用
# 比較保守的做法:對所有看起來像「開機核心參數列」的 append/linux 行
# 統一加上 preseed 相關參數，而不是假設某個固定檔名——修改後務必用
# QEMU 開機檢查選單，見 README.md。
echo "==> patching boot menu configs to auto-load the GoNAS preseed"
# priority=high(不是 critical)是刻意的選擇:preseed.cfg 裡凡是已經
# 明確給答案的問題,不管 priority 設多少都不會再問一次(debconf 的
# 優先權只影響「還沒有答案的問題要不要跳出來問」)——用 high 而不是
# critical,能大幅提高「preseed.cfg 沒有涵蓋到、或刻意留白的高優先權
# 問題(尤其是 partman/confirm 這類寫入磁碟前的確認)」仍然會真的
# 顯示出來、而不是被 debconf 用預設值悄悄帶過的機率,對這份 preseed
# 「寧可多停下來問一次,也不要猜錯」的設計原則而言，high 比 critical
# 安全。代價是如果真的遇到某個沒被涵蓋到的 medium/low 優先權問題,
# 会跳出來要人工回答,不算「完全零互動」,但對一份還沒有實機驗證過
# 的 preseed 來說，這是刻意要接受的取捨。
APPEND_EXTRA="auto=true priority=high preseed/file=/cdrom/gonas/preseed.cfg hostname=gonas domain="
# 拿來事後驗證「真的注入成功了嗎」的一小段獨特字串——不會跟 ISO 裡
# 其他既有內容重複，之後可以直接 grep 這個字串確認注入是否生效。
APPEND_MARKER="gonas/preseed.cfg"

# 原本這裡是 `find ... | while read ...; do ... done`——這是一個真的
# 會咬人的 POSIX shell 陷阱:管線右邊的指令(這裡是 while 迴圈)在
# dash/大多數 /bin/sh 底下是跑在一個子行程裡，迴圈裡面設定的變數
# (例如底下要用來計數的 PATCHED_COUNT)離開迴圈之後就消失了，父行程
# 完全看不到。改用 `find ... > 檔案` 再 `while read ... < 檔案` 的寫法
# ——這個版本的 while 迴圈是在目前的 shell 裡執行、不是子行程，裡面
# 設的變數在迴圈結束後還讀得到。
CFG_LIST="$WORK_DIR/boot-menu-cfgs.list"
# 第十八輪覆閱(使用者實測 arm64 建置)才抓到的問題:`find` 同時給兩個
# 起始路徑,如果其中一個根本不存在(arm64 的官方 ISO 是 EFI-only,
# 本來就沒有 isolinux/ 目錄,這是上面註解早就講過的正常情況),`find`
# 本身仍然會正確找到另一個路徑底下的檔案,但 exit code 因為那個
# 「路徑不存在」的錯誤還是非 0——在這支腳本一開頭就設定的 `set -eu`
# 底下,這會讓整支腳本立刻中止,而且是在下面「找到幾個設定檔」那行
# echo 都還沒印出來之前就死掉,螢幕上不會有任何一行看得懂的錯誤訊息,
# 只會看到 `make: *** [iso-arm64] Error 1`。已抽成
# lib/find-boot-menu-cfgs.sh 的 gonas_find_boot_menu_cfgs(),裡面
# 用 `|| true` 吃掉這個 exit code——真正「有沒有找到任何設定檔」的
# 判斷,交給下面這個既有、訊息更清楚的 `[ "$CFG_COUNT" = "0" ]` 檢查。
gonas_find_boot_menu_cfgs "$EXTRACT_DIR/isolinux" "$EXTRACT_DIR/boot/grub" "$CFG_LIST"
CFG_COUNT="$(wc -l < "$CFG_LIST" | tr -d ' ')"
echo "==> found $CFG_COUNT boot menu config file(s) to patch"
if [ "$CFG_COUNT" = "0" ]; then
    # 不管是 amd64 還是 arm64(EFI-only,理論上沒有 isolinux 目錄，但
    # 應該還是有 boot/grub 底下的 .cfg),一份正常的 netinst ISO
    # 不可能完全沒有任何 boot menu 設定檔——找不到任何一個，代表這個
    # 版本的官方 ISO 目錄結構跟這支腳本原本假設的不一樣(例如 Debian
    # 之後改版換了路徑),而不是「這個架構本來就沒有」。與其在這裡
    # 沉默地跳過、產出一份「preseed 沒有真的被注入、開機後會整個掉回
    # 手動安裝流程」卻毫無錯誤訊息的 ISO,不如直接中止,逼人回頭確認
    # 這個架構實際的 ISO 目錄結構。
    echo "error: no boot menu config files found under isolinux/ or boot/grub/ — the ISO's directory layout may not match what this script expects for $ARCH; refusing to produce a silently-broken (non-autoinstalling) ISO" >&2
    exit 1
fi

while read -r cfgfile; do
    # 實際的比對/插入邏輯在 lib/patch-boot-menu.sh 的
    # gonas_patch_boot_menu_file() 裡——這裡不再重複貼一份。那裡的
    # 註解記錄了為什麼 grub 的 "---" 不能假設是行尾(真的拿仿真 Debian
    # grub.cfg 格式的測試資料跑過,行尾假設會漏掉 `--- quiet` 這種常見
    # 格式,已修正),以及為什麼要獨立成函式庫檔案(讓
    # test-boot-menu-patch.sh 能跟這支正式建置腳本共用同一份邏輯,不會
    # 測試/正式兩份程式碼慢慢跑掉)。
    gonas_patch_boot_menu_file "$cfgfile" "$APPEND_EXTRA"
done < "$CFG_LIST"

# 驗證注入真的生效了——上面兩條 sed 規則各自都可能因為某個 Debian
# 版本的檔案格式跟預期不一樣而完全沒命中(例如 grub.cfg 的 "---" 前後
# 格式又變了),沒命中的話 sed 不會報任何錯，就會產出一份「看起來
# 建置成功、實際上開機後不會自動套用 preseed」的 ISO。與其只靠人在
# QEMU 裡開機才發現，這裡直接在建置階段就檢查:找到的設定檔裡，
# 至少要有一個真的包含剛剛注入的字串，不然就中止,不繼續往下包裝。
# 逐行用 while 讀檔案清單(不是把 `$(cat ...)` 直接展開成命令列參數)
# ,避免萬一檔名裡有空白/萬用字元被 shell 誤解析。
INJECTED=0
while read -r cfgfile; do
    if grep -q "$APPEND_MARKER" "$cfgfile" 2>/dev/null; then
        INJECTED=1
        break
    fi
done < "$CFG_LIST"
if [ "$INJECTED" != "1" ]; then
    echo "error: failed to inject the GoNAS preseed boot parameter into any boot menu config file — the sed patterns in this script no longer match this Debian release's isolinux/grub.cfg format. The resulting ISO would silently fall back to a fully manual install with no error at boot time. Refusing to continue; inspect the files listed in $CFG_LIST by hand and update the sed patterns above." >&2
    exit 1
fi
echo "==> confirmed the GoNAS preseed boot parameter was injected successfully"

# 縮短選單等待時間——這是「安裝媒體」的開機選單(裝完系統之後的
# GRUB 選單品牌化/等待時間是 late-command.sh 在目標系統裡處理的，
# 是兩個不同的東西)。
if [ -f "$EXTRACT_DIR/isolinux/isolinux.cfg" ]; then
    # 用 gonas_sed_inplace(見上面的說明跟 lib/portable-sed.sh)而不是
    # 直接 `sed -i 'script' file`——原本這裡就是那個 macOS/BSD sed
    # 不相容問題的其中一個現場。
    gonas_sed_inplace 's/^timeout .*/timeout 50/' "$EXTRACT_DIR/isolinux/isolinux.cfg" || true
fi
# 第十八輪覆閱(使用者實測用 Parallels 在 arm64 上開機)才抓到的問題:
# 上面這段從第七輪覆閱寫下來到現在,一直只處理 `isolinux.cfg`——這是
# amd64 專屬的檔案,arm64 官方 ISO 是 EFI-only,根本沒有這個檔案(跟
# 這一輪前面「找開機選單設定檔」那個 bug 是同一個原因)。使用者實際
# 用 Parallels 開機、拍照回報看到的畫面,是標準的 GRUB 選單卡在那邊
# 等按鍵,不會自動開始安裝——「開機不用人工介入就自動走完全自動安裝」
# 這個目標,從第七輪修完 isolinux 那一刻起,就從來沒有真的在 arm64 上
# 生效過,因為 grub.cfg 根本沒被同一段邏輯處理到。grub.cfg 用的是
# GRUB2 的 `set timeout=N`(單位:秒)語法,跟 isolinux.cfg 的
# `timeout N`(單位:1/10 秒)是兩種完全不同的格式,不能套用同一條
# sed 規則,需要另外處理。逐一套用在 $CFG_LIST 裡列出的每個檔案上,
# 不是只挑 `boot/grub/grub.cfg` 這一個固定路徑——理由跟上面找設定檔
# 那段一樣:不同版本的目錄結構可能不同,對著整份清單逐一嘗試,沒有
# `set timeout=` 這一行的檔案,這條 sed 規則本來就不會有任何動作
# (no-op),不會誤傷到其他內容。
# 第十九輪覆閱(全面排查「除了選硬碟其餘全自動」的原始目標)強化:
# 原本這條 sed 是 `s/^set timeout=.*/.../`,只比對「行首、沒有縮排」的
# `set timeout=`——但不同 Debian 版本的 grub.cfg,這一行可能有縮排、
# 也可能根本沒有 `set timeout=` 這一行(改用 theme 或預設值等待)。
# 只要沒命中,安裝媒體就會停在 GRUB 選單畫面等使用者按 Enter,直接
# 違背「開機不用人工介入就自動開始安裝」的目標(使用者實測就是卡在
# 這裡)。改成:(1) 比對時容許行首空白;(2) 如果一個看起來是 grub
# 設定檔(含 menuentry)的檔案完全沒有 `set timeout=` 行,就主動補一行
# ——確保不管哪種格式,安裝媒體最多等 5 秒就自動開始。isolinux 的
# txt.cfg/gtk.cfg 用的是 `label`/`menu` 語法、不含 `menuentry`,不會被
# 這個 append 分支誤傷。
while read -r cfgfile; do
    if grep -Eq '^[[:space:]]*set[[:space:]]+timeout=' "$cfgfile" 2>/dev/null; then
        gonas_sed_inplace 's/^[[:space:]]*set[[:space:]]*timeout=.*/set timeout=5/' "$cfgfile" || true
    elif grep -q 'menuentry' "$cfgfile" 2>/dev/null; then
        printf '\nset timeout=5\n' >> "$cfgfile"
    fi
done < "$CFG_LIST"

# --- 6. 重新計算 checksum 清單、重新包裝 -------------------------------
echo "==> recomputing md5sum.txt"
# 這裡原本是 `find ... -exec md5sum {} \;`——`-exec` 直接呼叫外部指令
# `md5sum`,沒辦法像其他地方一樣改成呼叫 shell 函式
# (`gonas_md5sum`,見上面 lib/portable-checksum.sh 的說明,原因同樣是
# macOS 內建 BSD 使用者空間沒有 GNU 的 md5sum)。改成 `find | while read`
# 逐一呼叫 gonas_md5sum,把結果導向同一份 md5sum.txt——這裡不需要迴圈
# 內部設定的變數在迴圈結束後還讀得到(不是 build-iso.sh 前面
# 「開機選單設定檔清單」那種情境),單純把每一行的輸出接力寫進檔案,
# 用管線(而不是先寫檔案再讀)完全沒問題。
( cd "$EXTRACT_DIR" && find . -type f ! -name 'md5sum.txt' ! -path './isolinux/*' | while read -r f; do gonas_md5sum "$f"; done > md5sum.txt )

OUT_DIR="$REPO_ROOT/dist/release"
mkdir -p "$OUT_DIR"
OUT_ISO="$OUT_DIR/gonas-$VERSION-$ARCH.iso"

# 重跑這支腳本(同一個 VERSION、同一個 ARCH)是很常見的情況——先清掉
# 舊的輸出檔案再交給 xorriso,不依賴 xorriso 自己對「輸出路徑已經有
# 一個檔案」這種情況的處理方式(-outdev 指向一個既有檔案時，某些
# xorriso 版本/選項組合下可能會嘗試把它當成既有的多重 session ISO
# 處理，而不是單純覆蓋掉重寫，具體行為沒有在這裡實際測試驗證過)，
# 確保每次都是從一份全新、乾淨的檔案開始寫。
rm -f "$OUT_ISO" "$OUT_ISO.sha256"

echo "==> repacking as $OUT_ISO"
# `-boot_image any replay` 沿用原始 ISO 的開機目錄結構(El Torito/
# isohybrid 這些底層細節)，只是把整個目錄樹換成我們修改過的版本——
# 這是 Debian wiki「RepackBootableISO」文件建議的標準做法，比手動
# 重新計算 isohybrid MBR 偏移量可靠很多。
xorriso -indev "$WORK_DIR/base.iso" \
        -outdev "$OUT_ISO" \
        -map "$EXTRACT_DIR" / \
        -boot_image any replay \
        -changes_pending yes \
        -end

gonas_sha256sum "$OUT_ISO" > "$OUT_ISO.sha256"
echo "==> done: $OUT_ISO"
echo "==> checksum: $(cat "$OUT_ISO.sha256")"
echo
echo "下一步:務必先用 QEMU/VirtualBox 開機測試整個安裝流程，見"
echo "build/appliance/README.md「如何驗證」一節，確認過再燒錄到真實硬體。"
