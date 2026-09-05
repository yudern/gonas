// Package security 提供 GoNAS Web 管理介面的身分驗證與傳輸層安全:
// 密碼雜湊、TOTP 兩步驟驗證、登入 session、以及 HTTPS 用的自簽憑證產生。
//
// 跟專案裡其他套件一樣零第三方依賴 —— 這個開發環境的網路白名單擋掉了
// Go module proxy(見 internal/docker 套件註解),所以正常會拿
// golang.org/x/crypto 解決的東西(bcrypt/scrypt、TOTP 函式庫)這裡都是
// 直接從標準函式庫的密碼學原語(crypto/hmac、crypto/sha256、crypto/sha1、
// crypto/subtle、crypto/rand、crypto/ecdsa、crypto/x509)手刻,而且刻意
// 選擇「一定拿得到標準函式庫」而不是等一個可能拉得到套件的環境。
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

// pbkdf2Iterations 是 PBKDF2-HMAC-SHA256 的疊代次數。OWASP 密碼儲存指引
// 對 PBKDF2-SHA256 建議至少 600,000 次,但那是假設一個公開、大量帳號、
// 高價值目標的服務;GoNAS 是使用者自己家用 NAS 的單一管理者帳號,
// 210,000 次(OWASP 舊版建議值)已經足夠抵抗離線暴力破解,又不會讓
// 每次登入都要多等將近一秒 —— 這是刻意在安全性與可用性之間抓的平衡點,
// 不是隨便選的數字。
const (
	pbkdf2Iterations = 210_000
	saltBytes        = 16
	keyLen           = 32
)

// HashPassword 把明文密碼雜湊成可以安全存進 state.json 的字串,格式是
// `pbkdf2-sha256$<iterations>$<saltHex>$<hashHex>` —— 把疊代次數编碼進
// 雜湊字串本身,之後要調高疊代次數也不會讓舊帳號的密碼突然驗證失敗
// (VerifyPassword 會讀字串裡記的疊代次數,而不是寫死目前的常數)。
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating password salt: %w", err)
	}
	key := pbkdf2HMACSHA256(password, salt, pbkdf2Iterations, keyLen)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// VerifyPassword 檢查明文密碼是不是跟 HashPassword 產生的編碼字串相符。
// 用 crypto/subtle.ConstantTimeCompare 比對,避免雜湊比對本身的時間差
// 洩漏「差在哪個位元組」這種可以拿來做時序攻擊的資訊。
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false, fmt.Errorf("unrecognized password hash format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false, fmt.Errorf("invalid iteration count in password hash: %q", parts[1])
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false, fmt.Errorf("invalid salt encoding in password hash: %w", err)
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("invalid hash encoding in password hash: %w", err)
	}

	got := pbkdf2HMACSHA256(password, salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// pbkdf2HMACSHA256 是 RFC 8018 PBKDF2 用 HMAC-SHA256 當虛擬亂數函式的
// 最小實作。標準寫法會直接 import golang.org/x/crypto/pbkdf2,但那個
// module proxy 拉不到(見套件開頭註解),而 PBKDF2 演算法本身簡單到
// 用標準函式庫的 hash.Hash 介面手寫大概 20 行就能對著 RFC 完整實作,
// 直接手刻遠比等一個能拉套件的環境合理。
func pbkdf2HMACSHA256(password string, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, []byte(password))
	hLen := prf.Size()
	numBlocks := (keyLen + hLen - 1) / hLen

	dk := make([]byte, 0, numBlocks*hLen)
	for block := 1; block <= numBlocks; block++ {
		dk = append(dk, pbkdf2Block(prf, salt, iterations, block)...)
	}
	return dk[:keyLen]
}

// pbkdf2Block 算出 PBKDF2 的第 blockNum 個輸出區塊:F(P, S, c, i) =
// U_1 XOR U_2 XOR ... XOR U_c,其中 U_1 = PRF(P, S || INT(i)),
// U_j = PRF(P, U_{j-1})。傳入的 prf 已經用密碼當金鑰建構好,這裡只需要
// 每次疊代呼叫 Reset() 重設狀態、換掉要雜湊的內容,不用每個區塊都重新
// 建一個新的 HMAC(省掉重複的金鑰初始化成本)。
func pbkdf2Block(prf hash.Hash, salt []byte, iterations, blockNum int) []byte {
	prf.Reset()
	prf.Write(salt)
	var blockIndex [4]byte
	binary.BigEndian.PutUint32(blockIndex[:], uint32(blockNum))
	prf.Write(blockIndex[:])
	u := prf.Sum(nil)

	result := make([]byte, len(u))
	copy(result, u)

	for i := 1; i < iterations; i++ {
		prf.Reset()
		prf.Write(u)
		u = prf.Sum(nil)
		for j := range result {
			result[j] ^= u[j]
		}
	}
	return result
}
