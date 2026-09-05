package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bng147/gonas/internal/selfupdate"
	"github.com/bng147/gonas/internal/state"
)

// newTestServerWithUpdateChecker 在 newTestServer 的基礎上補上
// handleSystemUpdate* 系列 handler 需要、但 newTestServer 故意不預設的
// 欄位:一個沒有 Start 過(所以不會有任何背景 goroutine、也不會發出
// 任何網路請求)的 selfupdate.Checker,以及跟正式環境一樣共用的
// HTTP client 跟 restartRequested channel。沒 Start 過的 Checker 呼叫
// Latest() 是安全的(回傳零值 CheckResult),CheckNow() 本來就不依賴
// Start 過,見 internal/selfupdate 套件裡 Checker 的方法註解。
func newTestServerWithUpdateChecker(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	s.updateChecker = selfupdate.NewChecker(s.logger)
	s.updateHTTPClient = http.DefaultClient
	s.restartRequested = make(chan string, 1)
	return s
}

func TestHandleSystemUpdateGet_NotConfigured(t *testing.T) {
	s := newTestServerWithUpdateChecker(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/update", nil)
	rec := httptest.NewRecorder()
	s.handleSystemUpdateGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp systemUpdateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Configured {
		t.Error("expected Configured=false when no manifestUrl has been set")
	}
	if resp.ManifestURL != "" {
		t.Errorf("expected empty manifestUrl, got %q", resp.ManifestURL)
	}
	if resp.UpdateAvailable {
		t.Error("expected UpdateAvailable=false with no check having run")
	}
	if resp.CurrentVersion == "" {
		t.Error("expected currentVersion to always be populated")
	}
}

func TestHandleSystemUpdateSettingsSet_PersistsAndTrims(t *testing.T) {
	s := newTestServerWithUpdateChecker(t)

	body := []byte(`{"manifestUrl": "  https://example.invalid/manifest.json  "}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/update/settings", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleSystemUpdateSettingsSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp systemUpdateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.ManifestURL != "https://example.invalid/manifest.json" {
		t.Errorf("expected trimmed manifestUrl, got %q", resp.ManifestURL)
	}
	if !resp.Configured {
		t.Error("expected Configured=true after setting a non-empty manifestUrl")
	}

	if got := s.store.Snapshot().Update.ManifestURL; got != "https://example.invalid/manifest.json" {
		t.Errorf("expected manifestUrl persisted to state, got %q", got)
	}
}

func TestHandleSystemUpdateSettingsSet_ClearingIsAllowed(t *testing.T) {
	s := newTestServerWithUpdateChecker(t)
	if err := s.store.Update(func(st *state.State) error {
		st.Update.ManifestURL = "https://example.invalid/manifest.json"
		return nil
	}); err != nil {
		t.Fatalf("seeding manifestUrl: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/update/settings", bytes.NewReader([]byte(`{"manifestUrl": ""}`)))
	rec := httptest.NewRecorder()
	s.handleSystemUpdateSettingsSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := s.store.Snapshot().Update.ManifestURL; got != "" {
		t.Errorf("expected manifestUrl cleared, got %q", got)
	}
}

func TestHandleSystemUpdateCheck_RequiresConfiguredManifestURL(t *testing.T) {
	s := newTestServerWithUpdateChecker(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/check", nil)
	rec := httptest.NewRecorder()
	s.handleSystemUpdateCheck(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when manifestUrl is unset, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleSystemUpdateCheck_RealHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(selfupdate.Manifest{
			Version: "v9.9.9",
			Notes:   "測試用的新版本",
			Assets:  map[string]selfupdate.Asset{},
		})
	}))
	defer srv.Close()

	s := newTestServerWithUpdateChecker(t)
	s.updateHTTPClient = srv.Client()
	if err := s.store.Update(func(st *state.State) error {
		st.Update.ManifestURL = srv.URL
		return nil
	}); err != nil {
		t.Fatalf("seeding manifestUrl: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/check", nil)
	rec := httptest.NewRecorder()
	s.handleSystemUpdateCheck(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp systemUpdateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.LatestVersion != "v9.9.9" {
		t.Errorf("expected latestVersion v9.9.9, got %q", resp.LatestVersion)
	}
	if resp.Notes != "測試用的新版本" {
		t.Errorf("expected notes to propagate, got %q", resp.Notes)
	}
	if resp.CheckedAt == "" {
		t.Error("expected checkedAt to be populated after a real check")
	}
}

func TestHandleSystemUpdateApply_RequiresConfiguredManifestURL(t *testing.T) {
	s := newTestServerWithUpdateChecker(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/apply", nil)
	rec := httptest.NewRecorder()
	s.handleSystemUpdateApply(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when manifestUrl is unset, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleSystemUpdateApply_EndToEndSwapsRealFileAndSignalsRestart 是
// Phase 17 對「套用更新」這條最敏感路徑的端到端驗證:真的架一個
// httptest.Server 同時服務 manifest.json 跟一份「新版執行檔」的內容,
// 對後者用 crypto/sha256 算出真正的 checksum(不是憑空編一個字串),
// 讓 handleSystemUpdateApply 走完整條 FetchManifest ->
// DownloadAndVerify -> ApplyUpdate 的路徑,操作 t.TempDir() 底下的真實
// 檔案(不是 mock 檔案系統)。確認:(1) 磁碟上「目前執行檔」路徑的內容
// 真的變成新版本的內容、(2) 舊內容被備份到 .previous、(3) 成功之後
// s.restartRequested 收到一次訊號,供 cmd/gonasd/main.go 觸發重啟。
func TestHandleSystemUpdateApply_EndToEndSwapsRealFileAndSignalsRestart(t *testing.T) {
	oldContent := []byte("#!/bin/sh\necho old-version\n")
	newContent := []byte("#!/bin/sh\necho new-version\n")
	sum := sha256.Sum256(newContent)
	checksum := hex.EncodeToString(sum[:])

	// handleSystemUpdateApply 內部用 runtime.GOOS/GOARCH 組出資產鍵值去
	// 查 Manifest(見 selfupdate.Manifest.AssetFor),所以這裡的假
	// Manifest 也要用「這台機器實際的平台」當鍵值,測試才能在任何
	// GOOS/GOARCH 底下都正確地驗證到「有找到對應資產、真的下載並套用」
	//這條路徑,而不是綁死在某一種平台上。
	platformKey := runtime.GOOS + "-" + runtime.GOARCH

	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(selfupdate.Manifest{
			Version: "v9.9.9",
			Assets: map[string]selfupdate.Asset{
				platformKey: {URL: "http://" + r.Host + "/gonasd-new", SHA256: checksum},
			},
		})
	})
	mux.HandleFunc("/gonasd-new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "gonasd")
	if err := os.WriteFile(execPath, oldContent, 0o755); err != nil {
		t.Fatalf("seeding fake current executable: %v", err)
	}

	s := newTestServerWithUpdateChecker(t)
	s.updateHTTPClient = srv.Client()
	s.updateExecPathFunc = func() (string, error) { return execPath, nil }
	if err := s.store.Update(func(st *state.State) error {
		st.Update.ManifestURL = srv.URL + "/manifest.json"
		return nil
	}); err != nil {
		t.Fatalf("seeding manifestUrl: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/apply", nil)
	rec := httptest.NewRecorder()
	s.handleSystemUpdateApply(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// 這裡特別驗證訊號帶的路徑「就是」套用前解析好的 execPath 字串,而
	// 不是隨便一個非空字串——這正是 Phase 17 真實踩到過的一個坑:如果
	// main.go 收到訊號後自己重新呼叫 os.Executable(),在 Linux 上會因為
	// ApplyUpdate 把原本的執行檔 rename 成 ".previous" 而讀到錯的路徑
	// (/proc/self/exe 這個 magic symlink 會跟著 rename 走),見
	// internal/api.Server 裡 restartRequested 欄位的完整說明。這個測試
	// 只能驗證「送出的路徑值是對的」,實際的 /proc/self/exe 行為需要真的
	// 執行一個程序才能重現,那部分的驗證見 docs/REAL_HARDWARE_TESTING.md
	// 記錄的手動端到端驗證過程。
	select {
	case gotPath := <-s.restartRequested:
		if gotPath != execPath {
			t.Errorf("expected restart signal to carry the resolved exec path %q, got %q", execPath, gotPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a restart signal after applying the update")
	}

	gotContent, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading swapped executable: %v", err)
	}
	if string(gotContent) != string(newContent) {
		t.Errorf("expected executable content to be swapped to the new version, got %q", gotContent)
	}

	backupContent, err := os.ReadFile(execPath + ".previous")
	if err != nil {
		t.Fatalf("reading backup file: %v", err)
	}
	if string(backupContent) != string(oldContent) {
		t.Errorf("expected .previous backup to hold the old content, got %q", backupContent)
	}
}

func TestHandleSystemUpdateApply_AlreadyInProgressReturnsConflict(t *testing.T) {
	s := newTestServerWithUpdateChecker(t)
	if err := s.store.Update(func(st *state.State) error {
		st.Update.ManifestURL = "https://example.invalid/manifest.json"
		return nil
	}); err != nil {
		t.Fatalf("seeding manifestUrl: %v", err)
	}
	s.applyStatus = applyUpdateStatus{Stage: applyStageDownloading, StartedAt: time.Now()}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/apply", nil)
	rec := httptest.NewRecorder()
	s.handleSystemUpdateApply(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while an apply is already in progress, got %d: %s", rec.Code, rec.Body.String())
	}
}
