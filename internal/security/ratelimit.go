package security

import (
	"sync"
	"time"
)

// LoginLimiter 是一個純記憶體的登入嘗試節流器,用來擋掉對登入端點的暴力
// 猜密碼攻擊。GoNAS 只有一個管理者帳號,帳號名稱本身也不是秘密(登入
// 表單早就要求輸入帳號),真正需要保護的是密碼/TOTP 驗證碼這兩道關卡
// 「可以無限次重試」這件事 —— 沒有節流的話,一支跑在同一個網段裡的
// 腳本可以用毫秒等級的速度窮舉密碼,TLS/HTTPS 跟密碼雜湊本身完全擋不住
// 這種攻擊(PBKDF2 的計算成本是設計給「合理速率的嘗試」用的,幾千次
// 每秒的暴力嘗試還是會在合理時間內撞出弱密碼)。
//
// 用「連續失敗達到門檻就鎖一段時間」這種簡單策略,而不是滑動視窗計數
// 或漏桶演算法:GoNAS 的登入端點使用量極低(一台家用 NAS,一天可能就
// 幾次真人登入),不需要為了這麼小的流量去換更精細但也更難驗證正確性
// 的演算法,「用 IP 位址當 key、失敗次數清楚可見、鎖定時間固定」對
// 管理者來說也更容易理解「為什麼我現在登入不了」。
//
// 跟 SessionManager 一樣刻意純記憶體:daemon 重啟會讓所有鎖定狀態歸零,
// 這是可接受的取捨(重啟通常代表管理者自己在操作這台機器,不是攻擊者
// 觸發的),換來不需要另外設計持久化格式。
type LoginLimiter struct {
	mu          sync.Mutex
	attempts    map[string]*loginAttemptState
	maxFailures int
	lockFor     time.Duration
}

type loginAttemptState struct {
	failures    int
	lockedUntil time.Time
}

// NewLoginLimiter 建立一個節流器:同一個 key(通常是客戶端 IP)連續失敗
// maxFailures 次之後,會被鎖定 lockFor 這麼久,鎖定時間內即使密碼/驗證碼
// 正確也一律拒絕(不然攻擊者可以用「先送一堆錯的觸發鎖定判斷,再送一次
// 真正想測的」這種方式繞過鎖定去做時間旁路分析,固定拒絕比較單純安全)。
func NewLoginLimiter(maxFailures int, lockFor time.Duration) *LoginLimiter {
	return &LoginLimiter{
		attempts:    make(map[string]*loginAttemptState),
		maxFailures: maxFailures,
		lockFor:     lockFor,
	}
}

// Allow 回報這個 key 現在能不能嘗試登入。不能的話,第二個回傳值是還要
// 等多久才能再試(給 handler 拿去設定 Retry-After 標頭用)。
//
// 鎖定時間一過,狀態會被整筆清掉重新計算(而不是失敗次數扣減或衰減)
// ——「鎖定到期後完全重新開始」比「慢慢衰減」容易驗證正確性,對這種
// 低流量端點來說也沒有實質差異。
func (l *LoginLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.attempts[key]
	if !ok || st.lockedUntil.IsZero() {
		return true, 0
	}

	now := time.Now()
	if now.Before(st.lockedUntil) {
		return false, st.lockedUntil.Sub(now)
	}

	delete(l.attempts, key)
	return true, 0
}

// RecordFailure 記一次失敗的登入嘗試,達到門檻就鎖定這個 key。
func (l *LoginLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.attempts[key]
	if !ok {
		st = &loginAttemptState{}
		l.attempts[key] = st
	}
	st.failures++
	if st.failures >= l.maxFailures {
		st.lockedUntil = time.Now().Add(l.lockFor)
	}
}

// RecordSuccess 清掉這個 key 的失敗紀錄 —— 登入成功之後,不該讓「之前
// 打錯過幾次密碼」繼續累計到下一次嘗試上。
func (l *LoginLimiter) RecordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
