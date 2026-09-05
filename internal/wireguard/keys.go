// Package wireguard 產生 WireGuard 金鑰、組出 wg-quick 看得懂的設定檔,
// 並透過 wg-quick/wg 這兩個外部指令套用/查詢介面狀態。
//
// 跟 GoNAS 其他套件一樣零第三方依賴:這個開發沙盒的網路白名單擋掉了
// Go module proxy(見 internal/docker 套件註解),沒有安裝
// wireguard-tools(`wg`/`wg-quick` 都找不到)。金鑰產生的部分完全不受
// 影響 —— WireGuard 的金鑰就是原始的 Curve25519(X25519)金鑰,Go 1.20
// 起標準函式庫的 crypto/ecdh 套件內建 X25519 曲線,不需要
// golang.org/x/crypto/curve25519 也能產生格式完全相容的金鑰。套用/查詢
// 介面狀態這半部分,因為這台機器沒裝 wireguard-tools,走的是跟 Phase 3
// Samba/NFS 一樣的模式:設定檔產生邏輯有完整單元測試覆蓋,套用動作走
// cmdrunner.Runner 抽象並對假的 Runner 驗證指令組裝正確,實機上有沒有
// 裝 wg-quick 交給呼叫端(internal/api)做「優雅降級、只回警告不擋
// 存檔」的軟失敗處理,詳見 internal/share 的既有模式。
package wireguard

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateKeyPair 產生一組 WireGuard 相容的金鑰對,以標準 base64 編碼
// ——這正是 `wg genkey`/`wg pubkey` 跟 WireGuard 設定檔使用的格式
// (32 bytes 原始金鑰位元組的 base64,不是 hex,也不用 URL-safe 變體)。
func GenerateKeyPair() (privateKeyBase64, publicKeyBase64 string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating wireguard key pair: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key.Bytes()),
		base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// PublicKeyFromPrivate 從一把已經存在的 WireGuard 私鑰(base64 編碼)
// 算出對應的公鑰。使用者從別的系統/裝置匯入既有私鑰時用得到,不需要
// 為了拿到公鑰而整組重新產生一次(那樣會作廢原本已經在用的私鑰)。
func PublicKeyFromPrivate(privateKeyBase64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(privateKeyBase64)
	if err != nil {
		return "", fmt.Errorf("decoding wireguard private key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("invalid wireguard private key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}
