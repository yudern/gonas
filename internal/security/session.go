package security

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Session 是一次成功登入之後,伺服器記得的一筆狀態。Token 特意標
// `json:"-"`:這個型別偶爾會被拿去序列化成 API 回應(例如列出目前
// session),token 本身是等同密碼的機密資料,絕對不該意外被序列化進
// 任何回應本文裡 —— 它只透過 HttpOnly Cookie 傳遞。
type Session struct {
	Token     string    `json:"-"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// SessionManager 是純記憶體的登入 session 儲存。GoNAS 只有單一管理者
// 帳號,daemon 重啟後要求重新登入是完全合理、甚至比較安全的行為,
// 沒有必要為了「重啟後還能保持登入」這種次要的方便性,把 session token
// 這種短時效的機密資料也塞進 state.json 長期保存 —— 這跟 Phase 5
// monitor.History 刻意不落地到磁碟是同一種取捨邏輯。
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]Session
	ttl      time.Duration
}

// NewSessionManager 建立一個 session 管理器,ttl 是每筆 session 的存活
// 時間(從建立那一刻起算,不是「閒置多久」的滑動視窗 —— 簡單、可預期,
// 對一個家用 NAS 管理介面來說已經足夠)。
func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{sessions: make(map[string]Session), ttl: ttl}
}

// Create 建立一筆新的 session,回傳的 Session.Token 是呼叫端要塞進
// Cookie 的值。
func (m *SessionManager) Create(username string) (Session, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Session{}, fmt.Errorf("generating session token: %w", err)
	}
	now := time.Now()
	s := Session{
		Token:     hex.EncodeToString(tokenBytes),
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.Token] = s
	m.purgeExpiredLocked(now) // 順手清掉過期項目,見下方說明
	return s, nil
}

// Validate 檢查 token 是不是一筆還沒過期的合法 session,過期的話直接
// 從記憶體裡刪掉(懶惰清理),不需要另外通知任何人。
func (m *SessionManager) Validate(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return Session{}, false
	}
	if time.Now().After(s.ExpiresAt) {
		delete(m.sessions, token)
		return Session{}, false
	}
	return s, true
}

// Revoke 讓一筆 session 立刻失效(登出用)。
func (m *SessionManager) Revoke(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

// RevokeAllForUser 讓某個使用者名下所有 session 立刻失效 —— 改密碼或
// 停用/啟用 2FA 之後,應該讓其他裝置上還在用舊憑證的 session 一併登出,
// 不留一段「密碼已經改了,但舊 session 還能繼續用」的空窗期。
func (m *SessionManager) RevokeAllForUser(username string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, s := range m.sessions {
		if s.Username == username {
			delete(m.sessions, token)
		}
	}
}

// purgeExpiredLocked 清掉已經過期的 session,呼叫端必須已經持有 mu。
// 只在 Create 的時候順便掃一次就夠 —— GoNAS 單一管理者的 session 數量
// 小到不需要獨立的背景清理 goroutine,道理跟 monitor.History 用
// 「附加時裁切」而不是獨立的清理迴圈一樣。
func (m *SessionManager) purgeExpiredLocked(now time.Time) {
	for token, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
}
