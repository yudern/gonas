package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/qrcode"
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

		// 第三十一輪(測試工程師覆核的後續強化):如果這個帳號被標記為
		// 「必須先改密碼」(appliance 預設 gonas/gonas 第一次登入),在這裡
		// 就擋掉「除了看自己是誰、改密碼、登出以外」的所有請求——不只是
		// 會改東西的 admin 端點。原本這道封鎖只在 requireAdmin 裡,代表
		// 還沒改預設密碼的帳號,仍然可以打 requireAuth 的「唯讀」端點
		// (列檔案、下載、讀文字檔)跟設定自己 TOTP——對一組公開記載的
		// 預設帳密(gonas/gonas)來說,這是一段不必要的曝險窗:有人搶在
		// 機主完成初次設定前用預設帳密登入,就能讀檔、甚至先設好 TOTP。
		// 把封鎖上移到 requireAuth,配合下面的白名單(查自己/改密碼/登出),
		// 讓「改掉預設密碼」成為這個帳號能做任何其他事情之前的硬性前置。
		// 允許清單要夠、但只夠讓前端完成「偵測到要改密碼 → 改 → 生效」
		// 這條路:/auth/me(前端據此跳改密碼畫面)、/auth/password(改)、
		// /auth/logout(放棄改、登出)。其餘一律 403,前端同樣靠
		// /auth/me 的旗標導向,繞過前端直接打 API 也一樣擋得住。
		if !passwordChangeExempt(r.Method, r.URL.Path) {
			if account, ok := findAdmin(s.store.Snapshot().Admins, sess.Username); ok && account.MustChangePassword {
				writeError(w, http.StatusForbidden, errPasswordChangeRequired)
				return
			}
		}

		ctx := context.WithValue(r.Context(), sessionContextKey, sess)
		next(w, r.WithContext(ctx))
	}
}

// passwordChangeExempt 回報某個端點在「帳號被標記必須先改密碼」時是否仍然
// 放行。只放行讓前端完成改密碼流程最低限度需要的三支端點,其餘一律擋下。
// 用精確比對方法 + 路徑,不是前綴比對,避免不小心放行到別的子路徑。
func passwordChangeExempt(method, path string) bool {
	switch path {
	case "/api/v1/auth/me":
		return method == http.MethodGet
	case "/api/v1/auth/password":
		return method == http.MethodPost
	case "/api/v1/auth/logout":
		return method == http.MethodPost
	}
	return false
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

		// 「必須先改密碼」的硬性封鎖,第三十一輪起主要的執行點已經上移到
		// requireAuth(見那裡的說明:不只擋 admin 操作,連唯讀端點也擋,
		// 只放行改密碼流程需要的三支)。因為 requireAdmin 本身就是包在
		// requireAuth 外面,任何走到這裡的請求其實都已經先通過 requireAuth
		// 那道檢查了,所以這裡這一段在正常情況下是到不了的。保留它純粹
		// 當第二道保險(萬一日後有人調整中介層包裝順序),成本只是多一次
		// 布林判斷,不影響正確性。
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

// Unwrap 讓 http.ResponseController(以及任何用 http.ResponseController 尋找
// 底層能力的程式碼)能穿透這層稽核包裝,取得底層 ResponseWriter 上的
// Flush / ReadFrom / SetReadDeadline / SetWriteDeadline 等方法。
//
// 第五十八輪全鏈路覆核(QA1)抓到的真 bug:檔案上傳端點是 requireAdmin 的
// 非 GET 請求,會被 statusRecorder 包住;上傳 handler 會呼叫
// http.NewResponseController(w).SetReadDeadline(...) 來「解除」伺服器層級
// 那個 20 秒 ReadTimeout(否則大檔案上傳傳超過 20 秒就會被中途切斷)。
// http.ResponseController 找不到 SetReadDeadline、也找不到 Unwrap 時會回
// errNotSupported,而 handler 把它 `_ =` 忽略掉——於是 deadline 從來沒被
// 解除,20 秒的 ReadTimeout 照樣套用在上傳上,大檔案(區網上的多 GB 影片)
// 傳到一半就失敗/被截斷。補上 Unwrap 後,ResponseController 會沿著這條
// wrapper 鏈找到底層 net/http 的 *response,SetReadDeadline 就會真的生效;
// 同時也一併恢復被這層包裝擋住的 Flush / ReadFrom 直通能力。
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
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
		// 帳號不存在時也做一次等量的 PBKDF2(丟棄結果),拉平回應時間,
		// 避免用「帳號存在要算雜湊、不存在秒回」的時間差枚舉帳號(第三十輪
		// 覆核抓到的時序側信道)。
		security.DummyVerify(req.Password)
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
		counter, valid, err := security.ValidateCodeWithCounter(admin.TOTPSecret, req.TOTPCode, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if valid {
			// 第五十六輪覆核(QA1/S2):防重放的「counter 是否比上次新」比對必須
			// 在交易「裡面」對「當下持久化的值」做,不能拿交易外的 snapshot
			// (admin.LastTOTPCounter)判斷——否則兩個並發登入帶同一個碼時,兩者
			// 都讀到同一個舊 snapshot、都通過檢查、都寫入,重放就成立;而且若有
			// 更新的登入已經把計數器推得更高,這裡還會把它「寫回較小值」重開重放窗。
			// 改成在 store.Update 內原子地重新比對:只有 counter 仍嚴格大於當下的
			// LastTOTPCounter 才接受並前進,accepted 記錄結果。
			accepted := false
			if err := s.store.Update(func(st *state.State) error {
				i := findAdminIndex(st.Admins, admin.Username)
				if i < 0 {
					return errAdminNotConfigured
				}
				if counter > st.Admins[i].LastTOTPCounter {
					st.Admins[i].LastTOTPCounter = counter
					accepted = true
				}
				return nil
			}); err != nil {
				// fail-closed(第五十二輪覆核 S-2):計數器沒能持久化就不能放行。
				s.logger.Error("recording totp counter failed", "err", err)
				writeError(w, http.StatusInternalServerError, errInternalServerError)
				return
			}
			if !accepted {
				// 碼合法,但交易內看到的 counter 不比目前值新 —— 這個碼(或更舊的)
				// 已經用過了,判定為重放,拒絕。
				s.loginLimiter.RecordFailure(ip)
				writeError(w, http.StatusUnauthorized, errInvalidTOTPCode)
				return
			}
		} else {
			// 不是合法 TOTP,再看看是不是一組還沒用過的救援碼。比對＋消耗都在
			// 同一個 store.Update 裡原子完成(避免兩個登入同時用同一組碼)。
			consumed := false
			if err := s.store.Update(func(st *state.State) error {
				i := findAdminIndex(st.Admins, admin.Username)
				if i < 0 {
					return nil
				}
				if idx, ok := security.MatchRecoveryCode(req.TOTPCode, st.Admins[i].RecoveryCodes); ok {
					codes := st.Admins[i].RecoveryCodes
					st.Admins[i].RecoveryCodes = append(codes[:idx], codes[idx+1:]...)
					consumed = true
				}
				return nil
			}); err != nil {
				// fail-closed(第五十二輪覆核 S-2):比對到救援碼、但寫檔失敗時,
				// consumed 已經是 true 卻沒真的持久化「移除這枚碼」——若這裡只記
				// log 就放行,這枚一次性救援碼在磁碟上仍然有效,等於能被重複使用。
				s.logger.Error("consuming recovery code failed", "err", err)
				writeError(w, http.StatusInternalServerError, errInternalServerError)
				return
			}
			if !consumed {
				s.loginLimiter.RecordFailure(ip)
				writeError(w, http.StatusUnauthorized, errInvalidTOTPCode)
				return
			}
			s.logger.Warn("login used a 2FA recovery code", "user", admin.Username)
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
	// QRCodeSVG 是把 ProvisioningURI 編成的 QR code(自成一體的 SVG 字串),
	// 讓使用者直接用手機驗證器 App 掃描,不用手動逐字輸入密鑰。用純標準函式庫
	// 的 internal/qrcode 產生(這台 NAS 常常沒網路,不能靠外部服務/CDN 產圖),
	// 前端直接把它塞進畫面即可。萬一編碼失敗(理論上不會,otpauth URI 長度
	// 遠在容量內),就留空字串,前端退回只顯示密鑰+URI。
	QRCodeSVG string `json:"qrCodeSvg,omitempty"`
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

	uri := security.ProvisioningURI(secret, sess.Username, "GoNAS")
	// QR 產生失敗不致命:密鑰與 URI 仍照舊回,前端還能手動輸入(見前端退路)。
	qrSVG, qrErr := qrcode.EncodeSVG(uri, 4, 4)
	if qrErr != nil {
		s.logger.Warn("generating totp qr code failed; returning secret/uri only", "err", qrErr)
	}
	writeJSON(w, http.StatusOK, totpSetupResponse{
		Secret:          secret,
		ProvisioningURI: uri,
		QRCodeSVG:       qrSVG,
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
	// 第五十六輪覆核(S3):啟用時就把用掉的 counter 記下來,否則這個「啟用碼」
	// 在它的有效窗內(30–90 秒)還能被拿去 /auth/login 登入一次(login 只看
	// counter > 0)——等於一個一次性重放窗。用 WithCounter 版本取得 counter,
	// 連同 TOTPEnabled 一起寫進 state。
	enableCounter, valid, err := security.ValidateCodeWithCounter(admin.TOTPSecret, req.Code, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, errInvalidTOTPCode)
		return
	}

	// 啟用成功的同時產生一組一次性救援碼(第三十輪覆核補上的 2FA 救援路徑)。
	// 明文只在這個回應裡回給使用者看一次,state 只存雜湊。
	plain, hashed, err := security.GenerateRecoveryCodes(0)
	if err != nil {
		s.logger.Error("generating recovery codes failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		i := findAdminIndex(st.Admins, sess.Username)
		if i < 0 {
			return errAdminNotConfigured
		}
		st.Admins[i].TOTPEnabled = true
		st.Admins[i].RecoveryCodes = hashed
		// 記下啟用時用掉的 counter,擋掉「用啟用碼再登入一次」的重放窗(S3)。
		st.Admins[i].LastTOTPCounter = enableCounter
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": plain})
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
		st.Admins[i].RecoveryCodes = nil
		st.Admins[i].LastTOTPCounter = 0
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthTOTPRecoveryCodes 讓已啟用 2FA 的使用者重新產生一組救援碼(舊的
// 全部作廢)—— 抄丟了、或用掉幾組想補滿時用。新的明文一樣只回一次。
func (s *Server) handleAuthTOTPRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)

	// 重新產生救援碼會讓舊的一次性碼全部作廢、換發一組新的——這跟停用 2FA
	// 是同一級的敏感動作(第五十二輪覆核 S-4):若 session 被劫持,攻擊者
	// 能一鍵作廢使用者手上的救援碼、拿到自己的一組。所以跟
	// handleAuthTOTPDisable 一致,要求重新輸入密碼,不只靠「已登入」。
	var req totpDisableRequest // 只需要 {password}
	if !readJSON(w, r, &req) {
		return
	}

	admin, found := findAdmin(s.store.Snapshot().Admins, sess.Username)
	if !found {
		writeError(w, http.StatusConflict, errAdminNotConfigured)
		return
	}
	if !admin.TOTPEnabled {
		writeError(w, http.StatusConflict, errTOTPNotSetUp)
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
	plain, hashed, err := security.GenerateRecoveryCodes(0)
	if err != nil {
		s.logger.Error("regenerating recovery codes failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.Update(func(st *state.State) error {
		i := findAdminIndex(st.Admins, sess.Username)
		if i < 0 {
			return errAdminNotConfigured
		}
		st.Admins[i].RecoveryCodes = hashed
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": plain})
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

// handleAuthAccountsResetTOTP 讓管理者清掉「另一個」帳號的 2FA(第三十輪覆核
// 補上的 lockout 救援路徑):某個使用者弄丟驗證器、救援碼也用光/沒抄,原本
// 就永久登不進去 —— 由另一個管理者從帳號管理頁把他的 2FA 重設掉,他就能只用
// 密碼登入、再重新設定 2FA。requireAdmin(見 router.go)。
//
// 刻意「不能重設自己」:重設自己的 2FA 應該走 /auth/totp/disable(要重新輸入
// 密碼),而不是這個免密碼的管理動作 —— 避免「趁別人登入的畫面沒鎖」就一鍵
// 拿掉本人的 2FA。想清掉自己的請用停用流程。
func (s *Server) handleAuthAccountsResetTOTP(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionContextKey).(security.Session)
	target := r.PathValue("username")

	if target == sess.Username {
		writeError(w, http.StatusConflict, errCannotResetOwnTOTP)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		i := findAdminIndex(st.Admins, target)
		if i < 0 {
			return errAdminAccountNotFound
		}
		st.Admins[i].TOTPSecret = ""
		st.Admins[i].TOTPEnabled = false
		st.Admins[i].RecoveryCodes = nil
		st.Admins[i].LastTOTPCounter = 0
		return nil
	}); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errAdminAccountNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	s.logger.Warn("admin reset another account's 2FA", "by", sess.Username, "target", target)
	w.WriteHeader(http.StatusNoContent)
}
