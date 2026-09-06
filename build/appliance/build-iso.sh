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
# 版本/路徑基於 Debian 目前的目錄慣例，Debian 發新的穩定版之後路徑
# 可能改變（例如 bookworm -> trixie），請對照
# https://www.debian.org/CD/netinst/ 確認目前的實際路徑再執行。
DEBIAN_RELEASE="${GONAS_DEBIAN_RELEASE:-bookworm}"
case "$ARCH" in
    amd64) DEBIAN_ARCH_DIR="amd64" ;;
    arm64) DEBIAN_ARCH_DIR="arm64" ;;
esac
BASE_ISO_URL="${GONAS_DEBIAN_ISO_URL:-https://cdimage.debian.org/debian-cd/current/$DEBIAN_ARCH_DIR/iso-cd}"
BASE_ISO_NAME="debian-$DEBIAN_RELEASE-$DEBIAN_ARCH_DIR-netinst.iso"

# 先抓 SHA256SUMS(每次都重新抓,不快取——這份清單很小,而且要用它來
# 判斷「快取的 base.iso 還算不算數」,快取 SHA256SUMS 本身會讓這個判斷
# 失去意義)。
SHA256SUMS_URL="$BASE_ISO_URL/SHA256SUMS"
echo "==> fetching $SHA256SUMS_URL"
if ! wget -q -O "$WORK_DIR/SHA256SUMS" "$SHA256SUMS_URL"; then
    echo "error: could not download $SHA256SUMS_URL to verify the base ISO's checksum — refusing to continue with an unverified ISO" >&2
    exit 1
fi
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
    wget -q --show-progress -O "$WORK_DIR/base.iso" "$BASE_ISO_URL/$BASE_ISO_NAME"
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

# --- 2.7 清掉同架構、不同 Debian 版本代號的舊快取 -----------------------
# 只在確認新版本快取成功寫入之後才做,清的對象限定在「同一個架構、
# 檔名裡代號不是目前 $BASE_ISO_NAME」的檔案——例如把
# GONAS_DEBIAN_RELEASE 從 bookworm 換成 trixie 之後,原本 bookworm 那份
# amd64 快取永遠不會再被用到(BASE_ISO_NAME 已經換了),但也永遠不會
# 自動消失,一直佔用磁碟空間卻沒有任何提示。故意不去動「其他架構」的
# 快取檔案(例如建 amd64 的時候不會去動 arm64 的快取)——避免使用者
# 分開兩次 `make iso-amd64`/`make iso-arm64` 時,這一步反而互相刪掉
# 對方仍然有效的快取,那樣就違背了當初做這個快取機制的本意。
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
chmod +x "$GONAS_ON_ISO/late-command.sh" "$GONAS_ON_ISO/release-$ARCH/install.sh"
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
find "$EXTRACT_DIR/isolinux" "$EXTRACT_DIR/boot/grub" -type f \( -name '*.cfg' -o -name 'txt.cfg' \) 2>/dev/null > "$CFG_LIST"
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
