package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// noopRunner 假裝所有外部指令(mergerfs mount 等)都成功，讓測試可以把
// *storage.Array 推進到 StateStarted，而不需要真的執行任何系統指令——
// 這裡要測的是 files_handlers.go 的路由/請求解析/錯誤對應邏輯，不是
// storage.MountPool 本身(那已經在 internal/storage 有自己的測試)。
type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return nil, nil
}
func (noopRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, nil
}

// newTestServerWithFiles 建立一個檔案管理員可以直接使用的 Server:陣列
// 的掛載點指向一個真正的臨時目錄，陣列狀態推進到 started。回傳的 root
// 就是那個臨時目錄，測試可以直接用 os 套件在裡面放測試檔案，再透過
// handler 呼叫驗證行為。
func newTestServerWithFiles(t *testing.T) (*Server, string) {
	t.Helper()
	s := newTestServer(t)
	root := t.TempDir()

	pool := storage.PoolConfig{
		Name:         "testpool",
		DataDisks:    []string{"/fake/disk1"},
		ParityDisks:  []string{"/fake/parity1"},
		MountPoint:   root,
		ContentFiles: []string{"/fake/disk1", "/fake/disk2"},
	}
	if err := s.store.Update(func(st *state.State) error {
		st.Pool = &pool
		return nil
	}); err != nil {
		t.Fatalf("seeding pool config: %v", err)
	}
	s.array = storage.NewArray(pool)
	if err := s.array.Start(context.Background(), noopRunner{}); err != nil {
		t.Fatalf("starting fake array: %v", err)
	}
	return s, root
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHandleFilesStatus_UnavailableWithoutPool(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/status", nil)
	rec := httptest.NewRecorder()

	s.handleFilesStatus(rec, req)

	var resp fileManagerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Available {
		t.Error("expected file manager to be unavailable when no pool is configured")
	}
}

func TestHandleFilesStatus_AvailableWhenArrayStarted(t *testing.T) {
	s, _ := newTestServerWithFiles(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/status", nil)
	rec := httptest.NewRecorder()

	s.handleFilesStatus(rec, req)

	var resp fileManagerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Available {
		t.Errorf("expected file manager to be available, got reason %q", resp.Reason)
	}
}

func TestHandleFilesList_ReturnsEntries(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "hi")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/list?path=", nil)
	rec := httptest.NewRecorder()
	s.handleFilesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var entries []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0]["name"] != "a.txt" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

func TestHandleFilesList_NoPoolConfiguredIsConflict(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/list", nil)
	rec := httptest.NewRecorder()

	s.handleFilesList(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleFilesMkdir(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	body, _ := json.Marshal(mkdirRequest{Path: "newdir"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/mkdir", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleFilesMkdir(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(filepath.Join(root, "newdir")); err != nil || !info.IsDir() {
		t.Errorf("expected newdir to exist: %v", err)
	}
}

func TestHandleFilesMove(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "content")

	body, _ := json.Marshal(moveOrCopyRequest{From: "a.txt", To: "b.txt"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleFilesMove(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err != nil {
		t.Errorf("expected b.txt to exist after move: %v", err)
	}
}

func TestHandleFilesCopy(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "content")

	body, _ := json.Marshal(moveOrCopyRequest{From: "a.txt", To: "b.txt"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/copy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleFilesCopy(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Error("expected source to still exist after copy")
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err != nil {
		t.Error("expected copy destination to exist")
	}
}

func TestHandleFilesDelete_DefaultsToSoftDelete(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "content")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/item?path=a.txt", nil)
	rec := httptest.NewRecorder()
	s.handleFilesDelete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from its original location")
	}

	trashReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/trash", nil)
	trashRec := httptest.NewRecorder()
	s.handleFilesTrashList(trashRec, trashReq)
	var trash []map[string]any
	if err := json.NewDecoder(trashRec.Body).Decode(&trash); err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 {
		t.Errorf("expected one item in trash, got %+v", trash)
	}
}

func TestHandleFilesDelete_PermanentSkipsTrash(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "content")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/item?path=a.txt&permanent=true", nil)
	rec := httptest.NewRecorder()
	s.handleFilesDelete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	trashReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/trash", nil)
	trashRec := httptest.NewRecorder()
	s.handleFilesTrashList(trashRec, trashReq)
	var trash []map[string]any
	if err := json.NewDecoder(trashRec.Body).Decode(&trash); err != nil {
		t.Fatal(err)
	}
	if len(trash) != 0 {
		t.Errorf("expected no trash entries for a permanent delete, got %+v", trash)
	}
}

func TestHandleFilesDelete_MissingPathIsBadRequest(t *testing.T) {
	s, _ := newTestServerWithFiles(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/item", nil)
	rec := httptest.NewRecorder()
	s.handleFilesDelete(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleFilesDownload_ServesFileContent(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "hello download")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=a.txt", nil)
	rec := httptest.NewRecorder()
	s.handleFilesDownload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello download" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Error("expected a Content-Disposition header")
	}
}

func TestHandleFilesDownload_DirectoryIsBadRequest(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=d", nil)
	rec := httptest.NewRecorder()
	s.handleFilesDownload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleFilesDownloadZip_ProducesZipForDirectory(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "docs", "a.txt"), "aaa")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/download-zip?path=docs", nil)
	rec := httptest.NewRecorder()
	s.handleFilesDownloadZip(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("expected zip content type, got %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty zip body")
	}
}

func TestHandleFilesSearch(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "report.txt"), "x")
	writeTestFile(t, filepath.Join(root, "sub", "other-report.txt"), "x")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/search?q=report", nil)
	rec := httptest.NewRecorder()
	s.handleFilesSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 {
		t.Errorf("expected 2 matches, got %+v", result.Entries)
	}
}

func TestHandleFilesText_RoundTrip(t *testing.T) {
	s, _ := newTestServerWithFiles(t)

	writeBody, _ := json.Marshal(textFileResponse{Content: "hello text"})
	writeReq := httptest.NewRequest(http.MethodPut, "/api/v1/files/text?path=notes.txt", bytes.NewReader(writeBody))
	writeRec := httptest.NewRecorder()
	s.handleFilesWriteText(writeRec, writeReq)
	if writeRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from write, got %d: %s", writeRec.Code, writeRec.Body.String())
	}

	readReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/text?path=notes.txt", nil)
	readRec := httptest.NewRecorder()
	s.handleFilesReadText(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from read, got %d: %s", readRec.Code, readRec.Body.String())
	}
	var resp textFileResponse
	if err := json.NewDecoder(readRec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello text" {
		t.Errorf("got %q", resp.Content)
	}
}

func TestHandleFilesUpload_SingleFile(t *testing.T) {
	s, root := newTestServerWithFiles(t)

	body, contentType := multipartBody(t, map[string]string{"myfile.txt": "uploaded content"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	s.handleFilesUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "myfile.txt"))
	if err != nil || string(data) != "uploaded content" {
		t.Fatalf("got %q, err %v", data, err)
	}
}

func TestHandleFilesUpload_MultipleFilesInOneRequest(t *testing.T) {
	s, root := newTestServerWithFiles(t)

	body, contentType := multipartBody(t, map[string]string{
		"one.txt": "first",
		"two.txt": "second",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	s.handleFilesUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{"one.txt": "first", "two.txt": "second"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Errorf("file %q: got %q, err %v", name, data, err)
		}
	}
}

func TestHandleFilesUpload_NoFilesIsBadRequest(t *testing.T) {
	s, _ := newTestServerWithFiles(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	s.handleFilesUpload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleFilesTrash_RestoreAndDeleteItem(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "content")

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/files/item?path=a.txt", nil)
	delRec := httptest.NewRecorder()
	s.handleFilesDelete(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d %s", delRec.Code, delRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/trash", nil)
	listRec := httptest.NewRecorder()
	s.handleFilesTrashList(listRec, listReq)
	var trash []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&trash); err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 {
		t.Fatalf("expected 1 trash entry, got %+v", trash)
	}

	restoreReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/trash/"+trash[0].ID+"/restore", nil)
	restoreReq.SetPathValue("id", trash[0].ID)
	restoreRec := httptest.NewRecorder()
	s.handleFilesTrashRestore(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusNoContent {
		t.Fatalf("restore failed: %d %s", restoreRec.Code, restoreRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("expected file restored: %v", err)
	}
}

func TestHandleFilesTrashEmpty(t *testing.T) {
	s, root := newTestServerWithFiles(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), "a")
	writeTestFile(t, filepath.Join(root, "b.txt"), "b")

	for _, name := range []string{"a.txt", "b.txt"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/item?path="+name, nil)
		rec := httptest.NewRecorder()
		s.handleFilesDelete(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("deleting %s failed: %d", name, rec.Code)
		}
	}

	emptyReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/trash/empty", nil)
	emptyRec := httptest.NewRecorder()
	s.handleFilesTrashEmpty(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", emptyRec.Code, emptyRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/trash", nil)
	listRec := httptest.NewRecorder()
	s.handleFilesTrashList(listRec, listReq)
	var trash []map[string]any
	if err := json.NewDecoder(listRec.Body).Decode(&trash); err != nil {
		t.Fatal(err)
	}
	if len(trash) != 0 {
		t.Errorf("expected empty trash, got %+v", trash)
	}
}

// multipartBody 組出一個 multipart/form-data 請求 body，files 的 key 是
// 檔名、value 是內容——用來測 handleFilesUpload 一次收多個檔案的情境。
func multipartBody(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for name, content := range files {
		part, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}
