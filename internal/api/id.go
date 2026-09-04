package api

import (
	"crypto/rand"
	"encoding/hex"
)

// newID 產生一個短的隨機識別碼，給告警規則、webhook 通知端點這類「使用者
// 自訂清單項目」當 ID 用。不用循序整數(重啟後容易撞號)也不用完整 UUID
// (多一個依賴、對這裡的用途也用不到那麼多資訊量)，8 bytes 的隨機 hex
// 在 GoNAS 這種單機、單一管理者的資料量下,碰撞機率可以忽略。
func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 幾乎不可能失敗(除非底層熵源壞掉),真的發生時
		// 寧可回傳一個明顯不獨特但至少不會 panic 的值,呼叫端(store 裡
		// 已有的重複檢查)還能擋下真正的碰撞。
		return "id-fallback"
	}
	return hex.EncodeToString(b)
}
