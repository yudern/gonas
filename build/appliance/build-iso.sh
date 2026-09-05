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
# 需要的工具（Debian/Ubuntu 上都是 `apt install xorriso wget`）：
#   xorriso, wget（或 curl）, sha256sum
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
if [ -f "$CACHED_ISO" ] && [ "$(sha256sum "$CACHED_ISO" | awk '{print $1}')" = "$EXPECTED_SHA256" ]; then
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
# 被中間人竄改成惡意版本）——真正的真實性驗證需要另外抓
# SHA256SUMS.sign 用 gpg 驗證簽章，這一步需要事先匯入 Debian 的
# 簽章金鑰才能做，這支腳本不假設執行環境已經有這把金鑰,所以只做到
# checksum 比對這一層,並在下面印出訊息提醒使用者如果要更高的信任
# 層級,可以自行另外做 GPG 簽章驗證(見
# https://www.debian.org/CD/verify 的官方說明)。
ACTUAL_SHA256="$(sha256sum "$WORK_DIR/base.iso" | awk '{print $1}')"
if [ "$EXPECTED_SHA256" != "$ACTUAL_SHA256" ]; then
    echo "error: checksum mismatch for $BASE_ISO_NAME" >&2
    echo "  expected: $EXPECTED_SHA256" >&2
    echo "  actual:   $ACTUAL_SHA256" >&2
    echo "  the download may be corrupt or the mirror may be serving something unexpected — refusing to continue" >&2
    rm -f "$CACHED_ISO" 2>/dev/null || true
    exit 1
fi
echo "==> checksum OK ($ACTUAL_SHA256)"
echo "    (this confirms the download is intact, not that it is authentic — for full authenticity,"
echo "    separately verify SHA256SUMS.sign with gpg against Debian's signing key, see"
echo "    https://www.debian.org/CD/verify)"
# 通過驗證才寫進快取(或更新快取)——避免一份沒通過驗證的檔案被誤存
# 起來,下次又被當成「快取命中」重用。
if [ ! -f "$CACHED_ISO" ] || [ "$(sha256sum "$CACHED_ISO" 2>/dev/null | awk '{print $1}')" != "$ACTUAL_SHA256" ]; then
    cp "$WORK_DIR/base.iso" "$CACHED_ISO"
fi

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

find "$EXTRACT_DIR/isolinux" "$EXTRACT_DIR/boot/grub" -type f \( -name '*.cfg' -o -name 'txt.cfg' \) 2>/dev/null | while read -r cfgfile; do
    # isolinux 語法用 "append ..." 這一行帶核心參數；grub.cfg 用
    # "linux ... ---" 這種格式，"---" 之後才是要交給核心的額外參數。
    if grep -q '^[[:space:]]*append ' "$cfgfile" 2>/dev/null; then
        sed -i "s#^\([[:space:]]*append .*\)\$#\\1 $APPEND_EXTRA#" "$cfgfile"
    fi
    # 容忍 "---" 後面可能還有空白字元(不同 grub.cfg 產生器有時候會留下
    # 尾端空白)——原本只用 \$ 錨定行尾,遇到這種情況會整條規則不命中、
    # 靜默地什麼都不做(sed 對沒匹配到的規則不會報錯),等於 preseed
    # 根本沒被套用卻毫無錯誤訊息，所以這裡改成容許尾端空白的版本。
    if grep -q '	linux ' "$cfgfile" 2>/dev/null || grep -q '^[[:space:]]*linux ' "$cfgfile" 2>/dev/null; then
        sed -i "s#\(---\)[[:space:]]*\$#$APPEND_EXTRA \\1#" "$cfgfile"
    fi
    # 選單標題品牌化——把看得到的 "Debian GNU/Linux installer" 字樣換成
    # "GoNAS Installer"，純粹是顯示文字，不影響實際安裝行為。
    sed -i 's/Debian GNU\/Linux installer/GoNAS Installer/g; s/Install Debian/Install GoNAS/g' "$cfgfile" || true
done

# 縮短選單等待時間——這是「安裝媒體」的開機選單(裝完系統之後的
# GRUB 選單品牌化/等待時間是 late-command.sh 在目標系統裡處理的，
# 是兩個不同的東西)。
if [ -f "$EXTRACT_DIR/isolinux/isolinux.cfg" ]; then
    sed -i 's/^timeout .*/timeout 50/' "$EXTRACT_DIR/isolinux/isolinux.cfg" || true
fi

# --- 6. 重新計算 checksum 清單、重新包裝 -------------------------------
echo "==> recomputing md5sum.txt"
( cd "$EXTRACT_DIR" && find . -type f ! -name 'md5sum.txt' ! -path './isolinux/*' -exec md5sum {} \; > md5sum.txt )

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

sha256sum "$OUT_ISO" > "$OUT_ISO.sha256"
echo "==> done: $OUT_ISO"
echo "==> checksum: $(cat "$OUT_ISO.sha256")"
echo
echo "下一步:務必先用 QEMU/VirtualBox 開機測試整個安裝流程，見"
echo "build/appliance/README.md「如何驗證」一節，確認過再燒錄到真實硬體。"
