package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestManifest_AssetFor(t *testing.T) {
	m := Manifest{
		Version: "v1.0.0",
		Assets: map[string]Asset{
			"linux-amd64": {URL: "https://example.com/amd64", SHA256: "abc"},
		},
	}

	asset, err := m.AssetFor("linux", "amd64")
	if err != nil {
		t.Fatalf("AssetFor returned error: %v", err)
	}
	if asset.URL != "https://example.com/amd64" {
		t.Errorf("unexpected asset: %+v", asset)
	}

	if _, err := m.AssetFor("linux", "arm64"); err == nil {
		t.Error("expected error for missing platform asset, got nil")
	}

	empty := Manifest{Assets: map[string]Asset{"linux-amd64": {SHA256: "abc"}}}
	if _, err := empty.AssetFor("linux", "amd64"); err == nil {
		t.Error("expected error for asset with empty URL, got nil")
	}
}

// TestFetchManifest_RealHTTPServer serves a real manifest over a real
// httptest server (not a mocked interface) and confirms FetchManifest
// parses it correctly — consistent with the project's "verify against
// something real" philosophy even when the "real" thing is a local server.
func TestFetchManifest_RealHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Manifest{
			Version: "v2.3.4",
			Notes:   "test release notes",
			Assets: map[string]Asset{
				"linux-amd64": {URL: "https://example.com/gonasd-linux-amd64", SHA256: "deadbeef"},
			},
		})
	}))
	defer srv.Close()

	m, err := FetchManifest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchManifest returned error: %v", err)
	}
	if m.Version != "v2.3.4" {
		t.Errorf("expected version v2.3.4, got %q", m.Version)
	}
	if m.Notes != "test release notes" {
		t.Errorf("expected notes to round-trip, got %q", m.Notes)
	}
}

func TestFetchManifest_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := FetchManifest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Error("expected error for 404 response, got nil")
	}
}

func TestFetchManifest_MissingVersionField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"assets":{}}`))
	}))
	defer srv.Close()

	if _, err := FetchManifest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Error("expected error for manifest missing a version field, got nil")
	}
}

func TestFetchManifest_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	if _, err := FetchManifest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in                  string
		major, minor, patch int
		ok                  bool
	}{
		{"v1.2.3", 1, 2, 3, true},
		{"1.2.3", 1, 2, 3, true},
		{"v1.2.3-4-gabcdef1", 1, 2, 3, true},
		{"v1.2.3-dirty", 1, 2, 3, true},
		{"v0.0.1", 0, 0, 1, true},
		{"dev", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"not-a-version", 0, 0, 0, false},
	}
	for _, tt := range cases {
		major, minor, patch, ok := ParseVersion(tt.in)
		if ok != tt.ok {
			t.Errorf("ParseVersion(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if major != tt.major || minor != tt.minor || patch != tt.patch {
			t.Errorf("ParseVersion(%q) = (%d,%d,%d), want (%d,%d,%d)", tt.in, major, minor, patch, tt.major, tt.minor, tt.patch)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, candidate string
		newer, comparable  bool
	}{
		{"v1.0.0", "v1.0.1", true, true},
		{"v1.0.0", "v1.1.0", true, true},
		{"v1.0.0", "v2.0.0", true, true},
		{"v1.0.1", "v1.0.0", false, true},
		{"v1.0.0", "v1.0.0", false, true},
		{"v1.0.0-3-gabcdef", "v1.0.1", true, true},
		// "dev" builds are never told they have an update available —
		// there's no meaningful ordering between "dev" and a real release.
		{"dev", "v1.0.0", false, false},
		{"v1.0.0", "dev", false, false},
	}
	for _, tt := range cases {
		newer, comparable := IsNewer(tt.current, tt.candidate)
		if newer != tt.newer || comparable != tt.comparable {
			t.Errorf("IsNewer(%q, %q) = (%v,%v), want (%v,%v)", tt.current, tt.candidate, newer, comparable, tt.newer, tt.comparable)
		}
	}
}

// TestDownloadAndVerify_RealServerRealChecksum builds a real (small) fake
// "binary" payload, serves it from a real HTTP server, computes its real
// SHA-256, and confirms DownloadAndVerify accepts it and rejects a
// deliberately wrong checksum — using genuine crypto/sha256 computation on
// both ends, not a stubbed comparison.
func TestDownloadAndVerify_RealServerRealChecksum(t *testing.T) {
	payload := []byte("this is a fake gonasd binary for testing, but the checksum below is real")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	destDir := t.TempDir()

	t.Run("correct checksum succeeds", func(t *testing.T) {
		path, err := DownloadAndVerify(context.Background(), srv.Client(), Asset{URL: srv.URL, SHA256: hexSum}, destDir)
		if err != nil {
			t.Fatalf("DownloadAndVerify returned error: %v", err)
		}
		defer os.Remove(path)

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading downloaded file: %v", err)
		}
		if string(got) != string(payload) {
			t.Error("downloaded file content does not match served payload")
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat downloaded file: %v", err)
		}
		if info.Mode()&0o111 == 0 {
			t.Error("expected downloaded file to be executable")
		}
	})

	t.Run("wrong checksum fails and cleans up", func(t *testing.T) {
		path, err := DownloadAndVerify(context.Background(), srv.Client(), Asset{URL: srv.URL, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}, destDir)
		if err == nil {
			os.Remove(path)
			t.Fatal("expected checksum mismatch error, got nil")
		}
		if path != "" {
			t.Errorf("expected empty path on failure, got %q", path)
		}

		entries, err := os.ReadDir(destDir)
		if err != nil {
			t.Fatalf("reading destDir: %v", err)
		}
		for _, e := range entries {
			t.Errorf("expected no leftover temp files after checksum failure, found %q", e.Name())
		}
	})
}

func TestDownloadAndVerify_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := DownloadAndVerify(context.Background(), srv.Client(), Asset{URL: srv.URL, SHA256: "anything"}, t.TempDir()); err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

// TestApplyUpdate_ReplacesFileAndKeepsBackup verifies the core file-swap
// mechanism using real files on a real filesystem (t.TempDir()): the
// current "binary" is replaced by the verified one, and a .previous backup
// of the original is left behind for manual rollback.
func TestApplyUpdate_ReplacesFileAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "gonasd")
	if err := os.WriteFile(currentPath, []byte("old version content"), 0o755); err != nil {
		t.Fatalf("writing current binary: %v", err)
	}

	newPath := filepath.Join(dir, ".gonasd-update-tmp")
	if err := os.WriteFile(newPath, []byte("new version content"), 0o755); err != nil {
		t.Fatalf("writing new binary: %v", err)
	}

	backupPath, err := ApplyUpdate(currentPath, newPath)
	if err != nil {
		t.Fatalf("ApplyUpdate returned error: %v", err)
	}
	if backupPath == "" {
		t.Fatal("expected a non-empty backup path")
	}

	gotCurrent, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("reading current path after update: %v", err)
	}
	if string(gotCurrent) != "new version content" {
		t.Errorf("expected current path to hold new content, got %q", gotCurrent)
	}

	gotBackup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("reading backup path: %v", err)
	}
	if string(gotBackup) != "old version content" {
		t.Errorf("expected backup to hold the original content, got %q", gotBackup)
	}

	if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected the temp new-binary path to no longer exist after being renamed into place")
	}
}

func TestApplyUpdate_MissingNewBinary_RestoresOriginalAndErrors(t *testing.T) {
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "gonasd")
	if err := os.WriteFile(currentPath, []byte("old version content"), 0o755); err != nil {
		t.Fatalf("writing current binary: %v", err)
	}

	nonExistent := filepath.Join(dir, "does-not-exist")
	_, err := ApplyUpdate(currentPath, nonExistent)
	if err == nil {
		t.Fatal("expected error when the verified binary path does not exist")
	}

	// The original binary must still be there (and still be readable under
	// its original name) — a failed update must not leave gonasd with no
	// executable at all.
	got, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("expected original binary to be restored/left in place, but reading it failed: %v", err)
	}
	if string(got) != "old version content" {
		t.Errorf("expected original content preserved, got %q", got)
	}
}

func TestRollbackToBackup_RestoresPreviousAndKeepsRolledBackCopy(t *testing.T) {
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "gonasd")
	backupPath := currentPath + ".previous"

	if err := os.WriteFile(currentPath, []byte("broken new version"), 0o755); err != nil {
		t.Fatalf("writing current binary: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte("known good old version"), 0o755); err != nil {
		t.Fatalf("writing backup binary: %v", err)
	}

	rolledBackFromPath, err := RollbackToBackup(currentPath)
	if err != nil {
		t.Fatalf("RollbackToBackup returned error: %v", err)
	}
	if rolledBackFromPath == "" {
		t.Fatal("expected a non-empty rolled-back-from path")
	}

	gotCurrent, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("reading current path after rollback: %v", err)
	}
	if string(gotCurrent) != "known good old version" {
		t.Errorf("expected current path to hold the restored backup content, got %q", gotCurrent)
	}

	gotRolledBack, err := os.ReadFile(rolledBackFromPath)
	if err != nil {
		t.Fatalf("reading rolled-back-from path: %v", err)
	}
	if string(gotRolledBack) != "broken new version" {
		t.Errorf("expected the rolled-back-from copy to hold the pre-rollback content, got %q", gotRolledBack)
	}

	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected the .previous path to no longer exist after being renamed into place")
	}
}

func TestRollbackToBackup_MissingBackup_LeavesCurrentUntouchedAndErrors(t *testing.T) {
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "gonasd")
	if err := os.WriteFile(currentPath, []byte("only version around"), 0o755); err != nil {
		t.Fatalf("writing current binary: %v", err)
	}

	_, err := RollbackToBackup(currentPath)
	if err == nil {
		t.Fatal("expected an error when no .previous backup exists")
	}

	got, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("expected current binary untouched and readable, but reading it failed: %v", err)
	}
	if string(got) != "only version around" {
		t.Errorf("expected current binary content unchanged, got %q", got)
	}
}

func TestChecker_SkipsCheckWhenManifestURLEmpty(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(Manifest{Version: "v9.9.9", Assets: map[string]Asset{}})
	}))
	defer srv.Close()

	c := NewChecker(discardLogger())
	c.Start(context.Background(), 5*time.Millisecond, "v1.0.0", func() string { return "" }, srv.Client())
	defer c.Stop()

	time.Sleep(30 * time.Millisecond)
	c.Stop()

	if called {
		t.Error("expected Checker to never call the manifest server when getManifestURL returns empty")
	}
	if result := c.Latest(); !result.CheckedAt.IsZero() {
		t.Errorf("expected no check result recorded, got %+v", result)
	}
}

func TestChecker_DetectsUpdateAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{Version: "v1.5.0", Notes: "new stuff", Assets: map[string]Asset{}})
	}))
	defer srv.Close()

	c := NewChecker(discardLogger())
	c.Start(context.Background(), time.Hour, "v1.0.0", func() string { return srv.URL }, srv.Client())
	defer c.Stop()

	deadline := time.Now().Add(time.Second)
	var result CheckResult
	for time.Now().Before(deadline) {
		result = c.Latest()
		if !result.CheckedAt.IsZero() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if result.CheckedAt.IsZero() {
		t.Fatal("expected an immediate check on Start, got no result within 1s")
	}
	if !result.UpdateAvailable {
		t.Errorf("expected UpdateAvailable=true for v1.0.0 -> v1.5.0, got %+v", result)
	}
	if result.LatestVersion != "v1.5.0" {
		t.Errorf("expected LatestVersion v1.5.0, got %q", result.LatestVersion)
	}
	if result.Notes != "new stuff" {
		t.Errorf("expected notes to propagate, got %q", result.Notes)
	}
}

func TestChecker_CheckNow_UpdatesLatestImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{Version: "v2.0.0", Notes: "on demand", Assets: map[string]Asset{}})
	}))
	defer srv.Close()

	c := NewChecker(discardLogger())
	// 故意不呼叫 Start——CheckNow 應該可以在背景 ticker 完全沒啟動的
	// 情況下獨立運作,這正是 handleSystemUpdateCheck(使用者按下「立即
	// 檢查」)會用到的呼叫方式。
	result := c.CheckNow(context.Background(), "v1.0.0", srv.URL, srv.Client())

	if result.CheckedAt.IsZero() {
		t.Fatal("expected CheckNow to return a populated result")
	}
	if !result.UpdateAvailable || result.LatestVersion != "v2.0.0" {
		t.Errorf("expected an available v2.0.0 update, got %+v", result)
	}

	// Latest() 應該立刻反映 CheckNow 剛寫入的結果，不需要等任何背景
	// goroutine。
	cached := c.Latest()
	if cached.LatestVersion != "v2.0.0" || !cached.UpdateAvailable {
		t.Errorf("expected Latest() to reflect the CheckNow result, got %+v", cached)
	}
}

func TestChecker_RecordsErrorOnFetchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewChecker(discardLogger())
	c.Start(context.Background(), time.Hour, "v1.0.0", func() string { return srv.URL }, srv.Client())
	defer c.Stop()

	deadline := time.Now().Add(time.Second)
	var result CheckResult
	for time.Now().Before(deadline) {
		result = c.Latest()
		if !result.CheckedAt.IsZero() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if result.Err == nil {
		t.Error("expected an error to be recorded for a failing manifest fetch")
	}
	if result.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when the check itself failed")
	}
}

func TestChecker_StopBeforeStart_DoesNotPanic(t *testing.T) {
	c := NewChecker(discardLogger())
	c.Stop()
}

func TestChecker_RunsRepeatedlyAndStopsCleanly(t *testing.T) {
	// count 是 atomic.Int64,不是普通的 int——這裡故意這樣寫是因為第
	// 十七輪覆閱跑 `go test ./... -race -count=1` 時,真的抓到一次
	// (非每次都會觸發,是時序相關的間歇性 flake)DATA RACE:htttest 的
	// handler 在它自己的連線 goroutine 裡執行 `count++`,而這個測試的
	// 主 goroutine 在 `c.Stop()` 回傳之後直接讀 `count`(`after := count`
	// 跟後面的 `count != after`)。雖然邏輯上 Stop() 保證了「回傳之後不會
	// 再有新的檢查」(Checker.Stop() 會等待背景 goroutine 真的執行完
	// 目前這一輪 check() 才關閉 done channel,見 selfupdate.go),但這只
	// 保證「不會再有新的 HTTP request 被發出」,不保證 race detector
	// 能沿著「HTTP round trip 完成」這條路徑辨識出足夠的
	// happens-before 關係——普通的 `int` 在兩個 goroutine 之間沒有任何
	// atomic/mutex 保護,就算功能上恰好每次都是對的,對 race detector
	// 來說仍然是未定義行為,而且是真的會被抓到的(不是誤報,實測跑十幾
	// 次會出現一次)。改用 atomic.Int64 讓讀寫都走原子操作,才是正確
	// 的修法,而不是靠「反正這個測試場景下不會真的同時存取」的假設。
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		_ = json.NewEncoder(w).Encode(Manifest{Version: "v1.0.0", Assets: map[string]Asset{}})
	}))
	defer srv.Close()

	c := NewChecker(discardLogger())
	c.Start(context.Background(), 5*time.Millisecond, "v1.0.0", func() string { return srv.URL }, srv.Client())

	time.Sleep(30 * time.Millisecond)
	c.Stop()

	if got := count.Load(); got < 2 {
		t.Errorf("expected at least 2 checks within the wait window, got %d", got)
	}

	// Stop() 保證 checker goroutine 不會再「發出」新請求,但計數是由
	// httptest 的 server goroutine 加的——Stop() 回傳的當下,可能還有
	// 一個「取消前就已經送出」的請求正在 server 端處理、稍後才 count.Add。
	// 這不是功能 bug(checker 確實已經停了),是這個測試把計數放在
	// server 端造成的量測窗。先睡一小段讓任何在途請求落地,再取
	// after 基準,然後才驗「之後不再增加」——避免在滿載 CPU 下偶發假紅。
	time.Sleep(20 * time.Millisecond)
	after := count.Load()
	time.Sleep(30 * time.Millisecond)
	if got := count.Load(); got != after {
		t.Errorf("expected no further checks after Stop(), count went from %d to %d", after, got)
	}
}

// 第三十輪覆核回歸:跨網路的明文 http 更新來源必須被拒絕(loopback 除外,
// 由既有的 httptest 測試涵蓋)。
func TestFetchManifest_RejectsPlaintextNonLoopback(t *testing.T) {
	if _, err := FetchManifest(context.Background(), http.DefaultClient, "http://example.com/manifest.json"); err == nil {
		t.Fatal("expected FetchManifest to reject a plaintext non-loopback http URL")
	}
}

func TestDownloadAndVerify_RejectsPlaintextNonLoopback(t *testing.T) {
	if _, err := DownloadAndVerify(context.Background(), http.DefaultClient, Asset{URL: "http://example.com/bin", SHA256: "x"}, t.TempDir()); err == nil {
		t.Fatal("expected DownloadAndVerify to reject a plaintext non-loopback http URL")
	}
}

// TestSecureRedirect_RejectsDowngrade(第五十二輪 S-1):CheckRedirect 政策
// 必須在每一跳都重跑 requireHTTPS —— 只驗初始 URL 不夠,因為 https 伺服器
// 可以 302 把我們導去 http://。這裡直接測 secureRedirect(NewHTTPClient
// 掛的就是它)。
func TestSecureRedirect_RejectsDowngrade(t *testing.T) {
	mk := func(rawurl string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, rawurl, nil)
		if err != nil {
			t.Fatalf("building request for %q: %v", rawurl, err)
		}
		return req
	}

	// 降級到跨網路的明文 http:必須擋。
	if err := secureRedirect(mk("http://evil.example/bin"), nil); err == nil {
		t.Fatal("expected a redirect to plaintext http:// to be rejected")
	}
	// 續留在 https:放行。
	if err := secureRedirect(mk("https://ok.example/bin"), nil); err != nil {
		t.Fatalf("expected an https:// redirect to be allowed, got %v", err)
	}
	// http 指向 loopback(本機鏡像/測試)放行,跟 requireHTTPS 的例外一致。
	if err := secureRedirect(mk("http://127.0.0.1:9000/bin"), nil); err != nil {
		t.Fatalf("expected a loopback http:// redirect to be allowed, got %v", err)
	}
	// 太多跳:即使目標是 https 也要中止,避免重導向迴圈。
	via := make([]*http.Request, 10)
	if err := secureRedirect(mk("https://ok.example/bin"), via); err == nil {
		t.Fatal("expected redirect chain longer than 10 hops to be stopped")
	}
}

// TestNewHTTPClient_WiresSecureRedirect 確認正式路徑用的建構子真的把
// CheckRedirect 掛上去了(不是只有 secureRedirect 本身正確、但沒人用它)。
func TestNewHTTPClient_WiresSecureRedirect(t *testing.T) {
	c := NewHTTPClient(0)
	if c.CheckRedirect == nil {
		t.Fatal("expected NewHTTPClient to set a CheckRedirect policy")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://evil.example/bin", nil)
	if err := c.CheckRedirect(req, nil); err == nil {
		t.Fatal("expected the wired CheckRedirect to reject a plaintext downgrade")
	}
}

func TestDownloadAndVerifyWithProgress_ReportsBytes(t *testing.T) {
	payload := make([]byte, 100000)
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	var last, total int64
	calls := 0
	path, err := DownloadAndVerifyWithProgress(context.Background(), srv.Client(), Asset{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, t.TempDir(), func(d, tt int64) {
		calls++
		last, total = d, tt
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if calls < 2 || last != 100000 || total != 100000 {
		t.Errorf("progress calls=%d last=%d total=%d", calls, last, total)
	}
}
