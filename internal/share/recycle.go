package share

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// RecycleDirName 是每個共享根目錄底下存放「已刪除檔案」的隱藏子目錄名稱,
// 對應 smb.conf 裡 recycle:repository 的前綴(見 sambaConfTemplate)。
const RecycleDirName = ".recycle"

// PruneRecycleBin 清理單一共享回收筒(<sharePath>/.recycle)裡「進回收筒超過
// maxAge」的檔案,回傳刪除的檔案數。判斷依據是檔案的 mtime —— smb.conf 開了
// recycle:touch = yes,所以檔案被丟進回收筒時 mtime 會被更新成「刪除當下」,
// 這裡用 mtime 衡量的就是「在回收筒裡待了多久」,而不是檔案原本的修改時間。
//
// maxAge <= 0 代表「永久保留、不清理」,直接回 0;回收筒目錄不存在也回 0
// (還沒有人刪過任何東西)。清掉檔案後會把因此變空的子目錄一併移除(由深到淺),
// 但絕不刪除 .recycle 根目錄本身 —— 留著它(空的)沒有壞處,而且避免跟 smbd
// 下次要寫入時搶建目錄。now 由呼叫端傳入方便測試餵固定時間。
func PruneRecycleBin(sharePath string, maxAge time.Duration, now time.Time) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	root := filepath.Join(sharePath, RecycleDirName)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat recycle bin %s: %w", root, err)
	}
	if !info.IsDir() {
		return 0, nil
	}

	cutoff := now.Add(-maxAge)
	removed := 0
	var dirs []string // 收集子目錄,最後由深到淺嘗試刪掉變空的

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 單一項目讀不到(權限/競態)不該中斷整次清理 —— 跳過它。
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		// 只刪「普通檔案」;symlink 之類的特殊檔跳過(不追進去刪到共享本體)。
		if !fi.Mode().IsRegular() {
			return nil
		}
		if fi.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		return nil
	})
	if walkErr != nil {
		return removed, fmt.Errorf("walking recycle bin %s: %w", root, walkErr)
	}

	// 由深到淺(路徑長度長的在前)刪掉已經空掉的子目錄。os.Remove 對非空目錄
	// 會失敗,正好當成「還有東西、別刪」的判斷,不需要自己數。
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = os.Remove(dir) // 失敗(非空/權限)忽略:留著無害
	}
	return removed, nil
}
