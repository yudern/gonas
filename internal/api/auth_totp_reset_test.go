package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/state"
)

// 第三十輪覆核新增:admin 可以重設「別人」的 2FA(lockout 救援),但不能用
// 這個免密碼動作重設自己的。
func TestHandleAuthAccountsResetTOTP(t *testing.T) {
	s := newAuthTestServer(t)
	adminCookie := seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)
	seedAdmin(t, s, "victim", "victim-password-1", state.RoleViewer)
	sess, _ := s.sessions.Validate(adminCookie.Value)

	// 給 victim 啟用 TOTP + 一組救援碼
	if err := s.store.Update(func(st *state.State) error {
		i := findAdminIndex(st.Admins, "victim")
		st.Admins[i].TOTPEnabled = true
		st.Admins[i].TOTPSecret = "SECRET"
		st.Admins[i].RecoveryCodes = []string{"deadbeef"}
		st.Admins[i].LastTOTPCounter = 42
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// admin1 重設自己 -> 409
	reqSelf := httptest.NewRequest(http.MethodPost, "/", nil)
	reqSelf.SetPathValue("username", "admin1")
	recSelf := httptest.NewRecorder()
	s.handleAuthAccountsResetTOTP(recSelf, reqSelf.WithContext(contextWithSession(reqSelf, sess)))
	if recSelf.Code != http.StatusConflict {
		t.Fatalf("expected 409 resetting own 2FA, got %d: %s", recSelf.Code, recSelf.Body.String())
	}

	// admin1 重設 victim -> 204,且 victim 的 2FA 全清掉
	reqV := httptest.NewRequest(http.MethodPost, "/", nil)
	reqV.SetPathValue("username", "victim")
	recV := httptest.NewRecorder()
	s.handleAuthAccountsResetTOTP(recV, reqV.WithContext(contextWithSession(reqV, sess)))
	if recV.Code != http.StatusNoContent {
		t.Fatalf("expected 204 resetting victim 2FA, got %d: %s", recV.Code, recV.Body.String())
	}
	victim, _ := findAdmin(s.store.Snapshot().Admins, "victim")
	if victim.TOTPEnabled || victim.TOTPSecret != "" || len(victim.RecoveryCodes) != 0 || victim.LastTOTPCounter != 0 {
		t.Fatalf("victim 2FA not fully cleared: %+v", victim)
	}
}
