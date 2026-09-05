package filemanager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMove_RenameWithinSameDirectory(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "old.txt"), "content")

	if err := Move(root, "old.txt", "new.txt"); err != nil {
		t.Fatalf("Move returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Error("expected old.txt to no longer exist")
	}
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(data) != "content" {
		t.Errorf("expected new.txt to contain original content, got %q, err %v", data, err)
	}
}

func TestMove_ToAnotherDirectory(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "hi")
	if err := os.Mkdir(filepath.Join(root, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Move(root, "a.txt", "dest/a.txt"); err != nil {
		t.Fatalf("Move returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dest", "a.txt")); err != nil {
		t.Errorf("expected file to exist at new location: %v", err)
	}
}

func TestMove_DestinationAlreadyExists(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")

	if err := Move(root, "a.txt", "b.txt"); err != ErrAlreadyExists {
		t.Errorf("got %v, want ErrAlreadyExists", err)
	}
	// 確認拒絕之後兩個檔案內容都沒被動過。
	data, _ := os.ReadFile(filepath.Join(root, "b.txt"))
	if string(data) != "b" {
		t.Error("destination file should be untouched after a rejected move")
	}
}

func TestMove_SourceNotFound(t *testing.T) {
	root := setupRoot(t)
	if err := Move(root, "nope.txt", "dest.txt"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestMove_DirectoryTree(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "dir", "nested", "f.txt"), "deep")

	if err := Move(root, "dir", "dir2"); err != nil {
		t.Fatalf("Move returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "dir2", "nested", "f.txt"))
	if err != nil || string(data) != "deep" {
		t.Errorf("expected nested content to survive the move, got %q, err %v", data, err)
	}
}

func TestCopy_File(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "original")

	if err := Copy(root, "a.txt", "b.txt"); err != nil {
		t.Fatalf("Copy returned error: %v", err)
	}
	// 來源應該還在，內容不變。
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(data) != "original" {
		t.Error("expected source file to remain unchanged after copy")
	}
	data2, err := os.ReadFile(filepath.Join(root, "b.txt"))
	if err != nil || string(data2) != "original" {
		t.Error("expected copy to have the same content as source")
	}
}

func TestCopy_DirectoryTree(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "dir", "nested", "f.txt"), "deep")

	if err := Copy(root, "dir", "dir-copy"); err != nil {
		t.Fatalf("Copy returned error: %v", err)
	}
	// 原始目錄樹應該還在。
	if _, err := os.Stat(filepath.Join(root, "dir", "nested", "f.txt")); err != nil {
		t.Error("expected original directory tree to remain after copy")
	}
	data, err := os.ReadFile(filepath.Join(root, "dir-copy", "nested", "f.txt"))
	if err != nil || string(data) != "deep" {
		t.Errorf("expected copied nested content, got %q, err %v", data, err)
	}
}

func TestCopy_SkipsSymlinks(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "dir", "real.txt"), "real")
	if err := os.Symlink(filepath.Join(root, "dir", "real.txt"), filepath.Join(root, "dir", "link.txt")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	if err := Copy(root, "dir", "dir-copy"); err != nil {
		t.Fatalf("Copy returned error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "dir-copy", "link.txt")); !os.IsNotExist(err) {
		t.Error("expected symlink to be skipped during directory copy")
	}
	if _, err := os.Stat(filepath.Join(root, "dir-copy", "real.txt")); err != nil {
		t.Error("expected the real file to still be copied")
	}
}

// TestCopy_MissingDestinationFolderReturnsNotFound 是一個回歸測試：複製一個
// 檔案到一個不存在的資料夾底下，曾經會在 copyFile 內部呼叫 os.CreateTemp
// 失敗，錯誤訊息夾帶著伺服器上的真實絕對路徑一路冒到 HTTP 層被當成 500
// 回傳，而不是乾淨的「目的資料夾不存在」404。Copy 現在應該在動手複製之前
// 就先確認目的地的父目錄存在，行為跟 Move 一致。
func TestCopy_MissingDestinationFolderReturnsNotFound(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "content")

	err := Copy(root, "a.txt", "no-such-folder/a.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if strings.Contains(err.Error(), root) {
		t.Errorf("error message should not leak the real filesystem path, got %q", err.Error())
	}
}

func TestDelete_SoftDeleteMovesToTrash(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "content")

	if err := Delete(root, "a.txt", false); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Error("expected original file to be gone after soft delete")
	}

	trash, err := ListTrash(root)
	if err != nil {
		t.Fatalf("ListTrash returned error: %v", err)
	}
	if len(trash) != 1 || trash[0].OriginalPath != "a.txt" {
		t.Fatalf("expected one trash entry for a.txt, got %+v", trash)
	}
}

func TestDelete_PermanentReallyDeletes(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "content")

	if err := Delete(root, "a.txt", true); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Error("expected file to be gone")
	}
	trash, err := ListTrash(root)
	if err != nil {
		t.Fatalf("ListTrash returned error: %v", err)
	}
	if len(trash) != 0 {
		t.Errorf("expected no trash entries for a permanent delete, got %+v", trash)
	}
}

func TestDelete_NotFound(t *testing.T) {
	root := setupRoot(t)
	if err := Delete(root, "nope.txt", false); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestRestoreFromTrash_PutsFileBack(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "sub", "a.txt"), "content")

	if err := Delete(root, "sub/a.txt", false); err != nil {
		t.Fatal(err)
	}
	trash, err := ListTrash(root)
	if err != nil || len(trash) != 1 {
		t.Fatalf("expected one trash entry, got %+v, err %v", trash, err)
	}

	if err := RestoreFromTrash(root, trash[0].ID); err != nil {
		t.Fatalf("RestoreFromTrash returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sub", "a.txt"))
	if err != nil || string(data) != "content" {
		t.Errorf("expected restored file with original content, got %q, err %v", data, err)
	}

	trashAfter, err := ListTrash(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(trashAfter) != 0 {
		t.Errorf("expected trash to be empty after restore, got %+v", trashAfter)
	}
}

func TestRestoreFromTrash_ConflictWhenOriginalPathReoccupied(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "first")
	if err := Delete(root, "a.txt", false); err != nil {
		t.Fatal(err)
	}
	// 原本的位置被重新佔用了(使用者又建了一個同名新檔案)。
	writeFile(t, filepath.Join(root, "a.txt"), "second")

	trash, err := ListTrash(root)
	if err != nil || len(trash) != 1 {
		t.Fatalf("expected one trash entry: %+v, %v", trash, err)
	}
	if err := RestoreFromTrash(root, trash[0].ID); err != ErrAlreadyExists {
		t.Errorf("got %v, want ErrAlreadyExists", err)
	}
}

func TestEmptyTrash_RemovesEverything(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	if err := Delete(root, "a.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "b.txt", false); err != nil {
		t.Fatal(err)
	}

	if err := EmptyTrash(root); err != nil {
		t.Fatalf("EmptyTrash returned error: %v", err)
	}
	trash, err := ListTrash(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 0 {
		t.Errorf("expected empty trash, got %+v", trash)
	}
}

func TestDeleteTrashItem_RemovesOnlyThatItem(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	if err := Delete(root, "a.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "b.txt", false); err != nil {
		t.Fatal(err)
	}
	trash, err := ListTrash(root)
	if err != nil || len(trash) != 2 {
		t.Fatalf("expected 2 trash entries: %+v, %v", trash, err)
	}

	if err := DeleteTrashItem(root, trash[0].ID); err != nil {
		t.Fatalf("DeleteTrashItem returned error: %v", err)
	}
	remaining, err := ListTrash(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != trash[1].ID {
		t.Errorf("expected only the other item to remain, got %+v", remaining)
	}
}
