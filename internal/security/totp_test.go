package security

import (
	"strings"
	"testing"
	"time"
)

// rfc6238SHA1Secret 是 RFC 6238 附錄 B 官方測試向量指定的 SHA1 測試金鑰:
// ASCII 字串 "12345678901234567890" 的原始位元組(20 bytes),不是 base32
// 編碼過的版本 —— 這裡直接測 hotp() 這個底層函式,繞過 secret 的 base32
// 編碼/解碼,單純驗證 HOTP/TOTP 動態截斷演算法本身對不對。
var rfc6238SHA1Secret = []byte("12345678901234567890")

// TestHOTP_RFC6238OfficialTestVectors 對著 RFC 6238 附錄 B 表格裡 SHA1
// 那幾列官方測試向量驗證,這是規格自己附的答案,不是 GoNAS 自己算出來
// 又拿來驗自己 —— hotp() 的動態截斷(dynamic truncation)步驟位元運算
// 很容易寫錯一個位移或遮罩,只有對著外部已知正確答案比對才抓得出來。
// 注意 RFC 6238 的範例用 8 位數,GoNAS 實際登入用的是 6 位數(totpDigits),
// 這裡刻意呼叫 hotp(..., 8) 而不是套件預設值,才能對得上官方向量。
func TestHOTP_RFC6238OfficialTestVectors(t *testing.T) {
	tests := []struct {
		unixTime int64
		wantCode string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, tt := range tests {
		counter := uint64(tt.unixTime) / 30
		got := hotp(rfc6238SHA1Secret, counter, 8)
		if got != tt.wantCode {
			t.Errorf("hotp(secret, counter=%d, 8) = %q, want %q (unixTime=%d)", counter, got, tt.wantCode, tt.unixTime)
		}
	}
}

func TestGenerateAndValidateCode_RoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}

	now := time.Unix(1_700_000_000, 0)
	code, err := GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode returned error: %v", err)
	}
	if len(code) != totpDigits {
		t.Fatalf("expected a %d-digit code, got %q", totpDigits, code)
	}

	ok, err := ValidateCode(secret, code, now)
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	if !ok {
		t.Error("expected a freshly generated code to validate at the same instant")
	}
}

// TestValidateCodeWithCounter_ReturnsMatchingWindow 固化防重放的核心不變量:
// 當一個碼在時間點 at 當窗合法時,回傳的 counter 必須正好等於 at 的時間窗
// counter(totpCounter(at))——登入路徑就是靠「只接受比上次記下的 counter
// 更新的窗」來擋重放,這個回傳值算錯,防重放就整個失效。
func TestValidateCodeWithCounter_ReturnsMatchingWindow(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	code, err := GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode returned error: %v", err)
	}
	counter, ok, err := ValidateCodeWithCounter(secret, code, now)
	if err != nil {
		t.Fatalf("ValidateCodeWithCounter returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected the freshly generated code to validate")
	}
	if counter != totpCounter(now) {
		t.Fatalf("expected matched counter %d, got %d", totpCounter(now), counter)
	}
	// 一個窗之後(30 秒)算出來的碼,counter 必須正好大 1 —— 這正是「更新的
	// 登入 counter 必須嚴格遞增」所倚賴的性質。
	next := now.Add(totpStep)
	nextCode, _ := GenerateCode(secret, next)
	nextCounter, ok, err := ValidateCodeWithCounter(secret, nextCode, next)
	if err != nil || !ok {
		t.Fatalf("expected next-window code to validate, ok=%v err=%v", ok, err)
	}
	if nextCounter != counter+1 {
		t.Fatalf("expected the next window's counter to be exactly one greater, got %d then %d", counter, nextCounter)
	}
}

func TestValidateCode_WrongCodeFails(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)

	ok, err := ValidateCode(secret, "000000", now)
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	// 極不可能剛好撞到正確答案(1/1,000,000),但為了測試的確定性,
	// 先確認 "000000" 真的不是這個時刻的正確碼。
	realCode, _ := GenerateCode(secret, now)
	if realCode == "000000" {
		t.Skip("test happened to generate 000000 as the real code, skipping")
	}
	if ok {
		t.Error("expected an incorrect code to fail validation")
	}
}

func TestValidateCode_ToleratesClockSkewWithinOneStep(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}
	base := time.Unix(1_700_000_030, 0) // 剛好落在一個 30 秒邊界上

	code, err := GenerateCode(secret, base)
	if err != nil {
		t.Fatalf("GenerateCode returned error: %v", err)
	}

	// 驗證端的時鐘慢了 30 秒(差一個時間窗)應該還是能通過。
	ok, err := ValidateCode(secret, code, base.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	if !ok {
		t.Error("expected a code to still validate one time-step behind (tolerated clock skew)")
	}

	// 驗證端的時鐘快了 30 秒同理。
	ok, err = ValidateCode(secret, code, base.Add(30*time.Second))
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	if !ok {
		t.Error("expected a code to still validate one time-step ahead (tolerated clock skew)")
	}
}

func TestValidateCode_RejectsBeyondSkewWindow(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}
	base := time.Unix(1_700_000_030, 0)

	code, err := GenerateCode(secret, base)
	if err != nil {
		t.Fatalf("GenerateCode returned error: %v", err)
	}

	// 差了 5 個時間窗(150 秒),遠超過 ±1 步的容忍範圍,除非發生
	// 極罕見的碰撞否則應該失敗。
	far := base.Add(5 * 30 * time.Second)
	farCode, _ := GenerateCode(secret, far)
	if farCode == code {
		t.Skip("test happened to collide with a far-future code, skipping")
	}

	ok, err := ValidateCode(secret, code, far)
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	if ok {
		t.Error("expected a code far outside the skew window to fail validation")
	}
}

func TestValidateCode_WrongLengthRejectedWithoutError(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}
	ok, err := ValidateCode(secret, "123", time.Now())
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	if ok {
		t.Error("expected a code of the wrong length to be rejected")
	}
}

func TestValidateCode_InvalidSecretReturnsError(t *testing.T) {
	_, err := ValidateCode("not-valid-base32!!!", "123456", time.Now())
	if err == nil {
		t.Fatal("expected an error for an invalid base32 secret, got nil")
	}
}

func TestValidateCode_NearUnixEpochDoesNotUnderflow(t *testing.T) {
	// totpCounter 在時間起點附近會很小(甚至 0),skew=-1 的減法如果沒有
	// 邊界檢查,uint64 underflow 會繞回一個天文數字的 counter —— 這支測試
	// 確保那個邊界情況至少不會 panic 或回傳一個假的 "true"。
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret returned error: %v", err)
	}
	early := time.Unix(10, 0) // counter = 0

	code, err := GenerateCode(secret, early)
	if err != nil {
		t.Fatalf("GenerateCode returned error: %v", err)
	}
	ok, err := ValidateCode(secret, code, early)
	if err != nil {
		t.Fatalf("ValidateCode returned error: %v", err)
	}
	if !ok {
		t.Error("expected a valid code near the Unix epoch to still validate")
	}
}

func TestProvisioningURI_ContainsExpectedFields(t *testing.T) {
	uri := ProvisioningURI("JBSWY3DPEHPK3PXP", "admin", "GoNAS")
	wantSubstrings := []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=GoNAS",
		"digits=6",
		"period=30",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(uri, want) {
			t.Errorf("expected provisioning URI to contain %q, got %q", want, uri)
		}
	}
}
