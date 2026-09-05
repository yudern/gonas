package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
)

// sessionCookieName 是登入 session 用的 Cookie 名稱。sessionTTL 是每次
// 登入核發的 session 存活時間 —— 24 小時對一個家用 NAS 管理介面是合理
// 的預設值,太短會讓人一直要重新登入,太長又跟「session 到期後強制
// 重新驗證」這個安全機制的意義相違背。
const (
	sessionCookieName = "gonas_session"
	sessionTTL        = 24 * time.Hour
	minPasswordLength = 8
)

type contextKey string

const sessionContextKey contextKey = "gonas-session"

// requireAuth 包住一個 handler,要求請求帶著一個還沒過期的合法 session
// Cookie 才能繼續。刻意用「包一層」而不是在每支 handler 裡各自檢查,
// 是為了讓「這支端點要不要驗證」在 router.go 註冊路由的那一行就能一眼
// 看出來,不用打開每支 handler 的原始碼才知道。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errNotAuthenticated)
			return
		}
		sess, ok := s.sessions.Validate(cookie.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, errNotAuthenticated)
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey, sess)
		next(w, r.WithContext(ctx))
	}
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil, // 走 HTTPS 連線時才加上 Secure,避免同一份程式碼在純 HTTP 部署時讓 Cookie 完全送不出去
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

type authStatusResponse struct {
	SetupRequired bool `json:"setupRequired"`
}

// handleAuthStatus 是唯一一個「連要不要顯示登入頁面都還不知道」時就能呼叫
// 的端點:告訴前端目前是全新安裝(還沒建立管理者帳號,應該顯示初始設定
// 畫面)還是已經有帳號了(應該顯示一般登入表單)。
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, authStatusResponse{SetupRequired: s.store.Snapshot().Admin == nil})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleAuthSetup 建立唯一的管理者帳號,只有在完全還沒有帳號的情況下
// 才會成功 —— 這道檢查是刻意的安全邊界:不這樣的話,任何能連到 gonasd
// 的人都可以呼叫這支端點「重設」管理者帳號跟密碼,等於整個身分驗證
// 形同虛設。成功之後直接核發 session,讓使用者不用設定完馬上又要
// 手動登入一次。
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.store.Snapshot().Admin != nil {
		writeError(w, http.StatusConflict, errAdminAlreadyConfigured)
		return
	}

	var req setupRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, errUsernameRequired)
		return
	}
	if len(req.Password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, errPasswordTooShort)
		return
	}

	hash, err := security.HashPassword(req.Password)
	if err != nil {
		s.logger.Error("hashing admin password failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	admin := &state.AdminAccount{Username: req.Username, PasswordHash: hash}
	if err := s.store.Update(func(st *state.State) error {
		st.Admin = admin
		return nil
	}); err != nil {
		s.logger.Error("persisting admin account failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.loginSession(w, r, req.Username)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTPCode string `json:"totpCode,omitempty"`
}

// clientIP 從 r.RemoteAddr 取出不含連接埠的來源位址,當作登入節流的
// key。RemoteAddr 一般是 "host:port" 的形式,但也可能是沒有埠號的裸
// 位址(例如某些測試/代理情境),SplitHostPort 失敗時就直接把整個
// RemoteAddr 當 key 用 —— 節流的目的是「同一個來源打太多次就擋一下」,
// 不是要做精確的身分識別,退化成用整串 RemoteAddr 當 key 一樣能達到
// 這個目的,只是萬一背後真的接了會變換來源埠的代理,節流的粒度會變成
// 「整個代理」而不是「代理後面的個別使用者」——這在 GoNAS 典型的區網
// 部署情境下不是問題。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if allowed, wait := s.loginLimiter.Allow(ip); !allowed {
		// Retry-After 用整數秒,無條件進位 —— 寧可讓使用者多等一點點,
		// 也不要因為無條件捨去讓前端算出「已經可以重試了」但伺服器這邊
		// 其實還沒解鎖,導致又白白吃一次 429。
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+1)))
		writeError(w, http.StatusTooManyRequests, errTooManyLoginAttempts)
		return
	}

	var req loginRequest
	if !readJSON(w, r, &req) {
		return
	}

	admin := s.store.Snapshot().Admin
	if admin == nil {
		writeError(w, http.StatusConflict, errAdminNotConfigured)
		return
	}
	// 用固定訊息一律回「帳號或密碼錯誤」,不區分「使用者名稱不對」跟
	// 「密碼不對」——避免讓攻擊者靠著錯誤訊息內容枚舉出哪些使用者名稱
	// 存在(雖然 GoNAS 只有一個管理者帳號,這裡養成的習慣在之後真的
	// 支援多帳號時也不用改)。
	if req.Username != admin.Username {
		s.loginLimiter.RecordFailure(ip)
		writeError(w, http.StatusUnauthorized, errInvalidCredentials)
		return
	}
	ok, err := security.VerifyPassword(req.Password, admin.PasswordHash)
	if err != nil {
		s.logger.Error("verifying admin password failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		s.loginLimiter.RecordFailure(ip)
		writeError(w, http.StatusUnauthorized, errInvalidCredentials)
		return
	}

	if admin.TOTPEnabled {
		valid, err := security.ValidateCode(admin.TOTPSecret, req.TOTPCode, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !valid {
			s.loginLimiter.RecordFailure(ip)
			writeError(w, http.StatusUnauthorized, errInvalidTOTPCode)
			return
		}
	}

	s.loginLimiter.RecordSuccess(ip)
	s.loginSession(w, r, admin.Username)
}

// loginSession 是 setup 跟 login 共用的「核發一個新 session、設定
// Cookie、回應目前登入者資訊」邏輯。
func (s *Server) loginSession(w http.ResponseWriter, r *http.Request, username string) {
	sess, err := s.sessions.Create(username)
	if err != nil {
		s.logger.Error("creating session failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	setSessionCookie(w, r, sess.Token, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, meResponse{Username: username, TOTPEnabled: s.currentAdminTOTPEnabled()})
}

func (s *Server) currentAdminTOTPEnabled() bool {
	admin := s.store.Snapshot().Admin
	return admin != nil && admin.TOTPEnabled
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.Revoke(cookie.Value)
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

type meResponse struct {
	Username    string `json:"username"`
	TOTPEnabled bool   `json:"totpEnabled"`
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)
	writeJSON(w, http.StatusOK, meResponse{Username: sess.Username, TOTPEnabled: s.currentAdminTOTPEnabled()})
}

type changePasswordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// handleAuthChangePassword 換密碼之後,會讓這個帳號名下*所有*既有
// session 失效再核發一個新的給目前這個請求 —— 這樣萬一密碼外洩、
// 使用者發現後改密碼,其他地方(可能是攻擊者)還留著的舊 session
// 會立刻被踢掉,而目前正在操作的這個瀏覽器分頁不會因此被登出,
// 不需要使用者改完密碼後還要手動再登入一次。
func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, errPasswordTooShort)
		return
	}

	admin := s.store.Snapshot().Admin
	if admin == nil {
		writeError(w, http.StatusConflict, errAdminNotConfigured)
		return
	}
	ok, err := security.VerifyPassword(req.OldPassword, admin.PasswordHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, errInvalidCredentials)
		return
	}

	newHash, err := security.HashPassword(req.NewPassword)
	if err != nil {
		s.logger.Error("hashing new admin password failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.Update(func(st *state.State) error {
		if st.Admin == nil {
			return errAdminNotConfigured
		}
		st.Admin.PasswordHash = newHash
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.sessions.RevokeAllForUser(admin.Username)
	s.loginSession(w, r, admin.Username)
}

type totpSetupResponse struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioningUri"`
}

// handleAuthTOTPSetup 產生一把新的 TOTP 密鑰並先存進 state(但
// TOTPEnabled 還是 false),讓使用者有機會把密鑰輸入到驗證器 App 裡、
// 再呼叫 /auth/totp/enable 帶一個當下的驗證碼確認設定無誤 —— 分成兩步
// 是為了不讓使用者「以為設定好了,但其實密鑰輸入錯」之後被鎖在外面。
func (s *Server) handleAuthTOTPSetup(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)

	secret, err := security.GenerateSecret()
	if err != nil {
		s.logger.Error("generating totp secret failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		if st.Admin == nil {
			return errAdminNotConfigured
		}
		st.Admin.TOTPSecret = secret
		st.Admin.TOTPEnabled = false
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, totpSetupResponse{
		Secret:          secret,
		ProvisioningURI: security.ProvisioningURI(secret, sess.Username, "GoNAS"),
	})
}

type totpCodeRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleAuthTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req totpCodeRequest
	if !readJSON(w, r, &req) {
		return
	}

	admin := s.store.Snapshot().Admin
	if admin == nil || admin.TOTPSecret == "" {
		writeError(w, http.StatusConflict, errTOTPNotSetUp)
		return
	}
	valid, err := security.ValidateCode(admin.TOTPSecret, req.Code, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, errInvalidTOTPCode)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		if st.Admin == nil {
			return errAdminNotConfigured
		}
		st.Admin.TOTPEnabled = true
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type totpDisableRequest struct {
	Password string `json:"password"`
}

// handleAuthTOTPDisable 要求重新輸入密碼才能關掉 2FA —— 停用兩步驟驗證
// 是一個會降低帳號安全性的動作,不該只靠「目前已經登入」這個較弱的
// 前提就能做,跟大多數服務停用 2FA 前要求重新驗證密碼是同樣的考量。
func (s *Server) handleAuthTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req totpDisableRequest
	if !readJSON(w, r, &req) {
		return
	}

	admin := s.store.Snapshot().Admin
	if admin == nil {
		writeError(w, http.StatusConflict, errAdminNotConfigured)
		return
	}
	ok, err := security.VerifyPassword(req.Password, admin.PasswordHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, errInvalidCredentials)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		if st.Admin == nil {
			return errAdminNotConfigured
		}
		st.Admin.TOTPSecret = ""
		st.Admin.TOTPEnabled = false
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
