package api

import (
	"context"
	"errors"
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

// requireAdmin 在 requireAuth 的基礎上多一層檢查:目前登入的帳號必須是
// RoleAdmin,才能繼續。router.go 裡「會新增/修改/刪除任何設定或資料」
// 的端點一律用這層而不是 requireAuth,RoleViewer 的帳號打這些端點會
// 收到 403——「只能看,不能改」對一台多人共用的家用/小型辦公室 NAS 是
// 常見需求(例如家人只需要能瀏覽檔案、看陣列狀態,不該不小心手滑刪掉
// 分享或改動陣列設定)。
//
// 每次請求都重新從 store 查目前的角色,而不是把角色存進 session 本身——
// 這樣「使用者的帳號被降級或刪除」能立刻反映到下一個請求,不用等
// session 過期或使用者重新登入,跟 handleAuthAccountsDelete 刪除帳號後
// 順手 RevokeAllForUser 是同一種「權限異動立刻生效」的考量。
//
// 少數「管理自己帳號」的端點(登出、改自己的密碼、設定/啟用/停用自己
// 的 TOTP)刻意不用這層,直接用 requireAuth——那些不算「管理 NAS 設定」,
// RoleViewer 一樣該能做,詳見 router.go 對應路由旁的註解。
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		sess, _ := r.Context().Value(sessionContextKey).(security.Session)
		account, ok := findAdmin(s.store.Snapshot().Admins, sess.Username)
		if !ok || account.Role != state.RoleAdmin {
			writeError(w, http.StatusForbidden, errInsufficientPermission)
			return
		}

		// 第十九輪:如果這個帳號被標記為「必須先改密碼」(appliance 預設
		// admin gonas/gonas 第一次登入的情況),擋掉所有 admin 操作,回一個
		// 前端認得的 403——使用者只能先去 /auth/password(那支走的是
		// requireAuth,不經過這裡,所以永遠開著)把預設密碼改掉,改完
		// handleAuthChangePassword 會清掉旗標,這道封鎖就自動解除。這是
		// 伺服器端的硬性強制,就算有人繞過前端直接打 API 也一樣擋得住,
		// 不是只靠前端畫面擋。
		if account.MustChangePassword {
			writeError(w, http.StatusForbidden, errPasswordChangeRequired)
			return
		}

		// Phase 18b:稽核紀錄。只記錄會改動系統狀態的請求(HTTP 方法不是
		// GET)——requireAdmin 底下少數幾支 GET 端點(例如
		// GET /api/v1/auth/accounts)是查詢,不是「動作」,見
		// state.AuditEntry 的說明。用 statusRecorder 包一層
		// http.ResponseWriter 才能在 next 執行完之後知道它實際回了
		// 什麼狀態碼——不管成功或失敗(400/403/500 等)都值得留下紀錄,
		// 「管理者嘗試做了什麼、結果如何」本身就是稽核紀錄要回答的問題,
		// 不是只記錄成功的操作。
		if r.Method == http.MethodGet {
			next(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		s.recordAudit(sess.Username, r.Method, r.URL.Path, rec.status)
	})
}

// statusRecorder 包住 http.ResponseWriter,記下實際呼叫 WriteHeader 的
// 狀態碼——如果 handler 從頭到尾沒呼叫 WriteHeader(直接寫 body),
// net/http 本身的行為是視同 200,這裡的預設值跟這個行為保持一致。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

// findAdmin 在帳號清單裡依使用者名稱找一筆,回傳的是副本——呼叫端如果
// 要修改,得透過 store.Update 用同樣的方式在陣列裡重新找到索引再改,
// 不能直接改這裡回傳的副本(修改不會反映回 store)。
func findAdmin(admins []state.AdminAccount, username string) (state.AdminAccount, bool) {
	for _, a := range admins {
		if a.Username == username {
			return a, true
		}
	}
	return state.AdminAccount{}, false
}

// findAdminIndex 是 findAdmin 給 store.Update 閉包內用的版本——回傳索引
// 而不是副本,才能直接改到 st.Admins 裡的那一筆。
func findAdminIndex(admins []state.AdminAccount, username string) int {
	for i, a := range admins {
		if a.Username == username {
			return i
		}
	}
	return -1
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
	writeJSON(w, http.StatusOK, authStatusResponse{SetupRequired: len(s.store.Snapshot().Admins) == 0})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleAuthSetup 建立第一個管理者帳號(角色一律是 RoleAdmin),只有在
// 完全還沒有任何帳號的情況下才會成功 —— 這道檢查是刻意的安全邊界:不
// 這樣的話,任何能連到 gonasd 的人都可以呼叫這支端點加一個帳號進來,
// 等於整個身分驗證形同虛設。成功之後直接核發 session,讓使用者不用
// 設定完馬上又要手動登入一次。之後要新增第二、第三個帳號(不管是
// RoleAdmin 還是 RoleViewer),走的是另一支需要先登入、而且呼叫者本身
// 要是 RoleAdmin 的 handleAuthAccountsCreate,不是這支——這支永遠只在
// 「完全沒有帳號」這個第一次執行的情境下有用。
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if len(s.store.Snapshot().Admins) > 0 {
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

	admin := state.AdminAccount{Username: req.Username, PasswordHash: hash, Role: state.RoleAdmin}
	if err := s.store.Update(func(st *state.State) error {
		if len(st.Admins) > 0 {
			return errAdminAlreadyConfigured
		}
		st.Admins = append(st.Admins, admin)
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

	admins := s.store.Snapshot().Admins
	if len(admins) == 0 {
		writeError(w, http.StatusConflict, errAdminNotConfigured)
		return
	}
	// 用固定訊息一律回「帳號或密碼錯誤」,不區分「使用者名稱不對」跟
	// 「密碼不對」——避免讓攻擊者靠著錯誤訊息內容枚舉出哪些使用者名稱
	// 存在。Phase 13 支援多帳號之後,這道原則更重要:不能讓登入錯誤
	// 訊息變成「列舉出這台 NAS 上有哪些帳號」的管道。
	admin, found := findAdmin(admins, req.Username)
	if !found {
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
	account, _ := findAdmin(s.store.Snapshot().Admins, username)
	writeJSON(w, http.StatusOK, meResponse{Username: username, TOTPEnabled: account.TOTPEnabled, Role: account.Role, MustChangePassword: account.MustChangePassword})
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
	// Role 是 state.RoleAdmin 或 state.RoleViewer。前端用這個欄位決定
	// 要不要顯示「帳號管理」這個只有 RoleAdmin 看得到的區塊——伺服器端
	// 的 requireAdmin 中介層才是真正的權限邊界,這裡純粹是為了不要讓
	// RoleViewer 的使用者在介面上看到一堆點了也只會得到 403 的按鈕。
	Role string `json:"role"`
	// MustChangePassword 為 true 時,前端會強制先跳到「修改密碼」畫面、
	// 擋住其他所有操作,直到使用者把預設密碼改掉為止(伺服器端的
	// requireAdmin 也會同步擋掉除了改密碼以外的 admin 操作,見該中介層)。
	// 目前只有 appliance 預設 admin(gonas/gonas)第一次登入會是 true。
	MustChangePassword bool `json:"mustChangePassword"`
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)
	account, _ := findAdmin(s.store.Snapshot().Admins, sess.Username)
	writeJSON(w, http.StatusOK, meResponse{Username: sess.Username, TOTPEnabled: account.TOTPEnabled, Role: account.Role, MustChangePassword: account.MustChangePassword})
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
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)

	var req changePasswordRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, errPasswordTooShort)
		return
	}

	admin, found := findAdmin(s.store.Snapshot().Admins, sess.Username)
	if !found {
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
		i := findAdminIndex(st.Admins, sess.Username)
		if i < 0 {
			return errAdminNotConfigured
		}
		st.Admins[i].PasswordHash = newHash
		// 改完密碼就清掉「必須改密碼」旗標——這是預設 admin(gonas/gonas)
		// 走完強制改密碼流程之後,解除 requireAdmin 封鎖的唯一途徑。
		st.Admins[i].MustChangePassword = false
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
		i := findAdminIndex(st.Admins, sess.Username)
		if i < 0 {
			return errAdminNotConfigured
		}
		st.Admins[i].TOTPSecret = secret
		st.Admins[i].TOTPEnabled = false
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
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)

	var req totpCodeRequest
	if !readJSON(w, r, &req) {
		return
	}

	admin, found := findAdmin(s.store.Snapshot().Admins, sess.Username)
	if !found || admin.TOTPSecret == "" {
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
		i := findAdminIndex(st.Admins, sess.Username)
		if i < 0 {
			return errAdminNotConfigured
		}
		st.Admins[i].TOTPEnabled = true
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
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)

	var req totpDisableRequest
	if !readJSON(w, r, &req) {
		return
	}

	admin, found := findAdmin(s.store.Snapshot().Admins, sess.Username)
	if !found {
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
		i := findAdminIndex(st.Admins, sess.Username)
		if i < 0 {
			return errAdminNotConfigured
		}
		st.Admins[i].TOTPSecret = ""
		st.Admins[i].TOTPEnabled = false
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Phase 13：管理其他帳號(帳號管理頁面)-------------------------------
//
// 下面三支端點在 router.go 裡一律包 requireAdmin(不是 requireAuth),
// 所以能呼叫到這裡的人已經確定是 RoleAdmin——不需要再重複檢查一次角色。

type adminAccountView struct {
	Username    string `json:"username"`
	Role        string `json:"role"`
	TOTPEnabled bool   `json:"totpEnabled"`
}

// handleAuthAccountsList 列出所有帳號，刻意不回傳 PasswordHash/TOTPSecret
// ——那是伺服器內部驗證用的祕密,前端從來不需要,也不該透過任何 API 拿到
// (就算是 RoleAdmin 也一樣;PasswordHash 本身雖然不是明文密碼,但沒有
// 理由讓它離開伺服器)。
func (s *Server) handleAuthAccountsList(w http.ResponseWriter, r *http.Request) {
	admins := s.store.Snapshot().Admins
	views := make([]adminAccountView, 0, len(admins))
	for _, a := range admins {
		views = append(views, adminAccountView{Username: a.Username, Role: a.Role, TOTPEnabled: a.TOTPEnabled})
	}
	writeJSON(w, http.StatusOK, views)
}

type createAccountRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleAuthAccountsCreate 新增一個帳號(RoleAdmin 或 RoleViewer 都可以,
// 由呼叫端指定)。新帳號一律從沒有設定 TOTP 開始——2FA 是帳號本人要
// 自己用 /auth/totp/setup 設定的,不能代替別人設定(那樣祕密金鑰會先
// 經過建立帳號那個人的手,失去 2FA「只有本人知道」的意義)。
func (s *Server) handleAuthAccountsCreate(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
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
	if !state.IsValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, errInvalidRole)
		return
	}

	hash, err := security.HashPassword(req.Password)
	if err != nil {
		s.logger.Error("hashing new account password failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		if findAdminIndex(st.Admins, req.Username) >= 0 {
			return errAdminUsernameTaken
		}
		st.Admins = append(st.Admins, state.AdminAccount{
			Username:     req.Username,
			PasswordHash: hash,
			Role:         req.Role,
		})
		return nil
	}); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errAdminUsernameTaken) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}

	writeJSON(w, http.StatusCreated, adminAccountView{Username: req.Username, Role: req.Role})
}

// handleAuthAccountsDelete 刪除一個帳號。兩道安全邊界刻意做在伺服器端、
// 不是只靠前端隱藏按鈕:不能刪自己目前登入的這個帳號(避免手滑把自己
// 鎖在外面,想切換帳號的話本來就該先登出、用另一個帳號登入),也不能
// 刪掉最後一個 RoleAdmin 帳號(不然這台 NAS 就再也沒有人能管理帳號、
// 改任何設定了,而且沒有辦法復原,只能直接改 state.json)——這兩個
// 檢查都要在 store.Update 的 fn 裡面對「當下」的 Admins 陣列做,不能只
// 看呼叫當下的 Snapshot,理由跟其他有唯一性檢查的 handler 一樣:避免
// 兩個請求前後腳進來時看到同一份舊快照、都通過檢查、結果一起把最後
// 兩個 RoleAdmin 帳號都刪掉的競態情況。
func (s *Server) handleAuthAccountsDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)
	target := r.PathValue("username")

	if target == sess.Username {
		writeError(w, http.StatusConflict, errCannotDeleteOwnAccount)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		i := findAdminIndex(st.Admins, target)
		if i < 0 {
			return errAdminAccountNotFound
		}
		if st.Admins[i].Role == state.RoleAdmin {
			remainingAdmins := 0
			for _, a := range st.Admins {
				if a.Role == state.RoleAdmin {
					remainingAdmins++
				}
			}
			if remainingAdmins <= 1 {
				return errCannotDeleteLastAdmin
			}
		}
		st.Admins = append(st.Admins[:i], st.Admins[i+1:]...)
		return nil
	}); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, errAdminAccountNotFound):
			status = http.StatusNotFound
		case errors.Is(err, errCannotDeleteLastAdmin):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}

	// 帳號已經從 state 移除，這個使用者名下任何還留著的 session 立刻
	// 失效——不用等 24 小時的 session TTL 到期，也不需要 gonasd 重啟。
	s.sessions.RevokeAllForUser(target)
	w.WriteHeader(http.StatusNoContent)
}
