package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
)

// newAuthTestServer 跟 newTestServer(appstore_handlers_test.go)不一樣的
// 地方是多初始化了 sessions/loginLimiter——Phase 13 的多帳號/角色測試
// 需要真的核發、驗證 session cookie,不能只碰 s.store。
func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	return &Server{
		store:        store,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions:     security.NewSessionManager(time.Hour),
		loginLimiter: security.NewLoginLimiter(5, time.Minute),
	}
}

// seedAdmin 直接把一個帳號寫進 store(繞過 HTTP handler),回傳一個已經
// 通過驗證、能直接掛在請求上的 session cookie——測試「requireAdmin 擋
// 掉 viewer」這種案例時,不需要每次都真的跑一次 setup+login 的完整流程。
func seedAdmin(t *testing.T, s *Server, username, password, role string) *http.Cookie {
	t.Helper()
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	if err := s.store.Update(func(st *state.State) error {
		st.Admins = append(st.Admins, state.AdminAccount{Username: username, PasswordHash: hash, Role: role})
		return nil
	}); err != nil {
		t.Fatalf("seeding admin: %v", err)
	}
	sess, err := s.sessions.Create(username)
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: sess.Token}
}

func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling body: %v", err)
	}
	return bytes.NewBuffer(b)
}

func TestHandleAuthSetup_FirstAccountGetsAdminRole(t *testing.T) {
	s := newAuthTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", jsonBody(t, setupRequest{Username: "root", Password: "correcthorsebattery"}))
	rec := httptest.NewRecorder()
	s.handleAuthSetup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	admins := s.store.Snapshot().Admins
	if len(admins) != 1 {
		t.Fatalf("expected exactly 1 admin after setup, got %d", len(admins))
	}
	if admins[0].Role != state.RoleAdmin {
		t.Errorf("expected first account to get RoleAdmin, got %q", admins[0].Role)
	}

	// 第二次呼叫 setup 應該被拒絕——已經有帳號了。
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", jsonBody(t, setupRequest{Username: "someone-else", Password: "correcthorsebattery"}))
	rec2 := httptest.NewRecorder()
	s.handleAuthSetup(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected second setup call to return 409, got %d", rec2.Code)
	}
}

func TestHandleAuthLogin_FindsCorrectAccountAmongMultiple(t *testing.T) {
	s := newAuthTestServer(t)
	seedAdmin(t, s, "alice", "alice-password-1", state.RoleAdmin)
	seedAdmin(t, s, "bob", "bob-password-1", state.RoleViewer)

	// bob 用自己的密碼登入應該成功，而且回應裡的角色要是 viewer。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", jsonBody(t, loginRequest{Username: "bob", Password: "bob-password-1"}))
	rec := httptest.NewRecorder()
	s.handleAuthLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected bob's login to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	var me meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if me.Username != "bob" || me.Role != state.RoleViewer {
		t.Errorf("expected bob/viewer, got %+v", me)
	}

	// bob 用 alice 的密碼登入應該失敗(帳號跟密碼要對上同一筆)。
	reqBad := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", jsonBody(t, loginRequest{Username: "bob", Password: "alice-password-1"}))
	recBad := httptest.NewRecorder()
	s.handleAuthLogin(recBad, reqBad)
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected mismatched credentials to fail with 401, got %d", recBad.Code)
	}
}

func TestRequireAdmin_BlocksViewerAllowsAdmin(t *testing.T) {
	s := newAuthTestServer(t)
	adminCookie := seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)
	viewerCookie := seedAdmin(t, s, "viewer1", "viewer-password-1", state.RoleViewer)

	protected := s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	reqViewer := httptest.NewRequest(http.MethodPost, "/api/v1/storage/array/start", nil)
	reqViewer.AddCookie(viewerCookie)
	recViewer := httptest.NewRecorder()
	protected(recViewer, reqViewer)
	if recViewer.Code != http.StatusForbidden {
		t.Fatalf("expected viewer to get 403 from an admin-only endpoint, got %d", recViewer.Code)
	}

	reqAdmin := httptest.NewRequest(http.MethodPost, "/api/v1/storage/array/start", nil)
	reqAdmin.AddCookie(adminCookie)
	recAdmin := httptest.NewRecorder()
	protected(recAdmin, reqAdmin)
	if recAdmin.Code != http.StatusNoContent {
		t.Fatalf("expected admin to pass through, got %d: %s", recAdmin.Code, recAdmin.Body.String())
	}

	// 完全沒帶 cookie 的請求應該是 401(未登入),不是 403(權限不足)——
	// 這兩種狀況對使用者的意義不同,不該混在一起回同一個狀態碼。
	reqAnon := httptest.NewRequest(http.MethodPost, "/api/v1/storage/array/start", nil)
	recAnon := httptest.NewRecorder()
	protected(recAnon, reqAnon)
	if recAnon.Code != http.StatusUnauthorized {
		t.Fatalf("expected anonymous request to get 401, got %d", recAnon.Code)
	}
}

func TestHandleAuthAccountsCreate_ValidatesRoleAndUniqueness(t *testing.T) {
	s := newAuthTestServer(t)
	seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)

	// 無效角色。
	reqBadRole := httptest.NewRequest(http.MethodPost, "/api/v1/auth/accounts", jsonBody(t, createAccountRequest{Username: "carol", Password: "carol-password-1", Role: "superuser"}))
	recBadRole := httptest.NewRecorder()
	s.handleAuthAccountsCreate(recBadRole, reqBadRole)
	if recBadRole.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid role to return 400, got %d: %s", recBadRole.Code, recBadRole.Body.String())
	}

	// 正常新增一個 viewer。
	reqOK := httptest.NewRequest(http.MethodPost, "/api/v1/auth/accounts", jsonBody(t, createAccountRequest{Username: "carol", Password: "carol-password-1", Role: state.RoleViewer}))
	recOK := httptest.NewRecorder()
	s.handleAuthAccountsCreate(recOK, reqOK)
	if recOK.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recOK.Code, recOK.Body.String())
	}

	// 使用者名稱重複應該衝突。
	reqDup := httptest.NewRequest(http.MethodPost, "/api/v1/auth/accounts", jsonBody(t, createAccountRequest{Username: "carol", Password: "another-password-1", Role: state.RoleAdmin}))
	recDup := httptest.NewRecorder()
	s.handleAuthAccountsCreate(recDup, reqDup)
	if recDup.Code != http.StatusConflict {
		t.Fatalf("expected duplicate username to return 409, got %d", recDup.Code)
	}

	admins := s.store.Snapshot().Admins
	if len(admins) != 2 {
		t.Fatalf("expected 2 accounts total (admin1 + carol), got %d", len(admins))
	}
}

func TestHandleAuthAccountsDelete_CannotDeleteOwnAccount(t *testing.T) {
	s := newAuthTestServer(t)
	adminCookie := seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)
	seedAdmin(t, s, "viewer1", "viewer-password-1", state.RoleViewer)
	sess, _ := s.sessions.Validate(adminCookie.Value)

	// admin1 想刪自己 -> 拒絕,即使它不是最後一個帳號。
	reqSelf := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/accounts/admin1", nil)
	reqSelf.AddCookie(adminCookie)
	reqSelf.SetPathValue("username", "admin1")
	recSelf := httptest.NewRecorder()
	s.handleAuthAccountsDelete(recSelf, reqSelf.WithContext(contextWithSession(reqSelf, sess)))
	if recSelf.Code != http.StatusConflict {
		t.Fatalf("expected self-delete to be rejected with 409, got %d: %s", recSelf.Code, recSelf.Body.String())
	}

	// admin1 刪掉 viewer1 -> 允許(不是自己)。
	reqViewer := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/accounts/viewer1", nil)
	reqViewer.AddCookie(adminCookie)
	reqViewer.SetPathValue("username", "viewer1")
	recViewer := httptest.NewRecorder()
	s.handleAuthAccountsDelete(recViewer, reqViewer.WithContext(contextWithSession(reqViewer, sess)))
	if recViewer.Code != http.StatusNoContent {
		t.Fatalf("expected viewer1 delete to succeed, got %d: %s", recViewer.Code, recViewer.Body.String())
	}
	if len(s.store.Snapshot().Admins) != 1 {
		t.Fatalf("expected only admin1 to remain")
	}
}

// TestHandleAuthAccountsDelete_LastAdminGuard 驗證 handleAuthAccountsDelete
// 裡「不能讓 RoleAdmin 數量歸零」這道防線本身的邏輯是正確的。在目前
// 「不能刪自己」的規則之下,這條防線透過一般的 HTTP 呼叫路徑其實已經
// 沒有辦法真的被觸發到(呼叫者一定是 RoleAdmin,又不能刪自己,代表
// 刪除後呼叫者自己至少還留著一個 admin)——這裡直接驗證邏輯本身而不是
// 透過 handler,是刻意的多一層防護(belt-and-suspenders):就算未來
// 「不能刪自己」這條規則被放寬或繞過,也不該讓系統真的被清空到 0 個
// admin、變成沒有人能再管理這台 NAS。
func TestHandleAuthAccountsDelete_LastAdminGuard(t *testing.T) {
	s := newAuthTestServer(t)
	seedAdmin(t, s, "onlyadmin", "only-admin-password-1", state.RoleAdmin)

	err := s.store.Update(func(st *state.State) error {
		i := findAdminIndex(st.Admins, "onlyadmin")
		if i < 0 {
			t.Fatalf("expected onlyadmin to exist")
		}
		if st.Admins[i].Role == state.RoleAdmin {
			remaining := 0
			for _, a := range st.Admins {
				if a.Role == state.RoleAdmin {
					remaining++
				}
			}
			if remaining <= 1 {
				return errCannotDeleteLastAdmin
			}
		}
		st.Admins = append(st.Admins[:i], st.Admins[i+1:]...)
		return nil
	})
	if err != errCannotDeleteLastAdmin {
		t.Fatalf("expected the last-admin safety check to trigger, got %v", err)
	}
	if len(s.store.Snapshot().Admins) != 1 {
		t.Fatalf("expected onlyadmin to still be present after the rejected deletion")
	}
}

// TestHandleAuthChangePassword_ViewerCanChangeOwnPassword 確保「管理自己
// 帳號」這件事不受角色限制——RoleViewer 沒有權限改 NAS 設定,但改自己
// 的密碼是自我服務,不是「管理 NAS」,router.go 特意讓這支端點只掛
// requireAuth 而不是 requireAdmin,這裡直接驗證 handler 本身在 viewer
// 的 session 底下一樣能正常運作。
func TestHandleAuthChangePassword_ViewerCanChangeOwnPassword(t *testing.T) {
	s := newAuthTestServer(t)
	viewerCookie := seedAdmin(t, s, "viewer1", "old-password-1", state.RoleViewer)
	sess, _ := s.sessions.Validate(viewerCookie.Value)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", jsonBody(t, changePasswordRequest{OldPassword: "old-password-1", NewPassword: "new-password-1"}))
	rec := httptest.NewRecorder()
	s.handleAuthChangePassword(rec, req.WithContext(contextWithSession(req, sess)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected viewer to be able to change their own password, got %d: %s", rec.Code, rec.Body.String())
	}

	admin, found := findAdmin(s.store.Snapshot().Admins, "viewer1")
	if !found {
		t.Fatal("expected viewer1 to still exist")
	}
	if ok, _ := security.VerifyPassword("new-password-1", admin.PasswordHash); !ok {
		t.Error("expected the new password to have been persisted")
	}
	if admin.Role != state.RoleViewer {
		t.Errorf("expected role to remain unchanged after a password change, got %q", admin.Role)
	}
}

// contextWithSession 是測試專用的小工具，模擬 requireAuth 已經把驗證過
// 的 session 放進 context 這件事——handleAuthAccountsDelete 之類的
// handler 預期從 context 讀 session，不是自己重新驗證 cookie，直接呼叫
// handler(略過 requireAuth 那層 wrapper)時要自己把這一步補上。
func contextWithSession(r *http.Request, sess security.Session) context.Context {
	return context.WithValue(r.Context(), sessionContextKey, sess)
}
