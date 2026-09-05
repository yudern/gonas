package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/state"
)

// TestRequireAdmin_RecordsAuditEntryForMutatingRequest 驗證
// Phase 18b 稽核紀錄的核心邏輯:透過 requireAdmin 包住的一支非 GET
// 端點被成功呼叫之後,state.State.AuditLog 應該多出一筆對應的紀錄,
// 欄位(使用者名稱、方法、路徑、狀態碼)都要跟實際發生的事情吻合。
func TestRequireAdmin_RecordsAuditEntryForMutatingRequest(t *testing.T) {
	s := newAuthTestServer(t)
	adminCookie := seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)

	protected := s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/array/start", nil)
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	protected(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	log := s.store.Snapshot().AuditLog
	if len(log) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(log), log)
	}
	entry := log[0]
	if entry.Username != "admin1" {
		t.Errorf("expected username admin1, got %q", entry.Username)
	}
	if entry.Method != http.MethodPost {
		t.Errorf("expected method POST, got %q", entry.Method)
	}
	if entry.Path != "/api/v1/storage/array/start" {
		t.Errorf("expected path /api/v1/storage/array/start, got %q", entry.Path)
	}
	if entry.StatusCode != http.StatusNoContent {
		t.Errorf("expected recorded status 204, got %d", entry.StatusCode)
	}
	if entry.At.IsZero() {
		t.Error("expected a non-zero timestamp")
	}
}

// TestRequireAdmin_DoesNotRecordAuditEntryForGetRequests 驗證單純查詢
// (GET)不算「動作」,不該被記進稽核紀錄——否則稽核紀錄會被大量的
// 查詢請求淹沒,見 state.AuditEntry 的套件註解。
func TestRequireAdmin_DoesNotRecordAuditEntryForGetRequests(t *testing.T) {
	s := newAuthTestServer(t)
	adminCookie := seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)

	protected := s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/accounts", nil)
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	protected(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if log := s.store.Snapshot().AuditLog; len(log) != 0 {
		t.Fatalf("expected no audit entries for a GET request, got %d: %+v", len(log), log)
	}
}

// TestRequireAdmin_RecordsAuditEntryEvenWhenHandlerFails 驗證失敗的操作
// 一樣要被記下來——稽核紀錄要回答的是「管理者嘗試做了什麼、結果如何」,
// 不是只記錄成功的操作,否則反而漏掉「有人一直嘗試某個被拒絕的動作」
// 這種真正該被留意的情況。
func TestRequireAdmin_RecordsAuditEntryEvenWhenHandlerFails(t *testing.T) {
	s := newAuthTestServer(t)
	adminCookie := seedAdmin(t, s, "admin1", "admin-password-1", state.RoleAdmin)

	protected := s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusBadRequest, errUpdateNotConfigured)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/check", nil)
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	protected(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	log := s.store.Snapshot().AuditLog
	if len(log) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d: %+v", len(log), log)
	}
	if log[0].StatusCode != http.StatusBadRequest {
		t.Errorf("expected recorded status 400, got %d", log[0].StatusCode)
	}
}

// TestHandleAuditLogGet_ReturnsNewestFirst 驗證 GET /api/v1/audit/log
// 回傳的順序是新到舊,以及這支端點本身(GET)不會把自己的呼叫記錄進
// 稽核紀錄裡造成無窮累積。
func TestHandleAuditLogGet_ReturnsNewestFirst(t *testing.T) {
	s := newAuthTestServer(t)

	if err := s.store.Update(func(st *state.State) error {
		st.AuditLog = []state.AuditEntry{
			{Username: "admin1", Method: http.MethodPost, Path: "/api/v1/share/shares", StatusCode: 200},
			{Username: "admin1", Method: http.MethodDelete, Path: "/api/v1/share/shares/media", StatusCode: 200},
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding audit log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/log", nil)
	rec := httptest.NewRecorder()
	s.handleAuditLogGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp auditLogResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Entries))
	}
	if resp.Entries[0].Path != "/api/v1/share/shares/media" {
		t.Errorf("expected the most recently-added entry first, got %q", resp.Entries[0].Path)
	}

	if log := s.store.Snapshot().AuditLog; len(log) != 2 {
		t.Fatalf("expected calling the GET handler to leave the audit log untouched, got %d entries", len(log))
	}
}

// TestRecordAudit_TrimsToCapacity 驗證超過 auditLogCapacity 筆之後,
// 最舊的紀錄會被丟掉,只留最新的那一段——不會讓 state.json 無限長大。
func TestRecordAudit_TrimsToCapacity(t *testing.T) {
	s := newAuthTestServer(t)

	for i := 0; i < auditLogCapacity+10; i++ {
		s.recordAudit("admin1", http.MethodPost, "/api/v1/test/action", http.StatusOK)
	}

	log := s.store.Snapshot().AuditLog
	if len(log) != auditLogCapacity {
		t.Fatalf("expected exactly %d entries after trimming, got %d", auditLogCapacity, len(log))
	}
}
