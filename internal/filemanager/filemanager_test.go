package filemanager

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// setupRoot 建立一個乾淨的臨時目錄當「陣列掛載點」，t.TempDir() 保證
// 每個測試各自獨立、測完自動清掉，不需要手動收拾。
func setupRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for test file: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
}

func TestList_ReturnsEntriesAndHidesTrashDir(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "hello")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, trashDirName), 0o700); err != nil {
		t.Fatal(err)
	}

	entries, err := List(root, "")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (a.txt, sub), got %d: %+v", len(entries), entries)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["a.txt"] || !names["sub"] {
		t.Errorf("missing expected entries: %+v", entries)
	}
	if names[trashDirName] {
		t.Error("trash directory should not appear in List results")
	}
}

func TestList_NonexistentPathReturnsErrNotFound(t *testing.T) {
	root := setupRoot(t)
	if _, err := List(root, "nope"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestList_FileInsteadOfDirReturnsErrNotADirectory(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "hello")
	if _, err := List(root, "a.txt"); err != ErrNotADirectory {
		t.Errorf("got %v, want ErrNotADirectory", err)
	}
}

func TestResolve_RejectsDotDotEscape(t *testing.T) {
	root := setupRoot(t)
	if _, err := List(root, "../../etc"); err == nil {
		t.Fatal("expected error escaping root via .., got nil")
	}
}

func TestResolve_RejectsDeepDotDotEscape(t *testing.T) {
	root := setupRoot(t)
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := List(root, "a/../../../etc"); err == nil {
		t.Fatal("expected error escaping root via nested .., got nil")
	}
}

func TestResolve_RejectsSymlinkEscape(t *testing.T) {
	root := setupRoot(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "top secret")

	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	if _, err := ResolvePath(root, "escape/secret.txt"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Errorf("got %v, want ErrPathEscapesRoot", err)
	}
}

func TestResolve_AllowsInternalSymlink(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "real", "a.txt"), "hi")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	// 指向陣列「內部」其他地方的 symlink 是合法的捷徑，不該被擋。
	if _, err := ResolvePath(root, "link/a.txt"); err != nil {
		t.Errorf("expected internal symlink to resolve fine, got %v", err)
	}
}

func TestMkdir_CreatesDirectory(t *testing.T) {
	root := setupRoot(t)
	if err := Mkdir(root, "newdir"); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "newdir"))
	if err != nil || !info.IsDir() {
		t.Fatalf("expected newdir to exist as a directory: %v", err)
	}
}

func TestMkdir_AlreadyExists(t *testing.T) {
	root := setupRoot(t)
	if err := Mkdir(root, "d"); err != nil {
		t.Fatal(err)
	}
	if err := Mkdir(root, "d"); err != ErrAlreadyExists {
		t.Errorf("got %v, want ErrAlreadyExists", err)
	}
}

func TestMkdir_MissingParentReturnsNotFound(t *testing.T) {
	root := setupRoot(t)
	if err := Mkdir(root, "missing/child"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
