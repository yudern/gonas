package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
)

// seedTOTPAdmin 在 store 裡建一個「已啟用 2FA」的帳號:密碼、TOTP 密鑰、
// (可選)一組已雜湊的救援碼。回傳 TOTP 密鑰(給測試算合法碼用)跟那組
// 救援碼的明文(給測試拿去登入用,可能是空的)。
func seedTOTPAdmin(t *testing.T, s *Server, username, password string) (secret string, recoveryPlain []string) {
	t.Helper()
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	secret, err = security.GenerateSecret()
	if err != nil {
		t.Fatalf("generating totp secret: %v", err)
	}
	plain, hashed, err := security.GenerateRecoveryCodes(0)
	if err != nil {
		t.Fatalf("generating recovery codes: %v", err)
	}
	if err := s.store.Update(func(st *state.State) error {
		st.Admins = append(st.Admins, state.AdminAccount{
			Username:      username,
			PasswordHash:  hash,
			Role:          state.RoleAdmin,
			TOTPEnabled:   true,
			TOTPSecret:    secret,
			RecoveryCodes: hashed,
		})
		return nil
	}); err != nil {
		t.Fatalf("seeding totp admin: %v", err)
	}
	return secret, plain
}

func loginWithTOTP(s *Server, username, password, code string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		jsonBodyRaw(loginRequest{Username: username, Password: password, TOTPCode: code}))
	s.handleAuthLogin(rec, req)
	return rec
}

// jsonBodyRaw 是 jsonBody 的無 *testing.T 版本(這個檔案的 helper 常在
// 迴圈裡呼叫,不想每次都傳 t)。
func jsonBodyRaw(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// TestHandleAuthLogin_TOTPSuccessPersistsCounter:帶著合法 TOTP 碼登入成功,
// 並且把用到的時間窗 counter 持久化(之後才擋得住重放)。
func TestHandleAuthLogin_TOTPSuccessPersistsCounter(t *testing.T) {
	s := newAuthTestServer(t)
	secret, _ := seedTOTPAdmin(t, s, "alice", "alice-password-1")

	code, err := security.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generating code: %v", err)
	}
	rec := loginWithTOTP(s, "alice", "alice-password-1", code)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with a valid TOTP code, got %d: %s", rec.Code, rec.Body.String())
	}
	admin, _ := findAdmin(s.store.Snapshot().Admins, "alice")
	if admin.LastTOTPCounter == 0 {
		t.Fatalf("expected LastTOTPCounter to be persisted after a successful TOTP login")
	}
}

// TestHandleAuthLogin_TOTPReplayRejected:同一個 TOTP 碼在 30 秒窗內被重放
// (第二次登入)應該被拒絕——counter 不比上次新。
func TestHandleAuthLogin_TOTPReplayRejected(t *testing.T) {
	s := newAuthTestServer(t)
	secret, _ := seedTOTPAdmin(t, s, "alice", "alice-password-1")

	code, err := security.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generating code: %v", err)
	}
	if rec := loginWithTOTP(s, "alice", "alice-password-1", code); rec.Code != http.StatusOK {
		t.Fatalf("first login should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	// 立刻用同一個碼再登入一次 —— 重放,必須被擋。
	rec := loginWithTOTP(s, "alice", "alice-password-1", code)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected replayed TOTP code to be rejected with 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAuthLogin_RecoveryCodeConsumedOnce:用救援碼登入成功一次,同一組
// 碼第二次就必須失效(一次性)。
func TestHandleAuthLogin_RecoveryCodeConsumedOnce(t *testing.T) {
	s := newAuthTestServer(t)
	_, recovery := seedTOTPAdmin(t, s, "alice", "alice-password-1")
	if len(recovery) == 0 {
		t.Fatal("expected at least one recovery code")
	}
	one := recovery[0]

	if rec := loginWithTOTP(s, "alice", "alice-password-1", one); rec.Code != http.StatusOK {
		t.Fatalf("first recovery-code login should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	// 同一組救援碼再用一次 —— 已經被消耗掉,必須失敗。
	rec := loginWithTOTP(s, "alice", "alice-password-1", one)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected a consumed recovery code to be rejected with 401, got %d: %s", rec.Code, rec.Body.String())
	}
	// 而且它應該真的從持久化狀態裡少了一組(從 10 變 9)。
	admin, _ := findAdmin(s.store.Snapshot().Admins, "alice")
	if len(admin.RecoveryCodes) != len(recovery)-1 {
		t.Fatalf("expected one recovery code to be consumed (%d left), got %d", len(recovery)-1, len(admin.RecoveryCodes))
	}
}

// TestHandleAuthLogin_WrongTOTPRecordsFailureAndLocks:連續用錯的 TOTP 碼
// 會被記為登入失敗,達到門檻後整個登入被節流(429),不是無限重試。
func TestHandleAuthLogin_WrongTOTPRecordsFailureAndLocks(t *testing.T) {
	s := newAuthTestServer(t) // loginLimiter(5, time.Minute)
	seedTOTPAdmin(t, s, "alice", "alice-password-1")

	// 密碼對、但 TOTP 碼是亂填的 6 位數(既不是合法 TOTP、也不是救援碼)。
	for i := 0; i < 5; i++ {
		rec := loginWithTOTP(s, "alice", "alice-password-1", "000000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401 for a wrong TOTP code, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	// 第 6 次應該被節流器擋在門口(429),證明錯誤的 TOTP 也算失敗次數。
	rec := loginWithTOTP(s, "alice", "alice-password-1", "000000")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding the failure threshold, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAuthTOTPRecoveryCodes_RequiresPassword(第五十二輪 S-4):重新
// 產生救援碼是敏感動作,要求重新輸入密碼——錯的/沒帶密碼一律拒絕,對的
// 才會換發一組跟原本不一樣的新碼。
func TestHandleAuthTOTPRecoveryCodes_RequiresPassword(t *testing.T) {
	s := newAuthTestServer(t)
	seedTOTPAdmin(t, s, "alice", "alice-password-1")
	sess, err := s.sessions.Create("alice")
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}
	before, _ := findAdmin(s.store.Snapshot().Admins, "alice")

	// 錯密碼 -> 401,且救援碼不變。
	reqBad := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/recovery-codes",
		jsonBodyRaw(totpDisableRequest{Password: "wrong-password"}))
	recBad := httptest.NewRecorder()
	s.handleAuthTOTPRecoveryCodes(recBad, reqBad.WithContext(contextWithSession(reqBad, sess)))
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 regenerating recovery codes with a wrong password, got %d: %s", recBad.Code, recBad.Body.String())
	}
	after, _ := findAdmin(s.store.Snapshot().Admins, "alice")
	if !sameStringSlice(before.RecoveryCodes, after.RecoveryCodes) {
		t.Fatalf("recovery codes must NOT change when the password is wrong")
	}

	// 對密碼 -> 200,回一組新的明文,且持久化的雜湊也換了。
	reqOK := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/recovery-codes",
		jsonBodyRaw(totpDisableRequest{Password: "alice-password-1"}))
	recOK := httptest.NewRecorder()
	s.handleAuthTOTPRecoveryCodes(recOK, reqOK.WithContext(contextWithSession(reqOK, sess)))
	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200 regenerating recovery codes with the correct password, got %d: %s", recOK.Code, recOK.Body.String())
	}
	var body struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(recOK.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.RecoveryCodes) == 0 {
		t.Fatalf("expected a fresh set of recovery codes in the response")
	}
	afterOK, _ := findAdmin(s.store.Snapshot().Admins, "alice")
	if sameStringSlice(before.RecoveryCodes, afterOK.RecoveryCodes) {
		t.Fatalf("recovery codes should have been replaced after a successful regenerate")
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
