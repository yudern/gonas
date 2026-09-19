package filemanager

import "testing"

// 第三十輪覆核回歸:回收桶 restore/delete 的 id 來自 HTTP 路徑參數,含
// 路徑分隔符或 ".." 的 id 必須被擋(回 ErrNotFound),不能被 filepath.Join
// 帶出回收桶目錄。
func TestTrashID_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"../x", "../../etc/passwd", "a/b", `..\x`, "", ".."} {
		if err := RestoreFromTrash(root, id); err != ErrNotFound {
			t.Errorf("RestoreFromTrash(%q) = %v, want ErrNotFound", id, err)
		}
		if err := DeleteTrashItem(root, id); err != ErrNotFound {
			t.Errorf("DeleteTrashItem(%q) = %v, want ErrNotFound", id, err)
		}
	}
}
