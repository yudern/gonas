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
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_ = json.NewEncoder(w).Encode(Manifest{Version: "v1.0.0", Assets: map[string]Asset{}})
	}))
	defer srv.Close()

	c := NewChecker(discardLogger())
	c.Start(context.Background(), 5*time.Millisecond, "v1.0.0", func() string { return srv.URL }, srv.Client())

	time.Sleep(30 * time.Millisecond)
	c.Stop()

	if count < 2 {
		t.Errorf("expected at least 2 checks within the wait window, got %d", count)
	}

	after := count
	time.Sleep(20 * time.Millisecond)
	if count != after {
		t.Error("expected no further checks after Stop()")
	}
}
