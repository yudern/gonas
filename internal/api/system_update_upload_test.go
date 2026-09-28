package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// versionRunner 是給「離線上傳更新」測試用的假 runner:對 `--version` 的呼叫
// 回一段可設定的輸出/錯誤,其餘呼叫都成功。用來驅動
// verifyUploadedGonasdRuns 的兩條路(是可用 gonasd / 不是)。
type versionRunner struct {
	versionOut []byte
	versionErr error
}

func (r *versionRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "--version" {
			return r.versionOut, r.versionErr
		}
	}
	return nil, nil
}
func (r *versionRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

// elfMachineForGOARCH 是測試端複製一份 selfupdate.elfMachineForGoarch 的對照
// (那個是未匯出的),用來造出「本機架構相符」的最小 ELF 檔頭。
func elfMachineForGOARCH(goarch string) (uint16, bool) {
	switch goarch {
	case "amd64":
		return 0x3E, true
	case "arm64":
		return 0xB7, true
	case "arm":
		return 0x28, true
	}
	return 0, false
}

func hostELFBytes(t *testing.T) []byte {
	t.Helper()
	m, ok := elfMachineForGOARCH(runtime.GOARCH)
	if !ok {
		t.Skipf("no ELF machine mapping for GOARCH %q", runtime.GOARCH)
	}
	b := make([]byte, 64)
	b[0], b[1], b[2], b[3] = 0x7F, 'E', 'L', 'F'
	b[4] = 2 // ELFCLASS64
	b[5] = 1 // little-endian
	b[18] = byte(m & 0xFF)
	b[19] = byte(m >> 8)
	return b
}

// multipartUpload 把 content 包成一個帶單一檔案欄位("file")的 multipart 請求。
func multipartUpload(t *testing.T, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "gonasd-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// 端到端:上傳一份「檔頭正確、--version 也自稱 gonasd」的執行檔,應該:
// (1) 回 202、(2) 磁碟上的執行檔內容真的被換成上傳的內容、(3) 舊內容被備份到
// .previous、(4) restartRequested 收到一次(值就是解析出的 execPath)。
func TestHandleSystemUpdateUpload_EndToEndSwapsAndSignalsRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("upload path verifies a linux ELF header")
	}
	oldContent := []byte("OLD-BINARY-CONTENT")
	newContent := hostELFBytes(t)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "gonasd")
	if err := os.WriteFile(execPath, oldContent, 0o755); err != nil {
		t.Fatalf("seeding fake current executable: %v", err)
	}

	s := newTestServerWithUpdateChecker(t)
	s.updateExecPathFunc = func() (string, error) { return execPath, nil }
	s.runner = &versionRunner{versionOut: []byte("gonasd v9.9.9 (commit abc, built now, linux/amd64)\n")}

	rec := httptest.NewRecorder()
	s.handleSystemUpdateUpload(rec, multipartUpload(t, newContent))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case gotPath := <-s.restartRequested:
		if gotPath != execPath {
			t.Errorf("expected restart signal to carry exec path %q, got %q", execPath, gotPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a restart signal after uploading the update")
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading swapped executable: %v", err)
	}
	if !bytes.Equal(got, newContent) {
		t.Error("expected the executable to be swapped to the uploaded content")
	}
	backup, err := os.ReadFile(execPath + ".previous")
	if err != nil {
		t.Fatalf("reading .previous backup: %v", err)
	}
	if !bytes.Equal(backup, oldContent) {
		t.Error("expected .previous to hold the old content")
	}
}

// 上傳的不是 ELF(例如整個 tar.gz / 文字檔):在替換前就要被擋下,回 400,
// 執行檔不能被動到,也不能送出重啟訊號。
func TestHandleSystemUpdateUpload_RejectsNonELFWithoutTouchingExec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("upload path verifies a linux ELF header")
	}
	oldContent := []byte("OLD-BINARY-CONTENT")
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "gonasd")
	if err := os.WriteFile(execPath, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}

	s := newTestServerWithUpdateChecker(t)
	s.updateExecPathFunc = func() (string, error) { return execPath, nil }
	s.runner = &versionRunner{versionOut: []byte("gonasd\n")}

	rec := httptest.NewRecorder()
	s.handleSystemUpdateUpload(rec, multipartUpload(t, []byte("this is not an ELF binary")))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-ELF upload, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := os.ReadFile(execPath)
	if !bytes.Equal(got, oldContent) {
		t.Error("the running executable must not be touched when the upload is rejected")
	}
	select {
	case <-s.restartRequested:
		t.Error("must not signal a restart when the upload is rejected")
	default:
	}
}

// 上傳的檔頭正確、但 `--version` 跑不起來(不是可用的 gonasd):brick 防護要
// 擋下,回 400,執行檔不能被替換。
func TestHandleSystemUpdateUpload_RejectsWhenVersionSmokeTestFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("upload path verifies a linux ELF header")
	}
	oldContent := []byte("OLD-BINARY-CONTENT")
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "gonasd")
	if err := os.WriteFile(execPath, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}

	s := newTestServerWithUpdateChecker(t)
	s.updateExecPathFunc = func() (string, error) { return execPath, nil }
	// --version 直接失敗,模擬「傳了一個跑不起來的東西」。
	s.runner = &versionRunner{versionErr: context.DeadlineExceeded}

	rec := httptest.NewRecorder()
	s.handleSystemUpdateUpload(rec, multipartUpload(t, hostELFBytes(t)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when the --version smoke test fails, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := os.ReadFile(execPath)
	if !bytes.Equal(got, oldContent) {
		t.Error("the running executable must not be replaced when the smoke test fails")
	}
}

// 沒有檔案欄位的上傳:回 400。
func TestHandleSystemUpdateUpload_NoFile(t *testing.T) {
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "gonasd")
	if err := os.WriteFile(execPath, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWithUpdateChecker(t)
	s.updateExecPathFunc = func() (string, error) { return execPath, nil }
	s.runner = &versionRunner{}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("notafile", "x")
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/update/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	rec := httptest.NewRecorder()
	s.handleSystemUpdateUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no file part is present, got %d: %s", rec.Code, rec.Body.String())
	}
}
