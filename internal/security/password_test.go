package security

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestPBKDF2HMACSHA256_KnownVectors 對著實際算出來的 PBKDF2-HMAC-SHA256
// 值驗證(用 Python 的 hashlib.pbkdf2_hmac 產生,這是業界通用、獨立於
// GoNAS 自己實作的參考實作),而不是只驗證「這支函式對自己一致」——
// pbkdf2Block 手刻的位元運算(XOR 疊代、大端序區塊索引)很容易寫出一個
// 自成一格但跟規格不符的版本,只靠 Hash→Verify 往返測試完全抓不出這種
// 錯誤,一定要對著外部產生的已知答案比對。
func TestPBKDF2HMACSHA256_KnownVectors(t *testing.T) {
	tests := []struct {
		name       string
		password   string
		salt       string
		iterations int
		keyLen     int
		wantHex    string
	}{
		{
			name:       "RFC 7914 vector 1: passwd/salt, 1 iteration",
			password:   "passwd",
			salt:       "salt",
			iterations: 1,
			keyLen:     64,
			wantHex:    "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc49ca9cccf179b645991664b39d77ef317c71b845b1e30bd509112041d3a19783",
		},
		{
			name:       "RFC 7914 vector 2: Password/NaCl, 80000 iterations",
			password:   "Password",
			salt:       "NaCl",
			iterations: 80000,
			keyLen:     64,
			wantHex:    "4ddcd8f60b98be21830cee5ef22701f9641a4418d04c0414aeff08876b34ab56a1d425a1225833549adb841b51c9b3176a272bdebba1d078478f62b397f33c8d",
		},
		{
			name:       "password/saltsalt, 4096 iterations, 32-byte key",
			password:   "password",
			salt:       "saltsalt",
			iterations: 4096,
			keyLen:     32,
			wantHex:    "662303fd5b832012ccf2b4fd665164726f2f3b8e0f1f68edbe9b07eace29c2f5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pbkdf2HMACSHA256(tt.password, []byte(tt.salt), tt.iterations, tt.keyLen)
			if hex.EncodeToString(got) != tt.wantHex {
				t.Errorf("pbkdf2HMACSHA256(%q, %q, %d, %d) = %x, want %s", tt.password, tt.salt, tt.iterations, tt.keyLen, got, tt.wantHex)
			}
		})
	}
}

func TestHashAndVerifyPassword_RoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	ok, err := VerifyPassword("correct horse battery staple", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword returned error: %v", err)
	}
	if !ok {
		t.Error("expected correct password to verify")
	}
}

func TestVerifyPassword_WrongPasswordFails(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	ok, err := VerifyPassword("wrong password", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword returned error: %v", err)
	}
	if ok {
		t.Error("expected wrong password to fail verification")
	}
}

func TestHashPassword_DifferentSaltEachTime(t *testing.T) {
	a, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	b, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if a == b {
		t.Error("expected two hashes of the same password to differ (random salt), got identical strings")
	}

	// 兩個雜湊字串都要能各自驗證回同一個明文密碼。
	for _, encoded := range []string{a, b} {
		ok, err := VerifyPassword("same password", encoded)
		if err != nil || !ok {
			t.Errorf("VerifyPassword(%q) = %v, %v; want true, nil", encoded, ok, err)
		}
	}
}

func TestHashPassword_EncodesIterationCount(t *testing.T) {
	encoded, err := HashPassword("x")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if !strings.HasPrefix(encoded, "pbkdf2-sha256$210000$") {
		t.Errorf("expected encoded hash to start with algorithm/iteration prefix, got %q", encoded)
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
	}{
		{"empty string", ""},
		{"wrong algorithm tag", "bcrypt$10$abc$def"},
		{"too few fields", "pbkdf2-sha256$210000$abc"},
		{"non-numeric iterations", "pbkdf2-sha256$abc$deadbeef$deadbeef"},
		{"non-hex salt", "pbkdf2-sha256$1000$not-hex!!$deadbeef"},
		{"non-hex hash", "pbkdf2-sha256$1000$deadbeef$not-hex!!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyPassword("anything", tt.encoded)
			if err == nil {
				t.Errorf("expected VerifyPassword to reject malformed hash %q, got nil error", tt.encoded)
			}
		})
	}
}

// TestVerifyPassword_RespectsEncodedIterationCount 確保 VerifyPassword
// 真的是讀雜湊字串裡記的疊代次數,而不是寫死目前的 pbkdf2Iterations 常數
// —— 這樣之後調高預設疊代次數時,舊帳號的密碼不會突然全部驗證失敗。
func TestVerifyPassword_RespectsEncodedIterationCount(t *testing.T) {
	const customIterations = 1000
	salt := []byte("0123456789abcdef")
	key := pbkdf2HMACSHA256("legacy password", salt, customIterations, keyLen)
	encoded := "pbkdf2-sha256$" + "1000" + "$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key)

	ok, err := VerifyPassword("legacy password", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword returned error: %v", err)
	}
	if !ok {
		t.Error("expected a hash encoded with a non-default iteration count to still verify correctly")
	}
}

// DummyVerify 只要能跑完、不 panic 即可(它的用途是拉平登入時序,回傳
// 值本身沒有意義)。
func TestDummyVerify_RunsWithoutPanic(t *testing.T) {
	DummyVerify("anything")
	DummyVerify("")
}
