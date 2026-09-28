// Package main(cmd/gonas-sign)是 GoNAS 自我更新的「簽章工具」——產生金鑰、
// 對更新 manifest 簽章、驗證簽章、算 asset 的 SHA256。純標準函式庫,跟 gonasd
// 本體一樣零第三方依賴。
//
// 為什麼需要它:gonasd 內建了「驗證 manifest 數位簽章」的能力(見
// internal/selfupdate 的 ManifestPublicKeyHex / verifyManifestSignature),
// 但那是「驗證端」。要真的用起來,還需要「簽章端」的工具:產生一對 ed25519
// 金鑰、把公鑰在編譯時嵌進 gonasd、每次發佈更新時用私鑰對 manifest 簽名產出
// detached 的 .sig 檔。這支工具就是那個簽章端,設計上跟 gonasd 的驗證行為
// 「位元對位元」一致:
//
//   - 私鑰檔:64-byte ed25519 私鑰的 base64(mode 0600)。
//   - 公鑰:32-byte 的 hex(64 個 hex 字元),就是要餵給 Makefile 的
//     MANIFEST_PUBKEY / ldflags 的那個值。
//   - 簽章檔:對「manifest 原始位元組」簽出來的 ed25519 簽章的 base64,存成
//     <manifest>.sig,對應 gonasd 抓 <manifestURL>.sig 來驗的慣例。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// generateKeypair 產生一對新的 ed25519 金鑰,回傳私鑰(64 bytes)與公鑰 hex。
func generateKeypair() (priv ed25519.PrivateKey, pubHex string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generating ed25519 key: %w", err)
	}
	return priv, hex.EncodeToString(pub), nil
}

// encodePrivateKey / decodePrivateKey 是私鑰檔的存/取格式(base64 的 64-byte
// 私鑰),集中在這裡讓 keygen 寫的跟 sign 讀的一定對得起來。
func encodePrivateKey(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv)
}

func decodePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("private key file is not valid base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key must be %d bytes, got %d — is this a gonas-sign private key file?", ed25519.PrivateKeySize, len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

// publicKeyHexFromPrivate 從私鑰導出公鑰 hex(gonasd 要嵌的那個值)。
func publicKeyHexFromPrivate(priv ed25519.PrivateKey) string {
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub)
}

// signManifest 對 manifest 原始位元組簽名,回傳簽章的 base64(就是要寫進
// <manifest>.sig 的內容)。跟 gonasd 驗證端一樣:簽的是「整份檔案的位元組」,
// 不是解析後的 JSON——所以簽完就不能再改動 manifest 檔的任何一個 byte
// (連空白、換行都不行),否則驗不過。
func signManifest(priv ed25519.PrivateKey, manifest []byte) string {
	sig := ed25519.Sign(priv, manifest)
	return base64.StdEncoding.EncodeToString(sig)
}

// verifyManifest 是 gonasd 驗證邏輯的鏡像,給工具的 verify 子指令與測試用:
// 用公鑰 hex 驗證 sigBase64 是不是蓋在 manifest 上的有效簽章。
func verifyManifest(pubHex string, manifest []byte, sigBase64 string) error {
	pub, err := hex.DecodeString(strings.TrimSpace(pubHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key must be a %d-byte hex string (%d hex chars)", ed25519.PublicKeySize, ed25519.PublicKeySize*2)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigBase64))
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), manifest, sig) {
		return errors.New("signature verification FAILED (wrong key, or the manifest was modified after signing)")
	}
	return nil
}

// sha256Hex 算一份檔案內容的 SHA256 hex——manifest 裡每個 asset 的 sha256
// 欄位就是填這個值(gonasd 下載後會比對)。
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeFileMode 是把內容寫檔的小包裝,私鑰用 0600、其餘用 0644。
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}
