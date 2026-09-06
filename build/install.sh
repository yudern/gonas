#!/bin/sh
# install.sh 安裝(或升級)GoNAS 到這台機器。
#
# 支援兩種佈局,方便同一支 script 在「下載 release tarball」跟「clone
# 原始碼自己編」兩種情境下都能用:
#
#   1. release tarball 佈局(`make release` 產出的內容解壓縮後):
#        gonasd            <- 這個架構的執行檔,跟 install.sh 放在一起
#        install.sh
#        uninstall.sh
#        gonas.service
#
#   2. 原始碼 checkout 佈局(直接跑 build/install.sh):
#        dist/gonasd-linux-amd64 或 dist/gonasd-linux-arm64  (先 make build-all)
#        build/install.sh
#        build/uninstall.sh
#        build/systemd/gonas.service
#
# 用 /bin/sh(不是 bash)寫,刻意只用 POSIX sh 語法 —— 這樣在精簡過的
# distro(例如某些用 dash 當 /bin/sh 的 Debian/Ubuntu minimal image、或
# Alpine)上也能直接跑,不需要額外裝 bash。
#
# 冪等設計:重複執行這支 script(例如升級到新版本)是安全的 —— 已經
# 存在的 /etc/gonas/gonas.env 不會被覆蓋(裡面可能有管理員手動改過的
# 設定),但執行檔跟 systemd unit 每次都會被最新版本覆蓋。

set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

BIN_DEST=/usr/local/bin/gonasd
CONF_DIR=/etc/gonas
ENV_FILE="$CONF_DIR/gonas.env"
DATA_DIR=/var/lib/gonas
SYSTEMD_UNIT_DIR=/etc/systemd/system
SYSTEMD_UNIT_DEST="$SYSTEMD_UNIT_DIR/gonas.service"

log() {
	printf '%s\n' "$*"
}

err() {
	printf 'install.sh: %s\n' "$*" >&2
}

# ---- 1. 權限檢查 -----------------------------------------------------
# GoNAS 要寫 /usr/local/bin、/etc、/var/lib、systemd unit,這些全部需要
# root。用 `id -u` 而不是檢查 $USER,因為 $USER 在某些非互動式 shell
# (例如某些 CI/自動化環境)不一定有設定。
if [ "$(id -u)" -ne 0 ]; then
	err "請用 root 執行(例如: sudo ./install.sh)"
	exit 1
fi

# ---- 2. 判斷 CPU 架構 -------------------------------------------------
ARCH_RAW=$(uname -m)
case "$ARCH_RAW" in
	x86_64 | amd64)
		ARCH=amd64
		;;
	aarch64 | arm64)
		ARCH=arm64
		;;
	*)
		err "不支援的 CPU 架構: $ARCH_RAW(GoNAS 目前只提供 amd64 / arm64 的預編譯執行檔)"
		err "如果你是自己編譯的,可以用 -bin 參數直接指定執行檔路徑(見下方說明)"
		exit 1
		;;
esac

# ---- 3. 找到要安裝的執行檔 --------------------------------------------
# 允許用 -bin <path> 明確指定(例如自己交叉編譯、或路徑跟預設佈局不同時)。
#
# 這裡用 `-f`(檔案存在)判斷,不是 `-x`(檔案存在且可執行)——這是
# 第十九輪覆閱(使用者實測 arm64 安裝、整台機器裝完卻是一台陽春 Debian)
# 抓到的**根本原因**:appliance 這條安裝路徑,install.sh 是從 ISO 9660
# 檔案系統上跑的(late-command.sh 在 in-target chroot 裡 `sh
# /cdrom/gonas/release-$ARCH/install.sh`),而 xorriso 把檔案重新包裝進
# ISO 時,Unix 執行位元能不能被保留取決於 Rock Ridge 擴充屬性有沒有
# 正確套用——這件事整個專案早就決定「不能信任」(late-command.sh、
# preseed.cfg 的 late_command 都因此改成用 `sh <路徑>` 執行,不靠執行
# 位元),但當時漏掉了 install.sh 內部「用 `-x` 找 gonasd 執行檔」這
# 一處:ISO 上的 gonasd 執行位元一旦遺失,`[ -x gonasd ]` 就判斷為
# 「找不到」,SRC_BIN 變成空的,install.sh 直接印「找不到 gonasd 執行檔」
# 並 `exit 1`——而 late-command.sh 是用 `set -e` 呼叫 install.sh 的,
# install.sh 一非零退出,late-command.sh 就在第一步整個中止,後面
# 安裝服務、換品牌、強制改密碼、留 uninstall.sh 全部不會執行,結果就是
# 使用者看到的「裝完開機,卻是一台什麼都沒品牌化的陽春 Debian」。
# 用 `-f` 只判斷檔案「存在且讀得到」就好——install.sh 下面第 4 步複製
# 完執行檔本來就會自己 `chmod 0755`(見 TMP_BIN 那一段),根本不需要
# 來源檔案本身帶著執行位元,所以放寬成 `-f` 完全不影響安裝正確性,
# 只是不再被那個「ISO 上執行位元有沒有活著」的不可靠假設卡住。
SRC_BIN=""
if [ "$1" = "-bin" ] && [ -n "$2" ]; then
	SRC_BIN="$2"
elif [ -f "$SCRIPT_DIR/gonasd" ]; then
	# release tarball 佈局:執行檔跟 install.sh 放在同一層。
	SRC_BIN="$SCRIPT_DIR/gonasd"
elif [ -f "$SCRIPT_DIR/../dist/gonasd-linux-$ARCH" ]; then
	# 原始碼 checkout 佈局:build/install.sh 往上一層找 dist/。
	SRC_BIN="$SCRIPT_DIR/../dist/gonasd-linux-$ARCH"
fi

if [ -z "$SRC_BIN" ] || [ ! -f "$SRC_BIN" ]; then
	err "找不到 gonasd 執行檔。"
	err "  - 如果你是解壓縮 release tarball,執行檔應該跟 install.sh 在同一層目錄。"
	err "  - 如果你是從原始碼安裝,請先執行: make build-all"
	err "  - 也可以用 './install.sh -bin /path/to/gonasd' 明確指定路徑。"
	exit 1
fi

log "偵測到架構: $ARCH_RAW -> $ARCH"
log "使用執行檔: $SRC_BIN"

# ---- 4. 安裝執行檔 -----------------------------------------------------
# 先裝到暫存檔再 mv,避免「正在執行中的 gonasd 被另一支新程序覆蓋
# 一半」這種極端狀況(雖然 systemctl restart 本來就會先停舊的,這裡是
# 多一層保險,做法跟專案裡其他地方寫檔案的 atomic-write 慣例一致)。
TMP_BIN="$BIN_DEST.new"
cp "$SRC_BIN" "$TMP_BIN"
chmod 0755 "$TMP_BIN"
mv "$TMP_BIN" "$BIN_DEST"
log "已安裝執行檔到 $BIN_DEST"

# ---- 5. 建立設定/資料目錄 ---------------------------------------------
mkdir -p "$CONF_DIR"
mkdir -p "$DATA_DIR"
chmod 0750 "$DATA_DIR"

# gonas.env 只在第一次安裝時建立(裡面是註解掉的預設值,方便管理員自己
# 打開改)——升級時絕對不能覆蓋,不然管理員手動改過的設定就沒了。
if [ ! -f "$ENV_FILE" ]; then
	cat > "$ENV_FILE" <<'EOF'
# GoNAS 執行期環境變數覆寫檔。
#
# 這個檔案由 install.sh 第一次安裝時建立,之後升級「不會」覆蓋它 ——
# 放心改。改完之後要 `systemctl restart gonas` 才會生效。
#
# 取消註解、改成你要的值:
#
# GONAS_LISTEN_ADDR=:8291
# GONAS_DATA_DIR=/var/lib/gonas
EOF
	log "已建立設定檔 $ENV_FILE(預設值皆為註解狀態)"
else
	log "設定檔 $ENV_FILE 已存在,保留不覆蓋"
fi

# ---- 6. systemd 整合(有的話) ------------------------------------------
# 這台開發沙盒裝了 systemctl 這個指令,但 /run/systemd/system 不存在
# ——代表 systemd 根本沒有以 PID 1 的身分在跑(常見於某些容器環境)。
# 這種狀況下呼叫 `systemctl enable/start` 一定會失敗(連不到 systemd
# 的 D-Bus socket),與其讓整支 script 因此中止、執行檔卻明明已經裝好了,
# 不如優雅跳過 systemd 整合、印出手動啟動的指令,把「執行檔裝好了」跟
# 「開機自動啟動有沒有設好」這兩件事分開判斷結果。
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	UNIT_SRC="$SCRIPT_DIR/gonas.service"
	if [ ! -f "$UNIT_SRC" ]; then
		UNIT_SRC="$SCRIPT_DIR/systemd/gonas.service"
	fi

	if [ -f "$UNIT_SRC" ]; then
		cp "$UNIT_SRC" "$SYSTEMD_UNIT_DEST"
		chmod 0644 "$SYSTEMD_UNIT_DEST"
		systemctl daemon-reload
		systemctl enable gonas.service >/dev/null 2>&1 || true
		systemctl restart gonas.service
		log "已安裝並啟動 systemd 服務 gonas.service(開機自動啟動已設定)"
		SYSTEMD_OK=1
	else
		err "找不到 gonas.service unit 檔案,跳過 systemd 整合(執行檔仍已安裝)"
		SYSTEMD_OK=0
	fi
else
	log "偵測到這台機器目前沒有以 systemd 作為 PID 1(或沒有 systemctl),跳過開機自動啟動設定。"
	log "你可以手動啟動 gonasd 測試: sudo env \$(cat $ENV_FILE | grep -v '^#' | xargs -0 2>/dev/null) $BIN_DEST"
	log "或最簡單直接: sudo $BIN_DEST"
	SYSTEMD_OK=0
fi

# ---- 7. 完成,印出摘要 --------------------------------------------------
LISTEN_ADDR=":8291"
if [ -f "$ENV_FILE" ]; then
	ENV_ADDR=$(grep -E '^GONAS_LISTEN_ADDR=' "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2-)
	[ -n "$ENV_ADDR" ] && LISTEN_ADDR="$ENV_ADDR"
fi

log ""
log "=========================================="
log " GoNAS 安裝完成"
log "=========================================="
log " 執行檔:   $BIN_DEST"
log " 設定檔:   $ENV_FILE"
log " 資料目錄: $DATA_DIR"
if [ "${SYSTEMD_OK:-0}" = "1" ]; then
	log " 服務狀態: systemctl status gonas.service"
fi
log " Web 介面: http://<這台機器的IP>${LISTEN_ADDR}"
log " (第一次打開會要求設定管理員帳號)"
log ""
log "接下來建議跑一次依賴檢查,看看這台機器還缺哪些選用工具"
log "(例如 mergerfs、snapraid、samba、rsync、wireguard-tools):"
log ""
log "  $BIN_DEST -check-deps"
log ""
"$BIN_DEST" -check-deps || true
