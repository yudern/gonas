#!/bin/sh
# uninstall.sh 移除 GoNAS。
#
# 預設「只移除程式本身」(執行檔 + systemd unit),保留 /etc/gonas 跟
# /var/lib/gonas —— 這兩個目錄裡分別是設定檔跟狀態(帳號、儲存池/陣列
# 設定、備份工作、WireGuard 金鑰等等)。多數人移除只是想「先解安裝
# 再重裝新版本」或「暫時不用了但可能之後還會裝回來」,保留這些資料
# 才是符合直覺的預設值(跟大部分套件管理器 `remove` vs `purge` 的
# 慣例一致,例如 apt)。
#
# 想要「連設定跟資料一起刪光光」的話,加上 --purge 參數。

set -e

BIN_DEST=/usr/local/bin/gonasd
CONF_DIR=/etc/gonas
DATA_DIR=/var/lib/gonas
SYSTEMD_UNIT_DEST=/etc/systemd/system/gonas.service

PURGE=0
for arg in "$@"; do
	case "$arg" in
		--purge)
			PURGE=1
			;;
		*)
			printf 'uninstall.sh: 未知參數: %s\n' "$arg" >&2
			exit 1
			;;
	esac
done

log() {
	printf '%s\n' "$*"
}

if [ "$(id -u)" -ne 0 ]; then
	printf 'uninstall.sh: 請用 root 執行(例如: sudo ./uninstall.sh)\n' >&2
	exit 1
fi

# ---- 1. 停用/停止 systemd 服務(如果有在跑) ----------------------------
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	if systemctl list-unit-files gonas.service >/dev/null 2>&1; then
		systemctl stop gonas.service >/dev/null 2>&1 || true
		systemctl disable gonas.service >/dev/null 2>&1 || true
		log "已停止並停用 gonas.service"
	fi
else
	log "這台機器沒有以 systemd 作為 PID 1,略過服務停止步驟(如果你是手動啟動 gonasd,請自行用 kill 停止該程序)"
fi

# ---- 2. 移除 systemd unit 檔 -------------------------------------------
if [ -f "$SYSTEMD_UNIT_DEST" ]; then
	rm -f "$SYSTEMD_UNIT_DEST"
	command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload >/dev/null 2>&1 || true
	log "已移除 $SYSTEMD_UNIT_DEST"
fi

# ---- 3. 移除執行檔 ------------------------------------------------------
if [ -f "$BIN_DEST" ]; then
	rm -f "$BIN_DEST"
	log "已移除執行檔 $BIN_DEST"
else
	log "找不到 $BIN_DEST(可能已經移除過了)"
fi

# ---- 4. 設定/資料目錄:預設保留,--purge 才刪 ---------------------------
if [ "$PURGE" = "1" ]; then
	log ""
	log "!! --purge 會刪除以下目錄,包含所有帳號/儲存池設定/備份工作/WireGuard 金鑰:"
	log "     $CONF_DIR"
	log "     $DATA_DIR"
	printf "確定要刪除嗎?輸入 yes 確認: "
	read -r CONFIRM
	if [ "$CONFIRM" = "yes" ]; then
		rm -rf "$CONF_DIR" "$DATA_DIR"
		log "已刪除 $CONF_DIR 與 $DATA_DIR"
	else
		log "已取消,設定與資料目錄保留不動"
	fi
else
	log ""
	log "已保留設定目錄 $CONF_DIR 與資料目錄 $DATA_DIR(裡面有帳號/儲存池/備份等設定)"
	log "如果要連同這些一起刪除,重新執行: sudo ./uninstall.sh --purge"
fi

log ""
log "GoNAS 已移除。"
