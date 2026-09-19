package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP(RFC 6238)規格本身指定 HMAC-SHA1,這不是拿來做一般雜湊安全性用途
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	totpStep       = 30 * time.Second
	totpDigits     = 6
	totpSecretSize = 20 // 160-bit,RFC 4226 建議的 HMAC-SHA1 金鑰長度
	totpSkewSteps  = 1  // 允許前後各一個時間窗口的時鐘飄移
)

// GenerateSecret 產生一個新的 TOTP 密鑰,以 base32(無填充字元)編碼,
// 方便使用者手動輸入到 Google Authenticator/Authy 之類的 App —— GoNAS
// 沒有內建產生 QR code(手刻一個可靠的 QR 編碼器超出這個階段的範圍,
// 而且拉不到現成的第三方套件),所以 2FA 設定流程一律走「手動輸入密鑰」
// 這條路,ProvisioningURI 產生的 otpauth:// 字串也是給看得懂的人直接
// 貼進支援這個格式的 App 用。
func GenerateSecret() (string, error) {
	raw := make([]byte, totpSecretSize)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating totp secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// GenerateCode 算出 secret 在時間點 at 當下的 6 位數 TOTP 驗證碼,主要
// 給測試用(產生一個「現在絕對合法」的碼);實際登入驗證一律呼叫
// ValidateCode,因為需要容忍時鐘飄移。
func GenerateCode(secret string, at time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, totpCounter(at), totpDigits), nil
}

// ValidateCode 檢查 code 是不是 secret 在 at 前後 totpSkewSteps 個時間窗
// 之內任何一個時間點算出來的合法驗證碼。允許小幅時鐘飄移是幾乎所有
// TOTP 實作(包含 Google Authenticator 本身)的標準做法 —— 使用者手機
// 時鐘只要慢個幾秒,沒有這個容忍度就永遠登不進去。
func ValidateCode(secret, code string, at time.Time) (bool, error) {
	_, ok, err := ValidateCodeWithCounter(secret, code, at)
	return ok, err
}

// ValidateCodeWithCounter 跟 ValidateCode 一樣驗證 TOTP,但額外回傳「是哪一個
// 時間窗(counter)對上的」。第三十輪覆核(資深安全工程師)指出 TOTP 碼在
// 30 秒窗內可被重放 —— 呼叫端(登入)可以把這個 counter 記下來,拒絕同一個
// (或更舊的)counter 再次被用,消除窗內重放。matched counter 只在 ok 為
// true 時有意義。
func ValidateCodeWithCounter(secret, code string, at time.Time) (uint64, bool, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false, err
	}
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false, nil
	}

	counter := totpCounter(at)
	for skew := -totpSkewSteps; skew <= totpSkewSteps; skew++ {
		c := counter
		switch {
		case skew < 0 && c < uint64(-skew):
			continue // 避免 counter 在時間起點附近時往下算出現 uint64 underflow
		case skew < 0:
			c -= uint64(-skew)
		default:
			c += uint64(skew)
		}
		if hmac.Equal([]byte(hotp(key, c, totpDigits)), []byte(code)) {
			return c, true, nil
		}
	}
	return 0, false, nil
}

// ProvisioningURI 產生標準的 otpauth:// URI,格式跟 Google Authenticator
// 的 Key URI Format 一致,給希望直接貼上而不是逐字手動輸入密鑰的使用者
// (部分驗證器 App 支援用「貼上文字」而非掃描 QR code 的方式加入帳號)。
func ProvisioningURI(secret, accountName, issuer string) string {
	label := fmt.Sprintf("%s:%s", issuer, accountName)
	values := url.Values{}
	values.Set("secret", secret)
	values.Set("issuer", issuer)
	values.Set("algorithm", "SHA1")
	values.Set("digits", strconv.Itoa(totpDigits))
	values.Set("period", strconv.Itoa(int(totpStep.Seconds())))
	return fmt.Sprintf("otpauth://totp/%s?%s", url.PathEscape(label), values.Encode())
}

func totpCounter(at time.Time) uint64 {
	return uint64(at.Unix()) / uint64(totpStep.Seconds())
}

func decodeSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	secret = strings.ReplaceAll(secret, " ", "")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("invalid totp secret encoding: %w", err)
	}
	return key, nil
}

// hotp 是 RFC 4226 定義的 HMAC-based One-Time Password 演算法本身,
// TOTP(RFC 6238)只是把「計數器」換成「目前時間除以步長」。實作照著
// 規格的動態截斷(dynamic truncation)步驟寫,細節有 RFC 6238 附錄 B
// 的官方測試向量驗證(見 totp_test.go),不是憑印象重寫。
func hotp(key []byte, counter uint64, digits int) string {
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(counterBytes[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	truncated := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, truncated%mod)
}
