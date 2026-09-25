#!/bin/sh
# build-iso.sh 把官方 Debian DVD-1 完整版 ISO 改造成一份「插上就能自動
# 裝好 GoNAS」的映像檔:下載官方 ISO -> 解開 -> 塞進 preseed.cfg / 這個
# 目錄裡的其他檔案 / 已經編譯好的 gonasd release tarball -> 修改開機選單
# 的標題跟預設開機參數(自動套用 preseed，不需要在安裝畫面手動選)
# -> 用 xorriso 重新包裝成一份新的、一樣可開機的 ISO。
#
# 第二十八輪(使用者決定):底層 ISO 從 netinst(~700MB,只含 base
# 系統的最小套件,裝任何額外套件都要連網)換成 DVD-1 完整版(~3.7GB,
# 本身就是一個很大的離線套件庫)。這樣做的直接好處:openssh-server、
# sudo 這些「netinst 光碟上沒有、非得連網或自己打包 .deb 才裝得到」的
# 套件,DVD-1 上本來就有,安裝程式用官方的 tasksel/pkgsel 機制就能在
# 完全離線的情況下裝好——因此把前面幾輪為了在 netinst 上硬做「離線
# SSH」而寫的那一整段脆弱的自訂 .deb 相依封閉集計算 + 逐一下載打包
# (原本的 4.5 節 + late-command.sh 的 1.7 節 + lib/deb-closure.sh)
# 整個刪掉,改回用 Debian 官方支援的 `pkgsel/include`。誠實邊界:DVD-1
# 只含「最熱門的一部分套件」,GoNAS 的冷門相依(mergerfs/snapraid/
# docker.io 等)不保證在 DVD-1 上;而且安裝媒體裝完會退出,所以「開機
# 後才裝的東西」一律還是走網路(late-command.sh 3.5 節會把 apt 來源
# 改指向網路鏡像)——DVD-1 的好處集中在「安裝當下」把 SSH/sudo 這類
# 一定會用到的套件可靠地離線裝好,不是讓日後所有 apt install 都免網路。
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
# 需要能連上網路下載官方 DVD-1 ISO 跟(可選)驗證 GPG 簽章。DVD-1 有
# ~3.7GB,下載、解開、重新包裝都比 netinst 久、也更吃磁碟空間(WORK_DIR
# 在 /tmp 底下,解開一份 + 重新包裝一份,建議至少留 ~12GB 可用空間)。

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
# WORK_DIR 預設放 /tmp,但可用 GONAS_ISO_WORK_ROOT 覆蓋。理由:DVD-1 建置
# 需要十幾 GB 暫存空間(下載一份 + 解開一份 + 重新包裝一份),而某些
# CI runner(例如 GitHub Actions)的系統碟很小,/tmp 會在建置中途被塞爆、
# 且常常是「毫無錯誤訊息就被砍斷」——這種環境要把工作目錄指到一顆比較大的
# 暫存碟(例如 GitHub runner 的 /mnt)。設定 GONAS_ISO_WORK_ROOT 即可。
WORK_ROOT="${GONAS_ISO_WORK_ROOT:-/tmp}"
mkdir -p "$WORK_ROOT" 2>/dev/null || true
WORK_DIR="$(mktemp -d "$WORK_ROOT/gonas-iso-build.XXXXXX")"
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
. "$SCRIPT_DIR/lib/scan-pool-packages.sh"
# 第二十八輪:換成 DVD-1 之後,原本第十九輪為了在 netinst 上硬做離線
# SSH 而寫的 lib/deb-closure.sh(算 openssh-server 相依封閉集、決定要
# 打包哪些 .deb)已經整個用不到了——openssh-server/sudo 直接由
# preseed.cfg 的 pkgsel/include 從 DVD-1 離線裝好。該檔案跟它的離線
# 回歸測試 test-deb-closure.sh 已一併刪除,這裡不再 source。

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

# --- 2. 下載官方 Debian DVD-1 完整版 ISO ------------------------------
# 第二十八輪:從 netinst 換成 DVD-1。兩者在 cdimage.debian.org 上是
# 不同的目錄跟檔名:
#   - netinst:`debian-cd/current/<arch>/iso-cd/debian-<版本>-<arch>-netinst.iso`
#   - DVD-1:  `debian-cd/current/<arch>/iso-dvd/debian-<版本>-<arch>-DVD-1.iso`
# 沿用第十八輪的做法(那一輪使用者實測才抓到「檔名是完整版本號、不是
# 代號」這個真相):不自己組出一個猜測的檔名,而是先抓該目錄的
# SHA256SUMS,再從裡面實際找出符合 `debian-<版本號>-<arch>-DVD-1.iso`
# 格式的那一行,檔名跟版本號都直接來自 Debian 官方當下真正發布的內容,
# 不管之後換代號或出新的 point release 都不需要回來改這支腳本。
# 可用 GONAS_DEBIAN_ISO_URL 覆寫整個目錄網址(例如指到本地鏡像)。
case "$ARCH" in
    amd64) DEBIAN_ARCH_DIR="amd64" ;;
    arm64) DEBIAN_ARCH_DIR="arm64" ;;
esac
BASE_ISO_URL="${GONAS_DEBIAN_ISO_URL:-https://cdimage.debian.org/debian-cd/current/$DEBIAN_ARCH_DIR/iso-dvd}"

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
# debian-<版本號,例如 13.6.0>-<arch>-DVD-1.iso 格式的那一行——不是
# 事先假設好一個檔名再回頭比對。`grep -o` 在 GNU 跟 BSD(macOS 內建)
# grep 底下都支援,不需要額外的相容性包裝。`head -n1` 是防呆:iso-dvd
# 目錄下只有一個 DVD-1 映像檔,理論上只會有一個結果,但如果 Debian
# 未來改變目錄結構、同時列出多個候選,寧可明確只取第一個也不要整個
# 比對邏輯壞掉。注意 DVD 檔名裡的 "DVD-1" 是大寫、中間有連字號。
BASE_ISO_NAME="$(grep -o 'debian-[0-9][0-9.]*-'"$DEBIAN_ARCH_DIR"'-DVD-1\.iso' "$WORK_DIR/SHA256SUMS" | head -n1)"
if [ -z "$BASE_ISO_NAME" ]; then
    echo "error: could not find a 'debian-<version>-$DEBIAN_ARCH_DIR-DVD-1.iso' entry in $SHA256SUMS_URL — Debian's directory layout or DVD filename convention may have changed; open the URL in a browser to see what's actually there" >&2
    exit 1
fi
echo "==> found official DVD-1 image: $BASE_ISO_NAME"

EXPECTED_SHA256="$(awk -v name="$BASE_ISO_NAME" '$2 == name || $2 == "*"name {print $1}' "$WORK_DIR/SHA256SUMS")"
if [ -z "$EXPECTED_SHA256" ]; then
    echo "error: $BASE_ISO_NAME not found in downloaded SHA256SUMS — refusing to continue with an unverified ISO" >&2
    exit 1
fi

# --- 2.5 下載官方 ISO(有本地快取就重用,雜湊對得上才算數)------------
# 這支腳本在第一次真的拿去跑之前,大概率會被反覆執行很多次(preseed/
# late-command 邏輯只要哪裡出錯就要重跑整個建置流程再進 QEMU 測一次)
# ——DVD-1 ISO 有 ~3.7GB,每次都重新下載對「反覆測試、反覆修正」這種
# 使用情境很不友善(而且比 netinst 更痛,檔案大 5 倍),所以在 repo 外的
# $REPO_ROOT/dist/.cache/ 底下留一份快取。快取是否可以重用完全看雜湊值
# 是否還跟官方最新的 SHA256SUMS 一致——不是看檔名或下載時間,這樣即使
# Debian 之後把同一個檔名的 DVD-1 ISO 換成新的內容(小版本更新常有這種
# 情況),也不會誤用一份過期的快取。
# GONAS_ISO_NO_CACHE=1 時完全跳過本地快取(不讀、不寫)。理由:CI runner
# 是用完即丟的,快取那份 3.7GB 複製既幫不上下次(下次是全新機器),又白白
# 多佔一份系統碟空間——在小碟 runner 上,少寫這 3.7GB 常常就是「爆不爆碟」
# 的差別。本機反覆建置時不要設這個變數,快取照樣有用。
CACHE_DIR="$REPO_ROOT/dist/.cache/debian-iso"
CACHED_ISO="$CACHE_DIR/$BASE_ISO_NAME"
USE_CACHE=1
[ "${GONAS_ISO_NO_CACHE:-0}" = "1" ] && USE_CACHE=0

if [ "$USE_CACHE" = "1" ] && [ -f "$CACHED_ISO" ] && [ "$(gonas_sha256sum "$CACHED_ISO" | awk '{print $1}')" = "$EXPECTED_SHA256" ]; then
    echo "==> reusing cached $CACHED_ISO (checksum matches current SHA256SUMS)"
    cp "$CACHED_ISO" "$WORK_DIR/base.iso"
else
    echo "==> downloading $BASE_ISO_URL/$BASE_ISO_NAME"
    echo "    (this requires real internet access to a Debian mirror — will fail in a network-restricted sandbox)"
    # 下載加固(第三十五輪後 CI 實測抓到:在 GitHub runner 上這步會在下到
    # 一半時毫無錯誤訊息就被砍斷,最像磁碟被塞爆):
    #   --continue        斷了重試時接續已下載的部分,不從頭再來
    #   --tries=3         最多重試 3 次
    #   --timeout=30      連線/讀取逾時 30 秒,卡死的連線及早放棄重試
    #   --progress=dot:giga  進度改成每 ~1GB 一行,而不是每 50K 一行——
    #                     原本 3.7GB 會刷出七萬多行 log,又慢又吵、還可能
    #                     觸發 CI 的 log 上限;giga 模式全程只印幾行。
    # 不加 `-q`,保留 wget 真正的錯誤訊息(DNS/連線被拒/逾時/中途斷線)。
    if ! wget --continue --tries=3 --timeout=30 --progress=dot:giga \
            -O "$WORK_DIR/base.iso" "$BASE_ISO_URL/$BASE_ISO_NAME"; then
        echo "error: download of $BASE_ISO_NAME failed (see the wget error above for the real reason — DNS, connection refused, timeout, connection dropped mid-transfer, or the work disk running out of space are the common causes for a ~3.7GB DVD-1 image)" >&2
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
# 起來,下次又被當成「快取命中」重用。GONAS_ISO_NO_CACHE=1(CI)時整段跳過。
if [ "$USE_CACHE" = "1" ]; then
    mkdir -p "$CACHE_DIR"
    if [ ! -f "$CACHED_ISO" ] || [ "$(gonas_sha256sum "$CACHED_ISO" 2>/dev/null | awk '{print $1}')" != "$ACTUAL_SHA256" ]; then
        cp "$WORK_DIR/base.iso" "$CACHED_ISO"
    fi
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
if [ "$USE_CACHE" = "1" ]; then
    for old_cached in "$CACHE_DIR"/debian-*-"$DEBIAN_ARCH_DIR"-DVD-1.iso; do
        [ -e "$old_cached" ] || continue
        [ "$(basename "$old_cached")" = "$BASE_ISO_NAME" ] && continue
        echo "==> removing stale cached ISO for a different Debian release ($DEBIAN_ARCH_DIR): $old_cached"
        rm -f "$old_cached"
    done
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
# 這裡設執行位元純粹是「如果 xorriso/Rock Ridge 真的保留得住,那就順便
# 帶著」的防禦性做法——實際的安裝路徑完全不依賴它(late-command.sh 用
# `sh` 呼叫、install.sh 用 `-f` 找 gonasd,見各自檔案的說明),第十九輪
# 覆閱把 gonasd 也一起加進來,理由同上:多帶一層保險,少一個「萬一哪天
# 又改回依賴執行位元」的隱患。
chmod +x "$GONAS_ON_ISO/late-command.sh" "$GONAS_ON_ISO/release-$ARCH/install.sh" "$GONAS_ON_ISO/release-$ARCH/gonasd" 2>/dev/null || true
cp -a "$SCRIPT_DIR/overlay/." "$GONAS_ON_ISO/overlay/"
cp "$SCRIPT_DIR/preseed.cfg" "$GONAS_ON_ISO/preseed.cfg"

# --- 4.7 第五十七輪:把「這片 DVD 上真的有的」選用套件加進 pkgsel/include ---
# 使用者第三次實機:先前用 late_command 自己掛 DVD 當 apt 來源去裝 samba/
# mergerfs 等,結果一個都沒裝進去——因為 preseed 關掉了 cdrom apt 來源
# (apt-setup/cdrom/set-first=false),late_command 階段根本沒有可用的 apt
# 來源。真正會動的離線機制是 d-i 官方的 pkgsel/include:它在「安裝基礎系統」
# 階段執行,那時 d-i 自己把 DVD 當套件來源(openssh-server/sudo 就是這樣離線
# 裝好的,實機已證實)。所以這裡改成:掃描剛解開的 DVD 的 pool/,把「確實
# 存在」的選用套件補進 pkgsel/include。只加 DVD 上真的有的,絕不會因為某個
# 套件不在這片 DVD 上就讓整個 pkgsel 步驟失敗(all-or-nothing 的雷)。DVD-1
# 對它收錄的套件是相依封閉的,所以只要主套件在,相依也在,離線裝得起來。
# 不在這片 DVD 上的(建置時會印出來),仍需開機後有網路用系統診斷補裝。
OPTIONAL_PKGS="mergerfs snapraid samba nfs-kernel-server smartmontools wireguard-tools rsync docker.io nut"
echo "==> scanning the DVD pool for optional packages to bake in via pkgsel/include"
# gonas_scan_pool_packages 印出「pool 底下真的有 .deb」的候選(見
# lib/scan-pool-packages.sh,有離線測試)。剩下的就是這片 DVD 沒有的。
# shellcheck disable=SC2086
PRESENT_PKGS="$(gonas_scan_pool_packages "$EXTRACT_DIR/pool" $OPTIONAL_PKGS)"
MISSING_PKGS=""
for _pkg in $OPTIONAL_PKGS; do
    case " $PRESENT_PKGS " in
        *" $_pkg "*) : ;;
        *) MISSING_PKGS="$MISSING_PKGS $_pkg" ;;
    esac
done
MISSING_PKGS="$(echo "$MISSING_PKGS" | sed 's/^ *//;s/ *$//')"
if [ -n "$PRESENT_PKGS" ]; then
    # 把找到的套件接在 pkgsel/include 既有的 openssh-server sudo 後面。用
    # gonas_sed_inplace(BSD/GNU sed 相容,見 lib/portable-sed.sh)。
    gonas_sed_inplace "s#^d-i pkgsel/include string \(.*\)#d-i pkgsel/include string \\1 $PRESENT_PKGS#" "$GONAS_ON_ISO/preseed.cfg"
    echo "==> baking these optional packages into the image (found on the DVD): $PRESENT_PKGS"
fi
if [ -n "$MISSING_PKGS" ]; then
    echo "==> NOT on this DVD: $MISSING_PKGS" >&2
fi

# --- 4.8 第五十八輪:把「DVD 上沒有」的選用套件做成「離線可裝」---------------
# 使用者:「這幾個軟件你為什麼還需要聯網你不做成離線安裝的?」——DVD-1 確實
# 不含 mergerfs/samba/snapraid/nfs-kernel-server/wireguard-tools/docker.io
# (上面 4.7 掃出來的 MISSING_PKGS 已證實)。要讓它們「安裝/使用時完全不
# 連網」,唯一的辦法就是在「建置期(Mac 有網路)」先把這些 .deb 連同相依
# 封閉集抓下來塞進 ISO;裝好開機後,late-command.sh 會把它們設成一個本機
# `file://` apt 來源,使用者在 Web Doctor 點「安裝」時 apt 就從本機裝、不
# 連網(見 lib/fetch-offline-debs.py 與 late-command.sh 的說明)。這同時
# 滿足使用者要的「兩者都要」:內建離線可裝,開機後有網路時 apt 也照樣能
# 走網路補裝/更新。
#
# 刻意設計成「best-effort、絕不中斷建置」:抓不到(沒網路、鏡像擋掉、沒
# python3)就印警告、跳過,ISO 照樣建得出來——那幾個套件退回「開機後有
# 網路再裝」,跟這一輪之前的行為一樣,不會更糟。也刻意「不」動 DVD 自己的
# 套件庫/索引,只在 gonas/debs/ 產生一份獨立的 flat repo,弄壞了最多是這
# 幾個選用套件離線裝不起來,絕不會影響「基礎系統安裝」本身。
#
# 鏡像可用 GONAS_DEB_MIRROR 覆寫(中國大陸使用者可指到 tuna/ustc 等),
# 預設 deb.debian.org。可用 GONAS_SKIP_OFFLINE_DEBS=1 整段跳過(除錯/趕時間)。
OFFLINE_DEB_DIR="$GONAS_ON_ISO/debs"
GONAS_DEB_MIRROR="${GONAS_DEB_MIRROR:-http://deb.debian.org/debian}"
if [ "${GONAS_SKIP_OFFLINE_DEBS:-0}" = "1" ]; then
    echo "==> GONAS_SKIP_OFFLINE_DEBS=1 — skipping offline .deb bundling (optional packages will need network at install time)" >&2
elif [ -z "$MISSING_PKGS" ]; then
    echo "==> every optional package is already on the DVD — no offline .deb bundling needed" >&2
elif ! command -v python3 >/dev/null 2>&1; then
    echo "warning: python3 not found — cannot bundle offline .debs for [$MISSING_PKGS]; they will need network at install time. Install python3 (Xcode CLT) to enable offline install." >&2
else
    # 從解開的 ISO 讀真正的 codename(trixie 等),不寫死——鏡像的套件版本
    # 要跟這片 DVD 的 suite 對得起來。任何一個 dists/*/Release 的 Codename
    # 欄位都一樣,取第一個。
    ISO_CODENAME="$(awk -F': ' '/^Codename:/{print $2; exit}' "$EXTRACT_DIR"/dists/*/Release 2>/dev/null | tr -d ' \r')"
    if [ -z "$ISO_CODENAME" ]; then
        echo "warning: could not detect the Debian codename from the ISO's dists/*/Release — skipping offline .deb bundling for [$MISSING_PKGS]" >&2
    else
        echo "==> bundling offline .debs for [$MISSING_PKGS] from $GONAS_DEB_MIRROR ($ISO_CODENAME/$ARCH) — this downloads a dependency closure, may take a few minutes"
        # shellcheck disable=SC2086
        if python3 "$SCRIPT_DIR/lib/fetch-offline-debs.py" \
                --mirror "$GONAS_DEB_MIRROR" \
                --codename "$ISO_CODENAME" \
                --arch "$ARCH" \
                --out "$OFFLINE_DEB_DIR" \
                $MISSING_PKGS; then
            _deb_count="$(find "$OFFLINE_DEB_DIR" -name '*.deb' 2>/dev/null | grep -c . || true)"
            echo "==> offline .deb bundle ready: $_deb_count package file(s) in gonas/debs/ (installable with NO network after boot via Web Doctor)"
        else
            echo "warning: offline .deb bundling did not complete (network/mirror/closure issue) — [$MISSING_PKGS] will need network at install time. The rest of the ISO is unaffected." >&2
            # 沒抓成別留半套目錄,免得 late-command.sh 誤以為有一份可用的 repo。
            rm -rf "$OFFLINE_DEB_DIR"
        fi
    fi
fi
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

# --- 4.5 (第二十八輪已移除)離線 SSH 的 .deb 打包 ---------------------
# 這裡原本(第十九輪起)是一整段「在建置機器上算 openssh-server 的相依
# 封閉集、逐一下載 .deb、打包進 ISO 的 gonas/debs/,再由 late-command.sh
# 離線 dpkg -i」的自訂邏輯——那是為了在只含 base 套件的 netinst 光碟上
# 硬做出「離線也裝得到 SSH」而寫的,脆弱(相依解析、版本比對、下載失敗
# 都要自己處理)且踩過好幾個坑。第二十八輪換成 DVD-1 完整版之後,
# openssh-server / sudo 本來就在光碟的套件庫裡,直接由 preseed.cfg 的
# `pkgsel/include` 用 Debian 官方機制從光碟離線裝好,這一整段連同
# lib/deb-closure.sh / test-deb-closure.sh 一併刪除,不再需要。

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
# 第二十二輪(使用者實測:amd64 + ESXi 真機安裝,preseed.cfg 明明已經
# 寫了 `d-i debian-installer/locale string en_US.UTF-8`,語系/國家選單
# 卻還是整個跳出來要手動選)新增 `language=`/`country=`/`locale=`/
# `keymap=` 這幾個「裸」核心參數,理由是 Debian 官方文件明確記載的一個
# preseed 陷阱:語系/國家/鍵盤這幾題(localechooser)是整個安裝流程裡
# 問得最早的幾題,早到「掛載光碟、讀取 preseed.cfg 檔案內容」這件事
# 本身都還沒發生——這幾題問完之前,debconf 根本還不知道 preseed.cfg
# 裡寫了什麼答案,所以就算檔案裡確實有寫,一樣會被問一次。這跟上面
# `priority=high` 那個決定完全是兩回事,不衝突:priority 只影響「還沒
# 有答案的問題要不要跳出來問」,這幾題的根本問題是「答案根本還沒被
# 讀到」,唯一解法是官方文件建議的做法——直接把這幾個值當成核心參數
# 寫在開機這一行,跳過「先讀 preseed.cfg 才知道答案」這個時序問題。
# preseed.cfg 裡原本那行 `debian-installer/locale` 保留不動,兩邊寫的
# 值一致,互相印證、不衝突。
# 安裝前端:保留 Debian 官方的「圖形(gtk)安裝界面」——這是使用者明確要
# 的(第五十九輪:使用者一直都是用圖形界面安裝,要求維持圖形、並把裡面的
# Debian logo 換成 GoNAS logo,不要改成文字模式)。第五十八輪一度加過
# DEBIAN_FRONTEND=newt 想用文字前端閃避 logo 破圖,已於第五十九輪移除。
# 圖形界面裡那張 Debian logo 由下面第 5c 步(lib/rebrand-installer-initrd.py)
# 在 gtk initrd 裡換成 GoNAS logo。
APPEND_EXTRA="auto=true priority=high language=en country=US locale=en_US.UTF-8 keymap=us preseed/file=/cdrom/gonas/preseed.cfg hostname=gonas domain="
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
# 第五十八輪建置覆核(#1):原本這裡只要「任何一個檔案」含 marker 就算通過
# (INJECTED=1; break)。但 amd64 的 DVD 同時有 isolinux/(BIOS 開機)與
# boot/grub/(UEFI 開機)兩套選單;如果哪天 grub.cfg 格式變了、grub 那條
# 注入沒命中,只要 isolinux 命中就仍然「通過」——結果是 BIOS 會自動安裝、
# UEFI 卻開進手動安裝,而這正是這個專案的目標機器(UEFI 的 mini PC/NAS)。
# 改成:isolinux 家族與 grub 家族「各自」只要存在(有檔案)就必須各自至少
# 命中一個,任一家族存在卻整組都沒命中就中止,不產出這種「一半會自動裝、
# 一半悄悄掉回手動」的 ISO。
ISOLINUX_TOTAL=0; ISOLINUX_HIT=0
GRUB_TOTAL=0; GRUB_HIT=0
while read -r cfgfile; do
    case "$cfgfile" in
        *isolinux*)
            ISOLINUX_TOTAL=$((ISOLINUX_TOTAL + 1))
            grep -q "$APPEND_MARKER" "$cfgfile" 2>/dev/null && ISOLINUX_HIT=1
            ;;
        *grub*)
            GRUB_TOTAL=$((GRUB_TOTAL + 1))
            grep -q "$APPEND_MARKER" "$cfgfile" 2>/dev/null && GRUB_HIT=1
            ;;
        *)
            # 其他路徑(理論上不會有,find 只收 isolinux/ 與 boot/grub/)——
            # 當成「沒有明確歸類的開機選單」,命中就記在 grub 家族一併看待。
            grep -q "$APPEND_MARKER" "$cfgfile" 2>/dev/null && GRUB_HIT=1
            ;;
    esac
done < "$CFG_LIST"
if [ "$ISOLINUX_TOTAL" -gt 0 ] && [ "$ISOLINUX_HIT" != "1" ]; then
    echo "error: the isolinux (BIOS) boot menu is present but the GoNAS preseed parameter was NOT injected into any of its config files — the sed patterns no longer match this Debian release's isolinux format. A BIOS boot would silently fall back to a manual install. Refusing to continue; inspect the isolinux/* files in $CFG_LIST by hand." >&2
    exit 1
fi
if [ "$GRUB_TOTAL" -gt 0 ] && [ "$GRUB_HIT" != "1" ]; then
    echo "error: the grub (UEFI) boot menu is present but the GoNAS preseed parameter was NOT injected into any of its config files — the sed patterns no longer match this Debian release's grub.cfg format. A UEFI boot (the common case for modern mini-PCs/NAS) would silently fall back to a manual install. Refusing to continue; inspect the boot/grub/* files in $CFG_LIST by hand." >&2
    exit 1
fi
echo "==> confirmed the GoNAS preseed boot parameter was injected into every boot-menu family present (isolinux hits=$ISOLINUX_HIT/$ISOLINUX_TOTAL, grub hits=$GRUB_HIT/$GRUB_TOTAL)"

# 縮短選單等待時間——這是「安裝媒體」的開機選單(裝完系統之後的
# GRUB 選單品牌化/等待時間是 late-command.sh 在目標系統裡處理的，
# 是兩個不同的東西)。
if [ -f "$EXTRACT_DIR/isolinux/isolinux.cfg" ]; then
    # 用 gonas_sed_inplace(見上面的說明跟 lib/portable-sed.sh)而不是
    # 直接 `sed -i 'script' file`——原本這裡就是那個 macOS/BSD sed
    # 不相容問題的其中一個現場。
    # 第二十九輪先前試過把選單「藏起來、瞬間自動開始安裝」,第三十輪
    # 使用者(以產品設計的角度)明確否決:他要的不是「一開機就自動裝」,
    # 而是一個「乾淨、掛著 GoNAS 品牌、有一個明確的『安裝 GoNAS』可以按下
    # 去才開始」的選擇畫面(類似群暉安裝助手)。所以這裡把等待時間設回
    # 一個「看得到、來得及看清楚品牌、也來得及自己按 Enter 開始」的長度
    # ——30 秒(isolinux timeout 單位是 1/10 秒,300=30 秒)。預設反白項目
    # 維持官方 ISO 的安裝項(下面第 5b 步會另外把選單背景換成 GoNAS
    # 潑濺圖、標題文字換成 GoNAS);使用者可以直接按 Enter 立刻開始
    # (等同「按下開始鈕」),或等 30 秒倒數結束自動開始(讓完全無人
    # 值守的情境仍然裝得完)。刻意不再設 prompt 0/藏選單——那正是上一版
    # 被否決的地方。
    gonas_sed_inplace 's/^timeout .*/timeout 300/' "$EXTRACT_DIR/isolinux/isolinux.cfg" || true
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
        # 第三十輪(使用者以產品設計角度否決「藏選單、瞬間自動安裝」,
        # 改要「掛 GoNAS 品牌、有明確安裝項可以按下才開始」的選擇畫面):
        # GRUB 的 timeout 單位是「秒」,設 30 秒——看得到、來得及看清楚
        # 品牌、也來得及自己按 Enter 立刻開始(等同按下開始鈕),或倒數
        # 結束自動開始(無人值守也裝得完)。
        gonas_sed_inplace 's/^[[:space:]]*set[[:space:]]*timeout=.*/set timeout=30/' "$cfgfile" || true
        # 明確把 timeout_style 設回 menu(顯示選單),推翻上一版的 hidden
        # ——那正是被否決的地方。有這一行就就地改,沒有就補一行。
        if grep -Eq '^[[:space:]]*set[[:space:]]+timeout_style=' "$cfgfile" 2>/dev/null; then
            gonas_sed_inplace 's/^[[:space:]]*set[[:space:]]*timeout_style=.*/set timeout_style=menu/' "$cfgfile" || true
        else
            printf '\nset timeout_style=menu\n' >> "$cfgfile"
        fi
    elif grep -q 'menuentry' "$cfgfile" 2>/dev/null; then
        printf '\nset timeout_style=menu\nset timeout=30\n' >> "$cfgfile"
    fi
done < "$CFG_LIST"

# --- 5b. 把開機選單的背景圖(潑濺圖)換成 GoNAS 品牌圖 -----------------
# 第三十輪(使用者以產品設計角度要求「安裝選擇畫面要掛 GoNAS 品牌、
# 不要再看到 Debian 標誌」)。前面第 5 步只換得動選單「文字」,真正讓
# 使用者一眼看到「這是 Debian」的,是那張背景潑濺圖(isolinux 的
# `menu background`、grub 的 `background_image` 指到的 PNG)。這一步把
# 那些背景圖換成事先設計好、commit 在 branding/splash.png 的 GoNAS 圖
# (640x480,isolinux vesamenu 的標準解析度;grub gfxmenu 會自行縮放)。
#
# 誠實邊界:選單「框」本身(反白顏色、字型)是 vesamenu.c32 / grub
# gfxmenu 自己畫的,這一步只換背景圖、不保證把反白色也調成 teal
# ——背景圖換成 GoNAS 之後,畫面主體已經是 GoNAS 品牌,反白色維持
# 安裝程式預設值也還是可讀的。這一步是「盡量不出錯」的可靠改動;要
# 追求跟設計稿完全一致的選單配色,得逐版對 stdmenu.cfg 的 `menu color`
# 動刀,格式敏感又只能靠真機開機驗證,刻意不在這裡做。
GONAS_SPLASH="$SCRIPT_DIR/branding/splash.png"
if [ ! -f "$GONAS_SPLASH" ]; then
    echo "warning: $GONAS_SPLASH not found — skipping boot-menu background rebranding (menu will keep the stock background image)" >&2
else
    echo "==> replacing boot menu background image(s) with the GoNAS splash"
    # 先蒐集所有「要被換掉」的背景圖檔的相對路徑,去重之後再逐一覆蓋。
    # 來源有二:(1) 固定已知路徑 isolinux/splash.png(Debian amd64 幾乎
    # 一定有);(2) 從選單設定檔裡實際被 `menu background` / `background_image`
    # 指到的檔名——這樣不管是哪個版本、指到哪個檔名都換得到,不寫死。
    BG_LIST="$WORK_DIR/gonas-bg-targets.list"
    : > "$BG_LIST"
    [ -f "$EXTRACT_DIR/isolinux/splash.png" ] && echo "isolinux/splash.png" >> "$BG_LIST"
    while read -r cfgfile; do
        # 抓 `menu background X` / `MENU BACKGROUND X`(取最後一個欄位)跟
        # `background_image X`(grub;取最後一個欄位)。awk 對大小寫不敏感
        # 地比對關鍵字,印出該行最後一欄(就是圖檔路徑)。
        awk 'tolower($0) ~ /(^|[[:space:]])menu[[:space:]]+background[[:space:]]/ || tolower($0) ~ /(^|[[:space:]])background_image([[:space:]]|=)/ { print $NF }' "$cfgfile" 2>/dev/null
    done < "$CFG_LIST" | while read -r ref; do
        # 設定檔裡寫的路徑可能是絕對(/isolinux/splash.png)或相對
        # (splash.png)。統一去掉開頭的斜線,當成相對 EXTRACT_DIR 的路徑;
        # 也可能是相對「該設定檔所在目錄」,兩種都試,存在才收進清單。
        rel="${ref#/}"
        if [ -f "$EXTRACT_DIR/$rel" ]; then
            echo "$rel"
        fi
    done | sort -u >> "$BG_LIST"
    # 逐一覆蓋(去重後),記數。用 while 讀清單避免檔名有空白。
    BG_REPLACED=0
    sort -u "$BG_LIST" | while read -r rel; do
        [ -n "$rel" ] || continue
        if [ -f "$EXTRACT_DIR/$rel" ]; then
            cp "$GONAS_SPLASH" "$EXTRACT_DIR/$rel" && echo "    - replaced $rel"
        fi
    done
    # 上面的 while 在管線子行程裡,計數拿不回來——這裡用檔案行數直接回報
    # 「找到幾個目標」,已經夠用(真正有沒有換成功,cp 失敗會自己印錯誤)。
    BG_REPLACED="$(sort -u "$BG_LIST" | grep -c . || true)"
    echo "==> boot menu background: $BG_REPLACED target image(s) replaced with the GoNAS splash"
    if [ "$BG_REPLACED" = "0" ]; then
        echo "warning: found no boot-menu background image to replace — the menu text was rebranded to GoNAS, but the background graphic (if any) may still be the stock one; verify on a real boot" >&2
    fi
fi

# --- 5c. 把圖形(gtk)安裝器裡的 Debian logo 換成 GoNAS -----------------
# 第三十一輪(使用者要求「圖形安裝界面的 Debian logo 要換成 GoNAS」)。
# 圖形安裝器畫面正上方那張最顯眼的 Debian 標誌,是 gtk installer initrd
# 裡的一個 PNG(usr/share/graphics/logo_installer.png)。lib/ 底下的
# rebrand-installer-initrd.py 會把 initrd 拆開、把那張圖換成
# branding/logo_installer.png、再原封不動打包回去(邏輯有離線單元測試:
# test-rebrand-installer-initrd.py,驗證無修改時 byte-identical、換圖後
# 其他檔不動、gzip/xz 都能來回)。
#
# 誠實邊界(這一步只換得動「圖片」):圖形安裝器每個畫面標題那些「文字」
# 上的 Debian 字樣,是編譯進安裝程式模板/翻譯檔裡的,不重建整個
# debian-installer 改不掉——見 README.md。這一步把「最顯眼的 logo 圖」
# 換掉,是「保留 Debian 安裝器」前提下能做到的最大品牌化。
#
# 依賴:python3(裝了 Xcode CLT 的 Mac 一定有)。沒有 python3 就跳過、
# 印 warning、不中斷建置——寧可少換這張圖,也不要讓整個 ISO 建不出來。
#
# 第五十九輪(使用者要維持圖形安裝界面、把 Debian logo 換成 GoNAS logo):
# 這一步預設「開啟」,把 gtk initrd 裡的 Debian logo 換成 branding/logo_installer.png
# (GoNAS logo)。先前第 53/55/57 輪實機都破圖,原因是我們在沒有原始 logo 檔可
# 對照的情況下盲改尺寸/格式;第五十九輪的做法是先拿到使用者真機 ISO 裡的原始
# gtk initrd,對照原始 logo 的「確切尺寸與 PNG 格式」產生相符的 GoNAS 版本,
# 才不會再破圖(見 README.md 與 rebrand-installer-initrd.py)。要臨時關掉這一步、
# 讓圖形安裝器顯示 Debian 自己的 logo,用 `GONAS_REBRAND_INSTALLER_LOGO=0 make ...`。
GONAS_INSTALLER_LOGO="$SCRIPT_DIR/branding/logo_installer.png"
if [ "${GONAS_REBRAND_INSTALLER_LOGO:-1}" = "0" ]; then
    echo "==> installer logo rebranding disabled (GONAS_REBRAND_INSTALLER_LOGO=0) — the graphical installer will show Debian's own logo" >&2
elif [ ! -f "$GONAS_INSTALLER_LOGO" ]; then
    echo "warning: $GONAS_INSTALLER_LOGO not found — skipping graphical-installer logo rebranding" >&2
elif ! command -v python3 >/dev/null 2>&1; then
    echo "warning: python3 not found on this build machine — skipping graphical-installer logo rebranding (the boot menu is still GoNAS-branded; install python3 to also rebrand the graphical installer logo)" >&2
else
    echo "==> replacing the graphical installer Debian logo with the GoNAS logo inside gtk initrd(s)"
    # 找出所有 initrd 檔(不同版本/架構路徑不同:install.amd/gtk/initrd.gz、
    # install.a64/gtk/initrd.gz 等)。對每個都跑一次 rebrander——沒有 logo
    # 的 initrd(例如純文字安裝的那個)會回傳 2、原檔不動,無害;有 logo
    # 的(gtk 那個)才會真的被改寫。initrd 路徑不含空白/換行,用 `-print`
    # 逐行讀即可;`while IFS= read -r` 也已能正確處理含空白的路徑。
    GTK_LOGO_REPLACED=0
    INITRD_FOUND=0
    # shellcheck disable=SC2044
    find "$EXTRACT_DIR" -type f \( -name 'initrd' -o -name 'initrd.gz' -o -name 'initrd.xz' \) -print > "$WORK_DIR/initrd-files.list" 2>/dev/null || true
    while IFS= read -r initrd; do
        [ -n "$initrd" ] || continue
        INITRD_FOUND=$((INITRD_FOUND + 1))
        # rebrander 回傳:0=換到、2=這個 initrd 沒 logo(略過)、1=出錯。
        if python3 "$SCRIPT_DIR/lib/rebrand-installer-initrd.py" "$initrd" "$GONAS_INSTALLER_LOGO"; then
            GTK_LOGO_REPLACED=$((GTK_LOGO_REPLACED + 1))
            echo "    - replaced logo in ${initrd#"$EXTRACT_DIR"/}"
        else
            _rc=$?
            # 2 是「這個 initrd 裡沒有 logo」,是正常情況(文字安裝的 initrd),
            # 不當錯誤。只有 1(真的出錯)才印出來提醒——但仍不中斷建置,
            # 因為開機選單品牌化已經生效,少換這張圖不至於毀掉整份 ISO。
            if [ "$_rc" != "2" ]; then
                echo "warning: logo replacement failed on ${initrd#"$EXTRACT_DIR"/} (rc=$_rc) — leaving it unchanged; the graphical installer for that image may still show the stock Debian logo" >&2
            fi
        fi
    done < "$WORK_DIR/initrd-files.list"
    echo "==> graphical installer logo: replaced with GoNAS in $GTK_LOGO_REPLACED of $INITRD_FOUND initrd file(s) (initrds without an installer logo are skipped, which is normal)"
    if [ "$INITRD_FOUND" = "0" ]; then
        echo "warning: found no initrd files under the ISO tree — the graphical installer logo could not be replaced; verify the ISO layout" >&2
    elif [ "$GTK_LOGO_REPLACED" = "0" ]; then
        echo "warning: found initrd files but none contained an installer logo to replace — the graphical installer may still show the Debian logo; this Debian build may store the logo elsewhere (verify on a real boot and report back)" >&2
    fi
fi

# --- 6. 重新計算 checksum 清單、重新包裝 -------------------------------
echo "==> recomputing md5sum.txt"
# md5sum.txt 是 Debian ISO 給「檢查光碟完整性」自我測試用的清單。我們
# 加了 gonas/ 檔案、也改了開機設定檔,所以重算一份才對得起來。
#
# 第二十八輪(換成 DVD-1)的效能考量:netinst 只有幾百個檔案,原本
# `find | while read; do gonas_md5sum; done`(逐檔各 spawn 一個程序)還
# 可以接受;但 DVD-1 的 pool/ 底下有「上萬個」套件檔案,逐檔 spawn 會
# 讓這一步在使用者的 Mac mini 上慢到好幾分鐘甚至更久。改成優先用「一次
# 吃多個檔名」的批次呼叫:Linux 建置機有 GNU `md5sum`、macOS 有內建的
# `md5 -r`,兩者都接受多個檔名參數、輸出格式也都是「雜湊 檔名」一行一個
# ——用 `xargs` 分批餵,程序 spawn 次數從「上萬」降到「個位數」。兩個
# 都沒有才退回原本的逐檔 gonas_md5sum(理論上不會發生,見
# lib/portable-checksum.sh:有 sha256 就幾乎一定有 md5 家族其一)。
# `-print0 | xargs -0` 處理檔名含空白/特殊字元;GNU 與 BSD 的 find/xargs
# 都支援 -print0/-0。
if command -v md5sum >/dev/null 2>&1; then
    ( cd "$EXTRACT_DIR" && find . -type f ! -name 'md5sum.txt' ! -path './isolinux/*' -print0 | xargs -0 md5sum > md5sum.txt )
elif command -v md5 >/dev/null 2>&1; then
    # macOS 內建 `md5 -r` 也接受多個檔名,輸出「雜湊 檔名」格式跟 GNU 相容。
    ( cd "$EXTRACT_DIR" && find . -type f ! -name 'md5sum.txt' ! -path './isolinux/*' -print0 | xargs -0 md5 -r > md5sum.txt )
else
    ( cd "$EXTRACT_DIR" && find . -type f ! -name 'md5sum.txt' ! -path './isolinux/*' | while read -r f; do gonas_md5sum "$f"; done > md5sum.txt )
fi

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
# 重封裝方法:用 xorriso 官方的「as_mkisofs 報告 + 重建」法。
#
# 先前用「-map 整棵樹 / + -boot_image any replay」在 amd64 上會失敗:
#   libisofs: FAILURE : Cannot refer by isohybrid MBR to data outside of
#             ISO 9660 filesystem.
# 原因是 replay 會沿用原 ISO 的 System Area(isohybrid MBR + GPT),裡面
# 對「append 在 ISO 9660 之後的 EFI 分割區」是用絕對位移參照的;可是整棵
# 目錄樹被重新 map 之後檔案系統大小/位移都變了,那個絕對參照就落到新映像
# 的 ISO 9660 範圍之外,於是失敗。
#
# 正確做法(xorriso man page「Emulation of mkisofs」與 Debian 官方重封裝
# 建議):讓 xorriso 從原始 ISO 讀出它自己的完整開機參數(BIOS isolinux、
# UEFI efi.img、isohybrid、GPT/append 分割區全都在內),再用這組參數把
# 「修改過的目錄樹」整個重建成一份新 ISO。開機記錄與 EFI 映像是重新算進
# 新檔案系統裡的,不會殘留指向原檔的絕對位移,從根本上避開上面那個失敗。
MKARGS_FILE="$WORK_DIR/mkisofs-args.txt"
xorriso -indev "$WORK_DIR/base.iso" -report_el_torito as_mkisofs 2>/dev/null \
    | grep -v '^[[:space:]]*$' > "$MKARGS_FILE" || true
if [ ! -s "$MKARGS_FILE" ]; then
    echo "error: 無法從原始 ISO 讀出開機參數(-report_el_torito as_mkisofs 沒有輸出)" >&2
    echo "  這通常代表這份 base ISO 的開機結構跟預期不同,或 xorriso 版本過舊(需 >= 1.4.8)" >&2
    exit 1
fi
echo "==> 從原始 ISO 抽出的開機參數:"
sed 's/^/    /' "$MKARGS_FILE"
# 這些行是 xorriso 為了「原封不動餵回 -as mkisofs」而輸出、已經處理好
# shell 引號的字串(含空白的 volume id 等都被單引號包好),所以用 eval
# 展開才能正確還原;$EXTRACT_DIR 當成新映像的根目錄。
eval "xorriso -as mkisofs $(tr '\n' ' ' < "$MKARGS_FILE") -o \"$OUT_ISO\" \"$EXTRACT_DIR\""

gonas_sha256sum "$OUT_ISO" > "$OUT_ISO.sha256"
echo "==> done: $OUT_ISO"
echo "==> checksum: $(cat "$OUT_ISO.sha256")"
echo
echo "下一步:務必先用 QEMU/VirtualBox 開機測試整個安裝流程，見"
echo "build/appliance/README.md「如何驗證」一節，確認過再燒錄到真實硬體。"
