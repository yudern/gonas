package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// 兩步驟驗證(TOTP)的「救援碼」。第三十輪覆核(資深產品經理)指出:TOTP
// 沒有任何救援路徑,唯一的 admin 一旦弄丟驗證器就永久鎖死。救援碼是業界標準
// 解法 —— 啟用 2FA 時產生一組一次性代碼,使用者抄下來收好;哪天驗證器不見
// 了,可以用其中一組代碼代替 TOTP 碼登入,用掉一組就作廢一組。
//
// 儲存:只存代碼的 SHA-256(hex),永遠不存明文 —— 跟密碼一樣的原則。救援碼
// 本身是高熵的隨機字串(不是使用者自選的低熵密碼),用一般 SHA-256 就足夠
// 抵抗離線暴力(不需要像密碼那樣走慢速 KDF)。

const (
	recoveryCodeCount = 10 // 啟用時產生幾組
	recoveryCodeBytes = 10 // 每組的隨機位元組數(base32 後約 16 字元)
)

// GenerateRecoveryCodes 產生一組全新的救援碼。回傳兩份:plain 是要「只顯示
// 一次」給使用者抄下來的明文(格式化成 xxxxx-xxxxx-xxxxx 方便讀寫),hashed
// 是要存進 state 的 SHA-256(hex)。兩份長度一致、一一對應。
func GenerateRecoveryCodes(n int) (plain []string, hashed []string, err error) {
	if n <= 0 {
		n = recoveryCodeCount
	}
	plain = make([]string, 0, n)
	hashed = make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("generating recovery code: %w", err)
		}
		code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		code = strings.ToLower(code)
		// 每 5 個字元插一個連字號,純粹讓人比較好抄、好唸;比對時會正規化掉。
		plain = append(plain, groupWithDashes(code, 5))
		hashed = append(hashed, HashRecoveryCode(code))
	}
	return plain, hashed, nil
}

func groupWithDashes(s string, size int) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%size == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// normalizeRecoveryCode 把使用者輸入的救援碼正規化:去掉大小寫差異、連字號、
// 空白 —— 這樣使用者抄成 "ABCDE-FGHIJ" 或 "abcde fghij" 都能對上。
func normalizeRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(code) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HashRecoveryCode 回傳一組救援碼正規化之後的 SHA-256(hex)。
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// MatchRecoveryCode 在 hashed 這批已存的救援碼雜湊裡,找出跟使用者輸入 input
// 相符的那一組,回傳它的索引與 true。用 subtle.ConstantTimeCompare 逐一比對,
// 避免比對時間洩漏資訊。沒有相符的回 (-1, false)。呼叫端比對成功後應該把
// 那一組從儲存裡移除(一次性)。
func MatchRecoveryCode(input string, hashed []string) (int, bool) {
	if strings.TrimSpace(input) == "" {
		return -1, false
	}
	want := HashRecoveryCode(input)
	matched := -1
	// 全部走完(不提早 return),讓比對時間不因「第幾組對上」而不同。
	for i, h := range hashed {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			matched = i
		}
	}
	return matched, matched >= 0
}
