package api

import (
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/state"
)

// recordAudit 把一筆稽核紀錄寫進 state.State.AuditLog,由 requireAdmin
// 在每一支非 GET 的管理端點執行完之後呼叫,見該函式的說明。超過
// auditLogCapacity 筆就從最舊的開始丟——只保留最新的那一段,理由跟
// internal/monitor.History 的容量上限是同一種考量。
//
// 這裡刻意不因為 store.Update 失敗就讓原本的請求也跟著失敗
// (recordAudit 沒有回傳值給呼叫端檢查)——稽核紀錄寫不進去是次要問題,
// 不該讓「使用者原本要做的那個操作已經成功了」這件事,因為事後的紀錄
// 動作失敗而變成看起來像失敗(這支函式呼叫的時間點在 next(w, r) 已經
// 完整執行、回應也已經送出之後)。寫入失敗的話只會在下一次 GET
// /api/v1/audit/log 看到少一筆,不會讓原本的操作被回滾或重試。
func (s *Server) recordAudit(username, method, path string, statusCode int) {
	entry := state.AuditEntry{
		At:         time.Now(),
		Username:   username,
		Method:     method,
		Path:       path,
		StatusCode: statusCode,
	}
	_ = s.store.Update(func(st *state.State) error {
		st.AuditLog = append(st.AuditLog, entry)
		if len(st.AuditLog) > auditLogCapacity {
			st.AuditLog = st.AuditLog[len(st.AuditLog)-auditLogCapacity:]
		}
		return nil
	})
}

// auditLogResponse 是 GET /api/v1/audit/log 的回應。Entries 依時間由新到
// 舊排序(最近的操作排最前面)——這是給人看的稽核紀錄列表,使用者通常
// 最關心「剛剛發生了什麼」,由新到舊排序不用讓前端自己再重新排一次。
type auditLogResponse struct {
	Entries []state.AuditEntry `json:"entries"`
}

// handleAuditLogGet 回傳目前保留的稽核紀錄。跟其他「管理 NAS 設定」的
// 端點一樣用 requireAdmin——稽核紀錄本身就是給管理者看「誰動過什麼」
// 用的,RoleViewer 不該看得到其他管理者帳號做過的操作紀錄。這支端點
// 本身是 GET,不會被 requireAdmin 對非 GET 請求的稽核記錄邏輯記錄
// 到——單純查看紀錄不是「動作」,見 requireAdmin 的說明。
func (s *Server) handleAuditLogGet(w http.ResponseWriter, r *http.Request) {
	snapshot := s.store.Snapshot().AuditLog
	entries := make([]state.AuditEntry, len(snapshot))
	for i, e := range snapshot {
		entries[len(snapshot)-1-i] = e
	}
	writeJSON(w, http.StatusOK, auditLogResponse{Entries: entries})
}
